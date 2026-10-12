from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import fetch, loader, snapshots
from modeltiers.errors import FetchError, SourceParseError, SourceUnavailable
from modeltiers.loader import SourceSpecs

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
T1 = T0 + timedelta(days=7)
Answers = dict[str, list[bytes | Exception]]


class Warnings:
    def __init__(self) -> None:
        self.lines: list[str] = []

    def __call__(self, line: str) -> None:
        self.lines.append(line)


def _load_openrouter(
    specs: SourceSpecs,
    cache: Path,
    answers: list[bytes | Exception],
    *,
    now: datetime = T0,
    offline: bool = False,
) -> tuple[loader.SourceResult[loader.ScoreRecords], Warnings, FakeFetcher]:
    fetcher = FakeFetcher({fetch.OPENROUTER_URL: answers})
    warnings = Warnings()
    result = loader.load_source(
        specs.openrouter,
        cache=cache,
        fetcher=fetcher,
        sleep=Sleeps(),
        now=now,
        offline=offline,
        warn=warnings,
    )
    return result, warnings, fetcher


def test_a_clean_fetch_is_ok_and_saved(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    body = read_fixture("openrouter.json")
    result, warnings, _ = _load_openrouter(specs, tmp_path, [body])
    assert (result.name, result.status, result.fallback) == ("openrouter", "ok", False)
    assert result.fetched_at == T0
    assert result.records == 4
    assert result.data[0].source_id == "z-ai/glm-5.3"
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"
    assert result.snapshot.read_bytes() == body
    assert warnings.lines == []


def test_a_failed_download_falls_back_to_the_newest_good_snapshot(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    result, warnings, _ = _load_openrouter(specs, tmp_path, [FetchError("HTTP 404")], now=T1)
    assert (result.status, result.fallback) == ("stale", True)
    assert result.fetched_at == T0  # the snapshot's own time, not this run's
    assert result.records == 4
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"
    assert warnings.lines == ["openrouter: download failed: HTTP 404"]
    # A failed download saves nothing.
    assert len(snapshots.list_snapshots(tmp_path, "openrouter")) == 1


def test_a_snapshot_that_does_not_parse_is_set_aside_and_the_run_falls_back(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    malformed = read_fixture("openrouter_malformed.json")
    result, warnings, _ = _load_openrouter(specs, tmp_path, [malformed], now=T1)
    assert (result.status, result.fallback, result.fetched_at) == ("stale", True, T0)
    bad = tmp_path / "openrouter" / "20261019T155800Z.bad.json"
    assert bad.read_bytes() == malformed  # the bytes are kept for a look
    assert warnings.lines == [
        f"openrouter: expected an object with a data list; snapshot set aside as {bad}"
    ]


def test_a_snapshot_under_its_row_minimum_is_bad(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    result, warnings, _ = _load_openrouter(
        specs, tmp_path, [read_fixture("openrouter_too_few.json")], now=T1
    )
    assert (result.status, result.fallback) == ("stale", True)
    assert (tmp_path / "openrouter" / "20261019T155800Z.bad.json").exists()
    assert "1 rows, under the minimum of 4" in warnings.lines[0]


def test_a_failure_with_no_good_snapshot_is_unavailable(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    with pytest.raises(SourceUnavailable, match="openrouter") as caught:
        _load_openrouter(specs, tmp_path, [FetchError("HTTP 404")])
    assert caught.value.names == ["openrouter"]
    with pytest.raises(SourceUnavailable):
        _load_openrouter(specs, tmp_path, [read_fixture("openrouter_malformed.json")])


def test_offline_reads_the_newest_good_snapshot_and_fetches_nothing(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    result, warnings, fetcher = _load_openrouter(specs, tmp_path, [], now=T1, offline=True)
    # Stale, and not a fallback: nothing failed, so this is not exit code 2.
    assert (result.status, result.fallback, result.fetched_at) == ("stale", False, T0)
    assert fetcher.calls == []
    assert warnings.lines == []
    assert len(snapshots.list_snapshots(tmp_path, "openrouter")) == 1


def test_offline_skips_a_bad_snapshot(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter_malformed.json")], now=T1)
    result, _, _ = _load_openrouter(specs, tmp_path, [], offline=True)
    assert result.fetched_at == T0
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"


def test_offline_with_no_good_snapshot_is_unavailable(specs: SourceSpecs, tmp_path: Path) -> None:
    with pytest.raises(SourceUnavailable, match="openrouter"):
        _load_openrouter(specs, tmp_path, [], offline=True)


def test_a_good_snapshot_that_no_longer_parses_is_set_aside_and_the_next_is_used(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    # A snapshot saved as good by an older version, in a shape this parser refuses.
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    snapshots.save(tmp_path, "openrouter", "json", b'{"models": []}', T1)
    result, warnings, _ = _load_openrouter(specs, tmp_path, [], offline=True)
    assert result.fetched_at == T0
    assert (tmp_path / "openrouter" / "20261019T155800Z.bad.json").exists()
    assert len(warnings.lines) == 1


def test_a_snapshot_whose_file_is_gone_is_skipped_not_fatal(
    specs: SourceSpecs, tmp_path: Path, read_fixture: Callable[[str], bytes]
) -> None:
    """A name can outlive its file: another run started in the same second can
    set the stamped snapshot aside, and a file that cannot be read must send
    the run on to the next newest good snapshot rather than fail it."""
    _load_openrouter(specs, tmp_path, [read_fixture("openrouter.json")], now=T0)
    gone = tmp_path / "openrouter" / "20261019T155800Z.json"
    gone.symlink_to(tmp_path / "openrouter" / "not-there.json")
    result, warnings, _ = _load_openrouter(specs, tmp_path, [], offline=True)
    assert result.fetched_at == T0
    assert result.snapshot == tmp_path / "openrouter" / "20261012T155800Z.json"
    assert len(warnings.lines) == 1
    assert warnings.lines[0].startswith(f"openrouter: cannot read the snapshot {gone}:")
    # Nothing is renamed: a file we could not read is not a file we judge.
    assert not (tmp_path / "openrouter" / "20261019T155800Z.bad.json").exists()


def test_load_all_reads_the_four_sources(
    specs: SourceSpecs, tmp_path: Path, good_answers: Answers
) -> None:
    fetcher = FakeFetcher(good_answers)
    sources = loader.load_all(
        specs,
        cache=tmp_path,
        fetcher=fetcher,
        sleep=Sleeps(),
        now=T0,
        offline=False,
        warn=Warnings(),
    )
    assert [(r.name, r.status, r.records) for r in sources.results()] == [
        ("modelsdev", "ok", 8),
        ("openrouter", "ok", 4),
        ("arena", "ok", 5),
        ("epoch", "ok", 5),
    ]
    assert sources.modelsdev.data.rows[0].canonical == "zhipuai/glm-5.3"
    assert sources.arena.data[0].effort == "high"
    assert sources.epoch.snapshot == tmp_path / "epoch" / "20261012T155800Z.zip"
    # Arena's two pages are one snapshot file.
    assert [s.path.name for s in snapshots.list_snapshots(tmp_path, "arena")] == [
        "20261012T155800Z.json"
    ]


def test_load_all_names_every_unavailable_source_and_still_saves_the_rest(
    specs: SourceSpecs, tmp_path: Path, good_answers: Answers
) -> None:
    good_answers[fetch.OPENROUTER_URL] = [FetchError("HTTP 404")]
    good_answers[fetch.EPOCH_URL] = [b"<html>not a zip</html>"]
    warnings = Warnings()
    with pytest.raises(SourceUnavailable) as caught:
        loader.load_all(
            specs,
            cache=tmp_path,
            fetcher=FakeFetcher(good_answers),
            sleep=Sleeps(),
            now=T0,
            offline=False,
            warn=warnings,
        )
    assert caught.value.names == ["openrouter", "epoch"]
    assert str(caught.value) == "no usable data and no good snapshot for: openrouter, epoch"
    # The sources that did answer are saved, so the next run can fall back on them.
    assert len(snapshots.good_snapshots(tmp_path, "modelsdev")) == 1
    assert len(snapshots.good_snapshots(tmp_path, "arena")) == 1


def test_build_specs_defaults_to_the_specs_minimums(
    read_fixture: Callable[[str], bytes],
    arena_doc: Callable[..., bytes],
    epoch_zip: Callable[..., bytes],
) -> None:
    defaults = loader.build_specs()
    golden = {
        "modelsdev": (defaults.modelsdev, "json", read_fixture("modelsdev.json"), 1000),
        "openrouter": (defaults.openrouter, "json", read_fixture("openrouter.json"), 100),
        "arena": (defaults.arena, "json", arena_doc(), 20),
        "epoch": (defaults.epoch, "zip", epoch_zip(), 100),
    }
    for name, (spec, ext, body, minimum) in golden.items():
        assert (spec.name, spec.ext) == (name, ext)
        with pytest.raises(SourceParseError, match=f"under the minimum of {minimum}"):
            spec.parse(body)
