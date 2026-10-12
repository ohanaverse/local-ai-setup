from dataclasses import replace

import pytest

from modeltiers import assign
from modeltiers.assign import Evidence, Facts
from modeltiers.contracts import Contracts, Sentinel
from modeltiers.records import ModelsDevRow, ScoreRecord


def _records(
    ii: float | None = None,
    arena: float | None = None,
    eci: float | None = None,
    *,
    arena_lower: float | None = None,
    effort: str | None = None,
) -> dict[str, ScoreRecord]:
    """Best records by signal, from bare scores."""
    found: dict[str, ScoreRecord] = {}
    if ii is not None:
        found["intelligence_index"] = ScoreRecord("openrouter", "v/m", ii)
    if arena is not None:
        found["arena_agent"] = ScoreRecord("arena", "M", arena, ci_lower=arena_lower, effort=effort)
    if eci is not None:
        found["eci"] = ScoreRecord("epoch", "M", eci, ci_lower=eci - 2.0)
    return found


# --- step 1: votes -----------------------------------------------------------


@pytest.mark.parametrize(
    ("signal", "score", "expected"),
    [
        ("intelligence_index", 10.59, "unrated"),
        ("intelligence_index", 10.6, "quick"),
        ("intelligence_index", 32.99, "quick"),
        ("intelligence_index", 33.0, "standard"),
        ("intelligence_index", 48.99, "standard"),
        ("intelligence_index", 49.0, "deep"),
        ("intelligence_index", 99.0, "deep"),
        ("arena_agent", -0.0901, "unrated"),
        ("arena_agent", -0.090, "quick"),
        ("arena_agent", -0.0291, "quick"),
        ("arena_agent", -0.029, "standard"),
        ("arena_agent", 0.0559, "standard"),
        ("arena_agent", 0.056, "deep"),
        ("eci", 137.09, "unrated"),
        ("eci", 137.1, "quick"),
        ("eci", 150.69, "quick"),
        ("eci", 150.7, "standard"),
        ("eci", 160.59, "standard"),
        ("eci", 160.6, "deep"),
    ],
)
def test_a_score_votes_the_highest_tier_whose_floor_it_reaches(
    rule: Contracts, signal: str, score: float, expected: str
) -> None:
    assert assign.vote(rule.signals[signal], score) == expected


def test_no_vote_is_above_max_vote(rule: Contracts) -> None:
    capped = replace(rule.signals["eci"], max_vote="standard")
    assert assign.vote(capped, 170.0) == "standard"
    assert assign.vote(capped, 140.0) == "quick"
    assert assign.vote(capped, 100.0) == "unrated"


# --- step 2: base tier -------------------------------------------------------


@pytest.mark.parametrize(
    ("votes", "expected"),
    [
        ([], "unrated"),
        (["deep"], "deep"),
        (["deep", "standard"], "standard"),  # two votes: the lower
        (["standard", "deep"], "standard"),
        (["deep", "quick", "standard"], "standard"),  # three: the middle
        (["deep", "deep", "quick"], "deep"),
        (["quick", "quick", "deep"], "quick"),
        (["unrated", "standard"], "unrated"),
        (["unrated", "standard", "standard"], "standard"),
    ],
)
def test_the_base_tier_is_the_lower_median(votes: list[str], expected: str) -> None:
    assert assign.base_tier(votes) == expected


@pytest.mark.parametrize(
    ("votes", "expected"),
    [
        ([], "none"),
        (["deep"], "low"),
        (["deep", "deep"], "high"),
        (["deep", "deep", "deep"], "high"),
        (["deep", "standard"], "medium"),
        (["deep", "standard", "standard"], "medium"),
        (["deep", "quick"], "low"),
        (["deep", "standard", "quick"], "low"),
        (["unrated", "quick"], "medium"),
    ],
)
def test_confidence_comes_from_the_votes_alone(votes: list[str], expected: str) -> None:
    assert assign.confidence(votes) == expected


