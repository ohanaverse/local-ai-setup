"""Tests for modelman.benchmark.eval.runner — provider-grouped isolation,
per-row category dispatch (judged vs. EvalPlus), and restore-on-finally.

Isolation and generation are faked throughout: these tests pin the
orchestration logic (grouping, dispatch, restore-failure handling), not real
network/process calls.
"""

from pathlib import Path
from unittest.mock import patch

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import RunSavedButRestoreFailed, run_suite
from modelman.benchmark.eval.suite import CodingConfig, JudgeConfig, RowConfig, Suite
from modelman.benchmark.judge_core import JudgeOutcome, JudgeScore, Rubric
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
