"""What `show` and `explain` print. Everything here reads the output document
alone: a dict as `output.read_document` returns it. No source, no inventory,
no contracts file. A field an older file lacks prints as `?`."""

from collections.abc import Callable, Iterable, Mapping
from datetime import UTC, datetime
from functools import partial
from math import isfinite
from typing import Any

from modeltiers.align import ANCHOR
from modeltiers.contracts import CI_LOWER, RANK, SIGNAL_SOURCE, SOURCES, STAMP

STALE_AFTER_DAYS = 14

Document = Mapping[str, Any]
# What a row under `[[unmapped]]` is called where a tier or a reason goes.
UNMAPPED = "unmapped"

# Which source each score in an entry came from, for the attribution lines:
# the signal's source, plus the record fields the signal's source adds. A
# recalibrated carrier (a signal gaining `marathon_ci_lower`) needs no edit
# here, so no source's attribution line can silently drop out of `explain`.
_SCORE_SOURCE: Mapping[str, str] = {
    key: source for name, source in SIGNAL_SOURCE.items() for key in (name, f"{name}{CI_LOWER}")
} | {"coding_index": "openrouter", "agentic_index": "openrouter"}

# The scores the wide table and the page show, with the table's heading for each.
SCORE_COLUMNS: Mapping[str, str] = {
    "intelligence_index": "intel",
    "arena_agent": "arena",
    "eci": "eci",
    "coding_index": "coding",
    "agentic_index": "agentic",
}


def model_name(entry: Mapping[str, Any]) -> str:
    """`provider_id/model_name`, the name a `[[models]]` or `[[unmapped]]`
    entry is listed and looked up by."""
    return f"{entry.get('provider_id', '?')}/{entry.get('model_name', '?')}"


def entries(doc: Document, name: str) -> list[Mapping[str, Any]]:
    """The `[[name]]` array as a list of tables. A file that holds a single
    table, or a list of non-tables, where an array belongs is compared by
    whatever it has instead: those read as absent."""
    value = doc.get(name, [])
    if not isinstance(value, list):
        return []
    return [entry for entry in value if isinstance(entry, dict)]


def table(value: Any) -> Mapping[str, Any]:
    """A table of the file, or an empty one when the file holds something
    else where a table belongs: every reader then finds nothing in it."""
    return value if isinstance(value, dict) else {}


def age_days(doc: Document, now: datetime) -> int | None:
    """Whole days since the file was generated; None when it does not say."""
    try:
        generated = datetime.strptime(str(doc.get("generated_at")), STAMP).replace(tzinfo=UTC)
    except ValueError:
        return None
    return max((now - generated).days, 0)


def age_line(doc: Document, now: datetime) -> str:
    days = age_days(doc, now)
    age = "of unknown age" if days is None else f"{days} days old"
    return (
        f"tiers.toml generated {doc.get('generated_at', '?')} ({age}), "
        f"contracts {doc.get('contracts_version', '?')}"
    )


def stale_warning(doc: Document, now: datetime) -> str | None:
    """The warning for a file past STALE_AFTER_DAYS, else None."""
    days = age_days(doc, now)
    if days is None or days <= STALE_AFTER_DAYS:
        return None
    return f"tiers.toml is {days} days old; run `modeltiers run` to refresh it"


def number(value: Any) -> float | None:
    """A number the file holds, or None for anything else. `True` is not a
    number here, though Python counts it as one, and neither are the `nan`
    and `inf` that TOML can spell, nor an integer too large for a float:
    nothing sorts or prints as any of them."""
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    try:
        found = float(value)
    except OverflowError:
        return None
    return found if isfinite(found) else None


def score(entry: Mapping[str, Any], name: str) -> float | None:
    """One of an entry's scores, or None when the file gives no number for it."""
    return number(table(entry.get("scores")).get(name))


def aligned_score(entry: Mapping[str, Any]) -> float | None:
    return number(entry.get("aligned_score"))


def aligned_count(entry: Mapping[str, Any]) -> int | None:
    """How many scores an entry's aligned score rests on: None without a
    score, and for anything but a list of signal names beside it."""
    sources = entry.get("aligned_sources")
    if aligned_score(entry) is None or not isinstance(sources, list):
        return None
    return len(sources) if all(isinstance(name, str) for name in sources) else None


def _rank(entry: Mapping[str, Any]) -> float | None:
    tier = entry.get("tier")
    return float(RANK[tier]) if isinstance(tier, str) and tier in RANK else None


# What each numeric column of `show --sort` orders an entry by.
_SORT_VALUES: Mapping[str, Callable[[Mapping[str, Any]], float | None]] = {
    "tier": _rank,
    "context": lambda entry: number(entry.get("context")),
    **{name: partial(score, name=name) for name in SCORE_COLUMNS},
    "aligned": aligned_score,
}
# What `show --sort` takes. `model` is alphabetical; the rest are highest first.
SORT_COLUMNS = ("tier", "model", *(name for name in _SORT_VALUES if name != "tier"))
# The order `show` and the page use when no `--sort` is given.
DEFAULT_SORT = "aligned"


