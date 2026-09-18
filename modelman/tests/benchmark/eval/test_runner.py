"""Tests for modelman.benchmark.eval.runner — provider-grouped isolation,
per-row category dispatch (judged vs. EvalPlus), and restore-on-finally.

Isolation and generation are faked throughout: these tests pin the
orchestration logic (grouping, dispatch, restore-failure handling), not real
network/process calls.
"""

from pathlib import Path
from unittest.mock import call, patch

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import RunSavedButRestoreFailed, run_suite
from modelman.benchmark.eval.suite import CodingConfig, JudgeConfig, RowConfig, Suite
from modelman.benchmark.judge_core import JudgeOutcome, JudgeScore, JudgeTransportError, Rubric
from modelman.registry import ModelEntry, ProviderEntry, Registry


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
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


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_isolates_once_per_provider_group_and_restores(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    # A single-provider suite should isolate that provider exactly once
    # (not once per row) and restore providers exactly once afterward —
    # the core cost-saving behavior of provider-grouped isolation.
    # ISOLATABLE_PROVIDERS is bound at runner.py import time from the real
    # lifecycle module, not from this mock, so it already contains "ollama"
    # (a real local provider) — nothing to set on mock_isolation for that.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

    run_dir, results = run_suite(
        _suite(),
        _registry(),
        [_mini_review_category()],
        results_dir=tmp_path,
        judge_transport_factory=lambda suite: object(),
    )

    mock_isolation.isolate_provider.assert_called_once_with("ollama")
    mock_isolation.restore_providers.assert_called_once()
    assert len(results) == 1
    assert results[0].category_results["mini_review"].score_100 == 50.0
    assert run_dir.is_dir()


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_row_categories_override_narrows_dispatch(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    # A row's own `categories` list should narrow dispatch to just the named
    # categories, even when the suite runner was handed a broader category
    # set — this is how a suite targets a subset of capabilities per row.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

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


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_raises_run_saved_but_restore_failed_on_restore_error(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    # A restore failure after every row has already completed and been
    # written to disk must not look like a lost run: run_suite raises
    # RunSavedButRestoreFailed carrying the same run_dir/results so the CLI
    # can still record the run instead of discarding good results.
    mock_isolation.restore_providers.side_effect = BenchmarkError("wedged")
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

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


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_survives_non_benchmark_error_row_failure_and_still_restores(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
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
    mock_run_judged.side_effect = JudgeTransportError("network exploded")

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


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_preserves_earlier_category_results_when_a_later_category_fails(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
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
    mock_run_judged.side_effect = [
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
    mock_run_coding.return_value = CodingResult(
        dataset="humaneval", pass_at_1=0.5, raw_output="ok"
    )

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


@patch("modelman.benchmark.eval.runner.judged_runner.run_judged_category")
@patch("modelman.benchmark.eval.runner.resolve_row_endpoint")
@patch("modelman.benchmark.eval.runner.isolation")
def test_run_suite_sleeps_cooldown_between_rows_in_a_group(
    mock_isolation, mock_resolve, mock_run_judged, tmp_path
):
    # Suite.cooldown_s is documented (and shipped in eval-sweep.toml as
    # 15.0) as thermal settling between rows; it must actually be honored
    # after each row except the first in a provider group — the first row
    # runs right after isolation warmup, with nothing to settle from. It
    # was previously parsed but never read: a dead knob that silently
    # ignored the user's config.
    mock_resolve.return_value = ("http://localhost:4000/v1", "ollama/a", "sk-x")
    mock_run_judged.return_value = _fake_category_result(50.0)

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
