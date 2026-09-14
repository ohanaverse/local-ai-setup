"""CLI wiring tests for `modelman start`/`modelman stop` (issue #65).
Orchestration logic itself is covered by tests/test_local_control.py; these
tests only cover argument parsing, exit codes, and that the CLI persists
the marker via the real state file."""

from unittest.mock import patch

from typer.testing import CliRunner

from modelman.main import app
from modelman.state import load_state

runner = CliRunner()


def _stub_provider(local_models: list[dict]):
    """An ollama-shaped provider stub: `local_models` is both what it
    enumerates (`list_local`) and what it confirms present for a registered
    variant (`is_downloaded`/`size_of`, keyed on the variant's name — for
    ollama the provider spelling and the registry spelling are the same
    string). `resolve_local` returns None (no batch implementation) so the
    per-variant methods are the ones exercised."""
    from unittest.mock import MagicMock

    by_name = {lm["variant_id"]: lm for lm in local_models}
    stub = MagicMock()
    stub.list_local.return_value = local_models
    stub.resolve_local.return_value = None
    stub.is_downloaded.side_effect = lambda spec, *a, **k: spec.get("name") in by_name
    stub.size_of.side_effect = lambda spec, *a, **k: by_name.get(spec.get("name"), {}).get("size_bytes")
    return stub


def test_start_command_success_writes_marker(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with patch("modelman.local_control.stop_all_local_providers"), patch(
        "modelman.local_control.isolate_provider"
    ) as mock_isolate:
        from modelman.benchmark.isolation import IsolateResult

        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="x", direct_url="http://localhost:11434/v1/chat/completions",
            ok=True, error=None,
        )
        result = runner.invoke(app, ["start", "ollama/x"])
    assert result.exit_code == 0, result.stdout
    assert load_state(path=state_path).get("ollama/x").running is True


def test_start_command_unknown_model_exits_nonzero(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = runner.invoke(app, ["start", "ollama/does-not-exist"])
    assert result.exit_code == 1
    assert "unknown model" in result.output


def test_stop_command_clears_marker(tmp_path, monkeypatch):
    # `modelman stop --all` must clear the running flag `start` set - the
    # counterpart to test_start_command_success_writes_marker's marker
    # write. (Bare `stop` used to do this implicitly; that behavior moved
    # to the explicit --all flag - see test_stop_command_bare_errors.)
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[model_state."ollama/x"]\nready = true\nrunning = true\n')
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = runner.invoke(app, ["stop", "--all"])
    assert result.exit_code == 0, result.stdout
    mock_stop.assert_called_once()
    assert load_state(path=state_path).get("ollama/x").running is False


def test_stop_command_noop_message_when_nothing_running(tmp_path, monkeypatch):
    # `modelman stop --all` against an empty state must report a clean
    # no-op rather than erroring - "nothing is running" is a normal state,
    # not a failure.
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    result = runner.invoke(app, ["stop", "--all"])
    assert result.exit_code == 0
    assert "No local model is running" in result.stdout


def test_start_command_no_args_lists_registered_downloaded_models(tmp_path, monkeypatch):
    # `modelman start` with no model_id must not require one - it should
    # list registered-and-downloaded models instead of failing argument
    # parsing.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[model_state."ollama/x"]\nready = true\nexposed = true\n')
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", return_value=object),
        patch(
            "modelman.local_control.ProviderRegistry.get",
            return_value=_stub_provider([{"variant_id": "x", "path": "ollama:x", "size_bytes": None}]),
        ),
        patch("modelman.local_control._probe_running", return_value=False),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/x" in result.stdout
    assert "Run `modelman start <model_id>` to start one." in result.stdout


def test_start_command_no_args_indicates_running_model(tmp_path, monkeypatch):
    # The no-args listing must distinguish "downloaded" from "currently
    # running" - a user picking a model to start needs to see which one is
    # already up (marked "(running)") rather than treating every downloaded
    # model as idle.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nexposed = true\nrunning = true\n'
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", return_value=object),
        patch(
            "modelman.local_control.ProviderRegistry.get",
            return_value=_stub_provider([{"variant_id": "x", "path": "ollama:x", "size_bytes": None}]),
        ),
        patch("modelman.local_control._probe_running", return_value=True),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/x" in result.stdout and "(running)" in result.stdout


def test_start_command_no_args_nothing_found(tmp_path, monkeypatch):
    # With an empty registry there is nothing to list - the command must
    # still exit cleanly with a clear "nothing found" message instead of
    # crashing or printing a blank/misleading list.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = runner.invoke(app, ["start"])
    assert result.exit_code == 0
    assert "No local models found." in result.stdout


def test_start_command_no_args_shows_three_sections(tmp_path, monkeypatch):
    # `modelman start` with no model_id must print all three inventory
    # buckets (registered+downloaded, registered-not-downloaded, and
    # discovered-but-unregistered) with human-readable sizes, so a user can
    # see everything modelman knows about without a separate `sync`.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n\n'
        '[[models]]\nid = "ollama/y"\nfamily = "y"\nprovider_id = "ollama"\nmodel_name = "y"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nexposed = true\n'
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        return _stub_provider(
            [
                {"variant_id": "x", "path": "ollama:x", "size_bytes": 4_900_000_000},
                {"variant_id": "z", "path": "ollama:z", "size_bytes": 1_073_741_824},
            ]
        )

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control._probe_running", return_value=False),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "Registered, on disk:" in result.stdout
    assert "ollama/x" in result.stdout and "4.6 GB" in result.stdout
    assert "Registered, not downloaded:" in result.stdout
    assert "ollama/y" in result.stdout
    assert "Discovered" in result.stdout
    # 1_073_741_824 B = 1 GiB exactly, chosen so format_size's repeated
    # /1024 division lands on a clean "1.0 GB" instead of a rounding-prone
    # value (e.g. 1_000_000_000 B formats as "953.7 MB", not "1000.0 MB").
    assert "ollama:z" in result.stdout and "1.0 GB" in result.stdout


def test_start_command_discovers_and_prompts_for_family(tmp_path, monkeypatch):
    # `modelman start <name>` against an on-disk-but-unregistered artifact
    # must prompt once for a family, then register+expose+start it - this is
    # the "discovered model" onboarding path the family prompt exists for.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
    )
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    from modelman.benchmark.isolation import IsolateResult

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        return _stub_provider(
            [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}]
        )

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions", ok=True, error=None,
        )
        result = runner.invoke(app, ["start", "llama3.2:3b"], input="general\n")

    assert result.exit_code == 0, result.stdout
    assert "isn't registered yet" in result.stdout
    assert "Started llama3.2:3b" in result.stdout or "Started ollama/llama3.2:3b" in result.stdout
    assert load_state(path=state_path).get("ollama/llama3.2:3b").running is True


