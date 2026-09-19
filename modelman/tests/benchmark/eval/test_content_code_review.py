"""Smoke test for the code_review category — see
test_content_reasoning.py's docstring for scope."""

from pathlib import Path

from modelman.benchmark.eval.category import load_category

CATEGORY_ROOT = Path(__file__).parent.parent.parent.parent.parent / "benchmarks" / "tasks" / "eval"


def test_code_review_category_loads_and_sums_to_100():
    # The shipped code_review content must load, weigh to 100 and give every item a
    # `seeded_issue` — the judge prompt keys on it, so a missing one would silently
    # score reviews against no ground truth.
    category = load_category(CATEGORY_ROOT / "code_review")
    assert category.rubric is not None
    assert sum(category.rubric.dimensions.values()) == 100
    assert len(category.items) >= 6
    for item in category.items:
        assert "seeded_issue" in item.meta
