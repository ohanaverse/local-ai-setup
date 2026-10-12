"""`contracts.toml`: the tier rule as data.

The floors each signal votes by, the context each tier needs, one attribution
line per source, and the sentinel models whose votes and tier are pinned.
"""

import tomllib
from collections.abc import Mapping
from dataclasses import dataclass
from importlib import resources
from pathlib import Path
from typing import Any

from modeltiers.errors import ConfigError

UNRATED = "unrated"
UNRATED_CODE = "-"
TIERS = ("quick", "standard", "deep", "marathon")  # the rated tiers, lowest first
RANK: Mapping[str, int] = {UNRATED: 0, "quick": 1, "standard": 2, "deep": 3, "marathon": 4}
FLOOR_TIERS = ("quick", "standard", "deep")  # the tiers a signal can vote
SIGNALS = ("intelligence_index", "arena_agent", "eci")
SIGNAL_SOURCE: Mapping[str, str] = {
    "intelligence_index": "openrouter",
    "arena_agent": "arena",
    "eci": "epoch",
}
SOURCES = ("modelsdev", "openrouter", "arena", "epoch")
STAMP = "%Y-%m-%dT%H:%M:%SZ"  # timestamps as written to tiers.toml and as printed

_TOP_KEYS = frozenset({"version", "tiers", "signals", "sources", "sentinels"})
_TIER_KEYS = frozenset({"code", "context_min"})
_SIGNAL_KEYS = frozenset({"source", "max_vote", "floors", "marathon_ci_lower"})
_SOURCE_KEYS = frozenset({"attribution"})
_SENTINEL_KEYS = frozenset({"canonical", "tier", "votes"})


@dataclass(frozen=True)
class Tier:
    name: str
    code: str  # one character, for the wt selector
    context_min: int


@dataclass(frozen=True)
class Signal:
    name: str
    source: str  # "openrouter" | "arena" | "epoch"
    max_vote: str  # no vote of this signal is higher
    floors: Mapping[str, float]  # quick, standard, deep
    marathon_ci_lower: float | None = None  # set on exactly one signal


@dataclass(frozen=True)
class Sentinel:
    canonical: str  # as written in the file
    tier: str  # the pinned pre-gate tier
    votes: Mapping[str, str]  # signal -> pinned vote; a signal left out is not pinned


@dataclass(frozen=True)
class Contracts:
    version: str
    tiers: Mapping[str, Tier]  # the four rated tiers, lowest first
    signals: Mapping[str, Signal]  # in the order of SIGNALS
    attribution: Mapping[str, str]  # source -> its line, in the order of SOURCES
    sentinels: tuple[Sentinel, ...]
    origin: str = "contracts.toml"  # the file's name, for messages

    def code(self, tier: str) -> str:
        """The one-character code of a tier; `-` for `unrated`."""
        return UNRATED_CODE if tier == UNRATED else self.tiers[tier].code

    @property
    def marathon(self) -> Signal:
        """The one signal whose record's lower bound can promote to marathon."""
        return next(s for s in self.signals.values() if s.marathon_ci_lower is not None)


def _table(where: str, value: Any, allowed: frozenset[str] | None = None) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ConfigError(f"{where}: missing or not a table")
    if allowed is not None:
        unknown = sorted(set(value) - allowed)
        if unknown:
            raise ConfigError(f"{where}: unknown key {unknown[0]!r}")
    return value


def _exactly(where: str, table: Mapping[str, Any], names: tuple[str, ...], kind: str) -> None:
    """The table has one entry for each of `names` and nothing else."""
    for name in names:
        if name not in table:
            raise ConfigError(f"{where}: no [{kind}.{name}] table")
    unknown = sorted(set(table) - set(names))
    if unknown:
        raise ConfigError(f"{where}: unknown {kind} entry {unknown[0]!r}")


def _text(where: str, entry: Mapping[str, Any], key: str) -> str:
    value = entry.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ConfigError(f"{where}: {key} is missing, empty or not a string")
    return value


def _number(where: str, value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, int | float):
        raise ConfigError(f"{where} is missing or not a number")
    return float(value)


def _tiers(origin: str, doc: Mapping[str, Any]) -> dict[str, Tier]:
    table = _table(f"{origin}: [tiers]", doc.get("tiers"))
    _exactly(origin, table, TIERS, "tiers")
    tiers: dict[str, Tier] = {}
    for name in TIERS:
        where = f"{origin}: [tiers.{name}]"
        entry = _table(where, table[name], _TIER_KEYS)
        code = _text(where, entry, "code")
        context_min = entry.get("context_min")
        if len(code) != 1 or code == UNRATED_CODE:
            raise ConfigError(f"{where}: code must be one character other than {UNRATED_CODE!r}")
        if code in {t.code for t in tiers.values()}:
            raise ConfigError(f"{where}: code {code!r} is used by another tier")
        if isinstance(context_min, bool) or not isinstance(context_min, int) or context_min < 0:
            raise ConfigError(f"{where}: context_min is missing or not a whole number")
        if tiers and context_min < max(t.context_min for t in tiers.values()):
            raise ConfigError(f"{where}: context_min must not fall as the tier rises")
        tiers[name] = Tier(name=name, code=code, context_min=context_min)
    return tiers


