import copy
import json
import re
from datetime import timedelta
from typing import Any

from conftest import T0

from modeltiers import page

_DATA = re.compile(r'<script id="data" type="application/json">(.*?)</script>', re.DOTALL)


def _data(text: str) -> dict[str, Any]:
    """The JSON a page carries, as its own script would read it."""
    (found,) = _DATA.findall(text)
    loaded = json.loads(found)
    assert isinstance(loaded, dict)
    return loaded


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
        "code": "S",
        "confidence": "high",
        "reason": None,
        "open_weights": True,
        "context": 1048576,
        "intelligence_index": 44.8,
        "arena_agent": 0.02147446416824893,
        "eci": 155.61,
        "coding_index": 74.8,
        "agentic_index": 53.1,
    }
    # An ignored row: no key, no flag, no score.
    assert rows["ollama/nomic-embed-text:latest"] == {
        "kind": "model",
        "name": "ollama/nomic-embed-text:latest",
        "canonical": None,
        "tier": "unrated",
        "code": "-",
        "confidence": "none",
        "reason": "not_applicable",
        "open_weights": None,
        "context": None,
        "intelligence_index": None,
        "arena_agent": None,
        "eci": None,
        "coding_index": None,
        "agentic_index": None,
    }
    # An unmapped row is unrated, and says why.
    unmapped = rows["ollama/medgemma:27b"]
    assert (unmapped["tier"], unmapped["code"], unmapped["reason"]) == ("unrated", "-", "unmapped")
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
    assert not re.search(r"\b(src|href|action)\s*=", text)
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
