"""`modelman sync` reconciles configured models against their providers
and writes modelman.toml. The sync logic itself is covered by
tests/test_sync.py; this covers the command wiring (load -> sync ->
save state -> report)."""

from unittest.mock import patch

from typer.testing import CliRunner

from modelman.main import app
from modelman.registry import AuthConfig, ProviderEntry, Registry, save_registry
from modelman.sync import SyncError, SyncResult


def _seed_registry(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id="ollama", name="Ollama", auth=AuthConfig(type="none"))]
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return registry_path, state_path


def test_sync_command_saves_state_and_reports(tmp_path, monkeypatch):
    registry_path, state_path = _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult(downloaded=["ollama/x"], not_downloaded=["ollama/y"])
        runner = CliRunner()
        result = runner.invoke(app, ["sync"])
        assert result.exit_code == 0
        assert "1 downloaded, 1 not downloaded" in result.stdout
        assert state_path.exists()  # modelman.toml written


def test_sync_command_reports_error_on_failure(tmp_path, monkeypatch):
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.side_effect = SyncError("`ollama list` failed (exit 1)")
        runner = CliRunner()
        result = runner.invoke(app, ["sync"])
        assert result.exit_code == 1
        assert "ollama list" in result.output


def test_sync_command_reports_error_when_registry_save_fails(tmp_path, monkeypatch):
    # A registry save failure (e.g. read-only directory) must surface as a
    # clean error + non-zero exit, not an unhandled traceback.
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult(providers_added=["ollama"])
        with patch("modelman.main.save_registry", side_effect=OSError("read-only")):
            runner = CliRunner()
            result = runner.invoke(app, ["sync"])
        assert result.exit_code == 1
        assert "failed to save registry" in result.output
