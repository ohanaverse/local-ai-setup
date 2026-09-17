"""Tests for modelman.benchmark.eval.category — loading a category
directory (items.toml + rubric.toml/rubric.md + meta.toml, except `coding`
which is EvalPlus-config-only)."""

from pathlib import Path

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import CODING_CATEGORY, list_categories, load_category

FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "categories"
INVALID_FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "categories_invalid"


def test_load_category_reads_items_and_rubric():
    category = load_category(FIXTURE_ROOT / "mini_review")
    assert category.name == "mini_review"
    assert len(category.items) == 1
    assert category.items[0].id == "off-by-one"
    assert category.items[0].meta["seeded_issue"].startswith("range(")
    assert category.rubric is not None
    assert category.rubric.dimensions == {"bug_detection": 60, "actionability": 40}
    assert "mini_review" in category.rubric_md


def test_load_category_coding_has_no_rubric():
    category = load_category(FIXTURE_ROOT / "coding")
    assert category.name == CODING_CATEGORY
    assert category.rubric is None
    assert category.coding_dataset == "humaneval"
    assert category.coding_limit == 5


def test_load_category_rejects_rubric_not_summing_to_100():
    with pytest.raises(BenchmarkError, match="sum to 100"):
        load_category(INVALID_FIXTURE_ROOT / "bad_sum")


def test_list_categories_finds_every_subdirectory():
    names = {c.name for c in list_categories(FIXTURE_ROOT)}
    assert names == {"mini_review", "coding"}
