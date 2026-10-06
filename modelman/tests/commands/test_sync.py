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


def test_sync_command_reports_error_when_state_save_fails(tmp_path, monkeypatch):
    # The state merge is written before the registry repair; a failure to save
    # modelman.toml (e.g. read-only directory) must surface as a clean error +
    # non-zero exit, not an unhandled traceback — the same shape as the
    # registry-save path right below it.
    _seed_registry(tmp_path, monkeypatch)
    with patch("modelman.main.run_sync") as run_sync:
        run_sync.return_value = SyncResult()
        with patch("modelman.state.save_state", side_effect=OSError("read-only")):
            runner = CliRunner()
            result = runner.invoke(app, ["sync"])
        assert result.exit_code == 1
        assert "failed to save state" in result.output


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


def test_sync_command_keeps_a_running_models_flag(tmp_path, monkeypatch):
    """#231, through the command and the state file: start a model, run
    `modelman sync`, and the model must still be recorded as running —
    otherwise `modelman stop <id>` answers "is not running" for a model that
    is serving. The real run_sync runs; only `ollama list` is supplied."""
    from modelman.registry import ModelEntry
    from modelman.state import ModelState, StateStore, load_state, save_state

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")
                )
            ],
            models=[
                ModelEntry(
                    id="ollama/a:1b",
                    family="f",
                    provider_id="ollama",
                    model_name="a:1b",
                    location="local",
                )
            ],
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    store = StateStore()
    store.set("ollama/a:1b", ModelState(running=True))
    save_state(store, state_path)

    with patch("modelman.sync.list_ollama", return_value={"a:1b": 7}):
        result = CliRunner().invoke(app, ["sync"])

    assert result.exit_code == 0, result.output
    after = load_state(state_path).get("ollama/a:1b")
    assert after.ready is True and after.size_bytes == 7
    assert after.running is True


def test_sync_command_clears_the_running_flag_of_an_ollama_model_that_is_gone(
    tmp_path, monkeypatch
):
    """#233, through the command and the state file: a started ollama model
    removed with `ollama rm` kept `running = true` for good. Sync ends by
    re-probing every flagged model (#242), and ollama's probe asks its
    daemon whether the model is still pulled."""
    from modelman.registry import ModelEntry
    from modelman.state import ModelState, StateStore, load_state, save_state

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")
                )
            ],
            models=[
                ModelEntry(
                    id="ollama/a:1b",
                    family="f",
                    provider_id="ollama",
                    model_name="a:1b",
                    location="local",
                )
            ],
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    store = StateStore()
    store.set("ollama/a:1b", ModelState(ready=True, running=True))
    save_state(store, state_path)

    with (
        patch("modelman.sync.list_ollama", return_value={}),
        patch("modelman.local_control._http_json", return_value={"models": []}),
    ):
        result = CliRunner().invoke(app, ["sync"])

    assert result.exit_code == 0, result.output
    after = load_state(state_path).get("ollama/a:1b")
    assert after.ready is False
    assert after.running is False
    assert "No longer running, marked stopped: ollama/a:1b" in result.output


def test_sync_command_keeps_the_flag_of_a_model_on_a_remote_ollama(tmp_path, monkeypatch):
    """#242: with the ollama row's base_url on another host, the local
    `ollama list` describes a different server. `modelman start` checked the
    remote one and flagged the model; sync then read the local CLI, saw
    nothing, and cleared the flag. Running is asked where the model is."""
    from modelman.registry import ModelEntry
    from modelman.state import ModelState, StateStore, load_state, save_state

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama",
                    name="Ollama",
                    location="local",
                    auth=AuthConfig(type="none", base_url="http://gpu-box:11434"),
                )
            ],
            models=[
                ModelEntry(
                    id="ollama/a:1b",
                    family="f",
                    provider_id="ollama",
                    model_name="a:1b",
                    location="local",
                )
            ],
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    store = StateStore()
    store.set("ollama/a:1b", ModelState(running=True))
    save_state(store, state_path)

    def remote_tags(url, timeout=2.0):
        assert url == "http://gpu-box:11434/api/tags", url
        return {"models": [{"name": "a:1b"}]}

    with (
        patch("modelman.sync.list_ollama", return_value={}),
        patch("modelman.local_control._http_json", remote_tags),
    ):
        result = CliRunner().invoke(app, ["sync"])

    assert result.exit_code == 0, result.output
    assert load_state(state_path).get("ollama/a:1b").running is True
    assert "marked stopped" not in result.output


