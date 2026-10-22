"""What calibration does to a run: the moves `docs/guides/12-model-tiers.md`
describes, over the golden fixtures. `aliases_calibrated.toml` is the fixture
`aliases.toml` after one pass: an ignore for the unmapped row, a declared key
for the unresolved record, and an alias for a model the inventory no longer
has. The alias that gives the unmapped row a key instead is built in the test
that needs it."""

import tomllib
from collections.abc import Callable
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any

import pytest
from conftest import ALIGN_MIN, FIXTURES, INVENTORY, T0, scaled_openrouter
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, fetch
from modeltiers.assign import NOTE_NO_ROWS
from modeltiers.loader import SourceSpecs

UNCALIBRATED = FIXTURES / "aliases.toml"
CALIBRATED = FIXTURES / "aliases_calibrated.toml"
CONTRACTS = FIXTURES / "contracts.toml"
RUN = ["run", "--inventory", str(INVENTORY)]
Answers = dict[str, list[bytes | Exception]]

MTPLX_WARNING = (
    "warning: provider mtplx probe status is partial; "
    "models it would have discovered may be absent from this run\n"
)


def _deps(
    specs: SourceSpecs,
    answers: Answers,
    *,
    now: datetime = T0,
    aliases_path: Path = CALIBRATED,
    contracts_path: Path = CONTRACTS,
) -> cli.Deps:
    return cli.Deps(
        fetcher=FakeFetcher(answers),
        sleep=Sleeps(),
        now=lambda: now,
        specs=specs,
        aliases_path=aliases_path,
        contracts_path=contracts_path,
        align_min=ALIGN_MIN,
    )


def _written(out: Path) -> dict[str, Any]:
    return tomllib.loads(out.read_text())


def test_a_calibrated_run_leaves_nothing_unmapped_or_unresolved(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers)) == 0
    report = capsys.readouterr().out
    assert "identity: 8 rows: 6 resolved, 2 ignored, 0 unmapped\n" in report
    assert (
        "unmapped: 0 (each needs an [[alias]] or an [[ignore]] in aliases.toml, "
        "or is left open until evidence identifies it)\n"
    ) in report
    assert "unresolved: 0 at or above a standard floor" in report
    doc = _written(out)
    # An array with no entries is absent from the file.
    assert "unmapped" not in doc
    assert "unresolved" not in doc
    assert len(doc["models"]) == 8


def test_an_ignored_row_is_written_unrated_and_not_applicable(
    specs: SourceSpecs, good_answers: Answers, out: Path
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers))
    (entry,) = [m for m in _written(out)["models"] if m["model_name"] == "medgemma:27b"]
    assert entry["tier"] == "unrated"
    assert entry["code"] == "-"
    assert entry["reason"] == "not_applicable"
    assert entry["confidence"] == "none"
    assert entry["notes"] == ["ignored: fixture: a fine-tune no source scores"]
    assert "canonical" not in entry
    assert "resolved_by" not in entry


def test_a_declared_key_becomes_a_candidate_with_both_gates_skipped(
    specs: SourceSpecs, good_answers: Answers, out: Path
) -> None:
    cli.main(RUN, deps=_deps(specs, good_answers))
    (entry,) = [c for c in _written(out)["candidates"] if c["canonical"] == "alibaba/qwen3.8-max"]
    assert entry["resolved_by"] == "alias"
    assert entry["tier"] == "standard"
    assert entry["confidence"] == "low"
    assert entry["votes"] == {"arena_agent": "standard"}
    assert entry["notes"] == [NOTE_NO_ROWS]
    # models.dev has no row for a declared key, so it knows neither of these.
    assert "tool_call" not in entry
    assert "context" not in entry
    assert "open_weights" not in entry


