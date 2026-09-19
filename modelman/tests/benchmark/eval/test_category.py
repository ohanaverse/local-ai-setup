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
    # The happy path: a judged category loads its items (with per-item
    # meta), its rubric dimensions, and the rubric markdown — the three
    # inputs judged_runner's prompt builder and judge_core's scorer both
    # consume, so any load-shape regression breaks the whole judged path.
    category = load_category(FIXTURE_ROOT / "mini_review")
    assert category.name == "mini_review"
    assert len(category.items) == 1
    assert category.items[0].id == "off-by-one"
    assert category.items[0].meta["seeded_issue"].startswith("range(")
    assert category.rubric is not None
    assert category.rubric.dimensions == {"bug_detection": 60, "actionability": 40}
    assert "mini_review" in category.rubric_md


def test_load_category_coding_has_no_rubric():
    # coding is the one rubric-less category (EvalPlus grades it, not a
    # judge): load_category must return rubric=None with its dataset/limit
    # config, and every dispatch site branches on exactly this None.
    category = load_category(FIXTURE_ROOT / "coding")
    assert category.name == CODING_CATEGORY
    assert category.rubric is None
    assert category.coding_dataset == "humaneval"
    assert category.coding_limit == 5


def test_load_category_rejects_rubric_not_summing_to_100():
    # Rubric dimensions must sum to 100: scores are reported on a /100
    # scale, so a mis-weighted rubric would silently make category scores
    # incomparable — the check is the guard that keeps every category's
    # score_100 directly comparable.
    with pytest.raises(BenchmarkError, match="sum to 100"):
        load_category(INVALID_FIXTURE_ROOT / "bad_sum")


def test_list_categories_finds_every_subdirectory():
    # list_categories is the --root inventory behind both `list-categories`
    # and run's category validation; it must surface every category
    # directory (judged and coding alike), or rows silently run nothing.
    names = {c.name for c in list_categories(FIXTURE_ROOT)}
    assert names == {"mini_review", "coding"}


def _write_category(tmp_path: Path, rubric: str, items: str) -> Path:
    root = tmp_path / "cat"
    root.mkdir()
    (root / "rubric.toml").write_text(rubric, encoding="utf-8")
    (root / "rubric.md").write_text("rubric", encoding="utf-8")
    (root / "items.toml").write_text(items, encoding="utf-8")
    return root


GOOD_ITEMS = '[[items]]\nid = "a"\nprompt = "p"\n'


def test_load_category_rejects_non_positive_integer_weights(tmp_path):
    # Float weights (33.3/33.3/33.4 sum to 99.99999999999999), negative
    # weights that offset to 100, and string weights must all be clean
    # BenchmarkErrors, not silent acceptance or a bare TypeError.
    for dims in (
        "a = 33.3\nb = 33.3\nc = 33.4",
        "a = 150\nb = -50",
        'a = "100"',
    ):
        sub = tmp_path / f"c{abs(hash(dims))}"
        sub.mkdir()
        root = _write_category(sub, f"[dimensions]\n{dims}\n", GOOD_ITEMS)
        with pytest.raises(BenchmarkError, match="positive integers"):
            load_category(root)


def test_load_category_rejects_items_missing_id_or_prompt(tmp_path):
    # An [[items]] entry without `id` or `prompt` used to raise a KeyError
    # traceback from the CLI; it must be a BenchmarkError naming the entry.
    root = _write_category(tmp_path, "[dimensions]\na = 100\n", '[[items]]\nid = "a"\n')
    with pytest.raises(BenchmarkError, match=r"#1 needs string `id` and `prompt`"):
        load_category(root)