def ordered(
    found: list[Mapping[str, Any]], column: str | None, name: Callable[[Mapping[str, Any]], str]
) -> list[Mapping[str, Any]]:
    """The entries in `show --sort` order: by `name` for `model`, else
    highest first with the entries that have no value last. Entries that
    compare equal keep the order the file has them in; None keeps it whole. A
    column that is not one of SORT_COLUMNS is read as a score of that name,
    which the file gives no entry for: the order it came in."""
    if column is None:
        return found
    if column == "model":
        return sorted(found, key=lambda entry: name(entry).lower())

    value = _SORT_VALUES.get(column, partial(score, name=column))

    def key(entry: Mapping[str, Any]) -> tuple[bool, float]:
        found_value = value(entry)
        return (found_value is None, -(found_value or 0.0))

    return sorted(found, key=key)


def _wide_heading() -> str:
    scores = "  ".join(f"{heading:>8}" for heading in (*SCORE_COLUMNS.values(), "aligned"))
    return f"{'open':<4}  {'context':>8}  {scores}  {'src':>3}  "


def _wide_cells(entry: Mapping[str, Any]) -> str:
    """Open weights, context, the scores, the aligned score and the number
    of scores it rests on, for one entry; `-` for each the file does not give."""
    opened = entry.get("open_weights")
    flag = "T" if opened is True else "F" if opened is False else "-"
    context = number(entry.get("context"))
    values = (score(entry, name) for name in SCORE_COLUMNS)
    scores = "  ".join(f"{'-' if v is None else short(v):>8}" for v in values)
    aligned = aligned_score(entry)
    count = aligned_count(entry)
    return (
        f"{flag:<4}  {'-' if context is None else int(context):>8}  {scores}  "
        f"{'-' if aligned is None else format(aligned, '.1f'):>8}  "
        f"{'-' if count is None else count:>3}  "
    )


def _row(entry: Mapping[str, Any], name: str, width: int, canonical: str | None, wide: bool) -> str:
    reason = f"  ({entry['reason']})" if "reason" in entry else ""
    tail = "" if canonical is None else f"  {canonical}"
    return (
        f"{entry.get('code', '?')}  {entry.get('tier', '?'):<8}  "
        f"{entry.get('confidence', '?'):<6}  {_wide_cells(entry) if wide else ''}"
        f"{name:<{width}}{tail}{reason}".rstrip()
    )


def candidate_name(entry: Mapping[str, Any]) -> str:
    """The canonical key, the name a `[[candidates]]` entry is listed by."""
    return str(entry.get("canonical", "?"))


def table_lines(
    doc: Document, *, candidates: bool = False, sort: str | None = None, wide: bool = False
) -> list[str]:
    """The tier table: one line per `[[models]]` entry, the unmapped rows,
    and with `candidates` one line per `[[candidates]]` entry. The models and
    the candidates are each in file order, or in `sort` order (a name from
    SORT_COLUMNS). `wide` adds open weights, context, the scores, the aligned
    score and the number of scores it rests on."""
    models = ordered(entries(doc, "models"), sort, model_name)
    unmapped = entries(doc, "unmapped")
    width = max([len(model_name(m)) for m in models] + [len("model")])
    extra = _wide_heading() if wide else ""
    lines = [f"T  {'tier':<8}  {'conf':<6}  {extra}{'model':<{width}}  canonical"]
    lines.extend(_row(m, model_name(m), width, m.get("canonical", "-"), wide) for m in models)
    lines.append(f"unmapped: {len(unmapped)} (unrated until aliased or ignored)")
    lines.extend(f"  {model_name(entry)}" for entry in unmapped)
    found = entries(doc, "candidates")
    if candidates:
        lines.append(f"candidates: {len(found)} (scored by a source, not in the inventory)")
        lines.extend(
            _row(c, candidate_name(c), 0, None, wide) for c in ordered(found, sort, candidate_name)
        )
    else:
        lines.append(f"candidates: {len(found)} (list them with --candidates)")
    return lines


def attribution_lines(doc: Document, sources: Iterable[str] = SOURCES) -> list[str]:
    """The attribution line of each named source the file has one for."""
    tables = table(doc.get("sources"))
    wanted = set(sources)
    return [
        f"{name}: {table(tables.get(name))['attribution']}"
        for name in SOURCES
        if name in wanted and "attribution" in table(tables.get(name))
    ]


def short(value: Any) -> str:
    """A score for display: at most four decimal places, no trailing zeros,
    and no sign on a value that rounds to zero."""
    if isinstance(value, float):
        text = f"{value:.4f}".rstrip("0").rstrip(".")
        return "0" if text == "-0" else text
    return str(value)


