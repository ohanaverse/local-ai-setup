from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch, snapshots
from modeltiers.errors import FetchError, InventoryError
from modeltiers.loader import SourceSpecs

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
T1 = T0 + timedelta(days=7)
INVENTORY = Path(__file__).parent / "fixtures" / "inventory.json"
Answers = dict[str, list[bytes | Exception]]

CLEAN_REPORT = """\
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 11 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 4 records, fetched 2026-10-12T15:58:00Z
arena: ok, 5 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 5 records, fetched 2026-10-12T15:58:00Z
nothing written: this version stops after parsing the sources
"""


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    run_wt: Callable[[], str] = _never_run_wt,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(fetcher=fetcher, sleep=Sleeps(), now=lambda: now, run_wt=run_wt, specs=specs)
    return deps, fetcher


def test_dry_run_prints_each_source_and_the_inventory_count(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    code = cli.main(argv, deps=_deps(specs, good_answers)[0])
    captured = capsys.readouterr()
    assert code == 0
    assert captured.out == CLEAN_REPORT
    # The fixture's mtplx probe is "partial".
    assert captured.err == (
        "warning: provider mtplx probe status is partial; "
        "models it would have discovered may be absent from this run\n"
    )


def test_plain_run_does_what_dry_run_does(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    code = cli.main(["run", "--inventory", str(INVENTORY)], deps=_deps(specs, good_answers)[0])
    assert code == 0
    assert capsys.readouterr().out == CLEAN_REPORT
    assert not (tmp_path / "out").exists()  # MODELTIERS_OUT's directory: nothing written


def test_snapshots_go_under_modeltiers_cache(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    cli.main(
        ["run", "--dry-run", "--inventory", str(INVENTORY)], deps=_deps(specs, good_answers)[0]
    )
    assert sorted(p.name for p in (tmp_path / "cache").iterdir()) == [
        "arena",
        "epoch",
        "modelsdev",
        "openrouter",
    ]


def test_without_inventory_flag_wt_is_asked(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    deps, _ = _deps(specs, good_answers, run_wt=INVENTORY.read_text)
    assert cli.main(["run", "--dry-run"], deps=deps) == 0
    assert capsys.readouterr().out.startswith("inventory: 8 models")


def test_a_source_that_falls_back_is_stale_and_exits_2(
    specs: SourceSpecs,
    good_answers: Answers,
    epoch_zip: Callable[..., bytes],
    read_fixture: Callable[[str], bytes],
    capsys: pytest.CaptureFixture[str],
) -> None:
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()
    second: Answers = {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [read_fixture("openrouter.json")],
        # Arena: the rows API says its index is loading, twice.
        fetch.arena_page_url(0): [read_fixture("arena_error.json")] * 2,
        fetch.EPOCH_URL: [epoch_zip()],
    }
    assert cli.main(argv, deps=_deps(specs, second, now=T1)[0]) == 2
    captured = capsys.readouterr()
    assert "arena: stale, 5 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 4 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
    assert (
        "warning: arena: download failed: arena: the rows API answered with an error"
        in captured.err
    )


def test_a_source_with_no_good_snapshot_exits_1_and_names_it(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    good_answers[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith("modeltiers: no usable data and no good snapshot for: epoch\n")


def test_offline_reads_snapshots_fetches_nothing_and_exits_0(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--dry-run", "--inventory", str(INVENTORY)]
    cli.main(argv, deps=_deps(specs, good_answers)[0])
    capsys.readouterr()
    deps, fetcher = _deps(specs, {}, now=T1)
    assert cli.main([*argv, "--offline"], deps=deps) == 0
    out = capsys.readouterr().out
    assert out == CLEAN_REPORT.replace(": ok,", ": stale,")
    assert fetcher.calls == []
    # Nothing saved by the offline run: still one snapshot per source.
    assert len(snapshots.list_snapshots(tmp_path / "cache", "arena")) == 1


def test_offline_with_an_empty_cache_exits_1(
    specs: SourceSpecs, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--dry-run", "--offline", "--inventory", str(INVENTORY)]
    assert cli.main(argv, deps=_deps(specs, {})[0]) == 1
    assert capsys.readouterr().err.endswith(
        "modeltiers: no usable data and no good snapshot for: modelsdev, openrouter, arena, epoch\n"
    )


def test_an_inventory_error_exits_1_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    def no_wt() -> str:
        raise InventoryError("wt is not on PATH; install it or pass --inventory FILE")

    deps, fetcher = _deps(specs, good_answers, run_wt=no_wt)
    assert cli.main(["run", "--dry-run"], deps=deps) == 1
    assert capsys.readouterr().err == (
        "modeltiers: wt is not on PATH; install it or pass --inventory FILE\n"
    )
    assert fetcher.calls == []


def test_a_missing_inventory_file_exits_1(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["run", "--inventory", str(tmp_path / "absent.json")]
    assert cli.main(argv, deps=_deps(specs, good_answers)[0]) == 1
    assert "cannot read" in capsys.readouterr().err


def test_an_unwritable_cache_exits_1_with_a_message(
    specs: SourceSpecs,
    good_answers: Answers,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """MODELTIERS_CACHE naming a file, a cache directory that cannot be
    written, or a full disk: a snapshot cannot be saved, and that is a message
    and exit 1 like any other failure, not a traceback."""
    clash = tmp_path / "cache-is-a-file"
    clash.write_text("not a directory")
    monkeypatch.setenv("MODELTIERS_CACHE", str(clash))
    code = cli.main(["run", "--inventory", str(INVENTORY)], deps=_deps(specs, good_answers)[0])
    assert code == 1
    err = capsys.readouterr().err
    assert "modeltiers: [Errno 20]" in err
    assert "Traceback" not in err


def test_no_command_is_a_usage_error() -> None:
    with pytest.raises(SystemExit) as caught:
        cli.main([])
    assert caught.value.code == 2


def test_default_deps_are_the_real_seams() -> None:
    deps = cli.Deps()
    assert deps.fetcher is fetch.http_fetcher
    assert deps.specs.arena.name == "arena"
    assert deps.now().tzinfo is UTC
