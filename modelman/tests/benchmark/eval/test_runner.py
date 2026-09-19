"""Tests for modelman.benchmark.eval.runner — provider-grouped isolation,
per-row category dispatch (judged vs. EvalPlus), and restore-on-finally.

Isolation and generation are faked throughout: these tests pin the
orchestration logic (grouping, dispatch, restore-failure handling), not real
network/process calls.
"""

import json
from pathlib import Path
from unittest.mock import call, patch

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval import judged_runner
from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import RunSavedButRestoreFailed, run_suite
from modelman.benchmark.eval.suite import (
    CodingConfig,
    DirectRouteConfig,
    JudgeConfig,
    RowConfig,
    Suite,
)
from modelman.benchmark.judge_core import JudgeOutcome, JudgeScore, JudgeTransportError, Rubric
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def _multi_provider_registry() -> Registry:
    # Shaped like the shipped eval-sweep.toml: two ollama rows around one
    # omlx row, so the (provider, model) execution sort reorders the suite.
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local"),
            ProviderEntry(id="omlx", name="oMLX", location="local"),
        ],
        models=[
            ModelEntry(id="ollama/qwen", family="f", provider_id="ollama", model_name="qwen"),
            ModelEntry(id="omlx/m", family="f", provider_id="omlx", model_name="m"),
            ModelEntry(id="ollama/glm", family="f", provider_id="ollama", model_name="glm"),
        ],
    )


def _multi_provider_suite() -> Suite:
    # Suite order: qwen (1), omlx (2), glm (3). Execution order (sorted by
    # (provider_id, model_id)): glm, qwen, omlx — deliberately different.
    return Suite(
        name="t",
        cooldown_s=0,
        judge=JudgeConfig(model="j", temperature=0.0, samples=1, max_attempts=1, route="litellm"),
        coding=CodingConfig(),
        rows=[
            RowConfig(label="row1", model_id="ollama/qwen", route="litellm", provider_id="ollama"),
            RowConfig(label="row2", model_id="omlx/m", route="litellm", provider_id="omlx"),
            RowConfig(label="row3", model_id="ollama/glm", route="litellm", provider_id="ollama"),
        ],
    )


def _suite(row_categories=None) -> Suite:
    return Suite(
        name="t",
        cooldown_s=0,
        judge=JudgeConfig(model="j", temperature=0.0, samples=1, max_attempts=1, route="litellm"),
        coding=CodingConfig(),
        rows=[
            RowConfig(
                label="row1",
                model_id="ollama/a",
                route="litellm",
                provider_id="ollama",
                categories=row_categories,
            )
        ],
    )