def _pairs(table: Mapping[str, Any]) -> str:
    if not isinstance(table, dict):
        return str(table)
    return ", ".join(f"{key} {short(value)}" for key, value in table.items()) or "none"


def _aligned(entry: Mapping[str, Any], alignment: Any) -> str:
    """The aligned score and what it is the mean of: the index as it is, and
    each other score with the index points the file's line turns it into,
    held within the file's bounds. A source whose line the file does not give
    is named alone."""
    fits = table(alignment)
    floor, ceiling = number(fits.get("floor")), number(fits.get("ceiling"))
    found = aligned_score(entry)
    if found is None:
        return "none"
    sources = entry.get("aligned_sources")
    if not isinstance(sources, list) or not sources:
        return f"{found:.1f}"
    parts = []
    for name in sources:
        raw = score(entry, str(name))
        fit = table(fits.get(name) if isinstance(name, str) else None)
        slope, intercept = number(fit.get("slope")), number(fit.get("intercept"))
        if raw is not None and name == ANCHOR:
            parts.append(f"{name} {short(raw)}")
        elif raw is None or slope is None or intercept is None:
            parts.append(str(name))
        else:
            points = slope * raw + intercept
            points = points if floor is None else max(points, floor)
            points = points if ceiling is None else min(points, ceiling)
            parts.append(f"{name} {short(raw)} as {points:.1f}")
    return f"{found:.1f} ({'; '.join(parts)})"


def _entry_lines(title: str, entry: Mapping[str, Any], alignment: Any) -> list[str]:
    tier = f"{entry.get('tier', '?')} ({entry.get('code', '?')})"
    if "reason" in entry:
        tier += f", reason {entry['reason']}"
    basis = f", basis {entry['basis']}" if "basis" in entry else ""
    identity = "not resolved (listed under [[ignore]])"
    if "canonical" in entry:
        identity = f"{entry['canonical']}, resolved by {entry.get('resolved_by', '?')}"
    lines = [
        title,
        f"  identity:    {identity}",
        f"  tier:        {tier}",
        f"  confidence:  {entry.get('confidence', '?')}{basis}",
        f"  votes:       {_pairs(entry.get('votes', {}))}",
        f"  scores:      {_pairs(entry.get('scores', {}))}",
        f"  aligned:     {_aligned(entry, alignment)}",
        f"  efforts:     {_pairs(entry.get('efforts', {}))}",
        f"  tool_call:   {str(entry['tool_call']).lower() if 'tool_call' in entry else 'unknown'}",
        f"  context:     {entry.get('context', 'unknown')}",
    ]
    if "open_weights" in entry:
        lines.append(f"  open_weights: {str(entry['open_weights']).lower()}")
    lines.extend(f"  note:        {note}" for note in entry.get("notes", []))
    return lines


def _used_sources(entries: Iterable[Mapping[str, Any]]) -> set[str]:
    used: set[str] = set()
    for entry in entries:
        if "canonical" in entry:
            used.add("modelsdev")
        used.update(
            _SCORE_SOURCE[name] for name in entry.get("scores", {}) if name in _SCORE_SOURCE
        )
    return used


def _has_key(entry: Mapping[str, Any], key: str) -> bool:
    """True when the entry resolved to `key`, compared without regard to
    case. An ignored row has no canonical and matches no key."""
    return "canonical" in entry and str(entry["canonical"]).lower() == key.lower()


def explain_lines(doc: Document, name: str) -> list[str] | None:
    """Everything the file says about one model, or None when nothing
    matches. `name` is tried as an exact `provider_id/model_name` first, then
    as a canonical key without regard to case: every `[[models]]` entry that
    resolved to it, or else the candidate. A row still under `[[unmapped]]`
    says it needs an alias, after the key searches missed."""
    models = entries(doc, "models")
    found = [(model_name(m), m) for m in models if model_name(m) == name]
    if not found:
        found = [(model_name(m), m) for m in models if _has_key(m, name)]
    if not found:
        found = [
            (f"candidate {c['canonical']}", c)
            for c in entries(doc, "candidates")
            if _has_key(c, name)
        ]
    if not found:
        for entry in entries(doc, "unmapped"):
            if model_name(entry) == name:
                return [
                    f"{name} is unmapped: no alias, host row or name match gave it a "
                    "canonical key, so it is unrated.",
                    "Add an [[alias]] or an [[ignore]] for it in aliases.toml and run "
                    "`modeltiers run`.",
                ]
        return None
    lines: list[str] = []
    for title, entry in found:
        lines.extend(_entry_lines(title, entry, doc.get("alignment")))
    lines.extend(attribution_lines(doc, _used_sources(entry for _, entry in found)))
    return lines