# --- steps 1 to 3 together ---------------------------------------------------


def test_weigh_records_votes_scores_and_efforts_in_file_order(rule: Contracts) -> None:
    records = _records(44.8, 0.0215, 155.61, arena_lower=0.0143, effort="max")
    records["intelligence_index"] = replace(
        records["intelligence_index"], coding_index=74.8, agentic_index=53.1
    )
    evidence = assign.weigh(records, rule)
    assert evidence.votes == {
        "intelligence_index": "standard",
        "arena_agent": "standard",
        "eci": "standard",
    }
    # The evidence-only indices are recorded and do not vote. The Epoch
    # interval is not written: only the marathon signal's lower bound is used.
    assert list(evidence.scores.items()) == [
        ("intelligence_index", 44.8),
        ("coding_index", 74.8),
        ("agentic_index", 53.1),
        ("arena_agent", 0.0215),
        ("arena_agent_ci_lower", 0.0143),
        ("eci", 155.61),
    ]
    assert evidence.efforts == {"arena_agent": "max"}
    assert (evidence.base, evidence.pre_gate, evidence.confidence) == (
        "standard",
        "standard",
        "high",
    )


def test_a_signal_with_no_record_abstains(rule: Contracts) -> None:
    evidence = assign.weigh(_records(eci=162.0), rule)
    assert evidence.votes == {"eci": "deep"}
    assert evidence.scores == {"eci": 162.0}
    assert evidence.efforts == {}
    assert (evidence.base, evidence.confidence) == ("deep", "low")


def test_no_records_is_unrated_with_confidence_none(rule: Contracts) -> None:
    evidence = assign.weigh({}, rule)
    assert evidence == Evidence({}, {}, {}, "unrated", "unrated", "none")


def test_a_deep_model_is_promoted_when_its_arena_lower_bound_reaches_the_floor(
    rule: Contracts,
) -> None:
    at_floor = assign.weigh(_records(57.6, 0.1433, 167.33, arena_lower=0.097), rule)
    assert (at_floor.base, at_floor.pre_gate) == ("deep", "marathon")
    # The point score does not promote: Sonnet 5.5 sat at 0.1195 with a lower
    # bound of 0.0868.
    under = assign.weigh(_records(57.6, 0.1195, 167.33, arena_lower=0.0868), rule)
    assert (under.base, under.pre_gate) == ("deep", "deep")
    # Promotion does not touch the votes or the confidence.
    assert set(at_floor.votes.values()) == {"deep"}
    assert at_floor.confidence == "high"


def test_promotion_is_refused_when_the_base_tier_is_standard(rule: Contracts) -> None:
    # Arena alone says marathon-grade; the other two votes hold the base down.
    evidence = assign.weigh(_records(44.8, 0.15, 155.0, arena_lower=0.12), rule)
    assert evidence.votes["arena_agent"] == "deep"
    assert (evidence.base, evidence.pre_gate) == ("standard", "standard")


def test_no_arena_record_means_no_route_to_marathon(rule: Contracts) -> None:
    evidence = assign.weigh(_records(ii=60.0, eci=170.0), rule)
    assert (evidence.base, evidence.pre_gate) == ("deep", "deep")
    missing_interval = assign.weigh(_records(60.0, 0.2, 170.0, arena_lower=None), rule)
    assert missing_interval.pre_gate == "deep"


# --- step 4: sentinels -------------------------------------------------------


def test_a_sentinel_holds_when_its_pinned_votes_and_tier_match(rule: Contracts) -> None:
    evidence = assign.weigh(_records(57.6, 0.1433, 167.33, arena_lower=0.1215), rule)
    result = assign.check_sentinel(rule.sentinels[0], "anthropic/claude-opus-5-5", evidence)
    assert result.ok
    assert result.problems == ()


