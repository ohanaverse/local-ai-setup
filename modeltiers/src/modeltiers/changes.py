"""The change report: the new document against the existing output file,
keyed on (provider_id, model_name). Both sides are plain documents, so a file
written by an older version is compared by whatever fields it has."""

from collections.abc import Mapping
from pathlib import Path
from typing import Any

from modeltiers.contracts import SOURCES
from modeltiers.show import model_name, table_lines

Document = Mapping[str, Any]
UNMAPPED = "unmapped"
# How an [[ignore]]d row's reason is written in its `notes`.
IGNORED_NOTE = "ignored: "


def _states(doc: Document) -> dict[str, tuple[str, str]]:
    """name -> (tier or "unmapped", confidence), in file order."""
    states = {
        model_name(m): (str(m.get("tier", "?")), str(m.get("confidence", "?")))
        for m in doc.get("models", [])
    }
    states.update({model_name(entry): (UNMAPPED, "") for entry in doc.get("unmapped", [])})
    return states


def _ignored(doc: Document) -> dict[str, str]:
    """name -> the reason, for every `[[models]]` entry an [[ignore]] covers."""
    reasons = {}
    for model in doc.get("models", []):
        for note in model.get("notes", []):
            if str(note).startswith(IGNORED_NOTE):
                reasons[model_name(model)] = str(note)[len(IGNORED_NOTE) :]
    return reasons


def _candidates(doc: Document) -> dict[str, str]:
    return {
        str(c.get("canonical", "?")).lower(): str(c.get("tier", "?"))
        for c in doc.get("candidates", [])
    }


def _source_statuses(old: Document, new: Document) -> str:
    parts = []
    for name in SOURCES:
        now = new.get("sources", {}).get(name, {}).get("status", "?")
        was = old.get("sources", {}).get(name, {}).get("status", "?")
        parts.append(f"{name} {now}" if now == was else f"{name} {now} (was {was})")
    return ", ".join(parts)


def change_lines(old: Document | None, new: Document, path: Path) -> list[str]:
    """What `run` prints before it writes. With no existing file it says so
    and prints the full table."""
    if old is None:
        return [f"changes: no existing file at {path}; the full table follows", *table_lines(new)]
    before, after = _states(old), _states(new)
    ignored = _ignored(new)
    lines = [f"changes since {old.get('generated_at', 'an undated file')}:"]
    confidence_only = 0
    for name, (tier, confidence) in after.items():
        if name not in before:
            lines.append(f"  added: {name} {tier}")
            continue
        was, was_confidence = before[name]
        if tier == was:
            confidence_only += confidence != was_confidence
        elif tier == UNMAPPED:
            lines.append(f"  newly unmapped: {name} (was {was})")
        elif was == UNMAPPED and name in ignored:
            # It left the unmapped list without being resolved to anything.
            lines.append(f"  now ignored: {name} ({ignored[name]})")
        elif was == UNMAPPED:
            lines.append(f"  newly resolved: {name} {tier}")
        else:
            lines.append(f"  tier: {name} {was} -> {tier}")
    lines.extend(
        f"  removed: {name} (was {was})" for name, (was, _) in before.items() if name not in after
    )
    if len(lines) == 1:
        lines.append("  no model changed tier, was added or was removed")
    old_candidates, new_candidates = _candidates(old), _candidates(new)
    added = sum(1 for key in new_candidates if key not in old_candidates)
    removed = sum(1 for key in old_candidates if key not in new_candidates)
    moved = sum(1 for key, tier in new_candidates.items() if old_candidates.get(key, tier) != tier)
    lines.append(f"  confidence only: {confidence_only}")
    lines.append(f"  candidates: {added} added, {removed} removed, {moved} changed tier")
    lines.append(f"  sources: {_source_statuses(old, new)}")
    return lines
