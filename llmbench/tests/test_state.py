"""The latest-run pointers: one flat file, and nothing read from anywhere else."""

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
    """(latest.toml, modelman.toml) under tmp_path, neither written yet.
    XDG_CONFIG_HOME points at tmp_path, so the second path is where modelman
    kept its state file under that variable, and MODELMAN_STATE, the override
    it honoured, names it as well. HOME is a directory of its own, for the
    test that also puts the old file at the pre-XDG default."""
    latest = tmp_path / "benchmarks" / "latest.toml"
    legacy = tmp_path / "local-ai" / "modelman.toml"
    legacy.parent.mkdir(parents=True)
    monkeypatch.setenv("LLMBENCH_LATEST", str(latest))
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
    monkeypatch.setenv("MODELMAN_STATE", str(legacy))
    return latest, legacy


def test_latest_path_default_and_override(monkeypatch, tmp_path):
    monkeypatch.delenv("LLMBENCH_LATEST", raising=False)
    assert latest_path() == Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"
    monkeypatch.setenv("LLMBENCH_LATEST", str(tmp_path / "x.toml"))
    assert latest_path() == tmp_path / "x.toml"


def test_latest_path_ignores_xdg_config_home(monkeypatch, tmp_path):
    """latest.toml sits beside the results it points at, and the results
    directory is ~/.config/local-ai/benchmarks whatever XDG_CONFIG_HOME says."""
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


def test_modelman_toml_is_never_read(paths):
    """llmbench used to read the pointers from modelman.toml's [benchmarks]
    table while latest.toml did not exist. modelman is gone and the file is
    one the maintenance guide says to delete, so it must not matter whether
    it is there: with no latest.toml there is no latest run, wherever the old
    file sits (the XDG path, the path MODELMAN_STATE names, ~/.config) and a
    read writes nothing. The file is present in all of those places, so this
    proves it is ignored, not that it is absent."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    home_copy = Path.home() / ".config" / "local-ai" / "modelman.toml"
    home_copy.parent.mkdir(parents=True)
    home_copy.write_text(MODELMAN_TOML, encoding="utf-8")

    assert load_state() == StateStore()
    assert not latest.exists()
    assert legacy.read_text(encoding="utf-8") == MODELMAN_TOML
    assert home_copy.read_text(encoding="utf-8") == MODELMAN_TOML


def test_a_recorded_run_is_all_that_latest_toml_holds(paths):
    """The first recorded run writes its own pointer and no other: nothing
    is carried over from modelman.toml, so a stale pointer there cannot come
    back as "the latest run"."""
    latest, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")

    store = load_state()
    store.extra.setdefault("benchmarks", {})["eval_last_run"] = "/results/eval-2"
    save_state(store)

    assert tomllib.loads(latest.read_text(encoding="utf-8")) == {"eval_last_run": "/results/eval-2"}
    assert load_state().extra["benchmarks"] == {"eval_last_run": "/results/eval-2"}


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


def test_latest_toml_yields_only_string_pointers(paths):
    """latest.toml is read by hand when a run goes missing, so it gets edited
    by hand too. `show-results --latest` passes last_run_dir to Path(): a
    number there must read as "no latest run", not raise TypeError, and the
    pointers beside it must survive."""
    latest, _ = paths
    latest.parent.mkdir(parents=True)
    latest.write_text(
        'last_run_dir = 7\nagent_last_run = "/results/agent-1"\nnote = "mine"\n'
        "[eval_last_run]\nx = 1\n",
        encoding="utf-8",
    )
    assert load_state().extra["benchmarks"] == {"agent_last_run": "/results/agent-1"}


def _assert_replaced(latest, legacy):
    store = load_state()
    assert store.extra.get("benchmarks", {}) == {}
    store.extra.setdefault("benchmarks", {})["last_run_dir"] = "/results"
    save_state(store)
    assert load_state().extra["benchmarks"] == {"last_run_dir": "/results"}


def test_an_explicit_path_reads_only_that_file(paths, tmp_path):
    """A caller that names the pointer file gets that file or nothing."""
    _, legacy = paths
    legacy.write_text(MODELMAN_TOML, encoding="utf-8")
    assert load_state(tmp_path / "elsewhere.toml") == StateStore()