def test_an_alias_for_a_model_the_inventory_no_longer_has_is_silent(
    specs: SourceSpecs, good_answers: Answers, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main([*RUN, "--dry-run"], deps=_deps(specs, good_answers)) == 0
    captured = capsys.readouterr()
    # Only the fixture's mtplx probe warning: nothing about the unused alias.
    assert captured.err == MTPLX_WARNING
    assert "MTPLX" not in captured.out


def test_the_first_calibrated_run_reports_what_changed_and_replaces_the_file(
    specs: SourceSpecs, good_answers: Answers, out: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    # A file from before calibration: medgemma unmapped, Qwen3.8 Max unresolved.
    assert cli.main(RUN, deps=_deps(specs, good_answers, aliases_path=UNCALIBRATED)) == 0
    before = _written(out)
    assert [u["model_name"] for u in before["unmapped"]] == ["medgemma:27b"]
    assert [u["id"] for u in before["unresolved"]] == ["Qwen3.8 Max"]
    capsys.readouterr()
    later = T0 + timedelta(days=1)
    assert cli.main(RUN, deps=_deps(specs, good_answers, now=later)) == 0
    report = capsys.readouterr().out
    assert (
        "changes since 2026-10-12T15:58:00Z:\n"
        "  now ignored: ollama/medgemma:27b (fixture: a fine-tune no source scores)\n"
        "  confidence only: 0\n"
        "  candidates: 1 added, 0 removed, 0 changed tier\n"
        "  sources: modelsdev ok, openrouter ok, arena ok, epoch ok\n"
    ) in report
    assert _written(out)["generated_at"] == "2026-10-13T15:58:00Z"


def _aliased(tmp_path: Path) -> Path:
    """The fixture aliases.toml after the owner found evidence for the
    unmapped row. Any key with votes stands for the model the evidence named."""
    entry = (
        "\n[[alias]]\n"
        'canonical = "zhipuai/glm-5.3"\n'
        'note = "fixture: stands for evidence that names the model"\n'
        'inventory = [{ provider_id = "ollama", model_name = "medgemma:27b" }]\n'
    )
    path = tmp_path / "aliases.toml"
    path.write_text(UNCALIBRATED.read_text() + entry)
    return path


def test_an_aliased_row_leaves_unmapped_and_is_reported_with_its_tier(
    specs: SourceSpecs,
    good_answers: Answers,
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    assert cli.main(RUN, deps=_deps(specs, good_answers, aliases_path=UNCALIBRATED)) == 0
    assert [u["model_name"] for u in _written(out)["unmapped"]] == ["medgemma:27b"]
    capsys.readouterr()
    later = T0 + timedelta(days=1)
    deps = _deps(specs, good_answers, now=later, aliases_path=_aliased(tmp_path))
    assert cli.main(RUN, deps=deps) == 0
    report = capsys.readouterr().out
    assert "identity: 8 rows: 7 resolved, 1 ignored, 0 unmapped\n" in report
    assert (
        "changes since 2026-10-12T15:58:00Z:\n"
        "  newly resolved: ollama/medgemma:27b standard\n"
        "  confidence only: 0\n"
        "  candidates: 0 added, 0 removed, 0 changed tier\n"
    ) in report
    doc = _written(out)
    assert "unmapped" not in doc
    (entry,) = [m for m in doc["models"] if m["model_name"] == "medgemma:27b"]
    assert entry["canonical"] == "zhipuai/glm-5.3"
    assert entry["resolved_by"] == "alias"
    assert entry["tier"] == "standard"
    assert entry["basis"] == "inherited"


def _recalibrated(tmp_path: Path) -> Path:
    """The fixture contracts.toml after the owner accepts a moved vote: the
    two sentinels that pinned three deep votes now pin a standard
    intelligence index, and the version is new."""
    old = 'votes = { intelligence_index = "deep", arena_agent = "deep", eci = "deep" }'
    new = 'votes = { intelligence_index = "standard", arena_agent = "deep", eci = "deep" }'
    text = CONTRACTS.read_text()
    assert text.count(old) == 2
    path = tmp_path / "contracts.toml"
    path.write_text(text.replace(old, new).replace('"fixture-1"', '"fixture-2"'))
    return path


def test_editing_the_pinned_votes_is_what_clears_a_moved_sentinel(
    specs: SourceSpecs,
    good_answers: Answers,
    read_fixture: Callable[[str], bytes],
    out: Path,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    # Every intelligence index drops by a fifth, as in the sentinel tests.
    drifted = scaled_openrouter(read_fixture("openrouter.json"), 0.8)
    good_answers[fetch.OPENROUTER_URL] = [drifted]

    # The same data twice. With the old pins nothing is written.
    assert cli.main(RUN, deps=_deps(specs, good_answers)) == 3
    assert not out.exists()
    assert "sentinels: 2 of 4 FAILED\n" in capsys.readouterr().out
    # With the pins edited the run is clean and the file names the new version.
    deps = _deps(specs, good_answers, contracts_path=_recalibrated(tmp_path))
    assert cli.main(RUN, deps=deps) == 0
    assert "sentinels: 4 of 4 hold\n" in capsys.readouterr().out
    assert _written(out)["contracts_version"] == "fixture-2"
