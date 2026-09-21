"""`modelman expose`/`unexpose` delegate the LiteLLM route write to wt and
flip the modelman.toml flag. The orchestration is covered by
tests/test_expose.py; this covers the command wiring (load -> validate ->
delegate -> save state -> report)."""

import pytest
from typer.testing import CliRunner

from modelman import wt_bridge
from modelman.main import app
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry, save_registry
from modelman.state import ModelState, StateStore, load_state, save_state
from tests.conftest import write_litellm_config


def _seed(tmp_path, monkeypatch, *, downloaded=True):
    registry_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    litellm_path = tmp_path / "litellm" / "config.yaml"
    litellm_path.parent.mkdir(parents=True, exist_ok=True)
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
    write_litellm_config({"model_list": [], "general_settings": {}}, litellm_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(registry_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    monkeypatch.setenv("MODELMAN_LITELLM_CONFIG", str(litellm_path))
    return litellm_path, state_path


@pytest.fixture
def bridge_calls(monkeypatch):
    """Record the (verb, ids, litellm_path) of every route call the command
    makes, and return an applied result with no warnings."""
    recorded: list[tuple] = []

    def fake(verb, ids, litellm_path, warnings=()):
        recorded.append((verb, list(ids), litellm_path))
        action = "exposed" if verb == "expose" else "unexposed"
        return wt_bridge.BridgeResult(
            [wt_bridge.BridgeOutcome(i, action, None) for i in ids], True, list(warnings)
        )

    monkeypatch.setattr(
        wt_bridge,
        "expose",
        lambda ids, *, litellm_path=None, **kw: fake("expose", ids, litellm_path),
    )
    monkeypatch.setattr(
        wt_bridge,
        "unexpose",
        lambda ids, *, litellm_path=None, **kw: fake("unexpose", ids, litellm_path),
    )
    return recorded


def test_expose_command_delegates_and_persists_flag(tmp_path, monkeypatch, bridge_calls):
    # The command must hand the id (and the resolved config path) to wt and
    # then merge the exposed flag back into modelman.toml — without the
    # merge, `modelman expose` would report success and leave nothing
    # persisted for the TUI or wt to read.
    litellm_path, state_path = _seed(tmp_path, monkeypatch)
    result = CliRunner().invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert "Exposed ollama/a" in result.stdout
    assert bridge_calls == [("expose", ["ollama/a"], litellm_path)]
    assert load_state(path=state_path).get("ollama/a").exposed is True


def test_expose_command_errors_on_not_ready(tmp_path, monkeypatch, bridge_calls):
    # The readiness gate stays on modelman's side, so a not-ready model
    # fails the command with exit 1 and never reaches wt.
    _seed(tmp_path, monkeypatch, downloaded=False)
    result = CliRunner().invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 1
    assert "not ready" in result.output
    assert bridge_calls == []


def test_unexpose_command_delegates_and_clears_flag(tmp_path, monkeypatch, bridge_calls):
    # Mirror of the expose command: one wt call and the flag cleared on disk.
    litellm_path, state_path = _seed(tmp_path, monkeypatch)
    CliRunner().invoke(app, ["expose", "ollama/a"])
    result = CliRunner().invoke(app, ["unexpose", "ollama/a"])
    assert result.exit_code == 0
    assert "Unexposed ollama/a" in result.stdout
    assert bridge_calls[-1] == ("unexpose", ["ollama/a"], litellm_path)
    assert load_state(path=state_path).get("ollama/a").exposed is False


def test_expose_command_surfaces_wt_warnings_without_failing(tmp_path, monkeypatch):
    # wt's proxy-restart notices are non-fatal: the route IS written, so
    # the command must still exit 0 and report success while printing the
    # warning, instead of making a stale proxy look like a failed expose.
    _seed(tmp_path, monkeypatch)
    monkeypatch.setattr(
        wt_bridge,
        "expose",
        lambda ids, **kw: wt_bridge.BridgeResult(
            [wt_bridge.BridgeOutcome(i, "exposed", None) for i in ids],
            True,
            ["failed to restart LiteLLM proxy"],
        ),
    )
    result = CliRunner().invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 0
    assert "Exposed ollama/a" in result.stdout
    assert "failed to restart LiteLLM proxy" in result.output


def test_expose_command_reports_bridge_failure_as_error(tmp_path, monkeypatch):
    # wt missing (or an unreadable config.yaml) must exit 1 with a clean
    # "error: ..." line, not a traceback — the CLI catches LiteLLMConfigError.
    _seed(tmp_path, monkeypatch)

    def boom(ids, **kw):
        raise wt_bridge.WtNotFoundError("wt not found on PATH")

    monkeypatch.setattr(wt_bridge, "expose", boom)
    result = CliRunner().invoke(app, ["expose", "ollama/a"])
    assert result.exit_code == 1
    assert "error: wt not found on PATH" in result.output
