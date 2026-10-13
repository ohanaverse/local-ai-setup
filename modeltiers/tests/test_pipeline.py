from collections.abc import Callable
from dataclasses import replace

import pytest

from modeltiers import aliases as aliases_mod
from modeltiers import pipeline
from modeltiers.aliases import Alias, Aliases
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.errors import ConfigError
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment
from modeltiers.records import Inventory, ModelsDevRow, ScoreRecord


def _outcomes(result: pipeline.Identity) -> list[tuple[str, str | None, str | None]]:
    return [(r.row.model_name, r.resolved_by, r.canonical) for r in result.rows]


def test_every_inventory_row_gets_one_outcome_in_wt_order(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases
) -> None:
    result = pipeline.resolve_all(inventory, sources, fixture_aliases)
    assert _outcomes(result) == [
        ("glm-5.3:cloud", "host", "zhipuai/glm-5.3"),
        ("deepseek-v4-pro:cloud", "alias", "deepseek/deepseek-v4-pro-0813"),
        ("z-ai/glm-5.3", "host", "zhipuai/glm-5.3"),
        ("qwen3.8:27b-mlx", "normalized", "alibaba/qwen3.8-27b"),
        ("medgemma:27b", None, None),
        ("minimax-m3:cloud", "normalized", "minimax/MiniMax-M3"),
        ("nomic-embed-text:latest", None, None),
        ("gemma4:31b-mlx", "normalized", "google/gemma-4-31b-it"),
    ]
    assert [r.row.model_name for r in result.unmapped] == ["medgemma:27b"]
    assert [r.row.model_name for r in result.review] == [
        "qwen3.8:27b-mlx",
        "minimax-m3:cloud",
        "gemma4:31b-mlx",
    ]
    by_name = {r.row.model_name: r for r in result.rows}
    assert by_name["nomic-embed-text:latest"].ignore_reason == "embedding model"
    assert by_name["qwen3.8:27b-mlx"].basis == "inherited"
    assert by_name["minimax-m3:cloud"].notes == ("host row has no canonical id; matched by name",)
    # The host row found on the way, canonical or not: step 3 reads its context.
    assert by_name["minimax-m3:cloud"].host_row is not None
    assert by_name["minimax-m3:cloud"].host_row.context == 512000
    assert result.warnings == ()


def test_the_three_sources_resolve_against_the_same_index(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases
) -> None:
    result = pipeline.resolve_all(inventory, sources, fixture_aliases)
    assert [s.source for s in result.sources()] == ["openrouter", "arena", "epoch"]
    assert [(s.records, len(s.best), len(s.unresolved), s.ignored) for s in result.sources()] == [
        (6, 4, 1, 0),  # glm-5.3 and its :batch share a key; lfm has no key
        (6, 5, 1, 0),  # Qwen3.8 Max has no key in the fixture
        (6, 5, 0, 1),  # GPT-5 is ignored by the fixture aliases
    ]
    assert result.arena.best["zhipuai/glm-5.3"].record.effort == "max"
    assert result.arena.best["deepseek/deepseek-v4-pro-0813"].resolved_by == "alias"
    assert result.epoch.best["minimax/minimax-m3"].canonical == "minimax/MiniMax-M3"
    assert [r.source_id for r in result.openrouter.unresolved] == ["liquid/lfm-2.5-2.6b:free"]
    assert result.index.spelling("ZHIPUAI/GLM-5.3") == "zhipuai/glm-5.3"


def test_without_the_alias_deepseek_cloud_reaches_the_april_key(
    inventory: Inventory, sources: Sources
) -> None:
    result = pipeline.resolve_all(inventory, sources, Aliases())
    by_name = {r.row.model_name: r for r in result.rows}
    assert by_name["deepseek-v4-pro:cloud"].canonical == "deepseek/deepseek-v4-pro"
    assert by_name["deepseek-v4-pro:cloud"].resolved_by == "normalized"
    assert by_name["nomic-embed-text:latest"].unmapped
    assert result.epoch.ignored == 0


def test_an_unanchored_alias_is_a_config_error(inventory: Inventory, sources: Sources) -> None:
    unanchored = aliases_mod.build(
        [Alias("google/gemini-4-argon", inventory=(("ollama", "medgemma:27b"),))], []
    )
    with pytest.raises(ConfigError, match=r"\(google/gemini-4-argon\): canonical is not a models"):
        pipeline.resolve_all(inventory, sources, unanchored)


