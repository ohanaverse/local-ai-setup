import copy
from pathlib import Path
from typing import Any

from modeltiers import changes

PATH = Path("/config/local-ai/tiers.toml")
STEADY = [
    "  confidence only: 0",
    "  candidates: 0 added, 0 removed, 0 changed tier",
    "  sources: modelsdev ok, openrouter ok, arena ok, epoch ok",
]


def test_with_no_existing_file_it_says_so_and_prints_the_full_table(
    document: dict[str, Any],
) -> None:
    lines = changes.change_lines(None, document, PATH)
    assert (
        lines[0]
        == "changes: no existing file at /config/local-ai/tiers.toml; the full table follows"
    )
    assert lines[1] == "T  tier      conf    model                           canonical"
    assert lines[2] == "S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3"
    assert lines[-1] == "candidates: 2 (list them with --candidates)"
    assert len(lines) == 1 + 11


def test_the_same_document_twice_reports_nothing(document: dict[str, Any]) -> None:
    assert changes.change_lines(document, document, PATH) == [
        "changes since 2026-10-12T15:58:00Z:",
        "  no model changed tier, was added or was removed",
        *STEADY,
    ]


def test_tier_changes_added_removed_newly_unmapped_and_newly_resolved(
    document: dict[str, Any],
) -> None:
    old = copy.deepcopy(document)
    models = {m["model_name"]: m for m in old["models"]}
    models["glm-5.3:cloud"]["tier"] = "deep"  # was deep, is standard
    models["minimax-m3:cloud"]["confidence"] = "medium"  # the tier is the same
    models["z-ai/glm-5.3"]["confidence"] = "low"
    # deepseek was unmapped and is now resolved; medgemma was a model and is now unmapped.
    old["models"].remove(models["deepseek-v4-pro:cloud"])
    old["models"].remove(models["gemma4:31b-mlx"])  # not in the old file: added
    old["models"].append(
        {
            "provider_id": "ollama",
            "model_name": "medgemma:27b",
            "tier": "quick",
            "confidence": "low",
        }
    )
    old["models"].append(
        {"provider_id": "ollama", "model_name": "gone:cloud", "tier": "deep", "confidence": "high"}
    )
    old["unmapped"] = [
        {"provider_id": "ollama", "model_name": "deepseek-v4-pro:cloud"},
        {"provider_id": "ollama", "model_name": "left-unmapped:1b"},
    ]
    assert changes.change_lines(old, document, PATH) == [
        "changes since 2026-10-12T15:58:00Z:",
        "  tier: ollama/glm-5.3:cloud deep -> standard",
        "  newly resolved: ollama/deepseek-v4-pro:cloud standard",
        "  added: ollama/gemma4:31b-mlx unrated",
        "  newly unmapped: ollama/medgemma:27b (was quick)",
        "  removed: ollama/gone:cloud (was deep)",
        "  removed: ollama/left-unmapped:1b (was unmapped)",
        "  confidence only: 2",
        "  candidates: 0 added, 0 removed, 0 changed tier",
        "  sources: modelsdev ok, openrouter ok, arena ok, epoch ok",
    ]


def test_a_row_an_ignore_now_covers_is_now_ignored_not_newly_resolved(
    document: dict[str, Any],
) -> None:
    old = copy.deepcopy(document)
    ignored = next(m for m in old["models"] if m["model_name"] == "nomic-embed-text:latest")
    old["models"].remove(ignored)
    old["unmapped"].append({"provider_id": "ollama", "model_name": "nomic-embed-text:latest"})
    assert changes.change_lines(old, document, PATH) == [
        "changes since 2026-10-12T15:58:00Z:",
        "  now ignored: ollama/nomic-embed-text:latest (embedding model)",
        *STEADY,
    ]
    # A row that was already ignored is no change at all.
    assert changes.change_lines(document, document, PATH)[1].startswith("  no model changed")


def test_a_row_added_as_unmapped_says_so(document: dict[str, Any]) -> None:
    old = copy.deepcopy(document)
    del old["unmapped"]
    assert changes.change_lines(old, document, PATH)[1] == "  added: ollama/medgemma:27b unmapped"


def test_candidate_changes_are_counts(document: dict[str, Any]) -> None:
    old = copy.deepcopy(document)
    old["candidates"][0]["tier"] = "deep"  # Opus 5.5 was deep, is marathon
    old["candidates"][1]["canonical"] = "Anthropic/Claude-Fable-5"  # the same key
    old["candidates"].append({"canonical": "openai/gpt-6-astra", "tier": "marathon"})
    new = copy.deepcopy(document)
    new["candidates"].append({"canonical": "xiaomi/mimo-v2.6-pro", "tier": "standard"})
    assert changes.change_lines(old, new, PATH)[-2] == (
        "  candidates: 1 added, 1 removed, 1 changed tier"
    )


def test_a_source_status_that_moved_shows_what_it_was(document: dict[str, Any]) -> None:
    new = copy.deepcopy(document)
    new["sources"]["arena"]["status"] = "stale"
    assert changes.change_lines(document, new, PATH)[-1] == (
        "  sources: modelsdev ok, openrouter ok, arena stale (was ok), epoch ok"
    )


def test_a_file_from_an_older_version_is_compared_by_the_fields_it_has(
    document: dict[str, Any],
) -> None:
    # No generated_at, no sources, no confidence, no candidates, a row with no tier.
    old = {
        "models": [
            {"provider_id": "ollama", "model_name": "glm-5.3:cloud", "tier": "standard"},
            {"provider_id": "ollama", "model_name": "minimax-m3:cloud"},
        ]
    }
    lines = changes.change_lines(old, document, PATH)
    assert lines[0] == "changes since an undated file:"
    assert "  tier: ollama/minimax-m3:cloud ? -> quick" in lines
    assert "  added: openrouter/z-ai/glm-5.3 standard" in lines
    assert lines[-3:] == [
        "  confidence only: 1",  # glm-5.3:cloud: the old file gave no confidence
        "  candidates: 2 added, 0 removed, 0 changed tier",
        "  sources: modelsdev ok (was ?), openrouter ok (was ?), arena ok (was ?), "
        "epoch ok (was ?)",
    ]
