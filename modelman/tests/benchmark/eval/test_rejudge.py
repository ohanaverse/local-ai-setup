# tests/benchmark/eval/test_rejudge.py
"""Tests for eval.runner.rejudge_run — re-scoring persisted responses
without regenerating them, using the judge config persisted in run.toml."""

import json
from pathlib import Path

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import rejudge_run
from modelman.benchmark.judge_core import Rubric
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _seed_run(tmp_path: Path) -> Path:
    row_dir = tmp_path / "01--row1"
    item_dir = row_dir / "mini_review" / "i1"
    item_dir.mkdir(parents=True)
    (item_dir / "response.txt").write_text("off by one in the loop", encoding="utf-8")
    (tmp_path / "run.toml").write_text(
        """
[run]
git_sha = "abc123"

[suite.judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[suite.coding]
""",
        encoding="utf-8",
    )
    return tmp_path


class _FakeJudgeTransport:
    def complete(self, prompt: str, *, temperature: float) -> str:
        return json.dumps({"scores": {"a": 90}, "total": 90})


def test_rejudge_run_rescores_from_persisted_response(tmp_path):
    # rejudge_run must read the response.txt written by the original run,
    # re-score it through the judge, and persist a fresh judge.json —
    # without ever regenerating the model's response. This is the core
    # contract: cheap re-scoring after a rubric/judge-model change.
    run_dir = _seed_run(tmp_path)
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    outcomes = rejudge_run(
        run_dir,
        [category],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert outcomes == [{"row": "01--row1", "category": "mini_review", "item": "i1", "total": 90}]
    judge_data = json.loads(
        (run_dir / "01--row1" / "mini_review" / "i1" / "judge.json").read_text()
    )
    assert judge_data["combined"]["total"] == 90


def test_rejudge_run_samples_override_takes_precedence_over_run_toml(tmp_path):
    # samples_override lets a rejudge call ask for more samples than the
    # original run used (run.toml says samples=1); this verifies the
    # override wins over the persisted config rather than being ignored.
    run_dir = _seed_run(tmp_path)
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    calls = []

    class _CountingTransport:
        def complete(self, prompt: str, *, temperature: float) -> str:
            calls.append(1)
            return json.dumps({"scores": {"a": 90}, "total": 90})

    rejudge_run(
        run_dir,
        [category],
        samples_override=3,
        judge_transport_factory=lambda judge_cfg: _CountingTransport(),
    )
    assert len(calls) == 3  # one sample per configured sample count, not run.toml's samples=1


def test_rejudge_run_recomputes_score_json_and_summary_after_rescoring(tmp_path):
    # Regression test for the stale-report finding: rejudge_run used to
    # overwrite item-level judge.json without ever recomputing the
    # category's score.json or re-rendering summary.md, so `eval show
    # --latest` kept displaying the OLD, pre-rejudge numbers with no
    # indication anything had changed — a silently-wrong-output bug, the
    # worst failure mode for a benchmark/reporting tool. This seeds a run
    # dir with STALE score.json/summary.md/metrics.jsonl (as if an original
    # run_suite() had scored the item 10/100), rejudges it against a fake
    # transport that always scores 90/100, and asserts every derived
    # artifact reflects the new score, not the stale one.
    run_dir = _seed_run(tmp_path)
    (run_dir / "01--row1" / "mini_review" / "score.json").write_text(
        json.dumps({"score_100": 10}), encoding="utf-8"
    )
    (run_dir / "summary.md").write_text("# stale\n\n10.0\n", encoding="utf-8")
    # Seeded with the REAL key write_metrics_jsonl uses: row.label ("row1"
    # for a row whose directory is "01--row1", i.e. index=1 + label
    # "row1") — NOT the directory name itself. Seeding this with the
    # directory name is exactly the bug that let the label/model_id/route
    # reconstruction mismatch through review (see the dedicated regression
    # test below).
    (run_dir / "metrics.jsonl").write_text(
        json.dumps(
            {
                "label": "row1",
                "model_id": "ollama/a",
                "route": "litellm",
                "error": None,
                "categories": {"mini_review": 10},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    rejudge_run(
        run_dir,
        [category],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )

    score_data = json.loads((run_dir / "01--row1" / "mini_review" / "score.json").read_text())
    assert score_data["score_100"] == 90.0  # the new judge score, not the stale 10

    summary = (run_dir / "summary.md").read_text()
    assert "90.0" in summary
    assert "10.0" not in summary  # the stale number must not survive the re-render

    metrics_line = json.loads((run_dir / "metrics.jsonl").read_text().splitlines()[0])
    assert metrics_line["categories"]["mini_review"] == 90.0


def test_rejudge_run_recovers_model_id_route_family_despite_double_index_prefix(tmp_path):
    # Regression test for a real bug found in the final whole-branch
    # review: a real suite-loaded row's auto-generated label already
    # starts with its own index prefix (eval/suite.py's
    # f"{index:02d}--{model}--{route}"), and _row_dir prepends ANOTHER
    # index prefix on top of that when building the row's directory name
    # — so a row's directory name and its label (what write_metrics_jsonl
    # actually persists) are two different strings, e.g. label
    # "01--a--litellm" vs. directory "01--01--a--litellm". Reconstruction
    # used to look the directory name up directly against a dict keyed by
    # the real label, always missing, and silently fell back to a garbage
    # model_id (the directory name itself), a blank route, and — since
    # report.py can't resolve a family from a garbage model_id — no
    # family grouping in the capability matrix either. This seeds a run
    # shaped exactly like a real suite-loaded row and a real Registry,
    # and confirms reconstruction recovers the real model_id/route and
    # that the family actually resolves.
    run_dir = tmp_path
    row_dir = run_dir / "01--01--a--litellm"
    item_dir = row_dir / "mini_review" / "i1"
    item_dir.mkdir(parents=True)
    (item_dir / "response.txt").write_text("off by one in the loop", encoding="utf-8")
    (run_dir / "run.toml").write_text(
        """
[run]
git_sha = "abc123"

[suite.judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[suite.coding]
""",
        encoding="utf-8",
    )
    (run_dir / "metrics.jsonl").write_text(
        json.dumps(
            {
                "label": "01--a--litellm",
                "model_id": "ollama/a",
                "route": "litellm",
                "error": None,
                "categories": {"mini_review": 10},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="fam-x", provider_id="ollama", model_name="a")],
    )
    rejudge_run(
        run_dir,
        [category],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
        registry=registry,
    )

    metrics_line = json.loads((run_dir / "metrics.jsonl").read_text().splitlines()[0])
    assert metrics_line["label"] == "01--a--litellm"  # stable, matches the original run's label
    assert metrics_line["model_id"] == "ollama/a"  # not the directory name "01--01--a--litellm"
    assert metrics_line["route"] == "litellm"  # not blank

    summary = (run_dir / "summary.md").read_text()
    assert "fam-x" in summary  # family resolves — proves model_id was recovered, not garbage
    assert "01--01--a--litellm" not in summary  # the raw directory name must never leak into it


def test_reconstruct_keeps_scored_categories_alongside_error_txt(tmp_path):
    # A row that scored one category then failed a later one has BOTH the
    # scored artifacts and error.txt on disk; the post-rejudge
    # reconstruction (which regenerates summary.md/metrics.jsonl) must keep
    # the scored categories, not blank them to N/A — a failure never costs
    # already-computed (possibly API-billed) results.
    from modelman.benchmark.eval.runner import _reconstruct_run_results

    run_dir = _seed_run(tmp_path)
    item_dir = run_dir / "01--row1" / "mini_review" / "i1"
    (item_dir / "judge.json").write_text(
        json.dumps(
            {
                "status": "scored",
                "combined": {
                    "scores": {"a": 90},
                    "total": 90,
                    "verdict": "",
                    "flags": [],
                    "rationale": "",
                    "raw_text": "",
                },
                "attempts_used": 1,
            }
        ),
        encoding="utf-8",
    )
    (run_dir / "01--row1" / "error.txt").write_text("doc_summary timed out", encoding="utf-8")

    results = _reconstruct_run_results(run_dir)
    assert len(results) == 1
    assert results[0].error == "doc_summary timed out"
    assert "mini_review" in results[0].category_results
    assert results[0].category_results["mini_review"].score_100 == 90.0


def test_reconstruct_prefers_row_dir_key_over_colliding_labels(tmp_path):
    # metrics.jsonl lines carry a row_dir key (the row's unique directory
    # basename); two rows with colliding labels must each recover their OWN
    # model_id/route via that key rather than both getting the last
    # metrics line's values.
    from modelman.benchmark.eval.runner import _reconstruct_run_results

    for name in ("01--dup", "02--dup"):
        # A row directory always holds error.txt and/or category
        # subdirectories — reconstruct_run_results skips bare dirs.
        (tmp_path / name / "mini_review").mkdir(parents=True)
    (tmp_path / "metrics.jsonl").write_text(
        json.dumps({"label": "dup", "row_dir": "01--dup", "model_id": "m/one", "route": "litellm"})
        + "\n"
        + json.dumps({"label": "dup", "row_dir": "02--dup", "model_id": "m/two", "route": "direct"})
        + "\n",
        encoding="utf-8",
    )

    results = _reconstruct_run_results(tmp_path)
    by_dir = {r.row_dir.name: r for r in results}
    assert by_dir["01--dup"].row.model_id == "m/one"
    assert by_dir["01--dup"].row.route == "litellm"
    assert by_dir["02--dup"].row.model_id == "m/two"
    assert by_dir["02--dup"].row.route == "direct"


def _mini_category() -> Category:
    return Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )


def test_rejudge_row_filter_accepts_label_and_index(tmp_path):
    # `run --row` accepts label-or-index, so `judge --row` must too: a user
    # who ran `eval run --row 2` reuses the same value for rejudge. The
    # filter used to match only the row-dir BASENAME ("02--row2"), so an
    # index or label silently matched nothing — zero re-judged items, exit
    # 0, with summary.md/metrics.jsonl rewritten as if a rejudge happened.
    run_dir = _seed_run(tmp_path)
    row2_dir = run_dir / "02--row2"
    item_dir = row2_dir / "mini_review" / "i1"
    item_dir.mkdir(parents=True)
    (item_dir / "response.txt").write_text("another response", encoding="utf-8")

    category = _mini_category()
    outcomes = rejudge_run(
        run_dir,
        [category],
        row_filter=["row2"],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert len(outcomes) == 1
    assert outcomes[0]["row"] == "02--row2"

    outcomes_idx = rejudge_run(
        run_dir,
        [category],
        row_filter=["2"],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert len(outcomes_idx) == 1
    assert outcomes_idx[0]["row"] == "02--row2"


def test_rejudge_row_filter_unknown_value_raises(tmp_path):
    # An unknown --row value must fail loudly (exit 1 in the CLI) rather
    # than silently producing an empty no-op rejudge that still rewrites
    # summary.md/metrics.jsonl and exits 0 — the user would believe a
    # rejudge happened when nothing was re-judged.
    from modelman.benchmark.errors import BenchmarkError

    run_dir = _seed_run(tmp_path)
    category = _mini_category()
    with pytest.raises(BenchmarkError, match="matched no row"):
        rejudge_run(
            run_dir,
            [category],
            row_filter=["no-such-row"],
            judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
        )


def test_reconstruct_skips_malformed_metrics_lines(tmp_path):
    # metrics.jsonl is append-style and can be left with a truncated final
    # line (run interrupted mid-write) or a hand-edited line; the
    # reconstruction must skip malformed lines rather than crash rejudge
    # with a raw JSONDecodeError — a skipped line only degrades to the
    # label-fallback keying (the same house convention as usage/wt_state's
    # "malformed lines are skipped, not fatal").
    from modelman.benchmark.eval.runner import _reconstruct_run_results

    run_dir = _seed_run(tmp_path)
    (run_dir / "metrics.jsonl").write_text(
        json.dumps(
            {
                "label": "row1",
                "model_id": "ollama/a",
                "route": "litellm",
                "error": None,
                "categories": {"mini_review": 10},
            }
        )
        + "\n"
        + '{"label": "trunc", "model_i'  # truncated mid-write
        + "\n",
        encoding="utf-8",
    )
    results = _reconstruct_run_results(run_dir)
    assert len(results) == 1
    assert results[0].row.model_id == "ollama/a"  # from the healthy line, not a crash


def test_reconstruct_skips_malformed_judge_json(tmp_path):
    # One corrupted judge.json anywhere in the run (a crash mid-write or a
    # hand edit) must not crash the whole rejudge/reconstruction with a raw
    # JSONDecodeError — judge_cmd catches only BenchmarkError/
    # FileNotFoundError, so a raw traceback would escape to the user. The
    # malformed item is skipped (the same tolerance convention
    # _read_metrics_row_meta applies to metrics.jsonl lines); the healthy
    # item in the same category still reconstructs.
    from modelman.benchmark.eval.runner import _reconstruct_run_results

    run_dir = tmp_path
    category_dir = run_dir / "01--row1" / "mini_review"
    good_judge = {
        "status": "scored",
        "combined": {
            "scores": {"a": 90},
            "total": 90,
            "verdict": "",
            "flags": [],
            "rationale": "",
            "raw_text": "",
        },
        "attempts_used": 1,
    }
    for name, judge_body in (
        ("i1", json.dumps(good_judge)),
        ("i2", '{"status": "scored", "combin'),  # truncated mid-write
    ):
        item_dir = category_dir / name
        item_dir.mkdir(parents=True)
        (item_dir / "response.txt").write_text("response", encoding="utf-8")
        (item_dir / "judge.json").write_text(judge_body, encoding="utf-8")
    (run_dir / "metrics.jsonl").write_text(
        json.dumps(
            {
                "label": "row1",
                "row_dir": "01--row1",
                "model_id": "ollama/a",
                "route": "litellm",
                "error": None,
                "categories": {"mini_review": 90},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    results = _reconstruct_run_results(run_dir)
    assert len(results) == 1
    cat = results[0].category_results["mini_review"]
    # The corrupted item is kept as UNJUDGED (judge=None) rather than dropped,
    # so the category mean cannot silently exclude it; the healthy item's
    # score still stands and rejudge_run can re-score the unjudged one.
    assert [i.item_id for i in cat.items] == ["i1", "i2"]
    assert cat.items[1].judge is None
    assert cat.score_100 == 90.0


def test_reconstruct_skips_malformed_evalplus_result_json(tmp_path):
    # Same tolerance class as the judge.json test, on the coding side: a
    # corrupted evalplus_result.json must degrade that one category to
    # empty (None) instead of crashing reconstruction with a raw
    # JSONDecodeError.
    from modelman.benchmark.eval.runner import _reconstruct_category_result

    category_dir = tmp_path / "coding"
    category_dir.mkdir()
    (category_dir / "evalplus_result.json").write_text(
        '{"dataset": "humaneval", "pass_at_1": 0.5, "raw_outp', encoding="utf-8"
    )
    assert _reconstruct_category_result(category_dir) is None


def test_reconstruct_skips_wellformed_but_reshaped_judge_json(tmp_path):
    # A judge.json that parses as JSON but no longer matches JudgeScore's
    # fields (a hand edit, or a file written by a newer/older modelman)
    # must be skipped with a warning — not crash the whole rejudge with a
    # raw TypeError escaping judge_cmd's BenchmarkError-only catch, which
    # would silently abandon rows already re-judged in this invocation.
    from modelman.benchmark.eval.runner import _reconstruct_run_results

    run_dir = tmp_path
    category_dir = run_dir / "01--row1" / "mini_review"
    good_judge = {
        "status": "scored",
        "combined": {
            "scores": {"a": 90},
            "total": 90,
            "verdict": "",
            "flags": [],
            "rationale": "",
            "raw_text": "",
        },
        "attempts_used": 1,
    }
    for name, judge_body in (
        ("i1", json.dumps(good_judge)),
        # Well-formed JSON, but "combined" carries a field JudgeScore
        # doesn't have and lacks one it requires.
        (
            "i2",
            json.dumps({"status": "scored", "combined": {"unexpected": True}, "attempts_used": 1}),
        ),
    ):
        item_dir = category_dir / name
        item_dir.mkdir(parents=True)
        (item_dir / "response.txt").write_text("response", encoding="utf-8")
        (item_dir / "judge.json").write_text(judge_body, encoding="utf-8")
    (run_dir / "metrics.jsonl").write_text(
        json.dumps(
            {
                "label": "row1",
                "row_dir": "01--row1",
                "model_id": "ollama/a",
                "route": "litellm",
                "error": None,
                "categories": {"mini_review": 90},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    results = _reconstruct_run_results(run_dir)
    assert len(results) == 1
    cat = results[0].category_results["mini_review"]
    # The reshaped item is kept as UNJUDGED, not dropped from the category.
    assert [i.item_id for i in cat.items] == ["i1", "i2"]
    assert cat.items[1].judge is None


def test_reconstruct_skips_wellformed_but_reshaped_evalplus_result_json(tmp_path):
    # Same tolerance on the coding side: an evalplus_result.json whose
    # keys don't match CodingResult's current fields (e.g. a future
    # version adds one) must degrade the category to None, not raise
    # TypeError through CodingResult(**data).
    from modelman.benchmark.eval.runner import _reconstruct_category_result

    category_dir = tmp_path / "coding"
    category_dir.mkdir()
    (category_dir / "evalplus_result.json").write_text(
        json.dumps({"dataset": "humaneval", "pass_at_1": 0.5, "brand_new_field": 1}),
        encoding="utf-8",
    )
    assert _reconstruct_category_result(category_dir) is None


def test_judge_config_from_run_toml_malformed_toml_raises_clean_error(tmp_path):
    # run.toml is user-editable on disk: a hand-edit or merge-conflict
    # resolution can drop [suite.judge] or mangle the TOML, and that must
    # surface as the clean BenchmarkError every other malformation in the
    # judge command path produces — judge_cmd catches only BenchmarkError
    # and FileNotFoundError, so raw KeyError/TOMLDecodeError would escape
    # as a traceback to the user.
    from modelman.benchmark.eval.runner import _judge_config_from_run_toml

    # Missing [suite.judge] entirely
    run_dir = tmp_path / "run1"
    run_dir.mkdir()
    (run_dir / "run.toml").write_text('[run]\ngit_sha = "abc"\n', encoding="utf-8")
    with pytest.raises(BenchmarkError, match="missing or malformed around"):
        _judge_config_from_run_toml(run_dir, samples_override=None)

    # Malformed TOML
    run_dir = tmp_path / "run2"
    run_dir.mkdir()
    (run_dir / "run.toml").write_text("[suite\nbroken", encoding="utf-8")
    with pytest.raises(BenchmarkError, match="missing or malformed around"):
        _judge_config_from_run_toml(run_dir, samples_override=None)


def test_rejudge_row_index_resolves_to_the_same_row_run_selected(tmp_path):
    # `judge --row N` must resolve to the SAME row `run --row N` selected:
    # run numbers row dirs by SUITE position while execution order (sorted
    # by (provider, model) for isolation grouping) differs on any
    # multi-provider suite — the shipped eval-sweep.toml shape below has
    # suite order qwen/omlx/glm but execution order glm/qwen/omlx. The
    # index must come from each directory's own NN-- prefix (a suite
    # position), never its position in the sorted listing (an execution
    # position) — otherwise rejudge silently re-judges the WRONG row.
    run_dir = tmp_path
    # Row dirs as run_suite would write them for suite rows 1..3
    # (qwen=01, omlx=02, glm=03) in execution order glm, qwen, omlx —
    # sorted() here yields 01, 02, 03 anyway, so seed the dirs' INNER
    # content to prove the index maps dir->row, not listing position.
    for dir_name, label in (("01--qwen", "qwen"), ("02--omlx", "omlx"), ("03--glm", "glm")):
        item_dir = run_dir / dir_name / "mini_review" / "i1"
        item_dir.mkdir(parents=True)
        (item_dir / "response.txt").write_text(f"response for {label}", encoding="utf-8")
    (run_dir / "run.toml").write_text(
        """
[run]
git_sha = "abc123"

[suite.judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[suite.coding]
""",
        encoding="utf-8",
    )
    category = _mini_category()
    outcomes = rejudge_run(
        run_dir,
        [category],
        row_filter=["2"],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    # Suite row 2 is the omlx row, whose dir is 02--omlx — not the second
    # directory in any other ordering.
    assert len(outcomes) == 1
    assert outcomes[0]["row"] == "02--omlx"


def test_rejudge_row_index_finds_filtered_run_dirs_with_gaps(tmp_path):
    # A filtered run (--row 1,3) leaves a GAP in the dir numbering
    # (01-- and 03--, no 02--). judge --row 3 must still find 03-- by
    # parsing the dir's own index prefix — matching it by listing position
    # (where 03-- is only the 2nd dir) would select nothing or the wrong
    # row.
    run_dir = tmp_path
    for dir_name in ("01--row1", "03--row3"):
        item_dir = run_dir / dir_name / "mini_review" / "i1"
        item_dir.mkdir(parents=True)
        (item_dir / "response.txt").write_text("response", encoding="utf-8")
    (run_dir / "run.toml").write_text(
        """
[run]
git_sha = "abc123"

[suite.judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[suite.coding]
""",
        encoding="utf-8",
    )
    category = _mini_category()
    outcomes = rejudge_run(
        run_dir,
        [category],
        row_filter=["3"],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert len(outcomes) == 1
    assert outcomes[0]["row"] == "03--row3"


def test_rejudge_selection_error_surfaces_before_transport_construction(tmp_path):
    # A bad --row must surface the "matched no row directories" error, not
    # a judge-transport error (e.g. missing API key): selection happens
    # before the transport is built, so the clearest error wins. The
    # transport factory here raises if it is ever reached.
    run_dir = _seed_run(tmp_path)
    category = _mini_category()

    def _boom_factory(_judge_cfg):
        raise AssertionError("transport must not be built when row selection fails")

    with pytest.raises(BenchmarkError, match="matched no row"):
        rejudge_run(
            run_dir, [category], row_filter=["no-such"], judge_transport_factory=_boom_factory
        )


def test_rejudge_run_keeps_existing_judge_json_when_rejudge_fails(tmp_path):
    # A failed re-judge (transient judge outage) must not overwrite a valid,
    # already-paid-for judge.json with a judge_fail outcome — that would
    # silently turn a good score into JUDGE_FAIL.
    run_dir = _seed_run(tmp_path)
    judge_path = run_dir / "01--row1" / "mini_review" / "i1" / "judge.json"
    original = json.dumps({"status": "scored", "combined": {"total": 77}, "attempts_used": 1})
    judge_path.write_text(original, encoding="utf-8")
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )

    class _GarbageTransport:
        def complete(self, prompt: str, *, temperature: float) -> str:
            return "not json"

    outcomes = rejudge_run(
        run_dir,
        [category],
        judge_transport_factory=lambda judge_cfg: _GarbageTransport(),
    )
    assert judge_path.read_text(encoding="utf-8") == original
    # The reported outcome must show the retained score (77), not the failed
    # attempt's None, so the console agrees with the rebuilt summary.
    assert [o["total"] for o in outcomes] == [77]
