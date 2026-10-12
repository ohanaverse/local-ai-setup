import ast
import copy
from datetime import timedelta
from pathlib import Path
from typing import Any

from conftest import T0

from modeltiers import show

ATTRIBUTION = [
    "modelsdev: models.dev (https://models.dev), MIT",
    "openrouter: Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter "
    "(openrouter.ai/rankings).",
    "arena: Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0",
    "epoch: Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0",
]

TABLE = [
    "T  tier      conf    model                           canonical",
    "S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3",
    "S  standard  high    ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813",
    "S  standard  high    openrouter/z-ai/glm-5.3         zhipuai/glm-5.3",
    "S  standard  low     ollama/qwen3.8:27b-mlx          alibaba/qwen3.8-27b",
    "Q  quick     high    ollama/minimax-m3:cloud         minimax/MiniMax-M3",
    "-  unrated   none    ollama/nomic-embed-text:latest  -  (not_applicable)",
    "-  unrated   none    ollama/gemma4:31b-mlx           google/gemma-4-31b-it  (no_votes)",
    "unmapped: 1 (unrated until aliased or ignored)",
    "  ollama/medgemma:27b",
]


def test_the_table_lists_inventory_models_and_counts_the_candidates(
    document: dict[str, Any],
) -> None:
    assert show.table_lines(document) == [
        *TABLE,
        "candidates: 2 (list them with --candidates)",
    ]


def test_the_table_lists_candidates_when_asked(document: dict[str, Any]) -> None:
    assert show.table_lines(document, candidates=True) == [
        *TABLE,
        "candidates: 2 (scored by a source, not in the inventory)",
        "M  marathon  high    anthropic/claude-opus-5-5",
        "D  deep      high    anthropic/claude-fable-5",
    ]


def test_an_empty_file_still_prints_a_table() -> None:
    assert show.table_lines({}, candidates=True) == [
        "T  tier      conf    model  canonical",
        "unmapped: 0 (unrated until aliased or ignored)",
        "candidates: 0 (scored by a source, not in the inventory)",
    ]


def test_age_in_whole_days_and_the_warning_past_fourteen(document: dict[str, Any]) -> None:
    assert show.age_line(document, T0) == (
        "tiers.toml generated 2026-10-12T15:58:00Z (0 days old), contracts fixture-1"
    )
    fourteen = T0 + timedelta(days=14, hours=23)
    assert show.age_days(document, fourteen) == 14
    assert show.stale_warning(document, fourteen) is None
    fifteen = T0 + timedelta(days=15)
    assert "(15 days old)" in show.age_line(document, fifteen)
    assert show.stale_warning(document, fifteen) == (
        "tiers.toml is 15 days old; run `modeltiers run` to refresh it"
    )
    # A clock behind the file is not a negative age.
    assert show.age_days(document, T0 - timedelta(days=2)) == 0


def test_a_file_with_no_readable_date_has_no_age() -> None:
    for doc in ({}, {"generated_at": "last tuesday"}, {"generated_at": 20261012}):
        assert show.age_days(doc, T0) is None
        assert show.stale_warning(doc, T0) is None
    assert show.age_line({}, T0) == "tiers.toml generated ? (of unknown age), contracts ?"


def test_attribution_lines_for_every_source_or_for_some(document: dict[str, Any]) -> None:
    assert show.attribution_lines(document) == ATTRIBUTION
    assert show.attribution_lines(document, {"epoch", "modelsdev"}) == [
        ATTRIBUTION[0],
        ATTRIBUTION[3],
    ]
    # An older file without the strings prints none rather than failing.
    assert show.attribution_lines({"sources": {"arena": {"status": "ok"}}}) == []
    assert show.attribution_lines({}) == []


GLM_CLOUD = [
    "ollama/glm-5.3:cloud",
    "  identity:    zhipuai/glm-5.3, resolved by host",
    "  tier:        standard (S)",
    "  confidence:  high, basis measured",
    "  votes:       intelligence_index standard, arena_agent standard, eci standard",
    "  scores:      intelligence_index 44.8, coding_index 74.8, agentic_index 53.1, "
    "arena_agent 0.0215, arena_agent_ci_lower 0.0143, eci 155.61",
    "  efforts:     arena_agent max",
    "  tool_call:   true",
    "  context:     1048576",
]


