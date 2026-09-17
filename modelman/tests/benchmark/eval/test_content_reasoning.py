"""Smoke test: the real reasoning category content loads and its rubric
sums to 100. Real content quality (are these puzzles actually hard/fair) is
a human judgment call, not something a unit test can verify — this test
only pins the load-time contract."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_reasoning_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "reasoning")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
    assert len({item.id for item in category.items}) == len(category.items)
