"""Tests for expose_model/unexpose_model orchestration."""

import pytest

from modelman.litellm import (
    ExposeError,
    LiteLLMConfigError,
    expose_model,
    save_litellm_config,
    unexpose_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


def _registry(*, cloud=False):
    providers = [
        ProviderEntry(
            id="ollama",
            name="Ollama",
            auth=AuthConfig(type="none", base_url="http://localhost:11434"),
        ),
        ProviderEntry(
            id="openrouter",
            name="OpenRouter",
            auth=AuthConfig(
                type="api_key",
                base_url="https://openrouter.ai/api/v1",
                secret_ref="sk-or-v1-abc",
            ),
        ),
    ]
    models = [
        ModelEntry(
            id="ollama/a",
            family="f",
            provider_id="ollama",
            model_name="a",
        ),
        ModelEntry(
            id="openrouter/x",
            family="f",
            provider_id="openrouter",
            model_name="x",
        ),
    ]
    return Registry(providers=providers, models=models)


def _state(*, downloaded_a=True):
    store = StateStore()
    store.set("ollama/a", ModelState(ready=downloaded_a))
    return store


def _seed_config(tmp_path):
    path = tmp_path / "config.yaml"
    save_litellm_config({"model_list": [], "general_settings": {"database_url": "x"}}, path)
    return path


def test_expose_model_writes_entry_and_flag(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is True
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(path)
    assert config["model_list"][0]["model_name"] == "ollama/a"
    assert config["general_settings"] == {"database_url": "x"}


def test_expose_model_cloud_ok_without_download(tmp_path):
    registry = _registry()
    state = _state(downloaded_a=False)
    path = _seed_config(tmp_path)
    expose_model(registry, state, "openrouter/x", path)
    assert state.get("openrouter/x").litellm_exposed is True


def test_expose_model_not_ready_raises(tmp_path):
    registry = _registry()
    state = _state(downloaded_a=False)
    path = _seed_config(tmp_path)
    with pytest.raises(ExposeError, match="not ready"):
        expose_model(registry, state, "ollama/a", path)


def test_expose_model_unknown_raises(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    with pytest.raises(ExposeError, match="not found in registry"):
        expose_model(registry, state, "ollama/nope", path)


def test_expose_model_dangling_provider_raises_expose_error(tmp_path):
    # A hand-edited registry can reference a provider it never defines.
    # This must surface as ExposeError (which the CLI catches and prints
    # as "error: ..."), not an uncaught KeyError traceback.
    registry = _registry()
    registry.models.append(ModelEntry(id="foo/x", family="f", provider_id="ghost", model_name="x"))
    state = _state()
    path = _seed_config(tmp_path)
    with pytest.raises(ExposeError, match="unknown provider 'ghost'"):
        expose_model(registry, state, "foo/x", path)


def test_expose_model_missing_config_raises(tmp_path):
    registry = _registry()
    state = _state()
    with pytest.raises(LiteLLMConfigError):
        expose_model(registry, state, "ollama/a", tmp_path / "missing.yaml")


def test_expose_model_idempotent(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    expose_model(registry, state, "ollama/a", path)
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(path)
    assert len(config["model_list"]) == 1


def test_unexpose_model_removes_entry_and_flag(tmp_path):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    unexpose_model(state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is False
    from modelman.litellm import load_litellm_config

    config = load_litellm_config(path)
    assert config["model_list"] == []


def test_unexpose_model_idempotent(tmp_path):
    state = _state()
    path = _seed_config(tmp_path)
    unexpose_model(state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is False


def test_unexpose_model_absent_from_registry_is_noop(tmp_path):
    # A model deleted earlier in the same apply cycle still has a config
    # row keyed by its id; unexposing it must remove that row without
    # failing on the missing registry entry (design doc: no-op,
    # idempotent) and must not materialize a state row for the corpse.
    state = StateStore()
    path = _seed_config(tmp_path)
    save_litellm_config(
        {"model_list": [{"model_name": "ollama/a", "litellm_params": {"model": "x"}}]},
        path,
    )
    unexpose_model(state, "ollama/a", path)
    from modelman.litellm import load_litellm_config

    assert load_litellm_config(path)["model_list"] == []
    assert "ollama/a" not in state.models


def test_expose_model_restarts_proxy(tmp_path, monkeypatch):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    expose_model(registry, state, "ollama/a", path)
    assert calls == ["restart"]


def test_unexpose_model_noop_does_not_restart_proxy(tmp_path, monkeypatch):
    # Unexposing a model with no state entry (deleted earlier in the same
    # apply) is a documented no-op: nothing changed, so the shared proxy
    # must not be bounced. A needless `launchctl kickstart -k` would
    # disrupt in-flight proxy requests for zero config change.
    state = StateStore()
    path = _seed_config(tmp_path)
    save_litellm_config(
        {"model_list": [{"model_name": "ollama/a", "litellm_params": {"model": "x"}}]},
        path,
    )
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart") or []
    )
    unexpose_model(state, "ollama/a", path)
    assert calls == []


def test_unexpose_model_restarts_proxy(tmp_path, monkeypatch):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)
    expose_model(registry, state, "ollama/a", path)
    calls = []
    monkeypatch.setattr(
        "modelman.litellm.restart_litellm_proxy", lambda: calls.append("restart")
    )
    unexpose_model(state, "ollama/a", path)
    assert calls == ["restart"]


def test_expose_model_restart_failure_nonfatal(tmp_path, monkeypatch):
    registry = _registry()
    state = _state()
    path = _seed_config(tmp_path)

    def boom(*args, **kwargs):
        raise RuntimeError("restart failed")

    monkeypatch.setattr("modelman.litellm.subprocess.run", boom)
    # Must not raise.
    expose_model(registry, state, "ollama/a", path)
    assert state.get("ollama/a").litellm_exposed is True
