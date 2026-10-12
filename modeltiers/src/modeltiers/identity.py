"""Identity: inventory rows and source records to canonical keys.

Three steps, the first that answers wins: an alias, the host's own row in
models.dev, then normalization (the token function matching exactly one key).
"""

from collections.abc import Iterable, Mapping
from dataclasses import dataclass

from modeltiers.aliases import Aliases
from modeltiers.records import InventoryRow, ModelsDev, ModelsDevRow, ScoreRecord
from modeltiers.tokens import is_floating, tokens

ALIAS = "alias"
HOST = "host"
NORMALIZED = "normalized"
MEASURED = "measured"
INHERITED = "inherited"
HOST_NOTE = "host row has no canonical id; matched by name"

# inventory provider_id or source name -> (models.dev provider, suffixes the
# host adds to a model id that models.dev does not list)
_HOSTS: Mapping[str, tuple[str, tuple[str, ...]]] = {
    "ollama": ("ollama-cloud", (":cloud", "-cloud")),
    "openrouter": ("openrouter", (":free", ":batch")),
}


@dataclass(frozen=True)
class Index:
    """What one models.dev snapshot and the aliases say about keys. Every
    mapping keyed by a canonical key uses the key in lower case."""

    keys: Mapping[str, str]  # lower-case key -> the spelling to store
    rows: Mapping[str, tuple[ModelsDevRow, ...]]  # lower-case key -> its models.dev rows
    hosts: Mapping[tuple[str, str], ModelsDevRow]  # (models.dev provider, model id) -> row
    by_tokens: Mapping[tuple[str, ...], tuple[str, ...]]  # token sequence -> stored keys

    def spelling(self, key: str) -> str | None:
        """The stored spelling of a key written in any case, or None."""
        return self.keys.get(key.lower())

    def rows_for(self, key: str) -> tuple[ModelsDevRow, ...]:
        """The models.dev rows of a key; empty for an alias-declared key."""
        return self.rows.get(key.lower(), ())

    def normalized(self, name: str) -> str | None:
        """The one key whose token sequence equals the name's, or None when
        no key or more than one key matches."""
        matches = self.by_tokens.get(tokens(name), ())
        return matches[0] if len(matches) == 1 else None


def build_index(modelsdev: ModelsDev, aliases: Aliases) -> Index:
    """Index a models.dev snapshot. The valid keys are every
    `canonical_model_id`, in models.dev's spelling, plus any key an alias
    declares. A key ending in `-latest` is valid and is left out of the
    normalization index."""
    keys: dict[str, str] = {}
    rows: dict[str, list[ModelsDevRow]] = {}
    hosts: dict[tuple[str, str], ModelsDevRow] = {}
    host_providers = {provider for provider, _ in _HOSTS.values()}
    for row in modelsdev.rows:
        if row.provider in host_providers:
            hosts[(row.provider, row.model_id)] = row
        if row.canonical is not None:
            keys.setdefault(row.canonical.lower(), row.canonical)
            rows.setdefault(row.canonical.lower(), []).append(row)
    for alias in aliases.aliases:
        keys.setdefault(alias.canonical.lower(), alias.canonical)
    by_tokens: dict[tuple[str, ...], list[str]] = {}
    for key in keys.values():
        sequence = tokens(key)
        if sequence and not is_floating(key):
            by_tokens.setdefault(sequence, []).append(key)
    return Index(
        keys=keys,
        rows={key: tuple(found) for key, found in rows.items()},
        hosts=hosts,
        by_tokens={sequence: tuple(found) for sequence, found in by_tokens.items()},
    )


def host_row(index: Index, host: str, model_id: str) -> ModelsDevRow | None:
    """The models.dev row a host lists for one of its own model ids. `host` is
    an inventory provider_id or a source name; one models.dev does not carry
    as a host has no rows. The host's serving suffix is removed first:
    `glm-5.3:cloud` is looked up as `glm-5.3`, `z-ai/glm-5.3:free` as
    `z-ai/glm-5.3`."""
    if host not in _HOSTS:
        return None
    provider, suffixes = _HOSTS[host]
    for suffix in suffixes:
        if model_id.endswith(suffix):
            model_id = model_id.removesuffix(suffix)
            break
    return index.hosts.get((provider, model_id))


