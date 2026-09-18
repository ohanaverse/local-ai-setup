"""Smoke test for the coding category's EvalPlus config (no rubric — see
plan Task 6/12 for how it's graded)."""

from pathlib import Path

from modelman.benchmark.eval.category import CODING_CATEGORY, load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_coding_category_loads_with_dataset_and_limit():
    category = load_category(CATEGORY_ROOT / "coding")
    assert category.name == CODING_CATEGORY
    assert category.rubric is None
    assert category.coding_dataset in ("humaneval", "mbpp")
    # No shipped default limit: EvalPlus 0.3.1 grading requires the FULL
    # dataset, so the shipped config runs limit-free; a suite may still
    # override [coding] limit (accepting the known grading gap), and a
    # suite-level override lands in coding_limit only via CodingConfig,
    # not here.
    assert category.coding_limit is None
