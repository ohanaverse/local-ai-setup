"""`modelman expose`/`unexpose` write/remove a LiteLLM model_list entry and
flip the modelman.toml flag. The orchestration is covered by
tests/test_expose.py; this covers the command wiring (load -> validate ->
write -> save state -> report)."""

from typer.testing import CliRunner

from modelman.litellm import save_litellm_config
from modelman.main import app
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry
from modelman.state import ModelState, StateStore, save_state


def _seed(tmp_path, monkeypatch, *, downloaded=True):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "litellm" / "config.yaml"
    save_registry(
        Registry(
            providers=[
                ProviderEntry(
                    id="ollama",
                    name="Ollama",
                    auth=AuthConfig(type="none", base_url="http://localhost:11434"),
                )
            ],
            models=[
                ModelEntry(
                    id="ollama/a",
                    family="f",
                    provider_id="ollama",
                    model_name="a",
                )
            ],
        ),
        registry_path,
    )
    store = StateStore()
    store.set("ollama/a", ModelState(ready=downloaded))
    save_state(store, state_path)
    save_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))
    return litellm_path


def test_expose_command_writes_and_reports(tmp_path, monkeypatch):
    litellm_path = _seed(tmp_path, monkeypatch)
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert "Exposed ollama/a" in result.stdout
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert config["model_list"][0]["model_name"] == "ollama/a"


def test_expose_command_errors_on_not_ready(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch, downloaded=False)
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 1
    assert "not ready" in result.output


def test_unexpose_command_writes_and_reports(tmp_path, monkeypatch):
    litellm_path = _seed(tmp_path, monkeypatch)
    runner = CliRunner()
    runner.invoke(app, ["expose", "ollama/a"])
    result = runner.invoke(app, ["unexpose", "ollama/a"])
    assert result.exit_code == 0
    assert "Unexposed ollama/a" in result.stdout
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(litellm_path)
    assert config["model_list"] == []


def test_expose_command_restarts_proxy(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert calls == ["restart"]


def test_expose_command_restart_failure_still_exits_zero(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    def boom(*args, **kwargs):
        raise RuntimeError("restart failed")

    monkeypatch.setattr("modelman.litellm.subprocess.run", boom)
    runner = CliRunner()
    result = runner.invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert "Exposed ollama/a" in result.stdout
