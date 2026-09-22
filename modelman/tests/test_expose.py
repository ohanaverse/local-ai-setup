"""Tests for expose_model/unexpose_model — modelman's gates plus the
delegation to `wt litellm expose|unexpose` (wt owns the config.yaml write
and the proxy restart since 2026-09-21)."""

import pytest

from modelman import wt_bridge
from modelman.litellm import (
    ExposeError,
    LiteLLMConfigError,
    apply_expose_queue,
    expose_model,
    unexpose_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import ModelState, StateStore


def _registry():
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
        ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
        ModelEntry(id="openrouter/x", family="f", provider_id="openrouter", model_name="x"),
    ]
    return Registry(providers=providers, models=models)


def _state(*, downloaded_a=True):
    store = StateStore()
    store.set("ollama/a", ModelState(ready=downloaded_a))
    return store


def _result(ids, action, *, error=None, warnings=()):
    return wt_bridge.BridgeResult(
        [wt_bridge.BridgeOutcome(i, None if error else action, error) for i in ids],
        True,
        list(warnings),
    )


@pytest.fixture
def calls(monkeypatch):
    """Record every wt_bridge.expose/unexpose call as (verb, ids, kwargs).

    `calls.result` (set by a test) overrides the all-succeeded default.
    """
    recorded: list[tuple] = []

    class _Recorder(list):
        result = None
        raises = None

    recorded = _Recorder()

    def fake(verb, ids, kwargs):
        recorded.append((verb, list(ids), kwargs))
        if recorded.raises is not None:
            raise recorded.raises
        if recorded.result is not None:
            return recorded.result
        return _result(ids, "exposed" if verb == "expose" else "unexposed")

    monkeypatch.setattr(wt_bridge, "expose", lambda ids, **kw: fake("expose", ids, kw))
    monkeypatch.setattr(wt_bridge, "unexpose", lambda ids, **kw: fake("unexpose", ids, kw))
    return recorded


def test_expose_model_delegates_and_flips_flag_after_success(tmp_path, calls):
    # Pins ordering: the exposed flag flips only after wt reports the route
    # was written, so state never claims an exposure config.yaml lost. Also
    # pins that modelman keeps its own ready gate (wt is told
    # skip_ready_gate) and that the caller's litellm_path is forwarded.
    registry, state = _registry(), _state()
    path = tmp_path / "config.yaml"
    calls.result = _result(["ollama/a"], "exposed", warnings=["restart warning"])

    warnings = expose_model(registry, state, "ollama/a", path)

    assert calls == [("expose", ["ollama/a"], {"litellm_path": path, "skip_ready_gate": True})]
    assert state.get("ollama/a").exposed is True
    assert warnings == ["restart warning"]


def test_expose_model_leaves_flag_when_wt_rejects_the_id(tmp_path, calls):
    # wt can reject an id it (unlike modelman) sees differently — a stale
    # registry on disk, an empty model_name. The flag must stay false and
    # the reason must surface as ExposeError, exactly as a local gate
    # failure does.
    registry, state = _registry(), _state()
    calls.result = _result(["ollama/a"], "exposed", error="provider has no LiteLLM mapping")

    with pytest.raises(ExposeError, match="no LiteLLM mapping"):
        expose_model(registry, state, "ollama/a", tmp_path / "config.yaml")
    assert state.get("ollama/a").exposed is False


def test_expose_model_bridge_failure_is_config_error_with_flag_untouched(tmp_path, calls):
    # wt missing / unreadable config.yaml / unparseable output: nothing was
    # applied on either side, so this surfaces as LiteLLMConfigError (the
    # type every caller already catches) and the flag never flips.
    registry, state = _registry(), _state()
    calls.raises = wt_bridge.WtNotFoundError("wt not found on PATH")

    with pytest.raises(LiteLLMConfigError, match="wt not found"):
        expose_model(registry, state, "ollama/a", tmp_path / "config.yaml")
    assert state.get("ollama/a").exposed is False


def test_expose_model_cloud_ok_without_download(tmp_path, calls):
    # Cloud models have nothing to download, so the ready gate must not
    # block them — the exemption modelman still owns (wt is told to skip
    # its own ready gate).
    registry, state = _registry(), _state(downloaded_a=False)
    expose_model(registry, state, "openrouter/x", tmp_path / "config.yaml")
    assert state.get("openrouter/x").exposed is True
    assert calls[0][1] == ["openrouter/x"]


def test_expose_model_not_ready_raises_before_reaching_wt(tmp_path, calls):
    # The ready gate runs against modelman's IN-MEMORY state, which wt
    # cannot see (queued toggles, unsaved flags) — so it must reject here,
    # without spawning wt.
    registry, state = _registry(), _state(downloaded_a=False)
    with pytest.raises(ExposeError, match="not ready"):
        expose_model(registry, state, "ollama/a", tmp_path / "config.yaml")
    assert calls == []


def test_expose_model_unknown_raises(tmp_path, calls):
    # An id absent from the registry is modelman's own error message, not a
    # round trip through wt.
    registry, state = _registry(), _state()
    with pytest.raises(ExposeError, match="not found in registry"):
        expose_model(registry, state, "ollama/nope", tmp_path / "config.yaml")
    assert calls == []


