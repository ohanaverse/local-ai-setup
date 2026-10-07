"""The latest-run pointers: their own file, and the one-time read of the
keys modelman.toml used to hold."""

import tomllib
from pathlib import Path

import pytest

from llmbench.state import StateStore, latest_path, load_state, save_state

MODELMAN_TOML = """\
price_refresh_last_run = "2026-10-01"

[benchmarks]
last_run = "2026-08-28T14:32:00+00:00"
last_run_dir = "/results"
agent_last_run = "/results/agent-1"
eval_last_run = "/results/eval-1"
some_other_key = "not a pointer"

[model_state."ollama/a"]
ready = true
"""


@pytest.fixture
def paths(monkeypatch, tmp_path):
    """(latest.toml, modelman.toml) under tmp_path, neither written yet."""
    latest = tmp_path / "benchmarks" / "latest.toml"
    legacy = tmp_path / "modelman.toml"
    monkeypatch.setenv("LLMBENCH_LATEST", str(latest))
    monkeypatch.setenv("MODELMAN_STATE", str(legacy))
    return latest, legacy


def test_latest_path_default_and_override(monkeypatch, tmp_path):
    monkeypatch.delenv("LLMBENCH_LATEST", raising=False)
    assert latest_path() == Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "x.toml"))
    assert latest_path() == tmp_path / "x.toml"


def test_latest_path_ignores_xdg_config_home(monkeypatch, tmp_path):
    """latest.toml sits beside the results it points at, and the results
    directory is ~/.config/local-ai/benchmarks whatever XDG_CONFIG_HOME says.
    (The fallback read of modelman.toml does honour XDG: see below.)"""
    monkeypatch.delenv("LLMBENCH_LATEST", raising=False)
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert latest_path() == Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"


def test_pointers_round_trip_through_a_flat_file(paths):
    """latest.toml is flat: the four keys at the top level, no [benchmarks]
    table. The file is read by hand when a run goes missing."""
    latest, _ = paths
    store = load_state()
    assert store.extra == {}
    store.extra.setdefault("benchmarks", {})["agent_last_run"] = "/results/agent-2"
    save_state(store)

    assert tomllib.loads(latest.read_text(encoding="utf-8")) == {
        "agent_last_run": "/results/agent-2"
    }
    assert load_state().extra["benchmarks"] == {"agent_last_run": "/results/agent-2"}


def test_first_read_falls_back_to_modelmans_benchmarks_table(paths):
    """A user who ran benchmarks through modelman has their --latest pointers
    in modelman.toml. `llmbench agent show --latest` must still find that run
    the first time, not report "no latest run recorded"."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    assert load_state().extra["benchmarks"] == {
        "last_run": "2026-08-28T14:32:00+00:00",
        "last_run_dir": "/results",
        "agent_last_run": "/results/agent-1",
        "eval_last_run": "/results/eval-1",
    }
    assert not latest.exists()  # a read never writes


def test_the_fallback_is_read_once(paths):
    """The first recorded run carries the old pointers into latest.toml;
    after that modelman.toml is never consulted, so a pointer modelman.toml
    still holds cannot shadow a newer one."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    store = load_state()
    store.extra["benchmarks"]["eval_last_run"] = "/results/eval-2"
    save_state(store)

    legacy.write_text(MODELMAN_TOML.replace("agent-1", "agent-STALE"), encoding="utf-8")
    assert load_state().extra["benchmarks"] == {
        "last_run": "2026-08-28T14:32:00+00:00",
        "last_run_dir": "/results",
        "agent_last_run": "/results/agent-1",
        "eval_last_run": "/results/eval-2",
    }
    assert legacy.read_text(encoding="utf-8").count("agent-STALE") == 1  # never written


@pytest.mark.parametrize(
    "legacy_text",
    [
        None,  # no modelman.toml at all: a machine that never ran modelman
        "[benchmarks\n",  # unreadable
        'benchmarks = "not a table"\n',
        "[benchmarks]\nlast_run_dir = 7\n",  # not a string: not a pointer
        "[model_state]\n",  # no [benchmarks] table
        b'[benchmarks]\nlast_run = "\xff\xfe"\n',  # not UTF-8
    ],
    ids=["absent", "corrupt", "not-a-table", "wrong-type", "no-table", "bytes"],
)
def test_an_unusable_modelman_toml_reads_as_no_pointers(paths, legacy_text):
    _, legacy = paths
    if isinstance(legacy_text, bytes):
        legacy.write_bytes(legacy_text)
    elif legacy_text is not None:
        legacy.write_text(legacy_text, encoding="utf-8")
    assert load_state().extra.get("benchmarks", {}) == {}


def test_a_corrupt_latest_toml_reads_as_no_pointers_and_is_replaced(paths):
    """The pointer file is disposable. A torn or hand-broken one must not
    crash the pointer write that follows a benchmark run that took hours."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    latest.parent.mkdir(parents=True)
    latest.write_text("last_run = \n", encoding="utf-8")
    _assert_replaced(latest, legacy)

    latest.write_bytes(b'last_run = "\xff\xfe"\n')  # not UTF-8
    _assert_replaced(latest, legacy)


def _assert_replaced(latest, legacy):
    store = load_state()
    assert store.extra.get("benchmarks", {}) == {}  # present: no fallback either
    store.extra.setdefault("benchmarks", {})["last_run_dir"] = "/results"
    save_state(store)
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/results"}


def test_the_fallback_finds_modelman_toml_where_modelman_kept_it(monkeypatch, tmp_path):
    """MODELMAN_STATE > XDG_CONFIG_HOME > ~/.config, modelman's own rule."""
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "latest.toml"))
    monkeypatch.delenv("MODELMAN_STATE", raising=False)
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    home_file = tmp_path / "home" / ".config" / "local-ai" / "modelman.toml"
    home_file.parent.mkdir(parents=True)
    home_file.write_text('[benchmarks]\nlast_run_dir = "/from-home"\n', encoding="utf-8")
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/from-home"}

    xdg_file = tmp_path / "xdg" / "local-ai" / "modelman.toml"
    xdg_file.parent.mkdir(parents=True)
    xdg_file.write_text('[benchmarks]\nlast_run_dir = "/from-xdg"\n', encoding="utf-8")
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path / "xdg"))
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/from-xdg"}


def test_an_explicit_path_never_falls_back(paths, tmp_path):
    _, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    assert load_state(tmp_path / "elsewhere.toml") == StateStore()
