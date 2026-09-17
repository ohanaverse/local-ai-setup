# tests/benchmark/eval/test_rejudge.py
"""Tests for eval.runner.rejudge_run — re-scoring persisted responses
without regenerating them, using the judge config persisted in run.toml."""

import json
from pathlib import Path

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
