from collections.abc import Callable
from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch, snapshots
from modeltiers.errors import FetchError, InventoryError
from modeltiers.identity import HOST_NOTE
from modeltiers.loader import SourceSpecs

T0 = datetime(2026, 10, 12, 15, 58, 0, tzinfo=UTC)
T1 = T0 + timedelta(days=7)
INVENTORY = Path(__file__).parent / "fixtures" / "inventory.json"
ALIASES = Path(__file__).parent / "fixtures" / "aliases.toml"
Answers = dict[str, list[bytes | Exception]]

CLEAN_REPORT = f"""\
inventory: 8 models (1 native rows dropped), registry /home/owner/.config/local-ai/registry.toml
modelsdev: ok, 12 records, fetched 2026-10-12T15:58:00Z
openrouter: ok, 6 records, fetched 2026-10-12T15:58:00Z
arena: ok, 6 records, fetched 2026-10-12T15:58:00Z
epoch: ok, 6 records, fetched 2026-10-12T15:58:00Z
identity: 8 rows: 6 resolved, 1 ignored, 1 unmapped
  ollama/glm-5.3:cloud            host        zhipuai/glm-5.3
  ollama/deepseek-v4-pro:cloud    alias       deepseek/deepseek-v4-pro-0813
  openrouter/z-ai/glm-5.3         host        zhipuai/glm-5.3
  ollama/qwen3.8:27b-mlx          normalized  alibaba/qwen3.8-27b  [inherited]
  ollama/medgemma:27b             unmapped
  ollama/minimax-m3:cloud         normalized  minimax/MiniMax-M3  [{HOST_NOTE}]
  ollama/nomic-embed-text:latest  ignored     embedding model
  ollama/gemma4:31b-mlx           normalized  google/gemma-4-31b-it  [inherited]
unmapped: 1 (each needs an [[alias]] or an [[ignore]] in aliases.toml)
  ollama/medgemma:27b
review: 3 matched by name alone (check each)
  ollama/qwen3.8:27b-mlx -> alibaba/qwen3.8-27b
  ollama/minimax-m3:cloud -> minimax/MiniMax-M3
  ollama/gemma4:31b-mlx -> google/gemma-4-31b-it
unresolved: source records no step resolved (counts only; listing those at or above a standard floor needs contracts.toml)
  openrouter: 1 of 6 records, 0 ignored, 4 keys resolved
  arena: 1 of 6 records, 0 ignored, 5 keys resolved
  epoch: 0 of 6 records, 1 ignored, 5 keys resolved
nothing written: this version stops after resolving names
"""


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    run_wt: Callable[[], str] = _never_run_wt,
    aliases_path: Path = ALIASES,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(
        fetcher=fetcher,
        sleep=Sleeps(),
        now=lambda: now,
        run_wt=run_wt,
        specs=specs,
        aliases_path=aliases_path,
    )
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
    assert "arena: stale, 6 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 6 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
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
    assert deps.aliases_path is None  # the committed data/aliases.toml


def test_an_invalid_aliases_file_exits_1_before_wt_and_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    bad = tmp_path / "aliases.toml"
    bad.write_text(
        "[[ignore]]\n"
        'inventory = { provider_id = "ollama", model_name = "nomic-embed-text:latest" }\n'
    )
    # No --inventory: _never_run_wt fails the test if wt is asked.
    deps, fetcher = _deps(specs, good_answers, aliases_path=bad)
    assert cli.main(["run", "--dry-run"], deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == "modeltiers: aliases.toml: [[ignore]] #1: an ignore needs a reason\n"
    assert fetcher.calls == []


def test_an_unanchored_alias_exits_1_once_models_dev_is_parsed(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    unanchored = tmp_path / "aliases.toml"
    unanchored.write_text(
        "[[alias]]\n"
        'canonical = "google/gemini-4-argon"\n'
        'inventory = [{ provider_id = "ollama", model_name = "medgemma:27b" }]\n'
    )
    deps, fetcher = _deps(specs, good_answers, aliases_path=unanchored)
    assert cli.main(["run", "--dry-run", "--inventory", str(INVENTORY)], deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith(
        "modeltiers: aliases.toml: [[alias]] #1 (google/gemini-4-argon): canonical is not a "
        "models.dev key and the entry lists no openrouter, arena or epoch id\n"
    )
    assert fetch.MODELSDEV_URL in fetcher.calls


def test_a_stale_alias_id_is_a_warning_and_the_run_goes_on(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    stale = tmp_path / "aliases.toml"
    stale.write_text(
        ALIASES.read_text() + '\n[[alias]]\ncanonical = "zhipuai/glm-5.3"\narena = ["GLM 5.2"]\n'
    )
    deps, _ = _deps(specs, good_answers, aliases_path=stale)
    assert cli.main(["run", "--dry-run", "--inventory", str(INVENTORY)], deps=deps) == 0
    captured = capsys.readouterr()
    assert captured.out == CLEAN_REPORT
    assert captured.err.endswith(
        "warning: alias for zhipuai/glm-5.3 names arena id 'GLM 5.2', "
        "which is not in this run's arena data\n"
    )


def test_with_no_aliases_every_row_falls_to_the_rules(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    empty = tmp_path / "aliases.toml"
    empty.write_text("")
    deps, _ = _deps(specs, good_answers, aliases_path=empty)
    assert cli.main(["run", "--dry-run", "--inventory", str(INVENTORY)], deps=deps) == 0
    out = capsys.readouterr().out
    assert "identity: 8 rows: 6 resolved, 0 ignored, 2 unmapped\n" in out
    # Joined by name to the April key, and listed for review with its note.
    assert (
        "  ollama/deepseek-v4-pro:cloud    normalized  deepseek/deepseek-v4-pro  "
        f"[{HOST_NOTE}]\n" in out
    )
    assert "  ollama/deepseek-v4-pro:cloud -> deepseek/deepseek-v4-pro\n" in out
    assert "unmapped: 2 (each needs" in out
    assert "  ollama/nomic-embed-text:latest\n" in out
    assert "  epoch: 1 of 6 records, 0 ignored, 5 keys resolved\n" in out
