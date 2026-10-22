"""`tiers.toml`: build the document, write it atomically, read it back.

The document is a plain dict in the order it is written. Values are formatted
by `tomli_w`; `render` lays the file out itself so that every entry of the
four arrays is its own `[[models]]`-style block, in the spec's table order,
whatever its length.
"""

import os
import tempfile
import tomllib
from datetime import datetime
from pathlib import Path
from typing import Any

import tomli_w

from modeltiers.contracts import STAMP, Contracts
from modeltiers.errors import OutputFileError
from modeltiers.loader import Sources
from modeltiers.pipeline import Assignment, Candidate, ModelResult, Rated
from modeltiers.records import Inventory

ARRAYS = ("models", "candidates", "unmapped", "unresolved")  # in the order written

Document = dict[str, Any]


def _rating(rated: Rated, contracts: Contracts, *, basis: str | None = None) -> Document:
    """The fields a `[[models]]` and a `[[candidates]]` entry share, from
    `tier` on. `basis` is an inventory row's alone. A field whose value is
    unknown is left out: `reason` on a rated entry, `open_weights` and
    `tool_call` for a key with no models.dev rows, `context` when no row
    gives one, `card_kind` with `card_url` when no link is known, and
    `aligned_score` with `aligned_sources` when no score could be aligned."""
    evidence, facts, verdict, aligned = rated.evidence, rated.facts, rated.verdict, rated.aligned
    entry: Document = {"tier": verdict.tier, "code": contracts.code(verdict.tier)}
    if verdict.reason is not None:
        entry["reason"] = verdict.reason
    entry["confidence"] = evidence.confidence
    if basis is not None:
        entry["basis"] = basis
    if rated.open_weights is not None:
        entry["open_weights"] = rated.open_weights
    if facts.tool_call is not None:
        entry["tool_call"] = facts.tool_call
    if facts.context is not None:
        entry["context"] = facts.context
    if rated.card is not None:
        entry["card_kind"] = rated.card.kind
        entry["card_url"] = rated.card.url
    if aligned is not None:
        entry["aligned_score"] = aligned.score
        entry["aligned_sources"] = list(aligned.sources)
    entry["votes"] = dict(evidence.votes)
    entry["efforts"] = dict(evidence.efforts)
    entry["notes"] = list(verdict.notes)
    entry["scores"] = dict(evidence.scores)
    return entry


def _model(model: ModelResult, contracts: Contracts) -> Document:
    found = model.identity
    entry: Document = {
        "provider_id": found.row.provider_id,
        "model_name": found.row.model_name,
        "registered": found.row.registered,
    }
    if found.canonical is not None:  # an ignored row has neither
        entry["canonical"] = found.canonical
        entry["resolved_by"] = found.resolved_by
    notes = (*found.notes, *model.verdict.notes)
    return {**entry, **_rating(model, contracts, basis=found.basis), "notes": list(notes)}


def _candidate(candidate: Candidate, contracts: Contracts) -> Document:
    return {
        "canonical": candidate.canonical,
        "resolved_by": candidate.resolved_by,
        **_rating(candidate, contracts),
    }


def _sources(assignment: Assignment, sources: Sources) -> Document:
    unresolved = {s.source: len(s.unresolved) for s in assignment.identity.sources()}
    tables: Document = {}
    for result in sources.results():
        table: Document = {
            "status": result.status,
            "fetched_at": result.fetched_at.strftime(STAMP),
            "records": result.records,
        }
        if result.name in unresolved:  # models.dev is not resolved against anything
            table["unresolved"] = unresolved[result.name]
        table["attribution"] = assignment.contracts.attribution[result.name]
        tables[result.name] = table
    return tables


