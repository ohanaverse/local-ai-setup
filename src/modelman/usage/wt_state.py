from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path


@dataclass(frozen=True)
class LaunchCounts:
    _1d: int
    _7d: int
    _30d: int


def read_usage_counts(
    path: Path,
    as_of: datetime,
    windows: tuple[timedelta, timedelta, timedelta] = (
        timedelta(days=1),
        timedelta(days=7),
        timedelta(days=30),
    ),
) -> dict[str, LaunchCounts]:
    """Read usage.jsonl and return per-model launch counts for 1d/7d/30d.

    `as_of` is the reference time; counts include events strictly after
    `as_of - window` and on or before `as_of`. Models with no events inside
    the largest window are omitted from the result.
    """
    raw: dict[str, list[datetime]] = {}
    if not path.exists():
        return {}

    max_cutoff = as_of - max(windows)
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
                model_id = record["model_id"]
                ts = datetime.fromisoformat(record["timestamp"])
            except (json.JSONDecodeError, KeyError, ValueError):
                # Skip malformed/legacy lines rather than failing the whole report.
                continue
            if ts.tzinfo is None:
                ts = ts.replace(tzinfo=UTC)
            if not (max_cutoff < ts <= as_of):
                continue
            raw.setdefault(model_id, []).append(ts)

    counts: dict[str, LaunchCounts] = {}
    for model_id, timestamps in raw.items():
        counts[model_id] = LaunchCounts(
            _1d=_count_in_window(timestamps, as_of, windows[0]),
            _7d=_count_in_window(timestamps, as_of, windows[1]),
            _30d=_count_in_window(timestamps, as_of, windows[2]),
        )
    return counts


def _count_in_window(timestamps: list[datetime], as_of: datetime, window: timedelta) -> int:
    cutoff = as_of - window
    return sum(1 for ts in timestamps if cutoff < ts <= as_of)


def read_last_launched(path: Path) -> str | None:
    """Return the single most recently launched model id, if recorded."""
    if not path.exists():
        return None
    return path.read_text().strip() or None
