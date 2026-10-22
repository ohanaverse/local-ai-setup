import copy
import json
import re
from datetime import timedelta
from typing import Any

from conftest import T0

from modeltiers import cards, page, show

_DATA = re.compile(r'<script id="data" type="application/json">(.*?)</script>', re.DOTALL)
_COLUMNS = re.compile(r"const COLUMNS = \[(.*?)^\s*\];", re.DOTALL | re.MULTILINE)


def _data(text: str) -> dict[str, Any]:
    """The JSON a page carries, as its own script would read it."""
    (found,) = _DATA.findall(text)
    loaded = json.loads(found)
    assert isinstance(loaded, dict)
    return loaded


def _page_sort_names(text: str) -> set[str]:
    """Every name the page's script matches `data.sort` against: a column's own
    `sort`, or its key where it names no other (`(column.sort || column.key)`)."""
    (block,) = _COLUMNS.findall(text)
    names: set[str] = set()
    for line in block.splitlines():
        key = re.search(r'\bkey: "(\w+)"', line)
        if key is None:
            continue
        sort = re.search(r'\bsort: "(\w+)"', line)
        names.add(sort.group(1) if sort else key.group(1))
    return names


def test_a_row_per_model_unmapped_row_and_candidate_in_file_order(
    document: dict[str, Any],
) -> None:
    rows = page.page_rows(document)
    assert [(row["kind"], row["name"], row["tier"]) for row in rows] == [
        ("model", "ollama/glm-5.3:cloud", "standard"),
        ("model", "ollama/deepseek-v4-pro:cloud", "standard"),
        ("model", "openrouter/z-ai/glm-5.3", "standard"),
        ("model", "ollama/qwen3.8:27b-mlx", "standard"),
        ("model", "ollama/minimax-m3:cloud", "quick"),
        ("model", "ollama/nomic-embed-text:latest", "unrated"),
        ("model", "ollama/gemma4:31b-mlx", "unrated"),
        ("model", "ollama/medgemma:27b", "unrated"),
        ("candidate", "anthropic/claude-opus-5-5", "marathon"),
        ("candidate", "anthropic/claude-fable-5", "deep"),
    ]


def test_a_row_has_every_column_with_none_for_what_the_file_does_not_give(
    document: dict[str, Any],
) -> None:
    rows = {row["name"]: row for row in page.page_rows(document)}
    assert rows["ollama/glm-5.3:cloud"] == {
        "kind": "model",
        "name": "ollama/glm-5.3:cloud",
        "canonical": "zhipuai/glm-5.3",
        "tier": "standard",
        "confidence": "high",
        "reason": None,
        "open_weights": True,
        "context": 1048576,
        "card_kind": "huggingface",
        "card_url": "https://huggingface.co/zai-org/GLM-5.3",
        "intelligence_index": 44.8,
        "arena_agent": 0.02147446416824893,
        "eci": 155.61,
        "coding_index": 74.8,
        "agentic_index": 53.1,
        "aligned_score": 44.3,
        "aligned_sources": 3,
    }
    # An ignored row: no key, no flag, no score.
    assert rows["ollama/nomic-embed-text:latest"] == {
        "kind": "model",
        "name": "ollama/nomic-embed-text:latest",
        "canonical": None,
        "tier": "unrated",
        "confidence": "none",
        "reason": "not_applicable",
        "open_weights": None,
        "context": None,
        "card_kind": None,
        "card_url": None,
        "intelligence_index": None,
        "arena_agent": None,
        "eci": None,
        "coding_index": None,
        "agentic_index": None,
        "aligned_score": None,
        "aligned_sources": None,
    }
    # An unmapped row is unrated, and says why.
    unmapped = rows["ollama/medgemma:27b"]
    assert (unmapped["tier"], unmapped["reason"]) == ("unrated", "unmapped")
    assert unmapped["confidence"] is None
    # A candidate is named by its key.
    opus = rows["anthropic/claude-opus-5-5"]
    assert (opus["canonical"], opus["open_weights"], opus["context"]) == (
        "anthropic/claude-opus-5-5",
        False,
        1000000,
    )


def test_a_value_of_the_wrong_type_is_none() -> None:
    doc = {
        "models": [
            {
                "provider_id": "p",
                "model_name": "m",
                "tier": 3,
                "open_weights": "yes",
                "context": "large",
                "scores": {"eci": True, "arena_agent": "high"},
            },
            {"provider_id": "p", "model_name": "n", "scores": "none"},
        ]
    }
    first, second = page.page_rows(doc)
    assert (first["tier"], first["open_weights"], first["context"]) == (None, None, None)
    assert (first["eci"], first["arena_agent"]) == (None, None)
    assert second["eci"] is None


def test_the_page_carries_the_rows_the_ranks_and_the_first_sort(
    document: dict[str, Any],
) -> None:
    data = _data(page.page_html(document, T0, sort="eci"))
    assert data["rows"] == page.page_rows(document)
    assert data["sort"] == "eci"
    assert data["ranks"] == {"unrated": 0, "quick": 1, "standard": 2, "deep": 3, "marathon": 4}
    # The stamp the page's script works the file's age out from, when opened.
    assert data["generated_at"] == "2026-10-12T15:58:00Z"
    assert _data(page.page_html(document, T0))["sort"] is None


