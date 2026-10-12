"""One source from download to records: fetch, save the snapshot, parse, and
fall back to the newest good snapshot when either step fails."""

from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime
from functools import partial
from pathlib import Path

from modeltiers import fetch, snapshots
from modeltiers.errors import FetchError, SourceParseError, SourceUnavailable
from modeltiers.fetch import Fetcher, Sleep
from modeltiers.records import ModelsDev, ScoreRecord
from modeltiers.sources import arena, epoch, modelsdev, openrouter

Warn = Callable[[str], None]
ScoreRecords = tuple[ScoreRecord, ...]


@dataclass(frozen=True)
class SourceSpec[T]:
    """Everything the loader needs to know about one source."""

    name: str  # the snapshot directory and the key in [sources.*]
    ext: str  # the snapshot file extension
    fetch: Callable[[Fetcher, Sleep], bytes]
    parse: Callable[[bytes], T]
    count: Callable[[T], int]


@dataclass(frozen=True)
class SourceSpecs:
    modelsdev: SourceSpec[ModelsDev]
    openrouter: SourceSpec[ScoreRecords]
    arena: SourceSpec[ScoreRecords]
    epoch: SourceSpec[ScoreRecords]


@dataclass(frozen=True)
class SourceResult[T]:
    name: str
    status: str  # "ok" | "stale"
    fallback: bool  # stale because this run's download or parse failed
    fetched_at: datetime  # when the bytes in `data` were downloaded
    records: int
    data: T
    snapshot: Path | None  # the file the data was read from or saved to


@dataclass(frozen=True)
class Sources:
    modelsdev: SourceResult[ModelsDev]
    openrouter: SourceResult[ScoreRecords]
    arena: SourceResult[ScoreRecords]
    epoch: SourceResult[ScoreRecords]

    def results(self) -> tuple[SourceResult[ModelsDev] | SourceResult[ScoreRecords], ...]:
        """The four results, in the order the file and the report list them."""
        return (self.modelsdev, self.openrouter, self.arena, self.epoch)


def _count_rows(data: ModelsDev) -> int:
    return len(data.rows)


def build_specs(
    *,
    modelsdev_min: int = modelsdev.MIN_ROWS,
    openrouter_min: int = openrouter.MIN_ROWS,
    arena_min: int = arena.MIN_ROWS,
    epoch_min: int = epoch.MIN_ROWS,
) -> SourceSpecs:
    """The four sources. The minimums default to the spec's constants; a test
    passes small ones to match its fixtures."""
    return SourceSpecs(
        modelsdev=SourceSpec(
            name=modelsdev.SOURCE,
            ext="json",
            fetch=fetch.fetch_modelsdev,
            parse=partial(modelsdev.parse, min_rows=modelsdev_min),
            count=_count_rows,
        ),
        openrouter=SourceSpec(
            name=openrouter.SOURCE,
            ext="json",
            fetch=fetch.fetch_openrouter,
            parse=partial(openrouter.parse, min_rows=openrouter_min),
            count=len,
        ),
        arena=SourceSpec(
            name=arena.SOURCE,
            ext="json",
            fetch=fetch.fetch_arena,
            parse=partial(arena.parse, min_rows=arena_min),
            count=len,
        ),
        epoch=SourceSpec(
            name=epoch.SOURCE,
            ext="zip",
            fetch=fetch.fetch_epoch,
            parse=partial(epoch.parse, min_rows=epoch_min),
            count=len,
        ),
    )


def _from_snapshots[T](
    spec: SourceSpec[T], cache: Path, *, fallback: bool, warn: Warn
) -> SourceResult[T]:
    """The newest good snapshot that still parses. One that does not is
    renamed `.bad` and the next is tried. One whose file cannot be read — it
    went away, or is not readable — is named and skipped, not renamed: a file
    that cannot be read is not one we can judge."""
    for snapshot in snapshots.good_snapshots(cache, spec.name):
        try:
            body = snapshot.path.read_bytes()
        except OSError as exc:
            warn(f"{spec.name}: cannot read the snapshot {snapshot.path}: {exc.strerror}")
            continue
        try:
            data = spec.parse(body)
        except SourceParseError as exc:
            bad = snapshots.mark_bad(snapshot)
            warn(f"{exc}; snapshot set aside as {bad.path}")
            continue
        return SourceResult(
            name=spec.name,
            status="stale",
            fallback=fallback,
            fetched_at=snapshot.fetched_at,
            records=spec.count(data),
            data=data,
            snapshot=snapshot.path,
        )
    raise SourceUnavailable([spec.name])


def load_source[T](
    spec: SourceSpec[T],
    *,
    cache: Path,
    fetcher: Fetcher,
    sleep: Sleep,
    now: datetime,
    offline: bool,
    warn: Warn,
) -> SourceResult[T]:
    """Load one source.

    Online: download, save the bytes, parse. A snapshot that does not parse is
    renamed `.bad` and its path is passed to `warn`. When the download or the
    parse fails, the newest good snapshot is used and the result is `stale`
    with `fallback` set. Offline: nothing is fetched or saved; the newest good
    snapshot is used and the result is `stale` without `fallback`. With no
    good snapshot either way, SourceUnavailable is raised.
    """
    if offline:
        return _from_snapshots(spec, cache, fallback=False, warn=warn)
    try:
        body = spec.fetch(fetcher, sleep)
    except FetchError as exc:
        warn(f"{spec.name}: download failed: {exc}")
        return _from_snapshots(spec, cache, fallback=True, warn=warn)
    snapshot = snapshots.save(cache, spec.name, spec.ext, body, now)
    try:
        data = spec.parse(body)
    except SourceParseError as exc:
        bad = snapshots.mark_bad(snapshot)
        warn(f"{exc}; snapshot set aside as {bad.path}")
        return _from_snapshots(spec, cache, fallback=True, warn=warn)
    return SourceResult(
        name=spec.name,
        status="ok",
        fallback=False,
        fetched_at=snapshot.fetched_at,
        records=spec.count(data),
        data=data,
        snapshot=snapshot.path,
    )


def load_all(
    specs: SourceSpecs,
    *,
    cache: Path,
    fetcher: Fetcher,
    sleep: Sleep,
    now: datetime,
    offline: bool,
    warn: Warn,
) -> Sources:
    """Load the four sources. Every source is attempted; when any is
    unavailable, one SourceUnavailable names them all."""
    missing: list[str] = []

    def load[T](spec: SourceSpec[T]) -> SourceResult[T] | None:
        try:
            return load_source(
                spec, cache=cache, fetcher=fetcher, sleep=sleep, now=now, offline=offline, warn=warn
            )
        except SourceUnavailable as exc:
            missing.extend(exc.names)
            return None

    md = load(specs.modelsdev)
    orr = load(specs.openrouter)
    ar = load(specs.arena)
    ep = load(specs.epoch)
    if md is None or orr is None or ar is None or ep is None:
        raise SourceUnavailable(missing)
    return Sources(modelsdev=md, openrouter=orr, arena=ar, epoch=ep)
