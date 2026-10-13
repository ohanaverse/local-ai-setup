"""The stages after loading, run in order over one inventory and one set of
sources: `resolve_all` (identity), then `assign_all` (tiers)."""

from collections.abc import Sequence
from dataclasses import dataclass

from modeltiers import aliases as aliases_mod
from modeltiers import assign, identity
from modeltiers.aliases import SOURCES, Aliases
from modeltiers.assign import Evidence, Facts, SentinelResult, Verdict
from modeltiers.contracts import RANK, UNRATED, Contracts
from modeltiers.errors import ConfigError
from modeltiers.identity import Index, Match, RowIdentity, SourceIdentity
from modeltiers.loader import Sources
from modeltiers.records import Inventory, ModelsDev, ModelsDevRow, ScoreRecord

# How an [[ignore]]d row's reason is written into the entry's `notes`; the
# change report matches an existing file's notes on this prefix.
IGNORED_NOTE = "ignored: "


@dataclass(frozen=True)
class Identity:
    """Every inventory row and every source record, resolved or not."""

    index: Index
    rows: tuple[RowIdentity, ...]  # one per inventory row, in wt's order
    openrouter: SourceIdentity
    arena: SourceIdentity
    epoch: SourceIdentity
    warnings: tuple[str, ...]  # alias source ids absent from this run's data

    def sources(self) -> tuple[SourceIdentity, ...]:
        """The three benchmark sources, in the order the report lists them."""
        return tuple(getattr(self, name) for name in SOURCES)

    @property
    def unmapped(self) -> tuple[RowIdentity, ...]:
        """Rows no step resolved and no `[[ignore]]` lists."""
        return tuple(r for r in self.rows if r.unmapped)

    @property
    def review(self) -> tuple[RowIdentity, ...]:
        """Rows matched by name alone, for a person to check."""
        return tuple(r for r in self.rows if r.resolved_by == identity.NORMALIZED)


def checked_index(modelsdev: ModelsDev, aliases: Aliases) -> Index:
    """The index of one models.dev snapshot, once the aliases are checked
    against its keys. Raises ConfigError for an alias whose canonical is
    neither a models.dev key nor anchored by a source id, and for one that
    declares a key reading the same as another."""
    modelsdev_keys = tuple(r.canonical for r in modelsdev.rows if r.canonical)
    aliases_mod.check_anchored(aliases, modelsdev_keys)
    aliases_mod.check_spelling(aliases, modelsdev_keys)
    return identity.build_index(modelsdev, aliases)


def resolve_all(inventory: Inventory, sources: Sources, aliases: Aliases) -> Identity:
    """Resolve the inventory and the three benchmark sources against one
    models.dev snapshot. Raises ConfigError for an alias `checked_index`
    refuses."""
    index = checked_index(sources.modelsdev.data, aliases)
    data = {name: getattr(sources, name).data for name in SOURCES}
    present = {name: frozenset(r.source_id for r in records) for name, records in data.items()}
    return Identity(
        index=index,
        rows=tuple(identity.resolve_row(row, index, aliases) for row in inventory.rows),
        warnings=tuple(aliases_mod.stale_ids(aliases, present)),
        **{
            name: identity.resolve_source(name, records, index, aliases)
            for name, records in data.items()
        },
    )


@dataclass(frozen=True)
class ModelResult:
    """One resolved or ignored inventory row, with its tier."""

    identity: RowIdentity
    open_weights: bool | None  # None: ignored, or the key has no models.dev rows
    evidence: Evidence
    facts: Facts
    verdict: Verdict


@dataclass(frozen=True)
class Candidate:
    """A key with at least one vote and no inventory row."""

    canonical: str  # in the stored spelling
    resolved_by: str  # the earliest of alias, host, normalized among its records
    open_weights: bool | None  # None: the key has no models.dev rows
    evidence: Evidence
    facts: Facts
    verdict: Verdict


@dataclass(frozen=True)
class Assignment:
    """Everything `tiers.toml` is built from."""

    identity: Identity
    contracts: Contracts
    models: tuple[ModelResult, ...]  # resolved and ignored rows, in wt's order
    candidates: tuple[Candidate, ...]  # rank descending, then key
    unresolved: tuple[ScoreRecord, ...]  # at or above a standard floor; source, then score
    sentinels: tuple[SentinelResult, ...]

    @property
    def sentinels_ok(self) -> bool:
        return all(result.ok for result in self.sentinels)


_NO_EVIDENCE = Evidence(
    votes={}, scores={}, efforts={}, base=UNRATED, pre_gate=UNRATED, confidence="none"
)
_RESOLUTION_ORDER = (identity.ALIAS, identity.HOST, identity.NORMALIZED)


