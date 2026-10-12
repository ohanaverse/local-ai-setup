"""`aliases.toml`: the hand-kept answers the resolver tries first.

`[[alias]]` pins inventory pairs and source ids to a canonical key.
`[[ignore]]` acknowledges a row or a record that will never resolve.
"""

import tomllib
from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field, replace
from importlib import resources
from pathlib import Path
from typing import Any

from modeltiers.errors import ConfigError
from modeltiers.sources.arena import SOURCE as ARENA
from modeltiers.sources.arena import split_effort
from modeltiers.sources.epoch import SOURCE as EPOCH
from modeltiers.sources.openrouter import SOURCE as OPENROUTER
from modeltiers.tokens import is_floating, tokens

# The benchmark sources, in the order the report lists them. `Alias`,
# `loader.Sources` and `pipeline.Identity` each have a field of every name.
SOURCES = (OPENROUTER, ARENA, EPOCH)
Pair = tuple[str, str]  # (provider_id, model_name)
SourceId = tuple[str, str]  # (source, the source's own id)

_ALIAS_KEYS = frozenset({"canonical", "note", "inventory", *SOURCES})
_IGNORE_KEYS = frozenset({"reason", "inventory", "source", "id"})
_PAIR_KEYS = frozenset({"provider_id", "model_name"})


@dataclass(frozen=True)
class Alias:
    """One `[[alias]]` entry. `build` removes the effort parenthesis from its
    Arena ids, so every entry of an `Aliases` holds them as the records do."""

    canonical: str
    note: str = ""
    inventory: tuple[Pair, ...] = ()
    openrouter: tuple[str, ...] = ()
    arena: tuple[str, ...] = ()
    epoch: tuple[str, ...] = ()

    def source_ids(self) -> tuple[SourceId, ...]:
        """Every (source, id) the entry lists, in the order of SOURCES."""
        return tuple((source, sid) for source in SOURCES for sid in getattr(self, source))


@dataclass(frozen=True)
class Ignore:
    """One `[[ignore]]` entry: an inventory pair, or a source and its id."""

    reason: str
    inventory: Pair | None = None
    source: str | None = None
    id: str | None = None


@dataclass(frozen=True)
class Aliases:
    """The file, with the four lookups the resolver needs. Build one with
    `parse_aliases` or `build`, which refuse a pair or an id claimed twice."""

    aliases: tuple[Alias, ...] = ()
    ignores: tuple[Ignore, ...] = ()
    inventory_alias: Mapping[Pair, str] = field(default_factory=dict)  # pair -> canonical
    source_alias: Mapping[SourceId, str] = field(default_factory=dict)  # id -> canonical
    inventory_ignore: Mapping[Pair, str] = field(default_factory=dict)  # pair -> reason
    source_ignore: Mapping[SourceId, str] = field(default_factory=dict)  # id -> reason
    origin: str = "aliases.toml"  # the file's name, for messages


def _alias_label(index: int, alias: Alias) -> str:
    return f"[[alias]] #{index} ({alias.canonical})"


