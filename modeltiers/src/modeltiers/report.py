"""What `run` prints about identity: how every inventory row resolved, the
unmapped list, the review list and each source's unresolved count."""

from modeltiers.identity import INHERITED, RowIdentity
from modeltiers.pipeline import Identity


def _name(entry: RowIdentity) -> str:
    return f"{entry.row.provider_id}/{entry.row.model_name}"


def _outcome(entry: RowIdentity) -> str:
    if entry.ignore_reason is not None:
        return f"{'ignored':<10}  {entry.ignore_reason}"
    if entry.canonical is None:
        return "unmapped"
    extras = [INHERITED] if entry.basis == INHERITED else []
    extras.extend(entry.notes)
    tail = f"  [{'; '.join(extras)}]" if extras else ""
    return f"{entry.resolved_by:<10}  {entry.canonical}{tail}"


def identity_lines(result: Identity) -> list[str]:
    """The identity section of the report, one string per line: a row per
    inventory model in wt's order, then the unmapped list, the review list
    (rows matched by name alone) and one count line per benchmark source."""
    rows = result.rows
    resolved = sum(1 for r in rows if r.canonical is not None)
    ignored = sum(1 for r in rows if r.ignore_reason is not None)
    width = max((len(_name(r)) for r in rows), default=0)
    lines = [
        f"identity: {len(rows)} rows: {resolved} resolved, {ignored} ignored, "
        f"{len(result.unmapped)} unmapped"
    ]
    lines.extend(f"  {_name(r):<{width}}  {_outcome(r)}" for r in rows)
    lines.append(
        f"unmapped: {len(result.unmapped)} (each needs an [[alias]] or an [[ignore]] "
        "in aliases.toml)"
    )
    lines.extend(f"  {_name(r)}" for r in result.unmapped)
    lines.append(f"review: {len(result.review)} matched by name alone (check each)")
    lines.extend(f"  {_name(r)} -> {r.canonical}" for r in result.review)
    lines.append(
        "unresolved: source records no step resolved (counts only; "
        "listing those at or above a standard floor needs contracts.toml)"
    )
    lines.extend(
        f"  {s.source}: {len(s.unresolved)} of {s.records} records, "
        f"{s.ignored} ignored, {len(s.best)} keys resolved"
        for s in result.sources()
    )
    return lines
