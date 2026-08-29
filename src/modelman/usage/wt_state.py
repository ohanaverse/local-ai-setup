from __future__ import annotations

import json
import os
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path


@dataclass(frozen=True)
class LaunchCounts:
    _1d: int
    _7d: int
    _30d: int


def wt_dir() -> Path:
    """Directory where agent-worktree keeps usage.jsonl and rotation.state."""
    env = os.environ.get("MODELMAN_WT_DIR")
    if env:
        return Path(env).expanduser()
    return Path.home() / ".config" / "agent-wt"


def read_usage_counts(path: Path, as_of: datetime) -> dict[str, LaunchCounts]:
    """Read usage.jsonl and return per-model launch counts for 1d/7d/30d.

    `as_of` is the reference time; counts include events strictly after
    `as_of - window` and on or before `as_of`. Models with no events inside
    the largest window are omitted from the result.
    """
    windows = (timedelta(days=1), timedelta(days=7), timedelta(days=30))
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
    cutoffs = tuple(as_of - w for w in windows)
    for model_id, timestamps in raw.items():
        d1 = d7 = d30 = 0
        for ts in timestamps:
            if cutoffs[0] < ts <= as_of:
                d1 += 1
            if cutoffs[1] < ts <= as_of:
                d7 += 1
            if cutoffs[2] < ts <= as_of:
                d30 += 1
        counts[model_id] = LaunchCounts(_1d=d1, _7d=d7, _30d=d30)
    return counts


def read_last_launched(path: Path) -> str | None:
    """Return the single most recently launched model id, if recorded."""
    if not path.exists():
        return None
    return path.read_text().strip() or None
