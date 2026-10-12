from modeltiers import identity, report
from modeltiers.aliases import Aliases
from modeltiers.identity import RowIdentity, SourceIdentity
from modeltiers.pipeline import Identity
from modeltiers.records import InventoryRow, ModelsDev


def _identity(*rows: RowIdentity) -> Identity:
    def empty(source: str) -> SourceIdentity:
        return SourceIdentity(source=source, records=0, best={}, unresolved=(), ignored=0)

    return Identity(
        index=identity.build_index(ModelsDev(rows=()), Aliases()),
        rows=rows,
        openrouter=empty("openrouter"),
        arena=empty("arena"),
        epoch=empty("epoch"),
        warnings=(),
    )


def test_an_empty_inventory_still_prints_every_section() -> None:
    assert report.identity_lines(_identity()) == [
        "identity: 0 rows: 0 resolved, 0 ignored, 0 unmapped",
        "unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml)",
        "review: 0 matched by name alone (check each)",
        "unresolved: source records no step resolved (counts only; "
        "listing those at or above a standard floor needs contracts.toml)",
        "  openrouter: 0 of 0 records, 0 ignored, 0 keys resolved",
        "  arena: 0 of 0 records, 0 ignored, 0 keys resolved",
        "  epoch: 0 of 0 records, 0 ignored, 0 keys resolved",
    ]


def test_the_name_column_is_as_wide_as_the_longest_name() -> None:
    short = RowIdentity(InventoryRow("omlx", "m", "local", True))
    long = RowIdentity(
        InventoryRow("mtplx", "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", "local", True),
        canonical="alibaba/qwen3.8-27b",
        resolved_by="alias",
    )
    lines = report.identity_lines(_identity(short, long))
    assert lines[1] == "  omlx/m" + " " * 45 + "  unmapped"
    assert lines[2] == (
        "  mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality  alias       "
        "alibaba/qwen3.8-27b  [inherited]"
    )
    # An alias-resolved row is not on the review list; only name matches are.
    assert lines[5] == "review: 0 matched by name alone (check each)"
