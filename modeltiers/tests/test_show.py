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
    "  aligned:     44.3 (intelligence_index 44.8; arena_agent 0.0215 as 44.1; eci 155.61 as 44.1)",
    "  efforts:     arena_agent max",
    "  tool_call:   true",
    "  context:     1048576",
    "  open_weights: true",
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
    assert lines[:11] == GLM_CLOUD
    assert lines[11] == "openrouter/z-ai/glm-5.3"
    assert lines[-4:] == ATTRIBUTION
    assert len(lines) == 11 + 11 + 4


def test_a_pair_that_looks_like_a_key_is_tried_as_a_pair_first(document: dict[str, Any]) -> None:
    doc = copy.deepcopy(document)
    # An inventory row whose provider_id/model_name spells another row's key.
    doc["models"][4].update(provider_id="zhipuai", model_name="glm-5.3")
    lines = show.explain_lines(doc, "zhipuai/glm-5.3")
    assert lines is not None
    assert [line for line in lines if not line.startswith("  ")][0] == "zhipuai/glm-5.3"
    assert "  identity:    minimax/MiniMax-M3, resolved by normalized" in lines


def test_an_unmapped_pair_does_not_shadow_the_canonical_key(
    document: dict[str, Any],
) -> None:
    doc = copy.deepcopy(document)
    # An [[unmapped]] row whose pair is a canonical key other models resolved
    # to: the key search still answers first.
    doc["unmapped"].append({"provider_id": "zhipuai", "model_name": "glm-5.3"})
    assert show.explain_lines(doc, "zhipuai/glm-5.3")[:11] == GLM_CLOUD


def test_explain_a_candidate_by_its_key_in_any_case(document: dict[str, Any]) -> None:
    assert show.explain_lines(document, "anthropic/Claude-Opus-5-5") == [
        "candidate anthropic/claude-opus-5-5",
        "  identity:    anthropic/claude-opus-5-5, resolved by host",
        "  tier:        marathon (M)",
        "  confidence:  high",
        "  votes:       intelligence_index deep, arena_agent deep, eci deep",
        "  scores:      intelligence_index 57.6, arena_agent 0.1433, "
        "arena_agent_ci_lower 0.1215, eci 167.33",
        "  aligned:     57 (intelligence_index 57.6; arena_agent 0.1433 as 56.7; eci 167.33 as 56.8)",
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
        "  aligned:     none",
        "  efforts:     none",
        "  tool_call:   true",
        "  context:     unknown",
        "  open_weights: true",
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
        "  aligned:     33.7 (intelligence_index 33.7)",
        "  efforts:     none",
        "  tool_call:   true",
        "  context:     unknown",
        "  open_weights: true",
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
        "  aligned:     none",
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
        "  aligned:     none",
        "  efforts:     none",
        "  tool_call:   unknown",
        "  context:     unknown",
    ]


def test_a_shape_broken_file_prints_like_an_empty_one() -> None:
    # [models] as a single table, unmapped as a string and a non-table
    # candidate entry: compared by whatever they have, as an absent array.
    broken = {"models": {"a": 1}, "unmapped": "?", "candidates": [3]}
    assert show.table_lines(broken) == show.table_lines({})
    vote = {"models": [{"provider_id": "a", "model_name": "b", "votes": "high"}]}
    assert "  votes:       high" in show.explain_lines(vote, "a/b")


def test_show_imports_nothing_of_the_package_but_the_constants() -> None:
    """`show` and `explain` work from the file alone: the module that prints
    them takes constants from `contracts` and the name of the anchor signal
    from `align`, and nothing from the pipeline, the sources or the writer."""
    assert show.__file__ is not None
    tree = ast.parse(Path(show.__file__).read_text())
    imported = {
        node.module
        for node in ast.walk(tree)
        if isinstance(node, ast.ImportFrom) and node.module and node.module.startswith("modeltiers")
    }
    assert imported == {"modeltiers.contracts", "modeltiers.align"}
    names = {
        alias.name
        for node in ast.walk(tree)
        if isinstance(node, ast.ImportFrom) and node.module == "modeltiers.align"
        for alias in node.names
    }
    assert names == {"ANCHOR"}
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