def test_the_page_says_how_old_the_file_is_and_credits_the_sources(
    document: dict[str, Any],
) -> None:
    text = page.page_html(document, T0 + timedelta(days=3))
    assert "tiers.toml generated 2026-10-12T15:58:00Z (3 days old), contracts fixture-1" in text
    assert "<li>modelsdev: models.dev (https://models.dev), MIT</li>" in text
    assert (
        "<li>epoch: Epoch AI, &#x27;Capabilities &amp; benchmarking&#x27;, "
        "https://epoch.ai/benchmarks, CC BY 4.0</li>" in text
    )
    assert text.startswith("<!doctype html>")


def test_the_page_loads_nothing_from_anywhere(document: dict[str, Any]) -> None:
    """One file, read from disk: no script, stylesheet, font or image is
    fetched, and nothing in it is a link."""
    text = page.page_html(document, T0)
    assert not re.search(r"\b(src|action)\s*=", text)
    # The one address the script sets is a card link the reader may follow: a
    # navigation, not a load, and it comes from the row.
    assert re.findall(r"\bhref\s*=[^;]*;", text) == ["href = row.card_url;"]
    assert not re.search(r"<(link|img|iframe|object|embed|form)\b", text)
    assert "@import" not in text
    assert "url(" not in text
    assert not re.search(r"\b(fetch|XMLHttpRequest|import)\s*\(", text)


def test_a_name_from_a_source_cannot_end_the_script_or_add_markup(
    document: dict[str, Any],
) -> None:
    hostile = "</script><script>alert(1)</script><img src=x onerror=alert(2)>&amp;"
    doc = copy.deepcopy(document)
    doc["models"][0]["model_name"] = hostile
    doc["candidates"][0]["canonical"] = "x/<!--" + hostile
    doc["generated_at"] = hostile
    doc["contracts_version"] = hostile
    doc["sources"]["arena"]["attribution"] = hostile
    text = page.page_html(doc, T0)
    # The page's own two script elements, and no other.
    assert text.count("<script") == 2
    assert text.count("</script>") == 2
    assert "<img" not in text
    assert "<!--" not in text
    # The data still says exactly what the file said.
    rows = _data(text)["rows"]
    assert rows[0]["name"] == "ollama/" + hostile
    assert rows[-2]["name"] == "x/<!--" + hostile


def test_an_empty_file_still_makes_a_page() -> None:
    text = page.page_html({}, T0)
    assert _data(text) == {
        "rows": [],
        "sort": None,
        "ranks": {"unrated": 0, "quick": 1, "standard": 2, "deep": 3, "marathon": 4},
        "generated_at": None,
    }
    assert "tiers.toml generated ? (of unknown age), contracts ?" in text


def test_a_score_that_is_not_finite_is_none_and_the_page_is_still_json() -> None:
    """TOML can spell `nan` and `inf`; JSON cannot."""
    doc = {
        "models": [
            {
                "provider_id": "p",
                "model_name": "m",
                "context": float("inf"),
                "scores": {
                    "eci": float("nan"),
                    "arena_agent": float("-inf"),
                    "coding_index": 10**400,
                },
            }
        ]
    }
    (row,) = page.page_rows(doc)
    assert (row["context"], row["eci"], row["arena_agent"]) == (None, None, None)
    assert row["coding_index"] is None
    assert _data(page.page_html(doc, T0))["rows"] == [row]


def test_the_page_tells_a_file_with_no_rows_from_filters_that_match_none() -> None:
    text = page.page_html({}, T0)
    assert "tiers.toml lists no model" in text
    assert "No row matches these filters." in text


def test_a_row_has_its_aligned_score_and_how_many_scores_it_rests_on(
    document: dict[str, Any],
) -> None:
    rows = {row["name"]: row for row in page.page_rows(document)}
    glm = rows["ollama/glm-5.3:cloud"]
    assert (glm["aligned_score"], glm["aligned_sources"]) == (44.3, 3)
    deepseek = rows["ollama/deepseek-v4-pro:cloud"]
    assert (deepseek["aligned_score"], deepseek["aligned_sources"]) == (43.5, 2)
    ignored = rows["ollama/nomic-embed-text:latest"]
    assert (ignored["aligned_score"], ignored["aligned_sources"]) == (None, None)
    odd = page.page_rows(
        {
            "models": [
                {
                    "provider_id": "p",
                    "model_name": "m",
                    "aligned_score": "x",
                    "aligned_sources": "all",
                }
            ]
        }
    )[0]
    assert (odd["aligned_score"], odd["aligned_sources"]) == (None, None)
    # A count is given only beside a score, and only for a list of names.
    for entry in (
        {"aligned_sources": ["eci"]},
        {"aligned_score": 40.0, "aligned_sources": ["eci", 7]},
    ):
        (row,) = page.page_rows({"candidates": [{"canonical": "v/m", **entry}]})
        assert row["aligned_sources"] is None
    opus = rows["anthropic/claude-opus-5-5"]  # a candidate
    assert (opus["aligned_score"], opus["aligned_sources"]) == (57.0, 3)


