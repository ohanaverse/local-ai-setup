"""Smoke test for the planning category — see test_content_reasoning.py's
docstring for what this does and doesn't verify."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_planning_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "planning")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