def test_a_drifted_vote_fails_the_sentinel_though_the_tier_holds(rule: Contracts) -> None:
    # 49.6 * 0.8 = 39.68: the intelligence index now votes standard. The
    # lower median of standard, deep, deep is still deep.
    evidence = assign.weigh(_records(39.68, 0.0894, 162.06, arena_lower=0.0767), rule)
    assert evidence.pre_gate == "deep"
    result = assign.check_sentinel(rule.sentinels[1], "anthropic/claude-fable-5", evidence)
    assert not result.ok
    assert result.problems == ("intelligence_index voted standard, pinned deep",)


def test_a_missing_pinned_vote_and_a_moved_tier_both_fail(rule: Contracts) -> None:
    evidence = assign.weigh(_records(arena=0.0894, arena_lower=0.0767), rule)
    pinned = Sentinel("v/m", "marathon", {"eci": "deep", "arena_agent": "deep"})
    result = assign.check_sentinel(pinned, "v/m", evidence)
    assert result.problems == (
        "eci voted nothing, pinned deep",
        "tier before the gates is deep, pinned marathon",
    )


def test_a_signal_the_sentinel_does_not_pin_is_not_checked(rule: Contracts) -> None:
    # The fixture's MiniMax sentinel pins Arena and Epoch only.
    evidence = assign.weigh(_records(60.0, -0.0695, 146.95), rule)
    assert evidence.votes["intelligence_index"] == "deep"
    assert assign.check_sentinel(rule.sentinels[3], "minimax/MiniMax-M3", evidence).ok


# --- the context table -------------------------------------------------------


def _row(*, tool_call: bool = True, context: int | None = 1_000_000) -> ModelsDevRow:
    return ModelsDevRow("vendor", "m", "vendor/m", tool_call, True, context)


def test_a_cloud_row_uses_its_host_rows_context() -> None:
    host = ModelsDevRow("ollama-cloud", "m", None, False, True, 512_000)
    facts = assign.gather((_row(context=1_048_576),), location="cloud", host_row=host)
    # The host row need not have a canonical id; tool_call comes from the
    # key's rows, never from the host row.
    assert facts == Facts(tool_call=True, context=512_000, notes=())


def test_a_cloud_row_with_no_host_row_and_a_candidate_use_the_largest_context() -> None:
    rows = (_row(context=131_072), _row(context=1_048_576), _row(context=None))
    expected = Facts(tool_call=True, context=1_048_576, notes=(assign.NOTE_LARGEST,))
    assert assign.gather(rows, location="cloud", host_row=None) == expected
    assert assign.gather(rows, location=None, host_row=None) == expected
    # A host row that gives no context is no better than none.
    blank = ModelsDevRow("openrouter", "v/m", "vendor/m", True, True, None)
    assert assign.gather(rows, location="cloud", host_row=blank) == expected


def test_a_local_rows_context_is_unknown() -> None:
    facts = assign.gather((_row(),), location="local", host_row=None)
    assert facts == Facts(tool_call=True, context=None, notes=(assign.NOTE_LOCAL,))


def test_a_key_with_no_models_dev_rows_skips_both_gates() -> None:
    host = ModelsDevRow("ollama-cloud", "m", None, True, True, 512_000)
    for location, host_row in (("cloud", host), ("local", None), (None, None)):
        facts = assign.gather((), location=location, host_row=host_row)
        assert facts == Facts(tool_call=None, context=None, notes=(assign.NOTE_NO_ROWS,))


def test_rows_that_give_no_context_leave_it_unknown() -> None:
    facts = assign.gather((_row(context=None),), location=None, host_row=None)
    assert facts == Facts(tool_call=True, context=None, notes=(assign.NOTE_NO_CONTEXT,))


def test_tool_call_is_true_when_any_row_of_the_key_reports_it() -> None:
    rows = (_row(tool_call=False), _row(tool_call=True))
    assert assign.gather(rows, location=None, host_row=None).tool_call is True
    assert assign.gather(rows[:1], location=None, host_row=None).tool_call is False