def _signal(where: str, name: str, value: Any) -> Signal:
    entry = _table(where, value, _SIGNAL_KEYS)
    source = _text(where, entry, "source")
    if source != SIGNAL_SOURCE[name]:
        raise ConfigError(f"{where}: source must be {SIGNAL_SOURCE[name]!r}, not {source!r}")
    max_vote = _text(where, entry, "max_vote")
    if max_vote not in FLOOR_TIERS:
        raise ConfigError(f"{where}: max_vote {max_vote!r} is not one of {', '.join(FLOOR_TIERS)}")
    table = _table(f"{where} floors", entry.get("floors"), frozenset(FLOOR_TIERS))
    floors = {tier: _number(f"{where}: floors.{tier}", table.get(tier)) for tier in FLOOR_TIERS}
    if not floors["quick"] < floors["standard"] < floors["deep"]:
        raise ConfigError(f"{where}: floors must rise with the tier: quick < standard < deep")
    marathon = entry.get("marathon_ci_lower")
    return Signal(
        name=name,
        source=source,
        max_vote=max_vote,
        floors=floors,
        marathon_ci_lower=(
            None if marathon is None else _number(f"{where}: marathon_ci_lower", marathon)
        ),
    )


def _signals(origin: str, doc: Mapping[str, Any]) -> dict[str, Signal]:
    table = _table(f"{origin}: [signals]", doc.get("signals"))
    _exactly(origin, table, SIGNALS, "signals")
    signals = {name: _signal(f"{origin}: [signals.{name}]", name, table[name]) for name in SIGNALS}
    carriers = [s.name for s in signals.values() if s.marathon_ci_lower is not None]
    if len(carriers) != 1:
        found = ", ".join(carriers) or "none"
        raise ConfigError(
            f"{origin}: exactly one signal must carry marathon_ci_lower; found {found}"
        )
    return signals


def _attribution(origin: str, doc: Mapping[str, Any]) -> dict[str, str]:
    table = _table(f"{origin}: [sources]", doc.get("sources"))
    _exactly(origin, table, SOURCES, "sources")
    lines: dict[str, str] = {}
    for name in SOURCES:
        where = f"{origin}: [sources.{name}]"
        lines[name] = _text(where, _table(where, table[name], _SOURCE_KEYS), "attribution")
    return lines


def _sentinel(where: str, value: dict[str, Any]) -> Sentinel:
    entry = _table(where, value, _SENTINEL_KEYS)
    canonical = _text(where, entry, "canonical")
    where = f"{where} ({canonical})"
    tier = _text(where, entry, "tier")
    if tier not in TIERS:
        raise ConfigError(f"{where}: tier {tier!r} is not one of {', '.join(TIERS)}")
    votes = _table(f"{where} votes", entry.get("votes"))
    for signal, vote in votes.items():
        if signal not in SIGNALS:
            raise ConfigError(f"{where}: votes names an unknown signal {signal!r}")
        if vote != UNRATED and vote not in FLOOR_TIERS:
            raise ConfigError(
                f"{where}: votes.{signal} {vote!r} is not one of "
                f"{', '.join((UNRATED, *FLOOR_TIERS))}"
            )
    return Sentinel(canonical=canonical, tier=tier, votes=dict(votes))


def _sentinels(origin: str, doc: Mapping[str, Any]) -> tuple[Sentinel, ...]:
    value = doc.get("sentinels", [])
    if not isinstance(value, list) or not all(isinstance(v, dict) for v in value):
        raise ConfigError(f"{origin}: sentinels is not an array of tables; write [[sentinels]]")
    sentinels: list[Sentinel] = []
    seen: set[str] = set()
    for index, entry in enumerate(value, start=1):
        sentinel = _sentinel(f"{origin}: [[sentinels]] #{index}", entry)
        if sentinel.canonical.lower() in seen:
            raise ConfigError(
                f"{origin}: [[sentinels]] #{index} ({sentinel.canonical}): listed twice"
            )
        seen.add(sentinel.canonical.lower())
        sentinels.append(sentinel)
    return tuple(sentinels)


def parse_contracts(text: str, *, origin: str = "contracts.toml") -> Contracts:
    """Read and validate the file's text. `origin` names the file in every
    message. Whether each sentinel names a known key needs models.dev and is
    checked later, by `pipeline.assign_all`."""
    try:
        doc = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{origin}: not TOML: {exc}") from exc
    unknown = sorted(set(doc) - _TOP_KEYS)
    if unknown:
        raise ConfigError(f"{origin}: unknown key {unknown[0]!r}")
    return Contracts(
        version=_text(origin, doc, "version"),
        tiers=_tiers(origin, doc),
        signals=_signals(origin, doc),
        attribution=_attribution(origin, doc),
        sentinels=_sentinels(origin, doc),
        origin=origin,
    )


def load_contracts(path: Path | None = None) -> Contracts:
    """Read `path`, or the committed `data/contracts.toml` when it is None."""
    if path is None:
        text = resources.files("modeltiers").joinpath("data/contracts.toml").read_text("utf-8")
        return parse_contracts(text, origin="contracts.toml")
    try:
        text = path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        raise ConfigError(f"contracts: {path} is not UTF-8 text: {exc}") from exc
    except OSError as exc:
        raise ConfigError(f"contracts: cannot read {path}: {exc.strerror}") from exc
    return parse_contracts(text, origin=path.name)
