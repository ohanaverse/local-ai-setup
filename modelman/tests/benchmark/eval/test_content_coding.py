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
    assert category.coding_limit is not None and category.coding_limit > 0