# --- sort and wide --------------------------------------------------------------


def _models(lines: list[str]) -> list[str]:
    """The model column of the inventory rows of a plain table."""
    end = next(i for i, line in enumerate(lines) if line.startswith("unmapped:"))
    return [line.split()[3] for line in lines[1:end]]


def test_sorting_by_a_score_puts_the_highest_first_and_the_unscored_last(
    document: dict[str, Any],
) -> None:
    assert _models(show.table_lines(document, sort="intelligence_index")) == [
        "ollama/glm-5.3:cloud",  # 44.8
        "openrouter/z-ai/glm-5.3",  # 44.8: a tie keeps the file's order
        "ollama/qwen3.8:27b-mlx",  # 33.7
        "ollama/deepseek-v4-pro:cloud",  # no index: the file's order from here
        "ollama/minimax-m3:cloud",
        "ollama/nomic-embed-text:latest",
        "ollama/gemma4:31b-mlx",
    ]
    assert _models(show.table_lines(document, sort="eci"))[:2] == [
        "ollama/glm-5.3:cloud",  # 155.61
        "openrouter/z-ai/glm-5.3",
    ]


def test_sorting_by_model_is_alphabetical_and_by_context_is_largest_first(
    document: dict[str, Any],
) -> None:
    assert _models(show.table_lines(document, sort="model")) == [
        "ollama/deepseek-v4-pro:cloud",
        "ollama/gemma4:31b-mlx",
        "ollama/glm-5.3:cloud",
        "ollama/minimax-m3:cloud",
        "ollama/nomic-embed-text:latest",
        "ollama/qwen3.8:27b-mlx",
        "openrouter/z-ai/glm-5.3",
    ]
    by_context = _models(show.table_lines(document, sort="context"))
    assert by_context[:4] == [
        "ollama/glm-5.3:cloud",  # 1048576
        "ollama/deepseek-v4-pro:cloud",  # 1048576
        "openrouter/z-ai/glm-5.3",  # 1048576
        "ollama/minimax-m3:cloud",  # 512000
    ]


def test_sorting_by_tier_puts_the_highest_rank_first(document: dict[str, Any]) -> None:
    doc = copy.deepcopy(document)
    doc["models"].reverse()
    doc["models"][0]["tier"] = "no such tier"  # an unknown tier sorts last
    lines = show.table_lines(doc, sort="tier")
    end = next(i for i, line in enumerate(lines) if line.startswith("unmapped:"))
    assert [line.split()[1] for line in lines[1:end]] == [
        "standard",
        "standard",
        "standard",
        "standard",
        "quick",
        "unrated",
        "no",
    ]


def test_candidates_are_sorted_among_themselves(document: dict[str, Any]) -> None:
    lines = show.table_lines(document, candidates=True, sort="model")
    assert lines[-2:] == [
        "D  deep      high    anthropic/claude-fable-5",
        "M  marathon  high    anthropic/claude-opus-5-5",
    ]
    lines = show.table_lines(document, candidates=True, sort="eci")
    assert [line.split()[-1] for line in lines[-2:]] == [
        "anthropic/claude-opus-5-5",  # 167.33
        "anthropic/claude-fable-5",  # 162.06
    ]
    # The inventory rows above them are sorted on their own.
    assert _models(lines)[0] == "ollama/glm-5.3:cloud"


