"""`modeltiers ladder`: one signal's resolved scores between two bounds, from
the newest snapshots, with the gaps a floor can go in."""

import shutil
from datetime import timedelta
from pathlib import Path

import pytest
from conftest import FIXTURES, T0
from fakes import FakeFetcher, Sleeps

from modeltiers import cli, ladder, snapshots
from modeltiers.identity import NORMALIZED, Match
from modeltiers.loader import SourceSpecs
from modeltiers.records import ScoreRecord

Answers = dict[str, list[bytes | Exception]]

SOURCE_LINES = """\
modelsdev: stale, 12 records, fetched 2026-10-12T15:58:00Z
arena: stale, 6 records, fetched 2026-10-12T15:58:00Z
"""


def _never_run_wt() -> str:
    raise AssertionError("ladder must not run wt")


def _deps(specs: SourceSpecs, answers: Answers) -> tuple[cli.Deps, FakeFetcher]:
    fetcher = FakeFetcher(answers)
    deps = cli.Deps(
        fetcher=fetcher,
        sleep=Sleeps(),
        now=lambda: T0,
        run_wt=_never_run_wt,
        specs=specs,
        aliases_path=FIXTURES / "aliases.toml",
        contracts_path=FIXTURES / "contracts.toml",
    )
    return deps, fetcher


@pytest.fixture
def cache(
    specs: SourceSpecs, good_answers: Answers, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> Path:
    """The cache after one run over the golden fixtures: a snapshot a source."""
    run = ["run", "--inventory", str(FIXTURES / "inventory.json"), "--dry-run"]
    assert cli.main(run, deps=_deps(specs, good_answers)[0]) == 0
    capsys.readouterr()
    return tmp_path / "cache"


def test_ladder_prints_every_resolved_score_highest_first_with_gaps_and_midpoints(
    specs: SourceSpecs, cache: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    deps, fetcher = _deps(specs, {})
    assert cli.main(["ladder", "arena_agent"], deps=deps) == 0
    # Qwen3.8 Max (0.0235) resolved to no key, so it is not a rung.
    assert capsys.readouterr().out == SOURCE_LINES + (
        "   0.1433  gap below  0.0539  midpoint    0.1163  anthropic/claude-opus-5-5\n"
        "   0.0894  gap below  0.0679  midpoint    0.0554  anthropic/claude-fable-5\n"
        "   0.0215  gap below  0.0080  midpoint    0.0175  zhipuai/glm-5.3\n"
        "   0.0134  gap below  0.0830  midpoint   -0.0281  deepseek/deepseek-v4-pro-0813\n"
        "  -0.0695  minimax/MiniMax-M3\n"
    )
    assert fetcher.calls == []


def test_low_and_high_bound_the_window(
    specs: SourceSpecs, cache: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["ladder", "arena_agent", "--low", "-0.01", "--high", "0.09"]
    assert cli.main(argv, deps=_deps(specs, {})[0]) == 0
    assert capsys.readouterr().out == SOURCE_LINES + (
        "   0.0894  gap below  0.0679  midpoint    0.0554  anthropic/claude-fable-5\n"
        "   0.0215  gap below  0.0080  midpoint    0.0175  zhipuai/glm-5.3\n"
        "   0.0134  deepseek/deepseek-v4-pro-0813\n"
    )


def test_a_ci_lower_signal_places_the_lower_bounds(
    specs: SourceSpecs, cache: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["ladder", "arena_agent_ci_lower", "--low", "0.01"]
    assert cli.main(argv, deps=_deps(specs, {})[0]) == 0
    assert capsys.readouterr().out == SOURCE_LINES + (
        "   0.1215  gap below  0.0448  midpoint    0.0991  anthropic/claude-opus-5-5\n"
        "   0.0767  gap below  0.0624  midpoint    0.0455  anthropic/claude-fable-5\n"
        "   0.0143  zhipuai/glm-5.3\n"
    )


def test_a_window_with_no_score_exits_1_and_says_so(
    specs: SourceSpecs, cache: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    argv = ["ladder", "arena_agent", "--low", "0.5"]
    assert cli.main(argv, deps=_deps(specs, {})[0]) == 1
    assert capsys.readouterr().err == (
        "modeltiers: no resolved arena_agent score from 0.5 to inf\n"
    )


def test_ladder_reads_only_models_dev_and_the_signals_own_source(
    specs: SourceSpecs, cache: Path
) -> None:
    shutil.rmtree(cache / "openrouter")
    shutil.rmtree(cache / "epoch")
    assert cli.main(["ladder", "arena_agent"], deps=_deps(specs, {})[0]) == 0


def test_ladder_with_an_empty_cache_exits_1_and_names_the_source(
    specs: SourceSpecs, capsys: pytest.CaptureFixture[str]
) -> None:
    assert cli.main(["ladder", "eci"], deps=_deps(specs, {})[0]) == 1
    assert capsys.readouterr().err == (
        "modeltiers: no usable data and no good snapshot for: modelsdev\n"
    )


def test_a_snapshot_that_no_longer_parses_is_a_warning_and_the_older_one_is_read(
    specs: SourceSpecs, cache: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    newer = snapshots.save(cache, "arena", "json", b"{}", T0 + timedelta(days=1))
    assert cli.main(["ladder", "arena_agent"], deps=_deps(specs, {})[0]) == 0
    captured = capsys.readouterr()
    assert captured.out.startswith(SOURCE_LINES)
    assert captured.err.startswith("warning: arena:")
    assert "snapshot set aside as" in captured.err
    assert not newer.path.exists()


def test_an_unknown_signal_is_a_usage_error() -> None:
    with pytest.raises(SystemExit) as raised:
        cli.main(["ladder", "coding_index"])
    assert raised.value.code == 2


def _match(key: str, score: float, ci_lower: float | None) -> Match:
    return Match(key, NORMALIZED, ScoreRecord("arena", key, score, ci_lower=ci_lower))


def test_a_record_without_a_lower_bound_is_not_a_rung_of_a_ci_lower_ladder() -> None:
    matches = [_match("a/one", 0.5, 0.4), _match("a/two", 0.3, None), _match("a/three", 0.2, 0.1)]
    assert ladder.ladder_lines(matches, lower_bound=True) == [
        "   0.4000  gap below  0.3000  midpoint    0.2500  a/one",
        "   0.1000  a/three",
    ]


def test_a_bound_is_inside_the_window() -> None:
    matches = [_match("a/one", 0.5, None), _match("a/two", 0.3, None)]
    assert ladder.ladder_lines(matches, lower_bound=False, low=0.3, high=0.5) == [
        "   0.5000  gap below  0.2000  midpoint    0.4000  a/one",
        "   0.3000  a/two",
    ]
