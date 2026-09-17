"""Smoke test for the code_review category — see
test_content_reasoning.py's docstring for scope."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_code_review_category_loads_and_sums_to_100():
    category = load_category(CATEGORY_ROOT / "code_review")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
    for item in category.items:
        assert "seeded_issue" in item.meta