def test_expose_model_dangling_provider_raises_expose_error(tmp_path, calls):
    # A hand-edited registry can reference a provider it never defines.
    # This must surface as ExposeError (which the CLI catches and prints
    # as "error: ..."), not an uncaught KeyError traceback.
    registry, state = _registry(), _state()
    registry.models.append(ModelEntry(id="foo/x", family="f", provider_id="ghost", model_name="x"))
    with pytest.raises(ExposeError, match="unknown provider 'ghost'"):
        expose_model(registry, state, "foo/x", tmp_path / "config.yaml")
    assert calls == []


def test_expose_model_unmapped_provider_raises(tmp_path, calls):
    # A provider absent from wt's LiteLLM table (a new provider nobody has
    # mapped yet) cannot produce a route; reject it before the bridge call.
    registry, state = _registry(), _state()
    registry.providers.append(
        ProviderEntry(id="newthing", name="New", auth=AuthConfig(type="none"))
    )
    registry.models.append(
        ModelEntry(id="newthing/x", family="f", provider_id="newthing", model_name="x")
    )
    state.set("newthing/x", ModelState(ready=True))
    with pytest.raises(ExposeError, match="no LiteLLM mapping"):
        expose_model(registry, state, "newthing/x", tmp_path / "config.yaml")
    assert calls == []


def test_unexpose_model_delegates_and_clears_flag(tmp_path, calls):
    # The mirror of expose: one wt call, then the flag clears. Nothing is
    # validated against the registry — removing a route never needed it.
    state = _state()
    state.set("ollama/a", ModelState(ready=True, exposed=True))
    path = tmp_path / "config.yaml"

    warnings = unexpose_model(state, "ollama/a", path)

    assert calls == [("unexpose", ["ollama/a"], {"litellm_path": path})]
    assert state.get("ollama/a").exposed is False
    assert warnings == []


def test_unexpose_model_absent_from_registry_is_fine(tmp_path, calls):
    # A model deleted earlier in the same apply cycle still has a config
    # row keyed by its id; unexposing it must remove that row without
    # failing on the missing registry entry, and must not materialize a
    # state row for the corpse.
    state = StateStore()
    unexpose_model(state, "ollama/a", tmp_path / "config.yaml")
    assert calls[0][1] == ["ollama/a"]
    assert "ollama/a" not in state.models


def test_apply_expose_queue_bridge_failure_in_expose_batch_is_per_id(tmp_path, calls, monkeypatch):
    # A bridge-level failure in the EXPOSE batch must not raise and wipe
    # already-computed errors from other ids in the same queue — it becomes
    # a per-id error for the expose batch's own ids only, mirroring the fix
    # already applied to the unexpose batch (a76b9d5).
    providers = [
        ProviderEntry(
            id="ollama",
            name="Ollama",
            auth=AuthConfig(type="none", base_url="http://localhost:11434"),
        ),
    ]
    models = [
        ModelEntry(id="bad", family="f", provider_id="ollama", model_name="bad"),
        ModelEntry(id="good", family="f", provider_id="ollama", model_name="good"),
    ]
    registry = Registry(providers=providers, models=models)
    state = StateStore()
    state.set("bad", ModelState(ready=False))
    state.set("good", ModelState(ready=True))

    def broken_expose(ids, *, litellm_path=None, skip_ready_gate=True):
        raise wt_bridge.WtBridgeError("wt not found")

    monkeypatch.setattr(wt_bridge, "expose", broken_expose)

    outcomes, warnings = apply_expose_queue(
        registry, state, [("bad", True), ("good", True)], tmp_path / "config.yaml"
    )
    by_id = {model_id: error for model_id, _target, error in outcomes}
    assert "not ready" in by_id["bad"]
    assert "wt not found" in by_id["good"]


def test_apply_expose_queue_validation_bridge_error_does_not_abort_the_loop(
    tmp_path, calls, monkeypatch
):
    # _validate_locally can itself raise LiteLLMConfigError (via
    # _provider_flags_for_write, when wt's provider table can't be read at
    # all) — that must become a per-id error too, not abort validation for
    # every remaining id in the queue.
    monkeypatch.setattr(wt_bridge, "provider_cloud_flags", lambda: {})  # wt unreachable

    providers = [
        ProviderEntry(
            id="ollama",
            name="Ollama",
            auth=AuthConfig(type="none", base_url="http://localhost:11434"),
        ),
    ]
    models = [
        ModelEntry(id="a", family="f", provider_id="ollama", model_name="a"),
        ModelEntry(id="b", family="f", provider_id="ollama", model_name="b"),
    ]
    registry = Registry(providers=providers, models=models)
    state = StateStore()
    state.set("a", ModelState(ready=True))
    state.set("b", ModelState(ready=True))

    outcomes, warnings = apply_expose_queue(
        registry, state, [("a", True), ("b", True)], tmp_path / "config.yaml"
    )
    by_id = {model_id: error for model_id, _target, error in outcomes}
    assert "cannot read wt's LiteLLM provider table" in by_id["a"]
    assert "cannot read wt's LiteLLM provider table" in by_id["b"]


def test_unexpose_model_bridge_failure_keeps_flag(tmp_path, calls):
    # A failed un-expose leaves the route in place, so the flag must stay
    # true — local_control turns the LiteLLMConfigError into a warning and
    # still stops the model.
    state = _state()
    state.set("ollama/a", ModelState(ready=True, exposed=True))
    calls.raises = wt_bridge.WtBridgeError("LiteLLM config not found")

    with pytest.raises(LiteLLMConfigError, match="config not found"):
        unexpose_model(state, "ollama/a", tmp_path / "config.yaml")
    assert state.get("ollama/a").exposed is True