def test_an_alias_id_absent_from_this_run_is_a_warning_not_an_error(
    inventory: Inventory, sources: Sources
) -> None:
    stale = aliases_mod.build(
        [Alias("zhipuai/glm-5.3", arena=("GLM 5.3",), epoch=("GLM-5.3", "GLM 5.3 (retired)"))], []
    )
    result = pipeline.resolve_all(inventory, sources, stale)
    assert result.warnings == (
        "alias for zhipuai/glm-5.3 names epoch id 'GLM 5.3 (retired)', "
        "which is not in this run's epoch data",
    )
    assert result.epoch.best["zhipuai/glm-5.3"].resolved_by == "alias"


def test_a_declared_key_spelt_like_a_models_dev_key_is_refused(
    inventory: Inventory, sources: Sources
) -> None:
    misspelt = aliases_mod.build([Alias("zhipuai/glm-5-3", epoch=("GLM-5.3",))], [])
    with pytest.raises(ConfigError, match="reads the same as the key zhipuai/glm-5.3"):
        pipeline.resolve_all(inventory, sources, misspelt)


# --- assign_all ---------------------------------------------------------------


def test_every_resolved_or_ignored_row_gets_a_tier_in_wt_order(assignment: Assignment) -> None:
    assert [
        (m.identity.row.model_name, m.verdict.tier, m.verdict.reason, m.evidence.confidence)
        for m in assignment.models
    ] == [
        ("glm-5.3:cloud", "standard", None, "high"),
        ("deepseek-v4-pro:cloud", "standard", None, "high"),
        ("z-ai/glm-5.3", "standard", None, "high"),
        ("qwen3.8:27b-mlx", "standard", None, "low"),
        ("minimax-m3:cloud", "quick", None, "high"),
        ("nomic-embed-text:latest", "unrated", "not_applicable", "none"),
        ("gemma4:31b-mlx", "unrated", "no_votes", "none"),
    ]
    # models plus unmapped is the inventory without its native rows.
    assert len(assignment.models) + len(assignment.identity.unmapped) == 8


def test_a_model_carries_its_votes_scores_and_the_best_records_effort(
    assignment: Assignment,
) -> None:
    glm = assignment.models[0]
    assert glm.evidence.votes == {
        "intelligence_index": "standard",
        "arena_agent": "standard",
        "eci": "standard",
    }
    assert glm.evidence.scores["coding_index"] == 74.8
    assert glm.evidence.efforts == {"arena_agent": "max"}
    # DeepSeek has no OpenRouter record in the fixture: two votes.
    deepseek = assignment.models[1]
    assert list(deepseek.evidence.votes) == ["arena_agent", "eci"]
    assert deepseek.evidence.efforts == {"arena_agent": "high"}


def test_context_comes_from_the_rows_own_host(assignment: Assignment) -> None:
    by_name = {m.identity.row.model_name: m for m in assignment.models}
    # The ollama-cloud row for minimax-m3 has no canonical id and serves
    # 512000; the key's only models.dev row (OpenRouter's) says 1048576.
    assert by_name["minimax-m3:cloud"].facts.context == 512000
    assert by_name["minimax-m3:cloud"].facts.notes == ()
    local = by_name["qwen3.8:27b-mlx"]
    assert (local.facts.tool_call, local.facts.context) == (True, None)
    assert local.verdict.notes == ("local build: served context unknown; context gate skipped",)


