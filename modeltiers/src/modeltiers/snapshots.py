"""The snapshot store: `<cache>/<source>/<UTC stamp>.<ext>`.

A snapshot that failed to parse is renamed `<UTC stamp>.bad.<ext>` and is
never read again. Any other file in a source's directory is ignored.
"""

import re
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

STAMP_FORMAT = "%Y%m%dT%H%M%SZ"
KEEP_GOOD = 4
KEEP_BAD = 2
_NAME = re.compile(r"^(\d{8}T\d{6}Z)(\.bad)?\.[a-z0-9]+$")


@dataclass(frozen=True)
class Snapshot:
    path: Path
    fetched_at: datetime  # UTC, whole seconds: the stamp in the file name
    bad: bool


def _read_name(path: Path) -> Snapshot | None:
    match = _NAME.match(path.name)
    if match is None:
        return None
    try:
        fetched_at = datetime.strptime(match.group(1), STAMP_FORMAT).replace(tzinfo=UTC)
    except ValueError:
        return None
    return Snapshot(path=path, fetched_at=fetched_at, bad=match.group(2) is not None)


def save(cache: Path, source: str, ext: str, data: bytes, now: datetime) -> Snapshot:
    """Write the fetched bytes, before anything parses them."""
    stamp = now.astimezone(UTC).strftime(STAMP_FORMAT)
    directory = cache / source
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{stamp}.{ext}"
    path.write_bytes(data)
    snapshot = _read_name(path)
    assert snapshot is not None
    return snapshot


def mark_bad(snapshot: Snapshot) -> Snapshot:
    """Rename `<stamp>.<ext>` to `<stamp>.bad.<ext>`."""
    stamp, ext = snapshot.path.name.split(".", 1)
    target = snapshot.path.with_name(f"{stamp}.bad.{ext}")
    snapshot.path.replace(target)
    return Snapshot(path=target, fetched_at=snapshot.fetched_at, bad=True)


def list_snapshots(cache: Path, source: str) -> list[Snapshot]:
    """Every snapshot of one source, newest first."""
    directory = cache / source
    if not directory.is_dir():
        return []
    found = [s for s in map(_read_name, directory.iterdir()) if s is not None]
    return sorted(found, key=lambda s: s.path.name, reverse=True)


def good_snapshots(cache: Path, source: str) -> list[Snapshot]:
    """The snapshots not marked bad, newest first."""
    return [s for s in list_snapshots(cache, source) if not s.bad]


def prune(
    cache: Path, source: str, *, keep_good: int = KEEP_GOOD, keep_bad: int = KEEP_BAD
) -> list[Path]:
    """Delete all but the newest `keep_good` good and `keep_bad` bad snapshots
    of one source. Returns the deleted paths."""
    snapshots = list_snapshots(cache, source)
    good = [s for s in snapshots if not s.bad]
    bad = [s for s in snapshots if s.bad]
    removed = [s.path for s in good[keep_good:] + bad[keep_bad:]]
    for path in removed:
        path.unlink()
    return removed