def test_explain_by_exact_provider_and_model_name(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/glm-5.3:cloud") == [*GLM_CLOUD, *ATTRIBUTION]
    # The pair is exact: another case is not this row.
    assert show.explain_lines(document, "Ollama/GLM-5.3:cloud") is None


def test_explain_by_canonical_key_prints_every_model_that_resolved_to_it(
    document: dict[str, Any],
) -> None:
    lines = show.explain_lines(document, "ZhipuAI/GLM-5.3")
    assert lines is not None
    assert lines[:9] == GLM_CLOUD
    assert lines[9] == "openrouter/z-ai/glm-5.3"
    assert lines[-4:] == ATTRIBUTION
    assert len(lines) == 9 + 9 + 4


def test_a_pair_that_looks_like_a_key_is_tried_as_a_pair_first(document: dict[str, Any]) -> None:
    doc = copy.deepcopy(document)
    # An inventory row whose provider_id/model_name spells another row's key.
    doc["models"][4].update(provider_id="zhipuai", model_name="glm-5.3")
    lines = show.explain_lines(doc, "zhipuai/glm-5.3")
    assert lines is not None
    assert [line for line in lines if not line.startswith("  ")][0] == "zhipuai/glm-5.3"
    assert "  identity:    minimax/MiniMax-M3, resolved by normalized" in lines


def test_explain_a_candidate_by_its_key_in_any_case(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "anthropic/Claude-Opus-5-5") == [
        "candidate anthropic/claude-opus-5-5",
        "  identity:    anthropic/claude-opus-5-5, resolved by host",
        "  tier:        marathon (M)",
        "  confidence:  high",
        "  votes:       intelligence_index deep, arena_agent deep, eci deep",
        "  scores:      intelligence_index 57.6, arena_agent 0.1433, "
        "arena_agent_ci_lower 0.1215, eci 167.33",
        "  efforts:     arena_agent high",
        "  tool_call:   true",
        "  context:     1000000",
        "  open_weights: false",
        "  note:        context is the largest among the key's models.dev rows",
        *ATTRIBUTION,
    ]


def test_explain_shows_the_reason_the_notes_and_only_the_sources_used(
    document: dict[str, Any],
) -> None:
    # No votes: models.dev identified it and nothing scored it.
    assert show.explain_lines(document, "ollama/gemma4:31b-mlx") == [
        "ollama/gemma4:31b-mlx",
        "  identity:    google/gemma-4-31b-it, resolved by normalized",
        "  tier:        unrated (-), reason no_votes",
        "  confidence:  none, basis inherited",
        "  votes:       none",
        "  scores:      none",
        "  efforts:     none",
        "  tool_call:   true",
        "  context:     unknown",
        "  note:        local build: served context unknown; context gate skipped",
        ATTRIBUTION[0],
    ]
    # Two votes, neither from OpenRouter.
    deepseek = show.explain_lines(document, "ollama/deepseek-v4-pro:cloud")
    assert deepseek is not None
    assert deepseek[-3:] == [ATTRIBUTION[0], ATTRIBUTION[2], ATTRIBUTION[3]]


def test_explain_a_local_build_rated_through_inheritance(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/qwen3.8:27b-mlx") == [
        "ollama/qwen3.8:27b-mlx",
        "  identity:    alibaba/qwen3.8-27b, resolved by normalized",
        "  tier:        standard (S)",
        "  confidence:  low, basis inherited",
        "  votes:       intelligence_index standard",
        "  scores:      intelligence_index 33.7, coding_index 68.1, agentic_index 45.8",
        "  efforts:     none",
        "  tool_call:   true",
        "  context:     unknown",
        "  note:        local build: served context unknown; context gate skipped",
        ATTRIBUTION[0],
        ATTRIBUTION[1],
    ]


def test_explain_an_ignored_row(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/nomic-embed-text:latest") == [
        "ollama/nomic-embed-text:latest",
        "  identity:    not resolved (listed under [[ignore]])",
        "  tier:        unrated (-), reason not_applicable",
        "  confidence:  none, basis inherited",
        "  votes:       none",
        "  scores:      none",
        "  efforts:     none",
        "  tool_call:   unknown",
        "  context:     unknown",
        "  note:        ignored: embedding model",
    ]


def test_explain_an_unmapped_row_says_it_needs_an_alias(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/medgemma:27b") == [
        "ollama/medgemma:27b is unmapped: no alias, host row or name match gave it a "
        "canonical key, so it is unrated.",
        "Add an [[alias]] or an [[ignore]] for it in aliases.toml and run `modeltiers run`.",
    ]


def test_explain_with_no_match_is_none(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "ollama/absent:cloud") is None
    assert show.explain_lines(document, "") is None
    assert show.explain_lines({}, "zhipuai/glm-5.3") is None


def test_an_entry_from_an_older_file_prints_question_marks() -> None:
    old = {"models": [{"provider_id": "ollama", "model_name": "m", "canonical": "v/m"}]}
    assert show.table_lines(old)[1] == "?  ?         ?       ollama/m  v/m"
    assert show.explain_lines(old, "v/M") == [
        "ollama/m",
        "  identity:    v/m, resolved by ?",
        "  tier:        ? (?)",
        "  confidence:  ?",
        "  votes:       none",
        "  scores:      none",
        "  efforts:     none",
        "  tool_call:   unknown",
        "  context:     unknown",
    ]


def test_show_imports_nothing_of_the_package_but_the_constants() -> None:
    """`show` and `explain` work from the file alone: the module that prints
    them takes two constants from `contracts` and nothing from the pipeline,
    the sources or the writer."""
    assert show.__file__ is not None
    tree = ast.parse(Path(show.__file__).read_text())
    imported = {
        node.module
        for node in ast.walk(tree)
        if isinstance(node, ast.ImportFrom) and node.module and node.module.startswith("modeltiers")
    }
    assert imported == {"modeltiers.contracts"}
    assert not [
        alias.name
        for node in ast.walk(tree)
        if isinstance(node, ast.Import)
        for alias in node.names
        if alias.name.startswith("modeltiers")
    ]


def test_short_scores() -> None:
    assert [show.short(v) for v in (44.8, 0.02147446, -0.0695464, 150.0, 7, "x")] == [
        "44.8",
        "0.0215",
        "-0.0695",
        "150",
        "7",
        "x",
    ]
