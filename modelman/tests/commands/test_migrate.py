"""`modelman migrate` is the one-time CLI entry point for importing legacy
config.yaml + families/*.yaml (and, optionally, wt's
config.toml) into the new registry.toml + modelman.toml. This covers the
command actually writing both output files and reporting what it
imported — the underlying merge logic is covered by tests/test_migrate.py."""

from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import load_registry
from modelman.state import ModelState, load_state, locked_state


def test_migrate_command_writes_registry_and_reports_counts(tmp_path, monkeypatch):
    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    runner = CliRunner()
    result = runner.invoke(app, ["migrate"])

    assert result.exit_code == 0
    assert "1 providers" in result.stdout
    assert "wt config not found" in result.stdout
    assert registry_path.exists()
    assert load_registry(registry_path).provider("ollama").id == "ollama"


def test_migrate_command_preserves_existing_state_on_rerun(tmp_path, monkeypatch):
    """`modelman migrate` is a documented repair step (see wt/CLAUDE.md's
    "unknown provider" note) that users re-run on an already-migrated
    machine. A naive whole-file overwrite of modelman.toml from migrate's
    fresh, mostly-empty StateStore would silently wipe the [litellm] table
    and every model's ready/exposed state on that second run; this guards
    against that regression by asserting they survive a re-run."""
    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    runner = CliRunner()
    assert runner.invoke(app, ["migrate"]).exit_code == 0

    # Simulate the user configuring litellm and exposing a model after the
    # first migrate — this is the state a repair re-run must not clobber.
    with locked_state(state_path) as state:
        state.litellm.enabled = True
        state.litellm.url = "http://localhost:4000"
        state.litellm.api_key = "sk-real-key"
        state.set("ollama/x", ModelState(ready=True, exposed=True))

    assert runner.invoke(app, ["migrate"]).exit_code == 0

    state = load_state(state_path)
    assert state.litellm.url == "http://localhost:4000"
    assert state.litellm.api_key == "sk-real-key"
    assert state.get("ollama/x").ready is True
    assert state.get("ollama/x").exposed is True
