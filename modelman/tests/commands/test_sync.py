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


def test_sync_command_saves_registry_even_without_providers_added(tmp_path, monkeypatch):
    # backfill_provider_defaults mutates existing provider entries in memory
    # regardless of whether any provider was added; gating the registry save
    # on providers_added alone silently dropped that repair on the floor.
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult()  # no providers_added
        with patch("modelman.main.save_registry") as save_registry_mock:
            runner = CliRunner()
            result = runner.invoke(app, ["sync"])
            assert result.exit_code == 0
            save_registry_mock.assert_called_once()


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


def test_sync_command_syncs_routes_once_after_saving_registry(tmp_path, monkeypatch, wt_calls):
    # `modelman sync` saves registry.toml after backfill_provider_defaults
    # (which can fill an auth.base_url or add a provider entry), so it must
    # run one `wt litellm sync` after that save, or a repaired provider's
    # models stay unrouted or stale until an unrelated command syncs (#179).
    from modelman import main, wt_bridge

    _seed_registry(tmp_path, monkeypatch)
    order: list[str] = []
    real_save = main.save_registry
    fake = wt_bridge._run

    def saving(*args, **kwargs):
        order.append("save")
        return real_save(*args, **kwargs)

    def recording(args, env=None, timeout=120):
        order.append("sync")
        return fake(args, env=env, timeout=timeout)

    monkeypatch.setattr(main, "save_registry", saving)
    monkeypatch.setattr(wt_bridge, "_run", recording)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult()
        assert CliRunner().invoke(app, ["sync"]).exit_code == 0
    assert wt_calls == [["sync", "--json"]]
    assert order == ["save", "sync"]


def test_sync_command_does_not_sync_routes_when_nothing_was_written(
    tmp_path, monkeypatch, wt_calls
):
    # A provider scan failure exits before any write, so there is nothing new
    # to route; a failed registry save likewise leaves registry.toml as it was.
    _seed_registry(tmp_path, monkeypatch)
    runner = CliRunner()
    with patch("modelman.main.run_sync", side_effect=SyncError("boom")):
        assert runner.invoke(app, ["sync"]).exit_code == 1
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult()
        with patch("modelman.main.save_registry", side_effect=OSError("read-only")):
            assert runner.invoke(app, ["sync"]).exit_code == 1
    assert wt_calls == []