def test_a_local_row_is_rated_through_inheritance(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    """A local build takes its key's votes, tier and tool-calling flag: the
    row is `inherited` and rated, on a score no source gave under its name."""
    identity = pipeline.resolve_all(inventory, sources, fixture_aliases)
    by_name = {m.identity.row.model_name: m for m in pipeline.assign_all(identity, rule).models}
    local = by_name["qwen3.8:27b-mlx"]
    assert (local.identity.row.location, local.identity.basis) == ("local", "inherited")
    assert local.identity.canonical == "alibaba/qwen3.8-27b"
    # The key's one record is OpenRouter's `qwen/qwen3.8-27b`, joined by name.
    assert identity.openrouter.best["alibaba/qwen3.8-27b"].resolved_by == "normalized"
    assert local.evidence.votes == {"intelligence_index": "standard"}
    assert local.evidence.scores == {
        "intelligence_index": 33.7,
        "coding_index": 68.1,
        "agentic_index": 45.8,
    }
    assert (local.verdict.tier, local.verdict.reason) == ("standard", None)
    assert local.evidence.confidence == "low"  # one vote
    assert local.facts.tool_call is True  # the key's deepinfra row says so
    # With no inventory row the key is a candidate, with the same evidence.
    alone = pipeline.resolve_all(replace(inventory, rows=()), sources, fixture_aliases)
    (candidate,) = [
        c
        for c in pipeline.assign_all(alone, rule).candidates
        if c.canonical == local.identity.canonical
    ]
    assert candidate.evidence == local.evidence
    assert candidate.verdict.tier == local.verdict.tier
    # A local row whose key nothing scored inherits that too.
    gemma = by_name["gemma4:31b-mlx"]
    assert (gemma.identity.basis, gemma.verdict.tier, gemma.verdict.reason) == (
        "inherited",
        "unrated",
        "no_votes",
    )


def _with_rows(sources: Sources, change: Callable[[ModelsDevRow], ModelsDevRow]) -> Sources:
    """The golden sources with every models.dev row passed through `change`."""
    modelsdev = sources.modelsdev
    rows = tuple(change(row) for row in modelsdev.data.rows)
    return replace(sources, modelsdev=replace(modelsdev, data=replace(modelsdev.data, rows=rows)))


def _tiers(result: Assignment) -> dict[str, tuple[str, str | None]]:
    return {m.identity.row.model_name: (m.verdict.tier, m.verdict.reason) for m in result.models}


def test_a_key_no_row_says_calls_tools_is_unrated_gate_tool_call(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    keys = {"minimax/MiniMax-M3", "alibaba/qwen3.8-27b"}
    mute = _with_rows(
        sources, lambda row: replace(row, tool_call=False) if row.canonical in keys else row
    )
    result = pipeline.assign_all(pipeline.resolve_all(inventory, mute, fixture_aliases), rule)
    by_name = {m.identity.row.model_name: m for m in result.models}
    cloud = by_name["minimax-m3:cloud"]
    assert (cloud.verdict.tier, cloud.verdict.reason) == ("unrated", "gate_tool_call")
    # The votes, the confidence and the tier before the gates are untouched.
    assert (cloud.evidence.pre_gate, cloud.evidence.confidence) == ("quick", "high")
    # A local build inherits the flag, and the gate with it.
    local = by_name["qwen3.8:27b-mlx"]
    assert local.facts.tool_call is False
    assert (local.verdict.tier, local.verdict.reason) == ("unrated", "gate_tool_call")
    assert local.evidence.pre_gate == "standard"
    # The sentinel check reads the pre-gate tier: MiniMax-M3 still holds.
    assert result.sentinels_ok
    assert _tiers(result)["glm-5.3:cloud"] == ("standard", None)


def test_a_host_under_the_quick_minimum_is_unrated_gate_context(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    def shrink(row: ModelsDevRow) -> ModelsDevRow:
        host = (row.provider, row.model_id) == ("ollama-cloud", "minimax-m3")
        return replace(row, context=16000) if host else row

    result = pipeline.assign_all(
        pipeline.resolve_all(inventory, _with_rows(sources, shrink), fixture_aliases), rule
    )
    cloud = next(m for m in result.models if m.identity.row.model_name == "minimax-m3:cloud")
    assert (cloud.verdict.tier, cloud.verdict.reason) == ("unrated", "gate_context")
    assert cloud.verdict.notes == (
        "gate_context: quick needs 32000, context is 16000; lowered to unrated",
    )
    assert cloud.evidence.pre_gate == "quick"
    assert result.sentinels_ok


def test_a_cloud_rows_own_host_context_decides_its_final_tier(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    """Two inventory rows of one key end in two tiers: Ollama's cloud serves
    GLM-5.3 with 64000 tokens here, OpenRouter with 1048576. The key's
    largest context, 1048576, would have left both at standard."""

    def shrink(row: ModelsDevRow) -> ModelsDevRow:
        host = (row.provider, row.model_id) == ("ollama-cloud", "glm-5.3")
        return replace(row, context=64000) if host else row

    identity = pipeline.resolve_all(inventory, _with_rows(sources, shrink), fixture_aliases)
    result = pipeline.assign_all(identity, rule)
    by_name = {m.identity.row.model_name: m for m in result.models}
    ollama, openrouter = by_name["glm-5.3:cloud"], by_name["z-ai/glm-5.3"]
    assert ollama.identity.canonical == openrouter.identity.canonical == "zhipuai/glm-5.3"
    assert ollama.evidence == openrouter.evidence
    assert max(row.context or 0 for row in identity.index.rows_for("zhipuai/glm-5.3")) == 1048576
    assert (ollama.facts.context, openrouter.facts.context) == (64000, 1048576)
    assert (ollama.verdict.tier, ollama.verdict.reason) == ("quick", None)
    assert ollama.verdict.notes == (
        "gate_context: standard needs 128000, context is 64000; lowered to quick",
    )
    assert (openrouter.verdict.tier, openrouter.verdict.notes) == ("standard", ())


def test_a_model_takes_open_weights_from_its_keys_rows(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    def closed(row: ModelsDevRow) -> ModelsDevRow:
        return replace(row, open_weights=False) if row.canonical == "zhipuai/glm-5.3" else row

    def result(data: Sources) -> dict[str, bool | None]:
        identity = pipeline.resolve_all(inventory, data, fixture_aliases)
        models = pipeline.assign_all(identity, rule).models
        return {m.identity.row.model_name: m.open_weights for m in models}

    assert result(sources)["glm-5.3:cloud"] is True
    # Every row of the key says closed: both inventory rows of the key do.
    shut = result(_with_rows(sources, closed))
    assert (shut["glm-5.3:cloud"], shut["z-ai/glm-5.3"]) == (False, False)
    assert shut["deepseek-v4-pro:cloud"] is True
    # An ignored row has no key, so nothing is known.
    assert shut["nomic-embed-text:latest"] is None

    # One row of the key is enough, whichever it is: first, last or between.
    glm = [row for row in sources.modelsdev.data.rows if row.canonical == "zhipuai/glm-5.3"]
    assert len(glm) >= 3
    for keep in (glm[0], glm[-1], glm[1]):

        def only(row: ModelsDevRow, keep: ModelsDevRow = keep) -> ModelsDevRow:
            return closed(row) if row is not keep else row

        assert result(_with_rows(sources, only))["glm-5.3:cloud"] is True


def test_an_ignored_row_is_unrated_not_applicable(assignment: Assignment) -> None:
    ignored = assignment.models[5]
    assert ignored.identity.canonical is None
    assert (ignored.verdict.tier, ignored.verdict.reason) == ("unrated", "not_applicable")
    assert ignored.verdict.notes == ("ignored: embedding model",)
    assert (ignored.facts.tool_call, ignored.facts.context) == (None, None)
    assert "nomic-embed-text:latest" not in [r.row.model_name for r in assignment.identity.unmapped]


def test_candidates_are_keys_with_a_vote_and_no_inventory_row(assignment: Assignment) -> None:
    assert [
        (c.canonical, c.verdict.tier, c.resolved_by, c.open_weights) for c in assignment.candidates
    ] == [
        ("anthropic/claude-opus-5-5", "marathon", "host", False),
        ("anthropic/claude-fable-5", "deep", "host", False),
    ]
    opus, fable = assignment.candidates
    # Opus 5.5's Arena lower bound is 0.1215; Fable 5's is 0.0767.
    assert (opus.evidence.base, opus.evidence.pre_gate) == ("deep", "marathon")
    assert (fable.evidence.base, fable.evidence.pre_gate) == ("deep", "deep")
    assert opus.facts.context == 1000000
    assert opus.verdict.notes == ("context is the largest among the key's models.dev rows",)


def test_candidates_are_ordered_by_rank_then_key(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    # No inventory: every scored key is a candidate.
    empty = replace(inventory, rows=())
    result = pipeline.assign_all(pipeline.resolve_all(empty, sources, fixture_aliases), rule)
    assert [(c.verdict.tier, c.canonical) for c in result.candidates] == [
        ("marathon", "anthropic/claude-opus-5-5"),
        ("deep", "anthropic/claude-fable-5"),
        ("standard", "alibaba/qwen3.8-27b"),
        ("standard", "deepseek/deepseek-v4-pro-0813"),
        ("standard", "zhipuai/glm-5.3"),
        ("quick", "minimax/MiniMax-M3"),
    ]
    # The earliest resolution step among a key's records is the one written.
    assert [c.resolved_by for c in result.candidates] == [
        "host",
        "host",
        "normalized",
        "alias",
        "host",
        "normalized",
    ]


def test_unresolved_lists_only_records_at_or_above_a_standard_floor(
    assignment: Assignment,
) -> None:
    # Arena's "Qwen3.8 Max" (0.0235) is above -0.029; OpenRouter's lfm (8.4)
    # is under 33.0 and is only counted.
    assert [(r.source, r.source_id) for r in assignment.unresolved] == [("arena", "Qwen3.8 Max")]
    assert len(assignment.identity.openrouter.unresolved) == 1


def test_unresolved_keeps_one_record_per_id_in_source_then_score_order(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    def grown(name: str, *extra: ScoreRecord) -> Sources:
        result = getattr(sources, name)
        return replace(sources, **{name: replace(result, data=(*result.data, *extra))})

    sources = grown("openrouter", ScoreRecord("openrouter", "x/unknown", 40.0))
    sources = grown(
        "arena",
        ScoreRecord("arena", "Mystery", -0.01, effort="low"),
        ScoreRecord("arena", "Mystery", 0.04, effort="high"),  # the better effort row
        ScoreRecord("arena", "Too Low", -0.03),
    )
    sources = grown(
        "epoch", ScoreRecord("epoch", "Unknown A", 151.0), ScoreRecord("epoch", "Unknown B", 158.0)
    )
    result = pipeline.assign_all(pipeline.resolve_all(inventory, sources, fixture_aliases), rule)
    assert [(r.source, r.source_id, r.score) for r in result.unresolved] == [
        ("openrouter", "x/unknown", 40.0),
        ("arena", "Mystery", 0.04),
        ("arena", "Qwen3.8 Max", 0.023467932612357068),
        ("epoch", "Unknown B", 158.0),
        ("epoch", "Unknown A", 151.0),
    ]
    # Every unresolved record is still counted, listed or not.
    assert len(result.identity.arena.unresolved) == 4


def test_every_fixture_sentinel_holds(assignment: Assignment) -> None:
    assert assignment.sentinels_ok
    assert [(s.canonical, s.evidence.pre_gate) for s in assignment.sentinels] == [
        ("anthropic/claude-opus-5-5", "marathon"),
        ("anthropic/claude-fable-5", "deep"),
        ("zhipuai/glm-5.3", "standard"),
        ("minimax/MiniMax-M3", "quick"),  # written minimax/minimax-m3 in the file
    ]


def test_scaled_intelligence_indices_fail_a_sentinel_whose_tier_holds(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    scaled = tuple(replace(r, score=r.score * 0.8) for r in sources.openrouter.data)
    drifted = replace(sources, openrouter=replace(sources.openrouter, data=scaled))
    result = pipeline.assign_all(pipeline.resolve_all(inventory, drifted, fixture_aliases), rule)
    assert not result.sentinels_ok
    fable = result.sentinels[1]
    assert fable.evidence.votes["intelligence_index"] == "standard"  # 49.6 * 0.8 = 39.68
    assert fable.evidence.pre_gate == "deep"  # standard, deep, deep: the median holds
    assert fable.problems == ("intelligence_index voted standard, pinned deep",)
    # GLM-5.3 at 35.84 still votes standard, and MiniMax has no index pinned.
    assert [s.ok for s in result.sentinels] == [False, False, True, True]


def test_a_sentinel_naming_an_unknown_key_is_a_config_error(
    inventory: Inventory, sources: Sources, fixture_aliases: Aliases, rule: Contracts
) -> None:
    unknown = Sentinel("anthropic/claude-fable-9", "deep", {})
    broken = replace(rule, sentinels=(*rule.sentinels, unknown))
    identity = pipeline.resolve_all(inventory, sources, fixture_aliases)
    with pytest.raises(
        ConfigError,
        match=r"contracts.toml: \[\[sentinels\]\] #5 \(anthropic/claude-fable-9\): not a canonical",
    ):
        pipeline.assign_all(identity, broken)


def test_a_sentinel_may_name_an_alias_declared_key(
    inventory: Inventory, sources: Sources, rule: Contracts
) -> None:
    declared = aliases_mod.build([Alias("alibaba/qwen3.8-max", arena=("Qwen3.8 Max",))], [])
    pinned = Sentinel("alibaba/qwen3.8-max", "standard", {"arena_agent": "standard"})
    result = pipeline.assign_all(
        pipeline.resolve_all(inventory, sources, declared), replace(rule, sentinels=(pinned,))
    )
    assert result.sentinels_ok
    (candidate,) = [c for c in result.candidates if c.canonical == "alibaba/qwen3.8-max"]
    # No models.dev rows: both gates skipped, tool_call and open_weights unknown.
    assert (candidate.facts.tool_call, candidate.open_weights) == (None, None)
    assert candidate.verdict.tier == "standard"
    assert candidate.verdict.notes == (
        "key has no models.dev rows; tool-calling and context gates skipped",
    )
