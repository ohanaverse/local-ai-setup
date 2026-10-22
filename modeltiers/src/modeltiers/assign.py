"""Assignment: from one key's best records to its tier.

The spec's order: (1) votes, (2) base tier, (3) marathon promotion, which
gives the pre-gate tier, (4) the sentinel check on the votes and the pre-gate
tier, (5) the tool-calling gate, (6) the context gate, (7) the final tier.
`weigh` is steps 1 to 3, `check_sentinel` is step 4, `decide` is steps 5 to 7.
"""

from collections.abc import Iterable, Mapping
from dataclasses import dataclass

from modeltiers.contracts import (
    CI_LOWER,
    FLOOR_TIERS,
    RANK,
    TIERS,
    UNRATED,
    Contracts,
    Sentinel,
    Signal,
)
from modeltiers.records import ModelsDevRow, ScoreRecord

NO_VOTES = "no_votes"
BELOW_FLOOR = "below_floor"
GATE_TOOL_CALL = "gate_tool_call"
GATE_CONTEXT = "gate_context"
NOT_APPLICABLE = "not_applicable"

NOTE_LARGEST = "context is the largest among the key's models.dev rows"
NOTE_LOCAL = "local build: served context unknown; context gate skipped"
NOTE_NO_ROWS = "key has no models.dev rows; tool-calling and context gates skipped"
NOTE_NO_CONTEXT = "no models.dev row gives a context; context gate skipped"


def vote(signal: Signal, score: float) -> str:
    """The tier one score votes for: the highest tier whose floor it reaches,
    never above the signal's `max_vote`, and `unrated` under the quick floor."""
    cast = UNRATED
    for tier in FLOOR_TIERS:
        if score >= signal.floors[tier]:
            cast = tier
    return cast if RANK[cast] <= RANK[signal.max_vote] else signal.max_vote