def build(
    aliases: Iterable[Alias], ignores: Iterable[Ignore], *, origin: str = "aliases.toml"
) -> Aliases:
    """Index the entries. An Arena id loses its effort parenthesis here, for
    an entry read from the file and for one built in code alike, since the
    records carry none. Two claims on one inventory pair or one source id, in
    two entries or in one, are a ConfigError that names both claimants."""
    kept_aliases = tuple(
        replace(alias, arena=tuple(split_effort(sid)[0] for sid in alias.arena))
        for alias in aliases
    )
    kept_ignores = tuple(
        replace(ignore, id=split_effort(ignore.id)[0])
        if ignore.source == ARENA and ignore.id is not None
        else ignore
        for ignore in ignores
    )
    pair_owner: dict[Pair, str] = {}
    id_owner: dict[SourceId, str] = {}

    def claim_pair(pair: Pair, label: str) -> None:
        if pair in pair_owner:
            raise ConfigError(
                f"{origin}: inventory pair {pair[0]}/{pair[1]} is claimed twice: "
                f"by {pair_owner[pair]} and by {label}"
            )
        pair_owner[pair] = label

    def claim_id(source_id: SourceId, label: str) -> None:
        if source_id in id_owner:
            raise ConfigError(
                f"{origin}: {source_id[0]} id {source_id[1]!r} is claimed twice: "
                f"by {id_owner[source_id]} and by {label}"
            )
        id_owner[source_id] = label

    inventory_alias: dict[Pair, str] = {}
    source_alias: dict[SourceId, str] = {}
    inventory_ignore: dict[Pair, str] = {}
    source_ignore: dict[SourceId, str] = {}
    for index, alias in enumerate(kept_aliases, start=1):
        label = _alias_label(index, alias)
        for pair in alias.inventory:
            claim_pair(pair, label)
            inventory_alias[pair] = alias.canonical
        for source_id in alias.source_ids():
            claim_id(source_id, label)
            source_alias[source_id] = alias.canonical
    for index, ignore in enumerate(kept_ignores, start=1):
        label = f"[[ignore]] #{index}"
        if ignore.inventory is not None:
            claim_pair(ignore.inventory, label)
            inventory_ignore[ignore.inventory] = ignore.reason
        if ignore.source is not None and ignore.id is not None:
            claim_id((ignore.source, ignore.id), label)
            source_ignore[(ignore.source, ignore.id)] = ignore.reason
    return Aliases(
        aliases=kept_aliases,
        ignores=kept_ignores,
        inventory_alias=inventory_alias,
        source_alias=source_alias,
        inventory_ignore=inventory_ignore,
        source_ignore=source_ignore,
        origin=origin,
    )


def _text(where: str, entry: Mapping[str, Any], key: str) -> str:
    value = entry.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ConfigError(f"{where}: {key} is missing, empty or not a string")
    return _unpadded(f"{where}: {key}", value)


def _unpadded(where: str, value: str) -> str:
    """A string that is matched exactly, so one with a space around it would
    be accepted and then never match anything."""
    if value != value.strip():
        raise ConfigError(f"{where} {value!r} has a space at its start or its end")
    return value


def _known_keys(where: str, entry: Mapping[str, Any], allowed: frozenset[str]) -> None:
    unknown = sorted(set(entry) - allowed)
    if unknown:
        raise ConfigError(f"{where}: unknown key {unknown[0]!r}")


def _pair(where: str, value: Any) -> Pair:
    if not isinstance(value, dict):
        raise ConfigError(f"{where}: expected {{ provider_id = ..., model_name = ... }}")
    _known_keys(where, value, _PAIR_KEYS)
    return (
        _text(where, value, "provider_id"),
        _text(where, value, "model_name"),
    )


def _id_list(where: str, entry: Mapping[str, Any], source: str) -> tuple[str, ...]:
    value = entry.get(source, [])
    if not isinstance(value, list) or not all(isinstance(v, str) and v.strip() for v in value):
        raise ConfigError(f"{where}: {source} is not a list of non-empty strings")
    return tuple(_unpadded(f"{where}: {source} id", v) for v in value)


def _tables(origin: str, doc: Mapping[str, Any], key: str) -> list[dict[str, Any]]:
    value = doc.get(key, [])
    if not isinstance(value, list) or not all(isinstance(v, dict) for v in value):
        raise ConfigError(f"{origin}: {key} is not an array of tables; write [[{key}]]")
    return value


def _alias(where: str, entry: Mapping[str, Any]) -> Alias:
    _known_keys(where, entry, _ALIAS_KEYS)
    canonical = _text(where, entry, "canonical")
    where = f"{where} ({canonical})"
    note = entry.get("note", "")
    pairs = entry.get("inventory", [])
    if not isinstance(note, str):
        raise ConfigError(f"{where}: note is not a string")
    if not isinstance(pairs, list):
        raise ConfigError(f"{where}: inventory is not a list of pairs")
    return Alias(
        canonical=canonical,
        note=note,
        inventory=tuple(
            _pair(f"{where} inventory[{position}]", pair) for position, pair in enumerate(pairs)
        ),
        **{source: _id_list(where, entry, source) for source in SOURCES},
    )


def _ignore(where: str, entry: Mapping[str, Any]) -> Ignore:
    _known_keys(where, entry, _IGNORE_KEYS)
    reason = entry.get("reason")
    if not isinstance(reason, str) or not reason.strip():
        raise ConfigError(f"{where}: an ignore needs a reason")
    has_pair = "inventory" in entry
    has_source = "source" in entry or "id" in entry
    if has_pair == has_source:
        raise ConfigError(f"{where}: give either inventory, or source and id")
    if has_pair:
        return Ignore(reason=reason, inventory=_pair(f"{where} inventory", entry["inventory"]))
    source = _text(where, entry, "source")
    if source not in SOURCES:
        raise ConfigError(f"{where}: source {source!r} is not one of {', '.join(SOURCES)}")
    return Ignore(reason=reason, source=source, id=_text(where, entry, "id"))