def _alignment(assignment: Assignment) -> Document:
    """The bounds every line's answer is held within, then one table per
    fitted signal with the line that turns its score into index points, so a
    reader of the file alone can work an aligned score out again. Empty when
    no signal was fitted."""
    alignment = assignment.alignment
    if not alignment.fits:
        return {}
    return {
        "floor": alignment.floor,
        "ceiling": alignment.ceiling,
        **{
            signal: {
                "slope": fit.slope,
                "intercept": fit.intercept,
                "shared": fit.shared,
                "typical_miss": fit.typical_miss,
            }
            for signal, fit in alignment.fits.items()
        },
    }


def build_document(
    assignment: Assignment, inventory: Inventory, sources: Sources, now: datetime
) -> Document:
    """The whole of `tiers.toml`. An array with no entries is left out: an
    absent `[[unmapped]]` is an empty one. So is `[alignment]` when no signal
    was fitted."""
    contracts = assignment.contracts
    doc: Document = {
        "generated_at": now.strftime(STAMP),
        "contracts_version": contracts.version,
        "inventory": {"registry": inventory.registry, "providers": dict(inventory.providers)},
        "sources": _sources(assignment, sources),
    }
    alignment = _alignment(assignment)
    if alignment:
        doc["alignment"] = alignment
    arrays: Document = {
        "models": [_model(model, contracts) for model in assignment.models],
        "candidates": [_candidate(c, contracts) for c in assignment.candidates],
        "unmapped": [
            {"provider_id": entry.row.provider_id, "model_name": entry.row.model_name}
            for entry in assignment.identity.unmapped
        ],
        "unresolved": [
            {"source": record.source, "id": record.source_id, "score": record.score}
            for record in assignment.unresolved
        ],
    }
    doc.update({name: entries for name, entries in arrays.items() if entries})
    return doc


def render(doc: Document) -> str:
    """The document as TOML text: the plain keys, `[inventory]`, `[sources.*]`
    `[alignment]` and `[alignment.*]` first, then one block per entry of each array in ARRAYS. A
    table inside an entry (`votes`, `efforts`, `scores`) follows the entry's
    plain values as `[models.votes]`; an empty one is a bare header."""
    parts = [tomli_w.dumps({key: value for key, value in doc.items() if key not in ARRAYS})]
    for name in ARRAYS:
        for entry in doc.get(name, []):
            plain = {key: value for key, value in entry.items() if not isinstance(value, dict)}
            parts.append(f"[[{name}]]\n{tomli_w.dumps(plain)}")
            parts.extend(
                f"[{name}.{key}]\n{tomli_w.dumps(value)}"
                for key, value in entry.items()
                if isinstance(value, dict)
            )
    return "\n".join(parts)


def write_text(path: Path, text: str) -> None:
    """Write a file atomically: a temporary file in the same directory, then
    a rename over `path`. A reader sees the old file or the new one."""
    path.parent.mkdir(parents=True, exist_ok=True)
    handle, name = tempfile.mkstemp(dir=path.parent, prefix=path.name + ".", suffix=".tmp")
    temporary = Path(name)
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as stream:
            stream.write(text)
        temporary.replace(path)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise


def write_document(path: Path, doc: Document) -> None:
    """Write `tiers.toml`, atomically."""
    write_text(path, render(doc))


def read_document(path: Path) -> Document:
    """Read `tiers.toml` for `show` and `explain`. Raises OutputFileError when
    it is missing, is not TOML, or is not UTF-8 text."""
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise OutputFileError(
            f"cannot read {path}: {exc.strerror}; run `modeltiers run` to write it"
        ) from exc
    except UnicodeDecodeError as exc:
        raise OutputFileError(
            f"{path} is not UTF-8 text; run `modeltiers run` to write it again"
        ) from exc
    try:
        return tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise OutputFileError(
            f"{path} is not TOML ({exc}); run `modeltiers run` to write it again"
        ) from exc


def read_previous(path: Path) -> Document | None:
    """The existing file, for the change report. The file is advisory: one
    that is absent or cannot be parsed reads as absent."""
    try:
        return read_document(path)
    except OutputFileError:
        return None