def test_a_value_that_is_not_a_number_sorts_as_missing() -> None:
    doc = {
        "models": [
            {"provider_id": "p", "model_name": "text", "scores": {"eci": "high"}},
            {"provider_id": "p", "model_name": "flag", "scores": {"eci": True}},
            {"provider_id": "p", "model_name": "table", "scores": "none"},
            {"provider_id": "p", "model_name": "nan", "scores": {"eci": float("nan")}},
            {"provider_id": "p", "model_name": "inf", "scores": {"eci": float("inf")}},
            # TOML integers have no upper bound; a float does.
            {
                "provider_id": "p",
                "model_name": "huge",
                "context": 10**400,
                "scores": {"eci": 10**400},
            },
            {"provider_id": "p", "model_name": "scored", "scores": {"eci": 150.0}},
        ]
    }
    lines = show.table_lines(doc, sort="eci", wide=True)
    assert [line.split()[-2] for line in lines[1:8]] == [
        "p/scored",
        "p/text",
        "p/flag",
        "p/table",
        "p/nan",
        "p/inf",
        "p/huge",
    ]
    # Each of them is a dash in its own column, not a score.
    assert [line.split()[7] for line in lines[1:8]] == ["150", "-", "-", "-", "-", "-", "-"]
    assert show.table_lines(doc, sort="context", wide=True)[7].split()[4] == "-"


def test_a_score_that_rounds_to_zero_from_below_prints_as_zero() -> None:
    assert show.short(-0.00001) == "0"
    assert show.short(-0.0001) == "-0.0001"
    assert show.short(0.0) == "0"


def test_sorting_by_model_ignores_case_and_candidates_follow_the_score() -> None:
    doc = {
        "models": [
            {"provider_id": "p", "model_name": "beta"},
            {"provider_id": "p", "model_name": "Alpha"},
            {"provider_id": "P", "model_name": "aardvark"},
        ],
        "candidates": [
            {"canonical": "v/low", "scores": {"eci": 140.0}},
            {"canonical": "v/none"},
            {"canonical": "v/high", "scores": {"eci": 160.0}},
        ],
    }
    assert _models(show.table_lines(doc, sort="model")) == ["P/aardvark", "p/Alpha", "p/beta"]
    lines = show.table_lines(doc, candidates=True, sort="eci")
    assert [line.split()[-1] for line in lines[-3:]] == ["v/high", "v/low", "v/none"]


WIDE = [
    "T  tier      conf    open   context     intel     arena       eci    coding   agentic"
    "   aligned  src  model                           canonical",
    "S  standard  high    T      1048576      44.8    0.0215    155.61      74.8      53.1"
    "      44.3    3  ollama/glm-5.3:cloud            zhipuai/glm-5.3",
    "S  standard  high    T      1048576         -    0.0134    155.31         -         -"
    "      43.5    2  ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813",
]


def test_the_wide_table_adds_open_weights_context_and_the_scores(
    document: dict[str, Any],
) -> None:
    lines = show.table_lines(document, candidates=True, wide=True)
    assert lines[:3] == WIDE
    # An ignored row knows none of them; a local build has no served context.
    assert lines[6] == (
        "-  unrated   none    -            -         -         -         -         -         -"
        "         -    -  ollama/nomic-embed-text:latest  -  (not_applicable)"
    )
    assert lines[4].split()[3:6] == ["T", "-", "33.7"]
    # A candidate row has the same columns before its key.
    assert lines[-2] == (
        "M  marathon  high    F      1000000      57.6    0.1433    167.33"
        "         -         -        57    3  anthropic/claude-opus-5-5"
    )
    assert len(lines) == len(show.table_lines(document, candidates=True))


def test_the_wide_table_of_an_empty_file_is_its_header() -> None:
    assert show.table_lines({}, wide=True)[0] == (
        "T  tier      conf    open   context     intel     arena       eci    coding   agentic"
        "   aligned  src  model  canonical"
    )


# --- the aligned score ------------------------------------------------------------