def parse_aliases(text: str, *, origin: str = "aliases.toml") -> Aliases:
    """Read and validate the file's text. This is the validation that runs
    before any fetch: shape, a string with a space around it, an `[[ignore]]`
    without a reason, and a pair or an id claimed twice. `origin` names the file in every message."""
    try:
        doc = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{origin}: not TOML: {exc}") from exc
    unknown = sorted(set(doc) - {"alias", "ignore"})
    if unknown:
        raise ConfigError(f"{origin}: unknown table {unknown[0]!r}")
    aliases = [
        _alias(f"{origin}: [[alias]] #{index}", entry)
        for index, entry in enumerate(_tables(origin, doc, "alias"), start=1)
    ]
    ignores = [
        _ignore(f"{origin}: [[ignore]] #{index}", entry)
        for index, entry in enumerate(_tables(origin, doc, "ignore"), start=1)
    ]
    return build(aliases, ignores, origin=origin)


def load_aliases(path: Path | None = None) -> Aliases:
    """Read `path`, or the committed `data/aliases.toml` when it is None."""
    if path is None:
        text = resources.files("modeltiers").joinpath("data/aliases.toml").read_text("utf-8")
        return parse_aliases(text, origin="aliases.toml")
    try:
        text = path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        raise ConfigError(f"aliases: {path} is not UTF-8 text: {exc}") from exc
    except OSError as exc:
        raise ConfigError(f"aliases: cannot read {path}: {exc.strerror}") from exc
    return parse_aliases(text, origin=path.name)


def check_anchored(aliases: Aliases, modelsdev_keys: Iterable[str]) -> None:
    """The validation that needs models.dev: an alias whose canonical is not
    a models.dev key (compared without regard to case) must list at least one
    source id. One that lists only inventory pairs would create a key no
    source can ever score."""
    known = {key.lower() for key in modelsdev_keys}
    for index, alias in enumerate(aliases.aliases, start=1):
        if alias.canonical.lower() not in known and not alias.source_ids():
            raise ConfigError(
                f"{aliases.origin}: {_alias_label(index, alias)}: canonical is not a models.dev key "
                f"and the entry lists no {', '.join(SOURCES[:-1])} or {SOURCES[-1]} id"
            )


def check_spelling(aliases: Aliases, modelsdev_keys: Iterable[str]) -> None:
    """The other validation that needs models.dev: a key an alias declares (a
    canonical that is not a models.dev key) must not have the token sequence
    of another key. It would be a second key for one model, such as
    `anthropic/claude-opus-5.5` beside `anthropic/claude-opus-5-5`, and name
    matching answers only when one key has the sequence, so it would stop
    answering for both. A `-latest` key is never matched by name and is not
    checked."""
    known = {key.lower(): key for key in modelsdev_keys}
    by_tokens: dict[tuple[str, ...], str] = {}
    for lowered in sorted(known):
        if not is_floating(lowered):
            by_tokens.setdefault(tokens(lowered), known[lowered])
    for index, alias in enumerate(aliases.aliases, start=1):
        lowered = alias.canonical.lower()
        sequence = tokens(lowered)
        if lowered in known or is_floating(lowered) or not sequence:
            continue
        other = by_tokens.setdefault(sequence, alias.canonical)
        if other.lower() != lowered:
            raise ConfigError(
                f"{aliases.origin}: {_alias_label(index, alias)}: canonical reads the same as "
                f"the key {other}, so name matching could not tell the two apart; "
                "use that key, or one that reads differently"
            )


def stale_ids(aliases: Aliases, present: Mapping[str, frozenset[str]]) -> list[str]:
    """One warning per alias source id that this run's snapshot of the source
    does not hold. Models leave leaderboards; the alias may be out of date."""
    return [
        f"alias for {alias.canonical} names {source} id {source_id!r}, "
        f"which is not in this run's {source} data"
        for alias in aliases.aliases
        for source, source_id in alias.source_ids()
        if source_id not in present.get(source, frozenset())
    ]