def base_tier(votes: Iterable[str]) -> str:
    """The lower median: the votes sorted by rank, lowest first, and the one
    at index (n-1)//2. With two votes it is the lower; with none, `unrated`."""
    ranked = sorted(votes, key=lambda tier: RANK[tier])
    return ranked[(len(ranked) - 1) // 2] if ranked else UNRATED


def confidence(votes: Iterable[str]) -> str:
    """How far the votes agree. Promotion and gates do not change it."""
    ranks = [RANK[tier] for tier in votes]
    if not ranks:
        return "none"
    spread = max(ranks) - min(ranks)
    if len(ranks) == 1 or spread >= 2:
        return "low"
    return "high" if spread == 0 else "medium"


@dataclass(frozen=True)
class Evidence:
    """Steps 1 to 3 for one key. Each mapping is in the order it is written."""

    votes: Mapping[str, str]  # signal -> the tier it voted; only signals with a record
    scores: Mapping[str, float]  # the voting scores and the evidence-only ones
    efforts: Mapping[str, str]  # signal -> the effort its record named
    base: str  # the lower median of the votes
    pre_gate: str  # after marathon promotion; what a sentinel pins
    confidence: str  # high | medium | low | none


def weigh(records: Mapping[str, ScoreRecord], contracts: Contracts) -> Evidence:
    """Votes, base tier and marathon promotion. `records` maps a signal name
    to the key's best record in that signal's source; a signal with no record
    abstains."""
    votes: dict[str, str] = {}
    scores: dict[str, float] = {}
    efforts: dict[str, str] = {}
    for name, signal in contracts.signals.items():
        record = records.get(name)
        if record is None:
            continue
        votes[name] = vote(signal, record.score)
        scores[name] = record.score
        if record.coding_index is not None:
            scores["coding_index"] = record.coding_index
        if record.agentic_index is not None:
            scores["agentic_index"] = record.agentic_index
        if signal.marathon_ci_lower is not None and record.ci_lower is not None:
            scores[f"{name}{CI_LOWER}"] = record.ci_lower
        if record.effort is not None:
            efforts[name] = record.effort
    base = base_tier(votes.values())
    marathon = contracts.marathon
    lower = scores.get(f"{marathon.name}{CI_LOWER}")
    promoted = (
        base == "deep"
        and lower is not None
        and marathon.marathon_ci_lower is not None
        and lower >= marathon.marathon_ci_lower
    )
    return Evidence(
        votes=votes,
        scores=scores,
        efforts=efforts,
        base=base,
        pre_gate="marathon" if promoted else base,
        confidence=confidence(votes.values()),
    )


@dataclass(frozen=True)
class SentinelResult:
    sentinel: Sentinel
    canonical: str  # in the stored spelling
    evidence: Evidence
    problems: tuple[str, ...]  # empty: the sentinel holds

    @property
    def ok(self) -> bool:
        return not self.problems


def check_sentinel(sentinel: Sentinel, canonical: str, evidence: Evidence) -> SentinelResult:
    """Step 4. A sentinel fails when a pinned vote differs or is missing, or
    when the pre-gate tier differs. A signal the sentinel does not pin is not
    looked at."""
    problems = [
        f"{signal} voted {evidence.votes.get(signal, 'nothing')}, pinned {pinned}"
        for signal, pinned in sentinel.votes.items()
        if evidence.votes.get(signal) != pinned
    ]
    if evidence.pre_gate != sentinel.tier:
        problems.append(f"tier before the gates is {evidence.pre_gate}, pinned {sentinel.tier}")
    return SentinelResult(
        sentinel=sentinel, canonical=canonical, evidence=evidence, problems=tuple(problems)
    )


@dataclass(frozen=True)
class Facts:
    """What models.dev says about one entry, for the two gates."""

    tool_call: bool | None  # None: the key has no models.dev rows
    context: int | None  # None: unknown, and the context gate is skipped
    notes: tuple[str, ...] = ()


def gather(
    rows: tuple[ModelsDevRow, ...], *, location: str | None, host_row: ModelsDevRow | None
) -> Facts:
    """The spec's context table. `rows` are the key's models.dev rows;
    `location` is the inventory row's, or None for a candidate; `host_row` is
    a cloud inventory row's own host row, with or without a canonical id."""
    if not rows:
        return Facts(tool_call=None, context=None, notes=(NOTE_NO_ROWS,))
    tool_call = any(row.tool_call for row in rows)
    if location == "local":
        return Facts(tool_call=tool_call, context=None, notes=(NOTE_LOCAL,))
    if host_row is not None and host_row.context is not None:
        return Facts(tool_call=tool_call, context=host_row.context)
    contexts = [row.context for row in rows if row.context is not None]
    if not contexts:
        return Facts(tool_call=tool_call, context=None, notes=(NOTE_NO_CONTEXT,))
    return Facts(tool_call=tool_call, context=max(contexts), notes=(NOTE_LARGEST,))


@dataclass(frozen=True)
class Verdict:
    tier: str  # the final tier
    reason: str | None  # set exactly when the tier is unrated
    notes: tuple[str, ...]


def decide(evidence: Evidence, facts: Facts, contracts: Contracts) -> Verdict:
    """Steps 5 to 7: the tool-calling gate, the context gate, the final tier.
    With no votes, or votes under the quick floor, neither gate is applied."""
    if not evidence.votes:
        return Verdict(UNRATED, NO_VOTES, facts.notes)
    if evidence.pre_gate == UNRATED:
        return Verdict(UNRATED, BELOW_FLOOR, facts.notes)
    if facts.tool_call is False:
        return Verdict(UNRATED, GATE_TOOL_CALL, facts.notes)
    tier = evidence.pre_gate
    if facts.context is None or facts.context >= contracts.tiers[tier].context_min:
        return Verdict(tier, None, facts.notes)
    met = [
        name
        for name in TIERS
        if RANK[name] < RANK[tier] and facts.context >= contracts.tiers[name].context_min
    ]
    lowered = met[-1] if met else UNRATED
    note = (
        f"{GATE_CONTEXT}: {tier} needs {contracts.tiers[tier].context_min}, "
        f"context is {facts.context}; lowered to {lowered}"
    )
    return Verdict(lowered, None if met else GATE_CONTEXT, (*facts.notes, note))