def test_sorting_by_aligned_ranks_models_that_different_sources_scored(
    document: dict[str, Any],
) -> None:
    assert _models(show.table_lines(document, sort="aligned")) == [
        "ollama/glm-5.3:cloud",  # 44.3
        "openrouter/z-ai/glm-5.3",  # 44.3
        "ollama/deepseek-v4-pro:cloud",  # 43.5, with no index of its own
        "ollama/minimax-m3:cloud",  # 34.7, likewise
        "ollama/qwen3.8:27b-mlx",  # 33.7
        "ollama/nomic-embed-text:latest",  # none: the file's order
        "ollama/gemma4:31b-mlx",
    ]
    assert "aligned" in show.SORT_COLUMNS


def test_the_wide_table_ends_its_scores_with_the_aligned_score_and_its_source_count(
    document: dict[str, Any],
) -> None:
    lines = show.table_lines(document, candidates=True, wide=True)
    heading, glm, deepseek = (line.split() for line in lines[:3])
    assert heading[10:12] == ["aligned", "src"]
    assert glm[10:12] == ["44.3", "3"]
    assert deepseek[10:12] == ["43.5", "2"]
    assert lines[6].split()[10:12] == ["-", "-"]  # the ignored row
    assert lines[-2].split()[10:12] == ["57", "3"]  # Opus 5.5, a candidate


def test_an_aligned_score_of_the_wrong_type_is_a_dash_and_sorts_last() -> None:
    doc = {
        "models": [
            {"provider_id": "p", "model_name": "text", "aligned_score": "high"},
            {"provider_id": "p", "model_name": "bare", "aligned_score": 40.0},
            {"provider_id": "p", "model_name": "odd", "aligned_score": 50.0, "aligned_sources": 3},
        ]
    }
    lines = show.table_lines(doc, sort="aligned", wide=True)
    assert [line.split()[-2] for line in lines[1:4]] == ["p/odd", "p/bare", "p/text"]
    assert [line.split()[10:12] for line in lines[1:4]] == [["50", "-"], ["40", "-"], ["-", "-"]]


def test_explain_says_what_the_aligned_score_is_made_of(document: dict[str, Any]) -> None:
    lines = show.explain_lines(document, "ollama/deepseek-v4-pro:cloud")
    assert lines is not None
    assert "  aligned:     43.5 (arena_agent 0.0134 as 43.3; eci 155.31 as 43.8)" in lines
    glm = show.explain_lines(document, "ollama/glm-5.3:cloud")
    assert glm is not None
    assert (
        "  aligned:     44.3 (intelligence_index 44.8; arena_agent 0.0215 as 44.1; "
        "eci 155.61 as 44.1)" in glm
    )
    # It follows the scores it was made from.
    assert glm.index(next(line for line in glm if line.startswith("  aligned:"))) == (
        glm.index(next(line for line in glm if line.startswith("  scores:"))) + 1
    )
    ignored = show.explain_lines(document, "ollama/nomic-embed-text:latest")
    assert ignored is not None
    assert "  aligned:     none" in ignored


def test_explain_without_the_files_fits_names_the_sources_alone(
    document: dict[str, Any],
) -> None:
    doc = copy.deepcopy(document)
    del doc["alignment"]["arena_agent"]
    doc["alignment"]["eci"] = "gone"
    lines = show.explain_lines(doc, "ollama/glm-5.3:cloud")
    assert lines is not None
    assert "  aligned:     44.3 (intelligence_index 44.8; arena_agent; eci)" in lines
    # A source the entry lists and has no score for is still named.
    del doc["models"][0]["scores"]["eci"]
    lines = show.explain_lines(doc, "ollama/glm-5.3:cloud")
    assert lines is not None
    assert "  aligned:     44.3 (intelligence_index 44.8; arena_agent; eci)" in lines
    doc["models"][0]["aligned_sources"] = "all"
    lines = show.explain_lines(doc, "ollama/glm-5.3:cloud")
    assert lines is not None
    assert "  aligned:     44.3" in lines
