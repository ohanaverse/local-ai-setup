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
    and every model's ready state on that second run; this guards
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

    # Simulate the user configuring litellm and readying a model after the
    # first migrate — this is the state a repair re-run must not clobber.
    with locked_state(state_path) as state:
        state.extra["litellm"] = {
            "enabled": True,
            "url": "http://localhost:4000",
            "api_key": "sk-real-key",
        }
        state.set("ollama/x", ModelState(ready=True))

    assert runner.invoke(app, ["migrate"]).exit_code == 0

    state = load_state(state_path)
    assert state.extra["litellm"]["url"] == "http://localhost:4000"
    assert state.extra["litellm"]["api_key"] == "sk-real-key"
    assert state.get("ollama/x").ready is True


def test_migrate_command_syncs_routes_once_after_writing(tmp_path, monkeypatch, wt_calls):
    # migrate rewrites registry.toml wholesale (it is the documented repair
    # step), and wt routes every registry cloud model, so it must run one
    # `wt litellm sync` after the save — otherwise repaired models stay
    # unrouted until some unrelated command syncs (#179).
    from modelman import wt_bridge

    config_path = tmp_path / "config.yaml"
    config_path.write_text("providers:\n  ollama:\n    type: ollama\n")
    family_dir = tmp_path / "families"
    family_dir.mkdir()
    registry_path = tmp_path / "registry.toml"
    monkeypatch.setenv("MODELMAN_CONFIG", str(config_path))
    monkeypatch.setenv("MODELMAN_FAMILY_DIR", str(family_dir))
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    monkeypatch.setenv("MODELMAN_WT_CONFIG", str(tmp_path / "no-wt-config.toml"))

    fake = wt_bridge._run
    registry_on_disk_at_sync: list[bool] = []

    def recording(args, env=None, timeout=120):
        registry_on_disk_at_sync.append(registry_path.exists())
        return fake(args, env=env, timeout=timeout)

    monkeypatch.setattr(wt_bridge, "_run", recording)

    assert CliRunner().invoke(app, ["migrate"]).exit_code == 0
    assert wt_calls == [["sync", "--json"]]
    assert registry_on_disk_at_sync == [True]