def _mini_review_category() -> Category:
    return Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="p", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_isolates_once_per_provider_group_and_restores(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # A single-provider suite should isolate that provider exactly once
    # (not once per row) and restore providers exactly once afterward —
    # the core cost-saving behavior of provider-grouped isolation.
    # ISOLATABLE_PROVIDERS is bound at runner.py import time from the real
    # lifecycle module, not from this mock, so it already contains "ollama"
    # (a real local provider) — nothing to set on mock_isolation for that.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )

    # The isolate call must NAME the row's model via the env dict (the only
    # in-process channel for ollama/omlx — their backends' resolve() ignores
    # positional extra_args). Without it, warmup loads the backend's baked-in
    # default (ornith) while the row benchmarks its own model, breaking the
    # one-model-at-a-time invariant isolation exists to enforce.
    mock_isolation.isolate_provider.assert_called_once_with(
        "ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "a"}
    )
    mock_isolation.restore_providers.assert_called_once()
    assert len(results) == 1
    assert results[0].category_results["mini_review"].score_100 == 50.0
    assert run_dir.is_dir()


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_row_categories_override_narrows_dispatch(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # A row's own `categories` list should narrow dispatch to just the named
    # categories, even when the suite runner was handed a broader category
    # set — this is how a suite targets a subset of capabilities per row.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    other_category = Category(
        name="other", path=Path("."), items=[], rubric=Rubric(dimensions={"a": 100}), rubric_md="x"
    )
    _, results = run_suite(
        _suite(row_categories=["mini_review"]),
        _registry(),
        [_mini_review_category(), other_category],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )
    assert set(results[0].category_results) == {"mini_review"}


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_raises_run_saved_but_restore_failed_on_restore_error(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # A restore failure after every row has already completed and been
    # written to disk must not look like a lost run: run_suite raises
    # RunSavedButRestoreFailed carrying the same run_dir/results so the CLI
    # can still record the run instead of discarding good results.
    mock_isolation.restore_providers.side_effect = BenchmarkError("wedged")
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    with pytest.raises(RunSavedButRestoreFailed) as exc_info:
        run_suite(
            _suite(),
            _registry(),
            [_mini_review_category()],
            results_dir=tmp_path,
            judge_transport_factory=lambda suite: object(),
        )
    assert exc_info.value.run_dir.is_dir()
    assert len(exc_info.value.results) == 1


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_survives_non_benchmark_error_row_failure_and_still_restores(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # Regression test for the Critical finding: judge_core.JudgeTransportError
    # (raised by row_transport.complete inside judged_runner, and equally
    # reachable from evalplus_runner's subprocess.TimeoutExpired) is a plain
    # Exception, NOT a BenchmarkError. Before this fix it escaped run_suite's
    # row-level try/except entirely, which skipped isolation.restore_providers()
    # (leaving a local provider isolated/stopped) and every persist call
    # (write_row_artifacts/render_summary/write_metrics_jsonl/write_run_toml),
    # losing a whole sweep's already-collected results. This pins that a
    # non-BenchmarkError row failure is caught, recorded as a failed
    # RowRunResult, and does not prevent restore or persist.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_generate.side_effect = JudgeTransportError("network exploded")

    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )

    assert len(results) == 1
    assert results[0].error is not None
    assert "network exploded" in results[0].error
    mock_isolation.restore_providers.assert_called_once()
    assert (run_dir / "summary.md").is_file()
    assert (run_dir / "metrics.jsonl").is_file()


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_preserves_earlier_category_results_when_a_later_category_fails(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # Regression test: a row with multiple categories must not lose an
    # earlier category's already-computed (and possibly API-billed) judge
    # score just because a LATER category in the same row fails. Before this
    # fix, _run_row built its results dict locally and only returned it on
    # full success, so any mid-loop exception silently discarded every
    # category that had already finished — this pins that the first
    # category's result survives, is persisted to disk, and the row is
    # still recorded with an error for the category that actually failed.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_generate.side_effect = [
        _fake_category_result(50.0),
        JudgeTransportError("second category exploded"),
    ]
    other_category = Category(
        name="other_review",
        path=Path("."),
        items=[Item(id="i1", prompt="p", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )

    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category(), other_category],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )

    assert len(results) == 1
    assert results[0].error is not None
    assert "second category exploded" in results[0].error
    assert results[0].category_results["mini_review"].score_100 == 50.0
    assert "other_review" not in results[0].category_results
    mock_isolation.restore_providers.assert_called_once()
    row_dir = run_dir / "01--row1"
    assert (row_dir / "mini_review" / "i1" / "response.txt").is_file()
    assert (row_dir / "error.txt").is_file()


def _fake_category_result(score: float):
    from modelman.benchmark.eval.judged_runner import CategoryRowResult, ItemResult

    outcome = JudgeOutcome(
        status="scored",
        samples=[],
        combined=JudgeScore(
            scores={"a": int(score)},
            total=int(score),
            verdict="",
            flags=[],
            rationale="",
            raw_text="",
        ),
        attempts_used=1,
    )
    return CategoryRowResult(
        category="mini_review",
        items=[ItemResult(item_id="i1", response_text="r", judge=outcome, score_100=score)],
        score_100=score,
    )


@patch("modelman.benchmark.eval.runner.evalplus_runner.run_coding_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_skips_judge_transport_for_coding_only_rows(
    mock_isolation, mock_resolve, mock_run_coding, tmp_path
):
    # A coding-only run never invokes the judge, so building the judge
    # transport (which hard-requires a LiteLLM/OpenRouter API key) must be
    # deferred until a judged category will actually run — a purely local
    # EvalPlus run must not need an API key. The factory here raises if it
    # is ever called; coding dispatch is patched to return a real
    # CodingResult so the run completes.
    from modelman.benchmark.eval.evalplus_runner import CodingResult

    mock_resolve.return_value = ("http://localhost:8000/v1", "b-server-name", "ollama")
    mock_run_coding.return_value = CodingResult(dataset="humaneval", pass_at_1=0.5, raw_output="ok")

    def _boom_factory(_judge_cfg):
        raise AssertionError("judge transport must not be built for a coding-only run")

    suite = _suite(row_categories=["coding"])
    suite.rows[0].route = "litellm"
    coding_category = Category(name="coding", path=Path("."), items=[], rubric=None, rubric_md=None)
    run_dir, results = run_suite(
        suite,
        _registry(),
        [coding_category],
        results_dir=tmp_path,
        judge_transport_factory=_boom_factory,
    )
    assert results[0].category_results["coding"].pass_at_1 == 0.5


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_sleeps_cooldown_between_rows_in_a_group(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # Suite.cooldown_s is documented (and shipped in eval-sweep.toml as
    # 15.0) as thermal settling between rows; it must actually be honored
    # after each row except the first in a provider group — the first row
    # runs right after isolation warmup, with nothing to settle from. It
    # was previously parsed but never read: a dead knob that silently
    # ignored the user's config.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    suite = _suite()
    suite.cooldown_s = 0.25
    suite.rows = [
        RowConfig(label="r1", model_id="ollama/a", route="litellm", provider_id="ollama"),
        RowConfig(label="r2", model_id="ollama/a", route="litellm", provider_id="ollama"),
        RowConfig(label="r3", model_id="ollama/a", route="litellm", provider_id="ollama"),
    ]
    with patch("modelman.benchmark.eval.runner.time.sleep") as mock_sleep:
        run_suite(
            suite,
            _registry(),
            [_mini_review_category()],
            results_dir=tmp_path,
            judge_transport_factory=lambda suite: object(),
        )
    # 3 rows, one group: cooldown after rows 1 and 2, none after row 3.
    assert mock_sleep.call_args_list == [call(0.25), call(0.25)]


@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_unmatched_row_filter_raises_and_writes_nothing(mock_isolation, tmp_path):
    # A --row value (label or index) that matches nothing must raise
    # BenchmarkError BEFORE any run directory is created: the old silent
    # empty selection created a run dir, wrote empty summary.md/
    # metrics.jsonl/run.toml, repointed eval_last_run at it, and exited 0
    # — the user believed a scoped benchmark ran. Mirrors the rejudge
    # selector's no-match error.
    suite = _multi_provider_suite()
    with pytest.raises(BenchmarkError, match="matched no suite rows"):
        run_suite(
            suite,
            _multi_provider_registry(),
            [_mini_review_category()],
            row_filter=["no-such-row"],
            results_dir=tmp_path,
            judge_transport_factory=lambda judge_cfg: object(),
        )
    with pytest.raises(BenchmarkError, match="matched no suite rows"):
        run_suite(
            suite,
            _multi_provider_registry(),
            [_mini_review_category()],
            row_filter=["99"],  # index past the end of the suite
            results_dir=tmp_path,
            judge_transport_factory=lambda judge_cfg: object(),
        )
    # No run dir was created for either failed attempt (the autouse
    # conftest fixture drops an unrelated _default_litellm_config dir into
    # tmp_path — only eval-* run dirs matter here).
    assert [p for p in tmp_path.iterdir() if p.name.startswith("eval-")] == []
    mock_isolation.isolate_provider.assert_not_called()


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_row_dirs_are_numbered_by_suite_position_not_execution_order(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # On a multi-provider suite (like the shipped eval-sweep.toml) the
    # execution order — sorted by (provider_id, model_id) for isolation
    # grouping — differs from suite order. Row directories must be named
    # by SUITE position anyway: `judge --row N` (and the docs' "reuse the
    # same value for rejudge" promise) resolves N against the directory's
    # own NN-- prefix, so numbering dirs by execution order made run and
    # judge disagree about which row an index meant — run --row 2 selected
    # the omlx row while judge --row 2 hit the qwen row's directory.
    mock_resolve.return_value = ("http://localhost:4000/v1", "m", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    run_dir, results = run_suite(
        _multi_provider_suite(),
        _multi_provider_registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: object(),
    )
    # Suite order row1=qwen, row2=omlx, row3=glm; execution order glm,
    # qwen, omlx. Dirs named by suite position — not 01--row3.
    assert sorted(r.row_dir.name for r in results) == ["01--row1", "02--row2", "03--row3"]


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_row_dirs_from_a_filtered_run_keep_suite_indexes(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # A filtered run (--row 1,3) executes only rows 1 and 3; its row dirs
    # must keep the SUITE numbering (01-- and 03--, a gap where 02-- would
    # be) so `judge --row 3` afterwards still finds the third suite row's
    # directory. Renumbering the filtered selection 01/02 would silently
    # re-point index 2 at the wrong row.
    mock_resolve.return_value = ("http://localhost:4000/v1", "m", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    run_dir, results = run_suite(
        _multi_provider_suite(),
        _multi_provider_registry(),
        [_mini_review_category()],
        row_filter=["1", "3"],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: object(),
    )
    assert sorted(r.row_dir.name for r in results) == ["01--row1", "03--row3"]


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_skips_rows_disjoint_from_the_category_selection(
    mock_isolation, mock_resolve, mock_generate, tmp_path, capsys
):
    # A row whose own `categories =` is disjoint from the runner's category
    # set (a valid --category value that row doesn't declare) must be
    # skipped with a notice rather than run with zero categories: running
    # it would isolate a provider, produce an empty row directory, an
    # all-N/A matrix row with no anomaly, and a metrics.jsonl line with
    # "categories": {} — the silent-empty failure mode the surrounding
    # guards were added to prevent. The sibling row that DOES overlap runs
    # normally.
    mock_resolve.return_value = ("http://localhost:4000/v1", "m", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    # Two rows: row1 declares only `coding` (disjoint — skipped), row2 has
    # no categories override (inherits the full set — runs). The single-row
    # all-disjoint case is covered separately by the raises test.
    suite = _suite()
    suite.rows = [
        RowConfig(
            label="row1",
            model_id="ollama/a",
            route="litellm",
            provider_id="ollama",
            categories=["coding"],
        ),
        RowConfig(label="row2", model_id="ollama/a", route="litellm", provider_id="ollama"),
    ]
    run_dir, results = run_suite(
        suite,
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: object(),
    )
    # The disjoint row was skipped, not run: no results, no row directory,
    # no isolation (nothing left to isolate), and a notice on stderr.
    assert [r.row.label for r in results] == ["row2"]
    assert not (run_dir / "01--row1").exists()
    assert (run_dir / "02--row2").is_dir()
    mock_isolation.isolate_provider.assert_called_once()
    err = capsys.readouterr().err
    assert "skipping row 'row1'" in err


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_raises_when_no_row_overlaps_the_category_selection(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # When EVERY selected row is disjoint from the category set, the run must
    # fail loudly (BenchmarkError → CLI exits 1) rather than create a run
    # dir, write empty artifacts, and repoint eval_last_run at an empty run.
    mock_isolation.isolate_provider.side_effect = AssertionError("must not isolate")

    with pytest.raises(BenchmarkError, match="no rows remain after category scoping"):
        run_suite(
            _multi_provider_suite(),
            _multi_provider_registry(),
            [],
            results_dir=tmp_path,
            judge_transport_factory=lambda judge_cfg: object(),
        )
    mock_isolation.isolate_provider.assert_not_called()


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_reisolates_when_the_model_changes_within_a_provider_group(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # Benchmark isolation is one-model-at-a-time even on multi-tenant ollama:
    # two ollama rows with DIFFERENT models must re-isolate between them
    # (the env dict is part of the isolation key, exactly as mlx_lm_server's
    # pairing args always were). Without the re-isolation, the second row's
    # requests would run while the first row's model is still resident —
    # two models in GPU/RAM, breaking the invariant the isolation call
    # exists to enforce.
    mock_resolve.return_value = ("http://localhost:4000/v1", "m", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    suite = _suite()
    suite.rows = [
        RowConfig(label="r1", model_id="ollama/a", route="litellm", provider_id="ollama"),
        RowConfig(label="r2", model_id="ollama/b", route="litellm", provider_id="ollama"),
    ]
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="f", provider_id="ollama", model_name="b"),
        ],
    )
    run_suite(
        suite,
        registry,
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: object(),
    )
    assert mock_isolation.isolate_provider.call_args_list == [
        call("ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "a"}),
        call("ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "b"}),
    ]


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_direct_model_name_wins_over_registry_model_name_for_warmup(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # An omlx direct-route row names `direct_model` because the daemon serves
    # the repo BASENAME while the registry model_name is org-prefixed; the
    # warm name must follow the same rule, or the warmed weights and the
    # benchmarked weights desynchronize (warmup POSTs a name the daemon
    # resolves differently from what the row's requests actually use).
    mock_resolve.return_value = ("http://localhost:8000/v1", "Qwen3.8-27B-4bit", "ollama")
    mock_generate.return_value = _fake_category_result(50.0)

    suite = _suite()
    suite.routes_direct = {"omlx": DirectRouteConfig(base_url="http://localhost:8000/v1")}
    suite.rows[0] = RowConfig(
        label="row1",
        model_id="omlx/m",
        route="direct",
        provider_id="omlx",
        direct_model="Qwen3.8-27B-4bit",
    )
    registry = Registry(
        providers=[ProviderEntry(id="omlx", name="oMLX", location="local")],
        models=[
            ModelEntry(
                id="omlx/m",
                family="f",
                provider_id="omlx",
                model_name="mlx-community/Qwen3.8-27B-4bit",
            )
        ],
    )
    run_suite(
        suite,
        registry,
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: object(),
    )
    mock_isolation.isolate_provider.assert_called_once_with(
        "omlx", env={"LLM_ISOLATE_OMLX_4BIT_MODEL": "Qwen3.8-27B-4bit"}
    )


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_cloud_rows_skip_isolation(mock_isolation, mock_resolve, mock_generate, tmp_path):
    # A cloud model on a local provider's id (the shipped eval-sweep.toml's
    # `ollama/glm-5.3-flash:cloud` row) reaches a remote API through the
    # LiteLLM proxy — it contends with nothing on this machine, and isolating
    # its provider id would warm the local daemon's default model for
    # minutes to no effect. Cloud rows must skip isolation entirely (and
    # still restore nothing they never stopped).
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/glm-5.3-flash:cloud", "sk-x")
    mock_generate.return_value = _fake_category_result(50.0)

    suite = _suite()
    suite.rows[0] = RowConfig(
        label="row1",
        model_id="ollama/glm-5.3-flash:cloud",
        route="litellm",
        provider_id="ollama",
    )
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[
            ModelEntry(
                id="ollama/glm-5.3-flash:cloud",
                family="f",
                provider_id="ollama",
                model_name="glm-5.3-flash:cloud",
                location="cloud",
            )
        ],
    )
    run_dir, results = run_suite(
        suite,
        registry,
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: object(),
    )
    mock_isolation.isolate_provider.assert_not_called()
    mock_isolation.restore_providers.assert_not_called()
    assert results[0].category_results["mini_review"].score_100 == 50.0
    assert (run_dir / "01--row1" / "mini_review" / "i1" / "response.txt").is_file()


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_judges_after_restore_not_interleaved_with_generation(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # Design spec step 4 / code-review finding #2: with the reference judge
    # config (samples=3, OpenRouter) a multi-item category holds the
    # exclusively-isolated local model pinned in RAM through minutes of
    # judge round-trips while its provider stays down — judging must run
    # strictly AFTER restore_providers(), never interleaved with
    # generation. This pins the call ORDER via a shared call log: every
    # generation call must precede restore, and every judge call must
    # follow it.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    # generate_category fake leaves items UNJUDGED; judge_category is the
    # real function, driven through the fake judge transport below.
    from modelman.benchmark.eval.judged_runner import ItemResult

    mock_generate.return_value = judged_runner.CategoryRowResult(
        category="mini_review",
        items=[ItemResult(item_id="i1", response_text="r", judge=None, score_100=None)],
        score_100=None,
    )
    call_log: list[str] = []

    def _fake_isolate(provider_id, *args, env=None):
        call_log.append(f"isolate:{provider_id}")

    def _fake_restore():
        call_log.append("restore")

    mock_isolation.isolate_provider.side_effect = _fake_isolate
    mock_isolation.restore_providers.side_effect = _fake_restore

    class _LoggingJudgeTransport:
        def complete(self, prompt, *, temperature):
            call_log.append("judge")
            return json.dumps({"scores": {"a": 50}, "total": 50})

    suite = _suite()
    suite.judge.samples = 1
    run_dir, results = run_suite(
        suite,
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: _LoggingJudgeTransport(),
    )
    # Generation (isolate → generate) fully precedes restore, and judging
    # follows restore: no judge call may appear before the restore entry.
    assert "judge" not in call_log[: call_log.index("restore") + 1]
    assert call_log[-1] == "judge" or "judge" in call_log[call_log.index("restore") :]
    assert results[0].category_results["mini_review"].score_100 == 50.0
    # The final on-disk state carries real scores, not the UNJUDGED snapshot.
    score = json.loads((run_dir / "01--row1" / "mini_review" / "score.json").read_text())
    assert score["score_100"] == 50.0
    judge_json = json.loads(
        (run_dir / "01--row1" / "mini_review" / "i1" / "judge.json").read_text()
    )
    assert judge_json["status"] == "scored"


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_interrupted_judge_phase_leaves_a_rejudge_recoverable_run(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # A judge transport that dies mid-phase (here: raises through
    # judge_category's per-item retry) must not lose the sweep: the
    # pre-judge snapshot in the finally block already persisted every
    # response + run.toml, so the run on disk is complete but UNJUDGED —
    # exactly the state rejudge_run recovers from. _judge_all records the
    # failure as judge_fail items (not a raise), so the run finishes with
    # an honest JUDGE_FAIL/UNJUDGED report instead of crashing.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    from modelman.benchmark.eval.judged_runner import ItemResult

    mock_generate.return_value = judged_runner.CategoryRowResult(
        category="mini_review",
        items=[ItemResult(item_id="i1", response_text="r", judge=None, score_100=None)],
        score_100=None,
    )

    class _DeadJudgeTransport:
        def complete(self, prompt, *, temperature):
            raise JudgeTransportError("judge route dead")

    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: _DeadJudgeTransport(),
    )
    # Generation survived: the response is on disk, and judge.json records
    # the failure (status judge_fail with the transport error) rather than
    # the item vanishing.
    row_dir = run_dir / "01--row1"
    assert (row_dir / "mini_review" / "i1" / "response.txt").is_file()
    judge_json = json.loads((row_dir / "mini_review" / "i1" / "judge.json").read_text())
    assert judge_json["status"] == "judge_fail"
    assert "judge route dead" in judge_json["error"]
    # The summary calls it what it is.
    assert "JUDGE_FAIL" in (run_dir / "summary.md").read_text()
    # run.toml exists even though judging failed — rejudge recovery needs it.
    assert (run_dir / "run.toml").is_file()


@patch("modelman.benchmark.eval.runner.judged_runner.generate_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_rejudge_transport_failure_is_contained_per_category(
    mock_isolation, mock_resolve, mock_generate, tmp_path
):
    # _judge_all must contain a judge_category failure to the category it
    # happened in: a second category with a healthy transport still gets
    # scored, rather than one dead judge round-trip aborting the whole
    # scoring phase.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    from modelman.benchmark.eval.judged_runner import ItemResult

    def _gen(category, transport, *, temperature):
        return judged_runner.CategoryRowResult(
            category=category.name,
            items=[ItemResult(item_id="i1", response_text="r", judge=None, score_100=None)],
            score_100=None,
        )

    mock_generate.side_effect = _gen

    class _FlakyJudgeTransport:
        """Dies on the SECOND category judged (one item each, so call 2 is
        the other_review category's) so per-category containment is
        observable without depending on prompt content."""

        def __init__(self):
            self.calls = 0

        def complete(self, prompt, *, temperature):
            self.calls += 1
            if self.calls == 2:
                raise JudgeTransportError("route dead on second category")
            return json.dumps({"scores": {"a": 70}, "total": 70})

    other_category = Category(
        name="other_review",
        path=Path("."),
        items=[Item(id="i1", prompt="p", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category(), other_category],
        results_dir=tmp_path,
        judge_transport_factory=lambda judge_cfg: _FlakyJudgeTransport(),
    )
    # The healthy category scored; the dead one is honestly judge_fail.
    assert results[0].category_results["mini_review"].score_100 == 70.0
    assert results[0].category_results["other_review"].score_100 is None
    assert "JUDGE_FAIL" in (run_dir / "summary.md").read_text()