def _matches(result: Identity, key: str, contracts: Contracts) -> dict[str, Match]:
    """The key's best record per signal, for the signals whose source has one."""
    by_source = {source.source: source for source in result.sources()}
    found: dict[str, Match] = {}
    for name, signal in contracts.signals.items():
        match = by_source[signal.source].best.get(key.lower())
        if match is not None:
            found[name] = match
    return found


def _evidence(result: Identity, key: str, contracts: Contracts) -> Evidence:
    matches = _matches(result, key, contracts)
    return assign.weigh({name: match.record for name, match in matches.items()}, contracts)


def _open_weights(rows: Sequence[ModelsDevRow]) -> bool | None:
    """True when any models.dev row of a key says its weights are open; None
    when the key has no rows to say either way."""
    return any(row.open_weights for row in rows) if rows else None


def _model(entry: RowIdentity, result: Identity, contracts: Contracts) -> ModelResult:
    if entry.canonical is None:
        # Listed under [[ignore]]: unrated, and no gate has anything to read.
        return ModelResult(
            identity=entry,
            open_weights=None,
            evidence=_NO_EVIDENCE,
            facts=Facts(tool_call=None, context=None),
            verdict=Verdict(
                UNRATED, assign.NOT_APPLICABLE, (f"{IGNORED_NOTE}{entry.ignore_reason}",)
            ),
        )
    evidence = _evidence(result, entry.canonical, contracts)
    rows = result.index.rows_for(entry.canonical)
    facts = assign.gather(rows, location=entry.row.location, host_row=entry.host_row)
    return ModelResult(
        identity=entry,
        open_weights=_open_weights(rows),
        evidence=evidence,
        facts=facts,
        verdict=assign.decide(evidence, facts, contracts),
    )


def _candidates(result: Identity, contracts: Contracts) -> tuple[Candidate, ...]:
    held = {entry.canonical.lower() for entry in result.rows if entry.canonical is not None}
    keys: dict[str, str] = {}
    for source in result.sources():
        for lower, match in source.best.items():
            if lower not in held:
                keys.setdefault(lower, match.canonical)
    found: list[Candidate] = []
    for key in keys.values():
        matches = _matches(result, key, contracts)
        steps = {match.resolved_by for match in matches.values()}
        evidence = assign.weigh({name: m.record for name, m in matches.items()}, contracts)
        rows = result.index.rows_for(key)
        facts = assign.gather(rows, location=None, host_row=None)
        found.append(
            Candidate(
                canonical=key,
                resolved_by=next(step for step in _RESOLUTION_ORDER if step in steps),
                open_weights=_open_weights(rows),
                evidence=evidence,
                facts=facts,
                verdict=assign.decide(evidence, facts, contracts),
            )
        )
    found.sort(key=lambda c: (-RANK[c.verdict.tier], c.canonical.lower()))
    return tuple(found)


def _unresolved(result: Identity, contracts: Contracts) -> tuple[ScoreRecord, ...]:
    """Unresolved records at or above their signal's standard floor: the best
    record of each id, in source order and then by score, highest first."""
    floors = {signal.source: signal.floors["standard"] for signal in contracts.signals.values()}
    listed: list[ScoreRecord] = []
    for source in result.sources():
        best: dict[str, ScoreRecord] = {}
        for record in source.unresolved:
            if record.score < floors[source.source]:
                continue
            if record.source_id not in best or record.score > best[record.source_id].score:
                best[record.source_id] = record
        listed.extend(sorted(best.values(), key=lambda record: -record.score))
    return tuple(listed)


def _sentinels(result: Identity, contracts: Contracts) -> tuple[SentinelResult, ...]:
    checked: list[SentinelResult] = []
    for number, sentinel in enumerate(contracts.sentinels, start=1):
        key = result.index.spelling(sentinel.canonical)
        if key is None:
            raise ConfigError(
                f"{contracts.origin}: [[sentinels]] #{number} ({sentinel.canonical}): "
                "not a canonical key in this run's models.dev data or in aliases.toml"
            )
        checked.append(assign.check_sentinel(sentinel, key, _evidence(result, key, contracts)))
    return tuple(checked)


def assign_all(result: Identity, contracts: Contracts) -> Assignment:
    """Give every resolved key its tier. Raises ConfigError for a sentinel
    whose canonical is not a known key. A failed sentinel is not an error
    here: it is in `Assignment.sentinels`, and the caller writes nothing."""
    sentinels = _sentinels(result, contracts)
    return Assignment(
        identity=result,
        contracts=contracts,
        models=tuple(
            _model(entry, result, contracts) for entry in result.rows if not entry.unmapped
        ),
        candidates=_candidates(result, contracts),
        unresolved=_unresolved(result, contracts),
        sentinels=sentinels,
    )