def test_start_command_empty_family_reprompts(tmp_path, monkeypatch):
    # An empty family answer must not be accepted silently - the prompt has
    # to re-ask until it gets a non-empty name, otherwise a blind Enter would
    # register the model under an empty family.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
    )
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))

    from modelman.benchmark.isolation import IsolateResult

    def get_class(name):
        return object if name == "ollama" else None

    def get(name, config):
        return _stub_provider(
            [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": None}]
        )

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", side_effect=get_class),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions", ok=True, error=None,
        )
        result = runner.invoke(app, ["start", "llama3.2:3b"], input="\ngeneral\n")

    assert result.exit_code == 0, result.stdout
    assert "cannot be empty" in result.output
    assert load_state(path=state_path).get("ollama/llama3.2:3b").running is True


def test_start_command_warns_but_proceeds_when_others_running(tmp_path, monkeypatch):
    # `modelman start` must not block on other local models already running
    # (cross-provider concurrency is intentionally unrestricted per Task 3) -
    # it should start the requested model and print an advisory warning
    # naming what else is up, rather than refusing or silently ignoring it.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n\n'
        '[[models]]\nid = "ollama/y"\nfamily = "y"\nprovider_id = "ollama"\nmodel_name = "y"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[model_state."ollama/y"]\nready = true\nrunning = true\n')
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    result = runner.invoke(app, ["start", "ollama/x"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/y" in result.output  # warning names the other running model
    assert load_state(path=state_path).get("ollama/x").running is True


def test_stop_command_with_id_stops_one(tmp_path, monkeypatch):
    # `modelman stop <id>` must stop only the named model, leaving any other
    # running local model (and its flag) untouched - this is the targeted
    # form that replaces the old bare-stops-everything behavior.
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nrunning = true\n\n'
        '[model_state."ollama/y"]\nready = true\nrunning = true\n'
    )
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    with patch("modelman.local_control._stop_ollama_model") as mock_stop:
        result = runner.invoke(app, ["stop", "ollama/x"])
    assert result.exit_code == 0, result.stdout
    mock_stop.assert_called_once_with("x")
    state = load_state(path=state_path)
    assert state.get("ollama/x").running is False
    assert state.get("ollama/y").running is True


def test_stop_command_all_stops_everything(tmp_path, monkeypatch):
    # `modelman stop --all` must stop every running local model and clear
    # every flag - the explicit replacement for what bare `stop` used to do
    # implicitly.
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[model_state."ollama/x"]\nready = true\nrunning = true\n\n'
        '[model_state."ollama/y"]\nready = true\nrunning = true\n'
    )
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    with patch("modelman.local_control.stop_all_local_providers"):
        result = runner.invoke(app, ["stop", "--all"])
    assert result.exit_code == 0, result.stdout
    state = load_state(path=state_path)
    assert state.get("ollama/x").running is False
    assert state.get("ollama/y").running is False


def test_stop_command_bare_errors(tmp_path, monkeypatch):
    # Bare `modelman stop` (no id, no --all) is now a usage error rather than
    # a silent stop-everything - this guards against accidentally taking down
    # every local model when the caller meant to stop just one.
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    result = runner.invoke(app, ["stop"])
    assert result.exit_code == 1
    assert "model id" in result.output.lower() or "--all" in result.output


def test_start_command_no_args_warns_when_a_provider_cannot_be_queried(tmp_path, monkeypatch):
    # A provider that can't answer (stopped ollama daemon, or a registry.toml
    # provider id modelman has no Provider class for) must not be reported as
    # "not downloaded" with no further comment — that would send the user off
    # to re-download models they already have. The ambiguity gets its own line.
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from unittest.mock import MagicMock

    def get(name, config):
        stub = MagicMock()
        stub.list_local.side_effect = RuntimeError("daemon unreachable")
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = RuntimeError("daemon unreachable")
        return stub

    with (
        patch("modelman.local_control.ProviderRegistry.get_class", return_value=object),
        patch("modelman.local_control.ProviderRegistry.get", side_effect=get),
    ):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.output
    assert "Registered, not downloaded:" in result.stdout
    assert "ollama: could not be queried" in result.output
