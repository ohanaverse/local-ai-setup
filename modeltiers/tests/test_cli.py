import json
import re
import tomllib
from collections.abc import Callable
from dataclasses import replace
from datetime import datetime, timedelta
from pathlib import Path

import pytest
from conftest import (
    ALIGN_MIN,
    FIXTURES,
    INVENTORY,
    SAMPLE,
    T0,
    scaled_openrouter,
    without_timestamps,
)
from fakes import FakeFetcher, FakeResolver, Sleeps

from modeltiers import cli, fetch, snapshots
from modeltiers.errors import FetchError, InventoryError
from modeltiers.identity import HOST_NOTE
from modeltiers.loader import SourceSpecs

T1 = T0 + timedelta(days=7)
ALIASES = FIXTURES / "aliases.toml"
CONTRACTS = FIXTURES / "contracts.toml"
RUN = ["run", "--inventory", str(INVENTORY)]
DRY = [*RUN, "--dry-run"]
Answers = dict[str, list[bytes | Exception]]

HEAD = f"""\
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
unmapped: 1 (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  ollama/medgemma:27b
review: 3 matched by name alone (check each)
  ollama/qwen3.8:27b-mlx -> alibaba/qwen3.8-27b
  ollama/minimax-m3:cloud -> minimax/MiniMax-M3
  ollama/gemma4:31b-mlx -> google/gemma-4-31b-it
unresolved: 1 at or above a standard floor (each needs an [[alias]] or an [[ignore]] in aliases.toml, or is left open until evidence identifies it)
  arena  Qwen3.8 Max  0.0235
  openrouter: 1 of 6 records unresolved, 0 ignored, 4 keys resolved
  arena: 1 of 6 records unresolved, 0 ignored, 5 keys resolved
  epoch: 0 of 6 records unresolved, 1 ignored, 5 keys resolved
"""

TABLE = """\
T  tier      conf    model                           canonical
S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3
S  standard  high    ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813
S  standard  high    openrouter/z-ai/glm-5.3         zhipuai/glm-5.3
S  standard  low     ollama/qwen3.8:27b-mlx          alibaba/qwen3.8-27b
Q  quick     high    ollama/minimax-m3:cloud         minimax/MiniMax-M3
-  unrated   none    ollama/nomic-embed-text:latest  -  (not_applicable)
-  unrated   none    ollama/gemma4:31b-mlx           google/gemma-4-31b-it  (no_votes)
unmapped: 1 (unrated until aliased or ignored)
  ollama/medgemma:27b
"""

# `show` lists the rows by aligned score, highest first; `run` prints the file's order.
SHOWN_TABLE = """\
T  tier      conf    model                           canonical
S  standard  high    ollama/glm-5.3:cloud            zhipuai/glm-5.3
S  standard  high    openrouter/z-ai/glm-5.3         zhipuai/glm-5.3
S  standard  high    ollama/deepseek-v4-pro:cloud    deepseek/deepseek-v4-pro-0813
Q  quick     high    ollama/minimax-m3:cloud         minimax/MiniMax-M3
S  standard  low     ollama/qwen3.8:27b-mlx          alibaba/qwen3.8-27b
-  unrated   none    ollama/nomic-embed-text:latest  -  (not_applicable)
-  unrated   none    ollama/gemma4:31b-mlx           google/gemma-4-31b-it  (no_votes)
unmapped: 1 (unrated until aliased or ignored)
  ollama/medgemma:27b
"""

ATTRIBUTION = """\
modelsdev: models.dev (https://models.dev), MIT
openrouter: Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter (openrouter.ai/rankings).
arena: Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0
epoch: Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0
"""

NO_CHANGES = """\
  no model changed tier, was added or was removed
  confidence only: 0
  candidates: 0 added, 0 removed, 0 changed tier
  sources: modelsdev ok, openrouter ok, arena ok, epoch ok
"""

MTPLX_WARNING = (
    "warning: provider mtplx probe status is partial; "
    "models it would have discovered may be absent from this run\n"
)


def failed(code: int, reason: str) -> str:
    """The last thing a failed `run` prints, on stderr."""
    return f"\nERROR: modeltiers run FAILED (exit {code}): {reason}; tiers.toml was not written.\n"


def first_report(out: Path, last: str) -> str:
    """What `run` prints over the golden fixtures when no file exists yet."""
    return (
        HEAD
        + "sentinels: 4 of 4 hold\n"
        + "alignment: arena_agent on 3 shared models, typical miss 1.1 points\n"
        + "alignment: eci on 3 shared models, typical miss 1.1 points\n"
        + f"changes: no existing file at {out}; the full table follows\n"
        + TABLE
        + "candidates: 2 (list them with --candidates)\n"
        + ATTRIBUTION
        + last
    )


def _never_run_wt() -> str:
    raise AssertionError("wt must not be run when --inventory is given")


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    run_wt: Callable[[], str] = _never_run_wt,
    aliases_path: Path = ALIASES,
    contracts_path: Path = CONTRACTS,
) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(
        fetcher=fetcher,
        sleep=Sleeps(),
        resolver=FakeResolver(),
        vendors_path=FIXTURES / "vendors.toml",
        now=lambda: now,
        run_wt=run_wt,
        specs=specs,
        aliases_path=aliases_path,
        contracts_path=contracts_path,
        align_min=ALIGN_MIN,
    )
    return deps, fetcher


