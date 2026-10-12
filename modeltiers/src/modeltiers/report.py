"""What `run` prints from the pipeline's own results: how every inventory row
resolved, the unmapped list, the review list, the unresolved records and the
sentinel check. The change report is `changes.py`; the table is `show.py`."""

from modeltiers.identity import INHERITED, RowIdentity
from modeltiers.pipeline import Assignment, Identity
from modeltiers.show import short


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
    inventory model in wt's order, then the unmapped list and the review list
    (rows matched by name alone)."""
    rows = result.rows
    resolved = sum(1 for r in rows if r.canonical is not None)
    ignored = sum(1 for r in rows if r.ignore_reason is not None)
    unmapped = result.unmapped
    review = result.review
    width = max((len(_name(r)) for r in rows), default=0)
    lines = [
        f"identity: {len(rows)} rows: {resolved} resolved, {ignored} ignored, "
        f"{len(unmapped)} unmapped"
    ]
    lines.extend(f"  {_name(r):<{width}}  {_outcome(r)}" for r in rows)
    lines.append(
        f"unmapped: {len(unmapped)} (each needs an [[alias]] or an [[ignore]] "
        "in aliases.toml, or is left open until evidence identifies it)"
    )
    lines.extend(f"  {_name(r)}" for r in unmapped)
    lines.append(f"review: {len(review)} matched by name alone (check each)")
    lines.extend(f"  {_name(r)} -> {r.canonical}" for r in review)
    return lines


def unresolved_lines(assignment: Assignment) -> list[str]:
    """The source records no step resolved: those at or above their signal's
    standard floor by name, then one count line per benchmark source."""
    listed = assignment.unresolved
    lines = [
        f"unresolved: {len(listed)} at or above a standard floor "
        "(each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)"
    ]
    lines.extend(f"  {r.source}  {r.source_id}  {short(r.score)}" for r in listed)
    lines.extend(
        f"  {s.source}: {len(s.unresolved)} of {s.records} records unresolved, "
        f"{s.ignored} ignored, {len(s.best)} keys resolved"
        for s in assignment.identity.sources()
    )
    return lines


def sentinel_lines(assignment: Assignment) -> list[str]:
    """One line when every sentinel holds. Otherwise every sentinel: its
    scores, its votes, its pinned values and what differs."""
    results = assignment.sentinels
    failed = [r for r in results if not r.ok]
    if not failed:
        return [f"sentinels: {len(results)} of {len(results)} hold"]
    lines = [f"sentinels: {len(failed)} of {len(results)} FAILED"]
    for result in results:
        evidence, pinned = result.evidence, result.sentinel
        scores = ", ".join(f"{k} {short(v)}" for k, v in evidence.scores.items()) or "none"
        votes = ", ".join(f"{k} {v}" for k, v in evidence.votes.items()) or "none"
        pins = ", ".join(f"{k} {v}" for k, v in pinned.votes.items()) or "none"
        lines.append(f"  {'ok  ' if result.ok else 'FAIL'}  {result.canonical}")
        lines.append(f"        scores:  {scores}")
        lines.append(f"        votes:   {votes}; tier before the gates {evidence.pre_gate}")
        lines.append(f"        pinned:  {pins}; tier {pinned.tier}")
        lines.extend(f"        differs: {problem}" for problem in result.problems)
    return lines
