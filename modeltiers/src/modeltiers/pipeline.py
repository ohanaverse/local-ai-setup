"""The stages after loading, run in order over one inventory and one set of
sources. Step 2 of the design stops at identity."""

from dataclasses import dataclass

from modeltiers import aliases as aliases_mod
from modeltiers import identity
from modeltiers.aliases import Aliases
from modeltiers.identity import Index, RowIdentity, SourceIdentity
from modeltiers.loader import Sources
from modeltiers.records import Inventory


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
        return (self.openrouter, self.arena, self.epoch)

    @property
    def unmapped(self) -> tuple[RowIdentity, ...]:
        """Rows no step resolved and no `[[ignore]]` lists."""
        return tuple(r for r in self.rows if r.unmapped)

    @property
    def review(self) -> tuple[RowIdentity, ...]:
        """Rows matched by name alone, for a person to check."""
        return tuple(r for r in self.rows if r.resolved_by == identity.NORMALIZED)


def resolve_all(inventory: Inventory, sources: Sources, aliases: Aliases) -> Identity:
    """Resolve the inventory and the three benchmark sources against one
    models.dev snapshot. Raises ConfigError for an alias whose canonical is
    neither a models.dev key nor anchored by a source id."""
    modelsdev = sources.modelsdev.data
    aliases_mod.check_anchored(aliases, (r.canonical for r in modelsdev.rows if r.canonical))
    index = identity.build_index(modelsdev, aliases)
    data = {
        "openrouter": sources.openrouter.data,
        "arena": sources.arena.data,
        "epoch": sources.epoch.data,
    }
    present = {name: frozenset(r.source_id for r in records) for name, records in data.items()}
    return Identity(
        index=index,
        rows=tuple(identity.resolve_row(row, index, aliases) for row in inventory.rows),
        openrouter=identity.resolve_source("openrouter", data["openrouter"], index, aliases),
        arena=identity.resolve_source("arena", data["arena"], index, aliases),
        epoch=identity.resolve_source("epoch", data["epoch"], index, aliases),
        warnings=tuple(aliases_mod.stale_ids(aliases, present)),
    )