# --- run: the clean path ------------------------------------------------------


def test_run_writes_the_contract_sample_and_reports(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    captured = capsys.readouterr()
    assert captured.out == first_report(out, f"wrote {out}\n")
    # The fixture's mtplx probe is "partial".
    assert captured.err == MTPLX_WARNING
    assert without_timestamps(out.read_text()) == without_timestamps(SAMPLE.read_text())
    assert [p.name for p in out.parent.iterdir()] == ["tiers.toml"]


def test_a_signal_too_few_models_share_is_warned_about_and_left_out(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    deps = replace(_deps(specs, good_answers)[0], align_min=4)  # the fixtures share three
    assert cli.main(RUN, deps=deps) == 0
    captured = capsys.readouterr()
    assert captured.err == (
        MTPLX_WARNING
        + "warning: alignment: arena_agent is in no aligned score: 3 models have both it and "
        "intelligence_index, under the minimum of 4\n"
        + "warning: alignment: eci is in no aligned score: 3 models have both it and "
        "intelligence_index, under the minimum of 4\n"
    )
    assert "alignment:" not in captured.out
    written = tomllib.loads(out.read_text())
    assert "alignment" not in written
    # A model with an index keeps it as its score; one without has none.
    by_name = {m["model_name"]: m for m in written["models"]}
    assert by_name["glm-5.3:cloud"]["aligned_score"] == 44.8
    assert by_name["glm-5.3:cloud"]["aligned_sources"] == ["intelligence_index"]
    assert "aligned_score" not in by_name["deepseek-v4-pro:cloud"]
    # The tiers are the ones a fitted run gives.
    assert [m["tier"] for m in written["models"]] == [
        m["tier"] for m in tomllib.loads(SAMPLE.read_text())["models"]
    ]


def test_dry_run_prints_the_same_report_and_writes_nothing(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(DRY, deps=_deps(specs, good_answers)[0]) == 0
    assert capsys.readouterr().out == first_report(out, f"dry run: {out} not written\n")
    assert not out.parent.exists()


def test_snapshots_go_under_modeltiers_cache(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])
    assert sorted(p.name for p in (tmp_path / "cache").iterdir()) == [
        "arena",
        "epoch",
        "modelsdev",
        "openrouter",
        "vendor-links.toml",
    ]


def test_without_inventory_flag_wt_is_asked(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    deps, _ = _deps(specs, good_answers, run_wt=INVENTORY.read_text)
    assert cli.main(["run", "--dry-run"], deps=deps) == 0
    assert capsys.readouterr().out.startswith("inventory: 8 models")


# --- run: the change report ---------------------------------------------------


def test_a_second_run_reports_against_the_existing_file(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers)[0])
    capsys.readouterr()
    assert cli.main(RUN, deps=_deps(specs, good_answers, now=T1)[0]) == 0
    report = capsys.readouterr().out
    assert report.endswith(
        "sentinels: 4 of 4 hold\n"
        "alignment: arena_agent on 3 shared models, typical miss 1.1 points\n"
        "alignment: eci on 3 shared models, typical miss 1.1 points\n"
        "changes since 2026-10-12T15:58:00Z:\n" + NO_CHANGES + ATTRIBUTION + f"wrote {out}\n"
    )
    assert 'generated_at = "2026-10-19T15:58:00Z"' in out.read_text()


def test_a_tier_that_moved_is_reported_old_to_new(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers)[0])
    out.write_text(out.read_text().replace('tier = "quick"', 'tier = "standard"'))
    capsys.readouterr()
    cli.main(DRY, deps=_deps(specs, good_answers, now=T1)[0])
    report = capsys.readouterr().out
    assert "  tier: ollama/minimax-m3:cloud standard -> quick\n" in report
    assert "no model changed tier" not in report
    # --dry-run left the edited file alone.
    assert 'tier = "standard"\ncode = "Q"' in out.read_text()


def test_an_existing_file_from_an_older_version_is_compared_and_replaced(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    out.parent.mkdir()
    out.write_text(
        "[[models]]\n"
        'provider_id = "ollama"\n'
        'model_name = "glm-5.3:cloud"\n'
        'tier = "deep"\n'
        "\n"
        "[[models]]\n"
        'provider_id = "ollama"\n'
        'model_name = "retired:cloud"\n'
    )
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    report = capsys.readouterr().out
    assert "changes since an undated file:\n" in report
    assert "  tier: ollama/glm-5.3:cloud deep -> standard\n" in report
    assert "  removed: ollama/retired:cloud (was ?)\n" in report
    assert without_timestamps(out.read_text()) == without_timestamps(SAMPLE.read_text())


def test_an_existing_file_that_is_not_toml_is_treated_as_absent_and_replaced(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    out.parent.mkdir()
    out.write_text("generated_at = \n")
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    captured = capsys.readouterr()
    assert captured.out == first_report(out, f"wrote {out}\n")
    assert captured.err == (
        MTPLX_WARNING + f"warning: {out} cannot be read as TOML; it is treated as absent\n"
    )
    assert without_timestamps(out.read_text()) == without_timestamps(SAMPLE.read_text())


# --- run: sources -------------------------------------------------------------


def test_a_source_that_falls_back_is_stale_and_exits_2(
    specs: SourceSpecs,
    good_answers: Answers,
    epoch_zip: Callable[..., bytes],
    read_fixture: Callable[[str], bytes],
    out: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()
    second: Answers = {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [read_fixture("openrouter.json")],
        # Arena: the rows API says its index is loading, twice.
        fetch.arena_page_url(0): [read_fixture("arena_error.json")] * 2,
        fetch.EPOCH_URL: [epoch_zip()],
    }
    assert cli.main(RUN, deps=_deps(specs, second, now=T1)[0]) == 2
    captured = capsys.readouterr()
    assert "arena: stale, 6 records, fetched 2026-10-12T15:58:00Z\n" in captured.out
    assert "openrouter: ok, 6 records, fetched 2026-10-19T15:58:00Z\n" in captured.out
    assert (
        "  sources: modelsdev ok, openrouter ok, arena stale (was ok), epoch ok\n" in captured.out
    )
    assert captured.out.endswith(f"wrote {out}\n")
    assert (
        "warning: arena: download failed: arena: the rows API answered with an error"
        in captured.err
    )
    # Written, with the stale source and its snapshot's own time.
    written = out.read_text()
    assert '[sources.arena]\nstatus = "stale"\nfetched_at = "2026-10-12T15:58:00Z"' in written
    assert '[sources.epoch]\nstatus = "ok"\nfetched_at = "2026-10-19T15:58:00Z"' in written


def test_dry_run_with_a_fallback_returns_2_and_writes_nothing(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])
    good_answers[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    assert cli.main(DRY, deps=_deps(specs, good_answers, now=T1)[0]) == 2
    assert not out.exists()


def test_a_source_with_no_good_snapshot_exits_1_and_names_it(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    good_answers[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith(failed(1, "no usable data and no good snapshot for: epoch"))
    assert not out.exists()


def test_offline_reads_snapshots_fetches_nothing_and_exits_0(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, out: Path
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])
    deps, fetcher = _deps(specs, {}, now=T1)
    assert cli.main([*RUN, "--offline"], deps=deps) == 0
    assert fetcher.calls == []
    # Stale because --offline was given: written, and exit 0.
    written = out.read_text()
    assert written.count('status = "stale"') == 4
    assert 'generated_at = "2026-10-19T15:58:00Z"' in written
    # Nothing saved by the offline run: still one snapshot per source.
    assert len(snapshots.list_snapshots(tmp_path / "cache", "arena")) == 1


def test_offline_with_an_empty_cache_exits_1(
    specs: SourceSpecs, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main([*DRY, "--offline"], deps=_deps(specs, {})[0]) == 1
    assert capsys.readouterr().err.endswith(
        failed(1, "no usable data and no good snapshot for: modelsdev, openrouter, arena, epoch")
    )


# --- run: pruning -------------------------------------------------------------


def _seed(cache: Path, count: int) -> None:
    """`count` older good Arena snapshots and three bad ones."""
    for day in range(count):
        snapshots.save(cache, "arena", "json", b"{}", T0 - timedelta(days=day + 1))
    for day in range(3):
        bad = snapshots.save(cache, "arena", "json", b"{}", T0 - timedelta(days=day + 20))
        snapshots.mark_bad(bad)


def _arena(cache: Path) -> tuple[int, int]:
    found = snapshots.list_snapshots(cache, "arena")
    return sum(1 for s in found if not s.bad), sum(1 for s in found if s.bad)


def test_a_successful_write_prunes_to_four_good_and_two_bad(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    _seed(tmp_path / "cache", 6)
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    assert _arena(tmp_path / "cache") == (4, 2)
    # The newest good one is this run's.
    assert snapshots.good_snapshots(tmp_path / "cache", "arena")[0].fetched_at == T0


def test_a_prune_failure_after_the_write_is_a_warning_not_a_failed_run(
    specs: SourceSpecs,
    good_answers: Answers,
    out: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    def broken(cache: Path, source: str) -> list[Path]:
        raise PermissionError("denied")

    monkeypatch.setattr(snapshots, "prune", broken)
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    assert out.exists()
    err = capsys.readouterr().err
    assert "warning: could not prune old snapshots: denied" in err
    assert "ERROR" not in err


def test_dry_run_and_offline_prune_nothing(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path
) -> None:
    cache = tmp_path / "cache"
    cli.main(DRY, deps=_deps(specs, good_answers)[0])  # one good snapshot per source
    _seed(cache, 6)
    assert cli.main(DRY, deps=_deps(specs, good_answers, now=T1)[0]) == 0
    assert _arena(cache) == (8, 3)
    # Every seeded Arena snapshot is `{}`; offline must read the two real ones.
    for snapshot in snapshots.good_snapshots(cache, "arena"):
        if snapshot.path.read_bytes() == b"{}":
            snapshot.path.unlink()
    assert cli.main([*RUN, "--offline"], deps=_deps(specs, {}, now=T1)[0]) == 0
    assert _arena(cache) == (2, 3)


# --- run: configuration and inventory errors ----------------------------------


def test_an_inventory_error_exits_1_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    def no_wt() -> str:
        raise InventoryError("wt is not on PATH; install it or pass --inventory FILE")

    deps, fetcher = _deps(specs, good_answers, run_wt=no_wt)
    assert cli.main(["run", "--dry-run"], deps=deps) == 1
    assert capsys.readouterr().err == failed(
        1, "wt is not on PATH; install it or pass --inventory FILE"
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
    assert "FAILED (exit 1): [Errno 20]" in err
    assert "Traceback" not in err


def test_no_command_is_a_usage_error() -> None:
    with pytest.raises(SystemExit) as caught:
        cli.main([])
    assert caught.value.code == 2


def test_default_deps_are_the_real_seams() -> None:
    deps = cli.Deps()
    assert deps.align_min == 20  # what a real run fits on, not the fixtures' three
    assert deps.fetcher is fetch.http_fetcher
    assert deps.specs.arena.name == "arena"
    assert deps.now().tzinfo is not None
    assert deps.aliases_path is None  # the committed data/aliases.toml
    assert deps.contracts_path is None  # the committed data/contracts.toml


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
    assert captured.err == failed(1, "aliases.toml: [[ignore]] #1: an ignore needs a reason")
    assert fetcher.calls == []


def test_an_invalid_contracts_file_exits_1_before_wt_and_before_any_fetch(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    bad = tmp_path / "contracts.toml"
    bad.write_text(CONTRACTS.read_text().replace("marathon_ci_lower = 0.097\n", ""))
    deps, fetcher = _deps(specs, good_answers, contracts_path=bad)
    assert cli.main(["run"], deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == failed(
        1, "contracts.toml: exactly one signal must carry marathon_ci_lower; found none"
    )
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
    assert cli.main(DRY, deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith(
        failed(
            1,
            "aliases.toml: [[alias]] #1 (google/gemini-4-argon): canonical is not a "
            "models.dev key and the entry lists no openrouter, arena or epoch id",
        )
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
    assert cli.main(DRY, deps=deps) == 0
    captured = capsys.readouterr()
    assert captured.out.startswith(HEAD)
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
    assert cli.main(DRY, deps=deps) == 0
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
    assert "  epoch: 1 of 6 records unresolved, 0 ignored, 5 keys resolved\n" in out
    # The April key has no record: the row is rated on nothing.
    assert "-  unrated   none    ollama/deepseek-v4-pro:cloud" in out


# --- run: the redirected registry ---------------------------------------------


def test_a_redirected_registry_is_refused_before_any_fetch(
    specs: SourceSpecs,
    good_answers: Answers,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # MODELTIERS_OUT unset: the output file is the default one, under
    # conftest's XDG_CONFIG_HOME. The fixture inventory names another registry.
    monkeypatch.delenv("MODELTIERS_OUT")
    default = tmp_path / "xdg-config" / "local-ai"
    deps, fetcher = _deps(specs, good_answers)
    assert cli.main(RUN, deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == failed(
        1,
        "the inventory came from /home/owner/.config/local-ai/registry.toml, "
        f"which is not {default / 'registry.toml'}; refusing to replace "
        f"{default / 'tiers.toml'} from it. Set MODELTIERS_OUT to write somewhere else",
    )
    assert fetcher.calls == []
    assert not default.exists()


def test_dry_run_with_a_redirected_registry_reports_and_warns(
    specs: SourceSpecs,
    good_answers: Answers,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # --dry-run writes nothing, so there is nothing to refuse: the whole
    # report, and a warning that the same run without the flag stops.
    monkeypatch.delenv("MODELTIERS_OUT")
    default = tmp_path / "xdg-config" / "local-ai"
    assert cli.main(DRY, deps=_deps(specs, good_answers)[0]) == 0
    captured = capsys.readouterr()
    out = default / "tiers.toml"
    assert captured.out == first_report(out, f"dry run: {out} not written\n")
    assert captured.err == (
        "warning: the inventory came from /home/owner/.config/local-ai/registry.toml, "
        f"which is not {default / 'registry.toml'}; without --dry-run this run is refused "
        "and exits 1. Set MODELTIERS_OUT to write somewhere else\n" + MTPLX_WARNING
    )
    assert not default.exists()


def test_the_registry_beside_the_default_output_file_is_accepted(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.delenv("MODELTIERS_OUT")
    default = tmp_path / "xdg-config" / "local-ai"
    saved = tmp_path / "inventory.json"
    saved.write_text(
        INVENTORY.read_text().replace(
            "/home/owner/.config/local-ai/registry.toml", str(default / "registry.toml")
        )
    )
    assert cli.main(["run", "--inventory", str(saved)], deps=_deps(specs, good_answers)[0]) == 0
    assert (default / "tiers.toml").exists()


# --- run: sentinels and exit-code precedence ----------------------------------


@pytest.fixture
def drifted(good_answers: Answers, read_fixture: Callable[[str], bytes]) -> Answers:
    """Clean answers, with every intelligence index scaled by 0.8."""
    good_answers[fetch.OPENROUTER_URL] = [scaled_openrouter(read_fixture("openrouter.json"), 0.8)]
    return good_answers


def test_a_failed_sentinel_writes_nothing_and_exits_3(
    specs: SourceSpecs,
    good_answers: Answers,
    read_fixture: Callable[[str], bytes],
    epoch_zip: Callable[..., bytes],
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # A good file from last week.
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    before = out.read_bytes()
    _seed(tmp_path / "cache", 6)
    capsys.readouterr()
    second: Answers = {
        fetch.MODELSDEV_URL: [read_fixture("modelsdev.json")],
        fetch.OPENROUTER_URL: [scaled_openrouter(read_fixture("openrouter.json"), 0.8)],
        fetch.arena_page_url(0): [read_fixture("arena_page1.json")],
        fetch.arena_page_url(3): [read_fixture("arena_page2.json")],
        fetch.EPOCH_URL: [epoch_zip()],
    }
    assert cli.main(RUN, deps=_deps(specs, second, now=T1)[0]) == 3
    captured = capsys.readouterr()
    assert out.read_bytes() == before  # the existing file is untouched
    assert "sentinels: 2 of 4 FAILED\n" in captured.out
    # Fable 5's vote moved from deep to standard; its tier is still deep.
    assert (
        "  FAIL  anthropic/claude-fable-5\n"
        "        scores:  intelligence_index 39.68, coding_index 76.5, agentic_index 50.7, "
        "arena_agent 0.0894, arena_agent_ci_lower 0.0767, eci 162.06\n"
        "        votes:   intelligence_index standard, arena_agent deep, eci deep; "
        "tier before the gates deep\n"
        "        pinned:  intelligence_index deep, arena_agent deep, eci deep; tier deep\n"
        "        differs: intelligence_index voted standard, pinned deep\n"
    ) in captured.out
    assert "  ok    zhipuai/glm-5.3\n" in captured.out
    # No change report and no "wrote" line after a failed check.
    assert "changes" not in captured.out
    assert "wrote" not in captured.out
    assert captured.err.endswith(
        failed(
            3,
            "a sentinel moved. If the move is real, edit contracts.toml "
            "(guide 12, step 5, or the sentinel-failure skill)",
        )
    )
    assert _arena(tmp_path / "cache") == (8, 3)  # and nothing pruned


def test_dry_run_returns_3_for_a_failed_sentinel(
    specs: SourceSpecs, drifted: Answers, out: Path
) -> None:
    assert cli.main(DRY, deps=_deps(specs, drifted)[0]) == 3
    assert not out.exists()


def test_3_takes_precedence_over_2(
    specs: SourceSpecs, good_answers: Answers, drifted: Answers, out: Path
) -> None:
    cli.main(DRY, deps=_deps(specs, good_answers)[0])  # a good snapshot of every source
    drifted[fetch.EPOCH_URL] = [FetchError("HTTP 404")]  # Epoch falls back: exit 2 alone
    assert cli.main(RUN, deps=_deps(specs, drifted, now=T1)[0]) == 3
    assert not out.exists()


def test_1_takes_precedence_over_3(
    specs: SourceSpecs, drifted: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    # These downloads fail two sentinels: on their own the run exits 3.
    assert cli.main(RUN, deps=_deps(specs, drifted)[0]) == 3
    assert "sentinels: 2 of 4 FAILED\n" in capsys.readouterr().out
    # The same downloads and the same four sentinels, plus a fifth that names
    # an unknown key: a configuration error in the run whose sentinels fail.
    unknown = tmp_path / "contracts.toml"
    unknown.write_text(
        CONTRACTS.read_text()
        + '\n[[sentinels]]\ncanonical = "anthropic/claude-fable-9"\ntier = "deep"\nvotes = {}\n'
    )
    deps, fetcher = _deps(specs, drifted, contracts_path=unknown)
    assert cli.main(RUN, deps=deps) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.endswith(
        failed(
            1,
            "contracts.toml: [[sentinels]] #5 (anthropic/claude-fable-9): "
            "not a canonical key in this run's models.dev data or in aliases.toml",
        )
    )
    # Every source was downloaded: the error came with the drifted data in hand.
    assert fetch.OPENROUTER_URL in fetcher.calls
    assert fetch.EPOCH_URL in fetcher.calls


def test_a_source_with_no_data_is_1_whatever_the_sentinels_would_say(
    specs: SourceSpecs, drifted: Answers, out: Path
) -> None:
    # The drifted OpenRouter data would fail two sentinels, and Epoch has no
    # download and no snapshot: the source error ends the run first.
    drifted[fetch.EPOCH_URL] = [FetchError("HTTP 404")]
    assert cli.main(RUN, deps=_deps(specs, drifted)[0]) == 1
    assert not out.exists()


# --- show ---------------------------------------------------------------------


def _reader_deps(now: datetime) -> cli.Deps:
    """Deps for `show` and `explain`: any fetch or wt call fails the test."""

    def no_fetch(url: str) -> bytes:
        raise AssertionError(f"show and explain never fetch: {url}")

    return cli.Deps(fetcher=no_fetch, sleep=Sleeps(), now=lambda: now, run_wt=_never_run_wt)


@pytest.fixture
def written(specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]) -> None:
    """A `tiers.toml` generated at T0 from the golden fixtures."""
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()


@pytest.mark.usefixtures("written")
def test_show_prints_the_age_the_table_and_the_attribution(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["show"], deps=_reader_deps(T1)) == 0
    captured = capsys.readouterr()
    assert captured.out == (
        "tiers.toml generated 2026-10-12T15:58:00Z (7 days old), contracts fixture-1\n"
        + SHOWN_TABLE
        + "candidates: 2 (list them with --candidates)\n"
        + ATTRIBUTION
    )
    assert captured.err == ""


@pytest.mark.usefixtures("written")
def test_show_candidates_lists_them_and_warns_past_fourteen_days(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["show", "--candidates"], deps=_reader_deps(T0 + timedelta(days=15))) == 0
    captured = capsys.readouterr()
    assert captured.out == (
        "tiers.toml generated 2026-10-12T15:58:00Z (15 days old), contracts fixture-1\n"
        + SHOWN_TABLE
        + "candidates: 2 (scored by a source, not in the inventory)\n"
        + "M  marathon  high    anthropic/claude-opus-5-5\n"
        + "D  deep      high    anthropic/claude-fable-5\n"
        + ATTRIBUTION
    )
    assert captured.err == (
        "warning: tiers.toml is 15 days old; run `modeltiers run` to refresh it\n"
    )


@pytest.mark.usefixtures("written")
def test_show_sort_and_wide_reorder_and_widen_the_table(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["show", "--sort", "model", "--wide"], deps=_reader_deps(T0)) == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[1].startswith(
        "T  tier      conf    open   context     intel     arena       eci    coding   agentic"
    )
    assert [line.split()[12] for line in lines[2:5]] == [
        "ollama/deepseek-v4-pro:cloud",
        "ollama/gemma4:31b-mlx",
        "ollama/glm-5.3:cloud",
    ]
    # Without --wide the sorted table has today's columns.
    assert cli.main(["show", "--sort", "intelligence_index"], deps=_reader_deps(T0)) == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[1:3] == TABLE.splitlines()[:2]
    assert lines[4].split()[3] == "ollama/qwen3.8:27b-mlx"


@pytest.mark.usefixtures("written")
def test_show_html_writes_the_page_and_prints_only_where(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    target = tmp_path / "pages" / "tiers.html"  # the directory does not exist yet
    late = _reader_deps(T0 + timedelta(days=15))
    assert cli.main(["show", "--html", str(target), "--sort", "eci"], deps=late) == 0
    captured = capsys.readouterr()
    assert captured.out == f"wrote {target}\n"
    assert captured.err == (
        "warning: tiers.toml is 15 days old; run `modeltiers run` to refresh it\n"
    )
    text = target.read_text(encoding="utf-8")
    assert text.startswith("<!doctype html>")
    assert "(15 days old), contracts fixture-1" in text
    assert '"sort": "eci"' in text
    assert "ollama/glm-5.3:cloud" in text
    assert "anthropic/claude-opus-5-5" in text  # the candidates are always there
    assert [p.name for p in target.parent.iterdir()] == ["tiers.html"]


@pytest.mark.usefixtures("written")
def test_show_html_names_the_whole_path_of_a_relative_file(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.chdir(tmp_path)
    assert cli.main(["show", "--html", "tiers.html"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out == f"wrote {tmp_path.resolve() / 'tiers.html'}\n"


@pytest.mark.usefixtures("written")
def test_show_html_that_cannot_be_written_exits_1(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(["show", "--html", str(tmp_path)], deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.startswith("modeltiers: ")
    # The half-written copy beside it is gone.
    assert [p.name for p in tmp_path.parent.iterdir() if p.name.endswith(".tmp")] == []


@pytest.mark.usefixtures("written")
def test_show_html_refuses_to_replace_the_tiers_file_with_the_page(
    out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    before = out.read_text()
    assert cli.main(["show", "--html", str(out)], deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        f"modeltiers: --html {out} is tiers.toml itself; name another file for the page\n"
    )
    assert out.read_text() == before


@pytest.mark.usefixtures("written")
def test_show_html_refuses_another_name_for_the_tiers_file(
    out: Path, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    before = out.read_text()
    link = tmp_path / "page.html"
    link.symlink_to(out)
    names = [link]
    shouted = out.with_name(out.name.upper())
    if shouted.exists():  # a file system that ignores case, as macOS has by default
        names.append(shouted)
    for name in names:
        assert cli.main(["show", "--html", str(name)], deps=_reader_deps(T0)) == 1
        assert "is tiers.toml itself" in capsys.readouterr().err
        assert out.read_text() == before


def test_show_html_without_a_tiers_file_writes_no_page(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    target = tmp_path / "tiers.html"
    assert cli.main(["show", "--html", str(target)], deps=_reader_deps(T0)) == 1
    assert "run `modeltiers run` to write it" in capsys.readouterr().err
    assert not target.exists()


def test_show_refuses_a_sort_column_it_does_not_have(
    capsys: pytest.CaptureFixture[str],
) -> None:
    with pytest.raises(SystemExit) as stopped:
        cli.main(["show", "--sort", "price"], deps=_reader_deps(T0))
    assert stopped.value.code == 2
    assert "invalid choice: 'price'" in capsys.readouterr().err


@pytest.mark.parametrize("argv", [["show"], ["explain", "zhipuai/glm-5.3"]])
def test_show_and_explain_without_a_file_name_the_run_command(
    argv: list[str], out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(argv, deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        f"modeltiers: cannot read {out}: No such file or directory; "
        "run `modeltiers run` to write it\n"
    )


@pytest.mark.parametrize("argv", [["show"], ["explain", "zhipuai/glm-5.3"]])
def test_show_and_explain_with_a_file_that_is_not_toml_exit_1(
    argv: list[str], out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    out.parent.mkdir()
    out.write_text("generated_at = \n")
    assert cli.main(argv, deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err.startswith(f"modeltiers: {out} is not TOML")
    assert captured.err.endswith("run `modeltiers run` to write it again\n")


# --- explain ------------------------------------------------------------------


@pytest.mark.usefixtures("written")
def test_explain_a_model_by_pair_and_by_key(capsys: pytest.CaptureFixture[str]) -> None:
    assert cli.main(["explain", "ollama/minimax-m3:cloud"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out == (
        "ollama/minimax-m3:cloud\n"
        "  identity:    minimax/MiniMax-M3, resolved by normalized\n"
        "  tier:        quick (Q)\n"
        "  confidence:  high, basis measured\n"
        "  votes:       arena_agent quick, eci quick\n"
        "  scores:      arena_agent -0.0695, arena_agent_ci_lower -0.0786, eci 146.95\n"
        "  aligned:     34.7 (arena_agent -0.0695 as 34.7; eci 146.95 as 34.8)\n"
        "  efforts:     none\n"
        "  tool_call:   true\n"
        "  context:     512000\n"
        "  open_weights: true\n"
        "  note:        host row has no canonical id; matched by name\n"
        "modelsdev: models.dev (https://models.dev), MIT\n"
        "arena: Arena (arena.ai), dataset lmarena-ai/leaderboard-dataset, CC BY 4.0\n"
        "epoch: Epoch AI, 'Capabilities & benchmarking', https://epoch.ai/benchmarks, CC BY 4.0\n"
    )
    # A key, in another case: both inventory rows that resolved to it.
    assert cli.main(["explain", "ZHIPUAI/GLM-5.3"], deps=_reader_deps(T0)) == 0
    by_key = capsys.readouterr().out
    assert by_key.startswith("ollama/glm-5.3:cloud\n")
    assert "\nopenrouter/z-ai/glm-5.3\n" in by_key
    # A key with no inventory row: the candidate.
    assert cli.main(["explain", "anthropic/claude-fable-5"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out.startswith("candidate anthropic/claude-fable-5\n")


@pytest.mark.usefixtures("written")
def test_explain_a_local_build_shows_the_tier_it_inherited(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["explain", "ollama/qwen3.8:27b-mlx"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out == (
        "ollama/qwen3.8:27b-mlx\n"
        "  identity:    alibaba/qwen3.8-27b, resolved by normalized\n"
        "  tier:        standard (S)\n"
        "  confidence:  low, basis inherited\n"
        "  votes:       intelligence_index standard\n"
        "  scores:      intelligence_index 33.7, coding_index 68.1, agentic_index 45.8\n"
        "  aligned:     33.7 (intelligence_index 33.7)\n"
        "  efforts:     none\n"
        "  tool_call:   true\n"
        "  context:     unknown\n"
        "  open_weights: true\n"
        "  note:        local build: served context unknown; context gate skipped\n"
        "modelsdev: models.dev (https://models.dev), MIT\n"
        "openrouter: Source: Artificial Analysis (artificialanalysis.ai) via OpenRouter "
        "(openrouter.ai/rankings).\n"
    )


@pytest.mark.usefixtures("written")
def test_explain_an_unmapped_row_and_a_name_that_matches_nothing(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["explain", "ollama/medgemma:27b"], deps=_reader_deps(T0)) == 0
    assert capsys.readouterr().out.startswith("ollama/medgemma:27b is unmapped: ")
    assert cli.main(["explain", "ollama/absent:cloud"], deps=_reader_deps(T0)) == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert captured.err == (
        "modeltiers: no model or canonical key 'ollama/absent:cloud' in the file generated "
        "2026-10-12T15:58:00Z\n"
    )


# --- card links and the redirects behind them -----------------------------------

ANTHROPIC_FIXTURE_URL = "https://vendor.example/anthropic/models"
ANTHROPIC_MOVED_URL = "https://vendor.example/anthropic/moved"


def _card_urls(out: Path) -> dict[str, str]:
    document = tomllib.loads(out.read_text())
    return {
        entry["canonical"]: entry["card_url"]
        for entry in [*document["models"], *document["candidates"]]
        if "card_url" in entry
    }


def test_run_writes_a_card_for_each_model_that_has_one(
    specs: SourceSpecs, good_answers: Answers, out: Path
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    assert _card_urls(out) == {
        "zhipuai/glm-5.3": "https://huggingface.co/zai-org/GLM-5.3",
        "alibaba/qwen3.8-27b": "https://huggingface.co/Qwen/Qwen3.8-27B",
        "anthropic/claude-opus-5-5": ANTHROPIC_FIXTURE_URL,
        "anthropic/claude-fable-5": ANTHROPIC_FIXTURE_URL,
    }


def test_a_catalog_that_moved_is_followed_written_and_kept_for_the_next_run(
    specs: SourceSpecs,
    good_answers: Answers,
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    deps, _ = _deps(specs, good_answers)
    moved = replace(deps, resolver=FakeResolver({ANTHROPIC_FIXTURE_URL: ANTHROPIC_MOVED_URL}))
    assert cli.main(RUN, deps=moved) == 0
    assert _card_urls(out)["anthropic/claude-fable-5"] == ANTHROPIC_MOVED_URL
    assert capsys.readouterr().err == (
        MTPLX_WARNING
        + f"warning: vendor link anthropic: {ANTHROPIC_FIXTURE_URL} moved to {ANTHROPIC_MOVED_URL}\n"
    )
    kept = tomllib.loads((tmp_path / "cache" / "vendor-links.toml").read_text())
    assert kept == {"anthropic": {"from": ANTHROPIC_FIXTURE_URL, "url": ANTHROPIC_MOVED_URL}}
    # The next run, with no network to the lab at all, starts from where it ended.
    offline_deps, _ = _deps(specs, good_answers)
    assert cli.main([*RUN, "--offline"], deps=offline_deps) == 0
    assert _card_urls(out)["anthropic/claude-fable-5"] == ANTHROPIC_MOVED_URL


def test_an_offline_run_follows_no_redirect(specs: SourceSpecs, good_answers: Answers) -> None:
    deps, _ = _deps(specs, good_answers)
    assert cli.main(RUN, deps=deps) == 0  # fills the snapshots
    resolver = FakeResolver({ANTHROPIC_FIXTURE_URL: ANTHROPIC_MOVED_URL})
    assert cli.main([*RUN, "--offline"], deps=replace(deps, resolver=resolver)) == 0
    assert resolver.calls == []


def test_a_catalog_that_does_not_answer_keeps_its_address_and_the_run_goes_on(
    specs: SourceSpecs,
    good_answers: Answers,
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    deps, _ = _deps(specs, good_answers)
    down = replace(deps, resolver=FakeResolver(down=frozenset({ANTHROPIC_FIXTURE_URL})))
    assert cli.main(RUN, deps=down) == 0
    assert _card_urls(out)["anthropic/claude-fable-5"] == ANTHROPIC_FIXTURE_URL
    err = capsys.readouterr().err
    assert (
        f"warning: vendor link anthropic: could not check it ({ANTHROPIC_FIXTURE_URL}: HTTP 503)"
        in err
    )
    # An address that never answered would shadow a later release's: not kept.
    assert not (tmp_path / "cache" / "vendor-links.toml").exists()


def test_a_bad_vendors_file_stops_the_run_before_any_download(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    bad = tmp_path / "vendors.toml"
    bad.write_text('[[vendor]]\nprefix = "x"\nurl = "http://insecure.example/"\nnote = "n"\n')
    deps, fetcher = _deps(specs, good_answers)
    assert cli.main(RUN, deps=replace(deps, vendors_path=bad)) == 1
    assert "not an https address" in capsys.readouterr().err
    assert fetcher.calls == []


def test_default_deps_follow_redirects_with_the_real_resolver() -> None:
    deps = cli.Deps()
    assert deps.resolver is fetch.http_resolver
    assert deps.vendors_path is None  # the committed data/vendors.toml


def test_show_orders_the_rows_by_aligned_score_unless_told_otherwise(
    written: None, capsys: pytest.CaptureFixture[str]
) -> None:
    def names(argv: list[str]) -> list[str]:
        assert cli.main(argv, deps=_reader_deps(T1)) == 0
        lines = capsys.readouterr().out.splitlines()
        return [line.split()[3] for line in lines if line[:3] in ("S  ", "Q  ", "-  ")]

    by_aligned = names(["show"])
    assert by_aligned == names(["show", "--sort", "aligned"])
    assert by_aligned[:2] == ["ollama/glm-5.3:cloud", "openrouter/z-ai/glm-5.3"]
    assert names(["show", "--sort", "model"]) == sorted(by_aligned, key=str.lower)
    assert names(["show", "--sort", "model"]) != by_aligned


def test_the_page_opens_in_aligned_order_unless_told_otherwise(
    written: None, tmp_path: Path
) -> None:
    def opens_in(*argv: str) -> str | None:
        target = tmp_path / "page.html"
        assert cli.main(["show", "--html", str(target), *argv], deps=_reader_deps(T1)) == 0
        found = re.search(r'"sort": ("[a-z_]+"|null)', target.read_text())
        assert found is not None
        return str(json.loads(found.group(1))) if found.group(1) != "null" else None

    assert opens_in() == "aligned"
    assert opens_in("--sort", "eci") == "eci"


# --- a failed run says so, last --------------------------------------------------


def test_a_failed_run_ends_on_a_blank_line_and_one_capitalised_error_line(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    """Whatever stops `run`, the last line on stderr says that it failed and
    that nothing was written, after a blank line, even when warnings came first."""
    drifted = scaled_openrouter(good_answers[fetch.OPENROUTER_URL][0], 1.3)
    deps, _ = _deps(specs, {**good_answers, fetch.OPENROUTER_URL: [drifted]})
    assert cli.main(RUN, deps=deps) == 3
    lines = capsys.readouterr().err.split("\n")
    assert lines[-1] == ""
    assert lines[-2].startswith("ERROR: modeltiers run FAILED (exit 3): ")
    assert lines[-2].endswith("; tiers.toml was not written.")
    assert lines[-3] == ""
    assert "warning: " in lines[0]  # the warnings stay above it


def test_a_run_that_did_not_fail_prints_no_error_line(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)[0]) == 0
    assert "ERROR" not in capsys.readouterr().err


def test_the_stale_exit_is_a_warning_not_a_failure(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    deps, _ = _deps(specs, good_answers)
    assert cli.main(RUN, deps=deps) == 0  # fills the snapshots
    capsys.readouterr()
    down = {url: [FetchError("down")] * 2 for url in good_answers}
    stale, _ = _deps(specs, down)
    assert cli.main([*RUN, "--dry-run"], deps=stale) == 2
    assert "ERROR" not in capsys.readouterr().err


def test_the_other_commands_keep_their_plain_message(
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(["show"], deps=_reader_deps(T0)) == 1
    err = capsys.readouterr().err
    assert err.startswith("modeltiers: cannot read ")
    assert "ERROR" not in err
