"""CLI wiring tests for `modelman start`/`modelman stop` (issue #65).
Orchestration logic itself is covered by tests/test_local_control.py; these
tests only cover argument parsing, exit codes, and that the CLI persists
the marker via the real state file."""

from unittest.mock import patch

from typer.testing import CliRunner

from modelman.main import app
from modelman.state import load_state

runner = CliRunner()


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
    assert load_state(path=state_path).local.running_model == "ollama/x"


def test_start_command_unknown_model_exits_nonzero(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = runner.invoke(app, ["start", "ollama/does-not-exist"])
    assert result.exit_code == 1
    assert "unknown model" in result.output


def test_stop_command_clears_marker(tmp_path, monkeypatch):
    state_path = tmp_path / "modelman.toml"
    state_path.write_text('[local]\nrunning_model = "ollama/x"\n')
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = runner.invoke(app, ["stop"])
    assert result.exit_code == 0, result.stdout
    mock_stop.assert_called_once()
    assert load_state(path=state_path).local.running_model is None


def test_stop_command_noop_message_when_nothing_running(tmp_path, monkeypatch):
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))
    result = runner.invoke(app, ["stop"])
    assert result.exit_code == 0
    assert "No local model is running" in result.stdout


def test_start_command_no_args_lists_exposed_local_models(tmp_path, monkeypatch):
    # `modelman start` with no model_id must not require one - it should
    # list local models with expose on instead of failing argument parsing.
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

    with patch("modelman.local_control._probe_running", return_value=False):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/x" in result.stdout
    assert "Run `modelman start <model_id>` to start one." in result.stdout


def test_start_command_no_args_indicates_running_model(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text(
        '[[providers]]\nid = "ollama"\nname = "Ollama"\nlocation = "local"\n'
        'auth = { type = "none" }\n\n'
        '[[models]]\nid = "ollama/x"\nfamily = "x"\nprovider_id = "ollama"\nmodel_name = "x"\n'
    )
    state_path = tmp_path / "modelman.toml"
    state_path.write_text(
        '[local]\nrunning_model = "ollama/x"\n\n'
        '[model_state."ollama/x"]\nready = true\nexposed = true\n'
    )
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    with patch("modelman.local_control._probe_running", return_value=True):
        result = runner.invoke(app, ["start"])
    assert result.exit_code == 0, result.stdout
    assert "ollama/x (running)" in result.stdout


def test_start_command_no_args_no_exposed_models(tmp_path, monkeypatch):
    registry_path = tmp_path / "registry.toml"
    registry_path.write_text("")
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    result = runner.invoke(app, ["start"])
    assert result.exit_code == 0
    assert "No local models are exposed." in result.stdout