@dataclass(frozen=True)
class RowIdentity:
    """How one inventory row resolved. Exactly one of three states: resolved
    (`canonical` and `resolved_by` set), ignored (`ignore_reason` set), or
    unmapped (neither)."""

    row: InventoryRow
    canonical: str | None = None  # in the stored spelling
    resolved_by: str | None = None  # ALIAS | HOST | NORMALIZED
    ignore_reason: str | None = None
    host_row: ModelsDevRow | None = None  # a cloud row's models.dev host row, canonical or not
    notes: tuple[str, ...] = ()

    @property
    def basis(self) -> str:
        """A local build inherits its tier: every public score describes a
        hosted serving of the model."""
        return INHERITED if self.row.location == "local" else MEASURED

    @property
    def unmapped(self) -> bool:
        return self.canonical is None and self.ignore_reason is None


def _stored(index: Index, key: str) -> str:
    return index.spelling(key) or key


def resolve_row(row: InventoryRow, index: Index, aliases: Aliases) -> RowIdentity:
    """Resolve one inventory row: ignore, alias, host row, normalization."""
    pair = (row.provider_id, row.model_name)
    if pair in aliases.inventory_ignore:
        return RowIdentity(row=row, ignore_reason=aliases.inventory_ignore[pair])
    host = host_row(index, row.provider_id, row.model_name) if row.location == "cloud" else None
    if pair in aliases.inventory_alias:
        return RowIdentity(
            row=row,
            canonical=_stored(index, aliases.inventory_alias[pair]),
            resolved_by=ALIAS,
            host_row=host,
        )
    if host is not None and host.canonical is not None:
        return RowIdentity(
            row=row, canonical=_stored(index, host.canonical), resolved_by=HOST, host_row=host
        )
    key = index.normalized(row.model_name)
    if key is None:
        return RowIdentity(row=row, host_row=host)
    return RowIdentity(
        row=row,
        canonical=key,
        resolved_by=NORMALIZED,
        host_row=host,
        notes=(HOST_NOTE,) if host is not None else (),
    )


@dataclass(frozen=True)
class Match:
    """The record a source contributes to one key."""

    canonical: str  # in the stored spelling
    resolved_by: str  # ALIAS | HOST | NORMALIZED
    record: ScoreRecord


@dataclass(frozen=True)
class SourceIdentity:
    """How one source's records resolved."""

    source: str
    records: int  # every record the source gave
    best: Mapping[str, Match]  # lower-case key -> its best-scoring record
    unresolved: tuple[ScoreRecord, ...]  # no step answered; source order
    ignored: int  # records listed under [[ignore]]


def resolve_record(record: ScoreRecord, index: Index, aliases: Aliases) -> Match | None:
    """Resolve one source record: alias, host row (OpenRouter only, since
    models.dev lists no Arena or Epoch ids), normalization."""
    source_id = (record.source, record.source_id)
    if source_id in aliases.source_alias:
        return Match(_stored(index, aliases.source_alias[source_id]), ALIAS, record)
    host = host_row(index, record.source, record.source_id)
    if host is not None and host.canonical is not None:
        return Match(_stored(index, host.canonical), HOST, record)
    key = index.normalized(record.source_id)
    return None if key is None else Match(key, NORMALIZED, record)


def resolve_source(
    source: str, records: Iterable[ScoreRecord], index: Index, aliases: Aliases
) -> SourceIdentity:
    """Resolve every record of one source. When several records reach one
    key, the best-scoring one is kept whole, so its effort and its interval
    are its own; on a tie the earlier record stays."""
    best: dict[str, Match] = {}
    unresolved: list[ScoreRecord] = []
    ignored = 0
    count = 0
    for record in records:
        count += 1
        if (record.source, record.source_id) in aliases.source_ignore:
            ignored += 1
            continue
        match = resolve_record(record, index, aliases)
        if match is None:
            unresolved.append(record)
            continue
        key = match.canonical.lower()
        if key not in best or record.score > best[key].record.score:
            best[key] = match
    return SourceIdentity(
        source=source, records=count, best=best, unresolved=tuple(unresolved), ignored=ignored
    )