def test_the_page_says_how_far_the_aligned_score_can_be_off(document: dict[str, Any]) -> None:
    text = page.page_html(document, T0, sort="aligned")
    assert _data(text)["sort"] == "aligned"
    assert (
        "The lines fitted for this file typically miss by 1.1 points (arena_agent) and "
        "1.1 points (eci); a smaller difference between two models means nothing." in text
    )
    # With no line in the file the page claims nothing about how it was made:
    # the file may be from a run that fitted none, or from before the score.
    none = "This file holds no fitted line: an aligned score in it is a model"
    bare = copy.deepcopy(document)
    del bare["alignment"]
    assert none in page.page_html(bare, T0)
    assert none in page.page_html({}, T0)
    odd = copy.deepcopy(document)
    odd["alignment"] = {"eci": {"typical_miss": "<b>big</b>"}, "arena_agent": 7}
    assert none in page.page_html(odd, T0)


def test_a_signal_name_from_the_file_is_text_in_the_footer(document: dict[str, Any]) -> None:
    doc = copy.deepcopy(document)
    doc["alignment"] = {"<img src=x onerror=alert(1)>": {"typical_miss": 2.0}}
    text = page.page_html(doc, T0)
    assert "<img" not in text
    assert "2.0 points (&lt;img src=x onerror=alert(1)&gt;)" in text


def test_the_page_has_the_aligned_columns_beside_the_model_and_opens_on_them() -> None:
    """The page's script is not run by any test, so what it must hold is read
    out of it: the two columns, where they sit, and the name `--sort aligned`
    is turned into."""
    text = page.page_html({}, T0)
    keys = re.findall(r'\{ key: "(\w+)", label: "(\w+)", type: "(\w+)"', text)
    assert keys[3:6] == [
        ("name", "model", "text"),
        ("aligned_score", "aligned", "num"),
        ("aligned_sources", "src", "num"),
    ]
    assert 'key: "name", label: "model", type: "text", sort: "model"' in text
    assert "(column.sort || column.key) === data.sort" in text
    # The aligned score keeps its one decimal on the page, as in the file.
    assert 'key: "aligned_score", label: "aligned", type: "num", places: 1, sort: "aligned"' in text


def test_every_name_the_cli_sorts_the_table_by_is_a_column_the_page_sorts_by() -> None:
    """What holds the CLI's sort names and the page's columns together: nothing
    at runtime does. The page looks a name up among its columns and falls back to
    the file's own order when none has it, so a name added to `show.SORT_COLUMNS`
    alone would leave `show --sort X` sorting the text table while
    `show --html --sort X` wrote an unsorted page, with no error on either side."""
    names = _page_sort_names(page.page_html({}, T0))
    assert set(show.SORT_COLUMNS) <= names


# --- card links ---------------------------------------------------------------


def test_a_row_carries_its_card_and_a_bad_one_is_none(document: dict[str, Any]) -> None:
    rows = {row["name"]: row for row in page.page_rows(document)}
    opus = rows["anthropic/claude-opus-5-5"]
    assert (opus["card_kind"], opus["card_url"]) == (
        "vendor",
        "https://vendor.example/anthropic/models",
    )

    doc = copy.deepcopy(document)
    doc["models"][0]["card_url"] = "javascript:alert(1)"
    doc["models"][1]["card_kind"] = "somewhere"
    doc["models"][1]["card_url"] = "https://ok.example/m"
    doc["models"][2]["card_url"] = 7
    bad = page.page_rows(doc)
    assert [(r["card_kind"], r["card_url"]) for r in bad[:3]] == [(None, None)] * 3


def test_a_hostile_address_never_reaches_the_pages_data(document: dict[str, Any]) -> None:
    doc = copy.deepcopy(document)
    doc["models"][0]["card_url"] = 'https://x.example/"><script>alert(1)</script>'
    text = page.page_html(doc, T0)
    assert "<script>alert(1)" not in text
    # An address with no space or control character is still only ever an attribute value.
    assert "</script><script" not in text


def test_the_pages_script_knows_every_kind_of_card() -> None:
    text = page.page_html({}, T0)
    (block,) = re.findall(r"const CARDS = \{(.*?)^\s*\};", text, re.DOTALL | re.MULTILINE)
    assert set(re.findall(r"^\s*(\w+): \{ tag:", block, re.MULTILINE)) == set(cards.KINDS)


def test_the_canonical_cell_is_the_link_and_nothing_is_written_as_markup() -> None:
    text = page.page_html({}, T0)
    assert 'column.key === "canonical"' in text
    assert "link.href = row.card_url;" in text
    assert 'link.rel = "noopener noreferrer";' in text
    assert "innerHTML" not in text