def test_sync_command_reports_only_the_flags_it_cleared(tmp_path, monkeypatch):
    """A flagged id with nothing to probe for — no registry entry and no
    provider row of its prefix — is neither verified nor cleared by
    running_model_ids. Reported as "flagged and not returned", it was
    announced as marked stopped on every sync while its flag stayed set."""
    from modelman.state import ModelState, StateStore, load_state, save_state

    _registry_path, state_path = _seed_registry(tmp_path, monkeypatch)
    store = StateStore()
    store.set("retired/x", ModelState(running=True))
    store.set("ollama/gone:1b", ModelState(running=True))
    save_state(store, state_path)

    with (
        patch("modelman.sync.list_ollama", return_value={}),
        patch("modelman.local_control._http_json", return_value={"models": []}),
    ):
        result = CliRunner().invoke(app, ["sync"])

    assert result.exit_code == 0, result.output
    after = load_state(state_path)
    assert after.get("retired/x").running is True
    assert after.get("ollama/gone:1b").running is False
    assert "No longer running, marked stopped: ollama/gone:1b\n" in result.output


def test_sync_command_does_not_undo_a_start_or_stop_made_while_it_ran(tmp_path, monkeypatch):
    """#231's neighbour. sync loads modelman.toml, scans the providers (slow),
    then writes back — and it wrote back every row of its stale snapshot. A
    model stopped while sync ran was recorded running again; one started
    meanwhile was recorded stopped; a discovered model's row, deleted by its
    stop, came back. Sync writes only what it observed (ready, disk_path,
    size_bytes) and only for the models it reconciled, onto the state as it is
    on disk at that moment."""
    from modelman.registry import ModelEntry
    from modelman.state import ModelState, StateStore, load_state, locked_state, save_state
    from modelman.sync import sync as real_sync

    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"

    def local(model_id: str) -> ModelEntry:
        name = model_id.split("/")[1]
        return ModelEntry(
            id=model_id, family="f", provider_id="ollama", model_name=name, location="local"
        )

    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")
                )
            ],
            models=[local("ollama/stopped-meanwhile"), local("ollama/started-meanwhile")],
        ),
        registry_path,
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    store = StateStore()
    store.set("ollama/stopped-meanwhile", ModelState(running=True))
    store.set("ollama/started-meanwhile", ModelState())
    store.set("ollama/discovered", ModelState(running=True))  # no registry entry
    save_state(store, state_path)

    def sync_while_the_user_starts_and_stops(registry, state, runner=None):
        result = real_sync(registry, state, runner)
        # ...and before sync writes back, another modelman process:
        with locked_state(state_path) as fresh:
            fresh.models["ollama/stopped-meanwhile"] = ModelState(running=False)
            fresh.models["ollama/started-meanwhile"] = ModelState(running=True)
            del fresh.models["ollama/discovered"]  # its stop drops the row
        return result

    with (
        patch(
            "modelman.sync.list_ollama",
            return_value={"stopped-meanwhile": 1, "started-meanwhile": 2},
        ),
        patch("modelman.main.run_sync", side_effect=sync_while_the_user_starts_and_stops),
    ):
        result = CliRunner().invoke(app, ["sync"])

    assert result.exit_code == 0, result.output
    after = load_state(state_path)
    assert after.get("ollama/stopped-meanwhile") == ModelState(
        ready=True, disk_path="ollama:stopped-meanwhile", size_bytes=1, running=False
    )
    assert after.get("ollama/started-meanwhile") == ModelState(
        ready=True, disk_path="ollama:started-meanwhile", size_bytes=2, running=True
    )
    assert "ollama/discovered" not in after.models