# --- steps 5 to 7: gates and the unrated reasons -----------------------------


def _evidence(rule: Contracts, pre_gate: str) -> Evidence:
    scores = {"marathon": 0.15, "deep": 0.06, "standard": 0.0, "quick": -0.05, "unrated": -0.2}
    evidence = assign.weigh(_records(arena=scores[pre_gate], arena_lower=0.0), rule)
    return replace(evidence, pre_gate=pre_gate)


def test_no_votes_is_unrated_and_no_gate_is_applied(rule: Contracts) -> None:
    facts = Facts(tool_call=False, context=10)
    verdict = assign.decide(assign.weigh({}, rule), facts, rule)
    assert (verdict.tier, verdict.reason) == ("unrated", "no_votes")


def test_votes_under_the_quick_floor_are_below_floor(rule: Contracts) -> None:
    evidence = assign.weigh(_records(ii=10.0), rule)  # gpt-oss:20b
    assert evidence.votes == {"intelligence_index": "unrated"}
    verdict = assign.decide(evidence, Facts(tool_call=False, context=10), rule)
    assert (verdict.tier, verdict.reason) == ("unrated", "below_floor")


def test_no_tool_calling_is_unrated(rule: Contracts) -> None:
    verdict = assign.decide(_evidence(rule, "deep"), Facts(False, 1_000_000), rule)
    assert (verdict.tier, verdict.reason, verdict.notes) == ("unrated", "gate_tool_call", ())


def test_the_tool_calling_gate_is_skipped_for_a_key_with_no_rows(rule: Contracts) -> None:
    facts = Facts(tool_call=None, context=None, notes=(assign.NOTE_NO_ROWS,))
    verdict = assign.decide(_evidence(rule, "deep"), facts, rule)
    assert (verdict.tier, verdict.reason, verdict.notes) == ("deep", None, (assign.NOTE_NO_ROWS,))


@pytest.mark.parametrize(
    ("pre_gate", "context", "tier", "reason"),
    [
        ("marathon", 400_000, "marathon", None),  # at the minimum: kept
        ("marathon", 399_999, "deep", None),
        ("marathon", 150_000, "standard", None),  # drops past deep
        ("deep", 200_000, "deep", None),
        ("deep", 199_999, "standard", None),
        ("standard", 127_999, "quick", None),
        ("standard", 31_999, "unrated", "gate_context"),
        ("quick", 32_000, "quick", None),
        ("quick", 31_999, "unrated", "gate_context"),
    ],
)
def test_the_context_gate_drops_to_the_highest_tier_the_context_meets(
    rule: Contracts, pre_gate: str, context: int, tier: str, reason: str | None
) -> None:
    verdict = assign.decide(_evidence(rule, pre_gate), Facts(True, context), rule)
    assert (verdict.tier, verdict.reason) == (tier, reason)
    if tier == pre_gate:
        assert verdict.notes == ()
    else:
        need = rule.tiers[pre_gate].context_min
        assert verdict.notes == (
            f"gate_context: {pre_gate} needs {need}, context is {context}; lowered to {tier}",
        )


def test_an_unknown_context_skips_the_context_gate(rule: Contracts) -> None:
    facts = Facts(tool_call=True, context=None, notes=(assign.NOTE_LOCAL,))
    verdict = assign.decide(_evidence(rule, "marathon"), facts, rule)
    assert (verdict.tier, verdict.reason, verdict.notes) == ("marathon", None, (assign.NOTE_LOCAL,))


def test_a_gate_does_not_change_the_evidence(rule: Contracts) -> None:
    evidence = _evidence(rule, "deep")
    assign.decide(evidence, Facts(True, 50_000), rule)
    assert evidence.pre_gate == "deep"
    assert evidence.confidence == "low"
