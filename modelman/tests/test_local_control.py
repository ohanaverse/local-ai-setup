"""Unit tests for modelman.local_control — the start/stop orchestration
behind `modelman start`/`modelman stop` (issue #65). Isolation subprocess
calls are mocked; these tests cover validation, probe-based idempotency,
stale-marker recovery, and marker mutation — not bin/llm-isolate-provider
itself (see tests/benchmark/test_isolation.py for that)."""

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from modelman import local_control, wt_bridge
from modelman.benchmark.isolation import IsolateResult
from modelman.local_control import (
    DiscoveredModel,
    InventoryEntry,
    LocalControlError,
    _discovered_entry,
    _name_matches,
    _ollama_name_matches,
    _probe_running,
    discover_unregistered_models,
    inventory_local_models,
    running_model_ids,
    same_provider_occupant,
    start_local_model,
    stop_all_local_models,
    stop_local_model,
)
from modelman.registry import (
    AuthConfig,
    Fetch,
    ModelEntry,
    ProviderEntry,
    Registry,
    load_registry,
    save_registry,
)
from modelman.state import ModelState, StateStore, load_state, locked_state, save_state


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")
            ),
            ProviderEntry(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="openrouter",
                name="OpenRouter",
                location="cloud",
                auth=AuthConfig(type="api_key"),
            ),
            ProviderEntry(
                id="llamacpp", name="llama.cpp", location="local", auth=AuthConfig(type="none")
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/qwen3.8:27b-mlx",
                family="qwen3.8",
                provider_id="ollama",
                model_name="qwen3.8:27b-mlx",
            ),
            ModelEntry(
                id="omlx/model-a",
                family="model-a",
                provider_id="omlx",
                model_name="model-a",
                fetch=Fetch(repo="org/model-a"),
            ),
            ModelEntry(
                id="omlx/model-b",
                family="model-b",
                provider_id="omlx",
                model_name="model-b",
                fetch=Fetch(repo="org/model-b"),
            ),
            ModelEntry(
                id="openrouter/z-ai/glm-5.3-flash",
                family="glm",
                provider_id="openrouter",
                model_name="z-ai/glm-5.3-flash",
                location="cloud",
            ),
            ModelEntry(
                id="llamacpp/retired-model",
                family="retired",
                provider_id="llamacpp",
                model_name="retired-model",
            ),
        ],
    )


def _state_path(tmp_path: Path, running: dict[str, bool] | None = None) -> Path:
    """Write an initial modelman.toml with the given per-model running
    flags and return its path."""
    path = tmp_path / "modelman.toml"
    store = StateStore()
    for model_id, is_running in (running or {}).items():
        store.set(model_id, ModelState(ready=True, running=is_running))
    save_state(store, path)
    return path


def _mtplx_registry() -> Registry:
    """_registry() plus an mtplx row with two models — a single-model
    provider, whose start still goes through isolate_provider()."""
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    for suffix in ("a", "b"):
        registry.models.append(
            ModelEntry(
                id=f"mtplx/org/model-{suffix}",
                family=f"mtplx-{suffix}",
                provider_id="mtplx",
                model_name=f"org/model-{suffix}",
            )
        )
    return registry


def test_start_unknown_model_raises():
    with pytest.raises(LocalControlError, match="unknown model"):
        start_local_model(_registry(), "ollama/not-in-registry")


def test_start_cloud_model_rejected():
    # A cloud model can never be "running locally" - modelman start only
    # manages the local-process lifecycle.
    with pytest.raises(LocalControlError, match="cloud model"):
        start_local_model(_registry(), "openrouter/z-ai/glm-5.3-flash")


def test_start_unsupported_provider_rejected():
    # llamacpp is local but retired (issue #33) - not in SUPPORTED_PROVIDER_IDS,
    # so bin/llm-isolate-provider can't isolate it.
    with pytest.raises(LocalControlError, match="cannot be started"):
        start_local_model(_registry(), "llamacpp/retired-model")


def test_start_already_running_and_probed_serving_is_idempotent(tmp_path):
    # The running flag alone is not enough to no-op: the flagged model must
    # also answer its availability probe — `modelman start <id>` is the
    # recovery command wt's "not running" message prescribes, and a no-op
    # on a dead flag would deadlock that recovery loop.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    with (
        patch("modelman.local_control._probe_running", return_value=True) as mock_probe,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert result.already_running is True
    mock_probe.assert_called_once()
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_start_flagged_ollama_model_with_an_unreadable_listing_is_already_running(tmp_path):
    # The daemon answers and the pull check passed, but /api/tags then gives
    # no usable listing (a proxy's error page in front of a remote ollama):
    # the probe cannot say. Ollama's start is flag-only, so there is nothing
    # to reload — clearing and re-setting the flag only turned "already
    # running" into "Started".
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    with patch("modelman.local_control._probe_running", return_value=None):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert result.already_running is True
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_start_ollama_refuses_when_the_daemon_is_not_answering(tmp_path, wt_calls):
    # Ollama's start is deliberately flag-only — no process action; the daemon
    # lazy-loads on the first request. But every start ends in one
    # `wt litellm sync`, and wt reads a REFUSED ollama probe as "nothing is
    # pulled" (Snapshot.Down is the one case it prunes rather than skips), so
    # it removes the LiteLLM route of every configured ollama model — the one
    # just reported as started included. Refuse before the flag flip and
    # before that sync: wt's own lifecycle never kickstarts a dead ollama
    # either, so a start that cannot serve must not report success.
    state_path = _state_path(tmp_path, {})
    with (
        patch("modelman.local_control._http_answers", return_value=False),
        pytest.raises(LocalControlError, match="ollama daemon is not answering"),
    ):
        start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is False
    assert wt_calls == []


def test_start_ollama_probes_the_configured_origin_not_just_the_default(tmp_path, wt_calls):
    # The check must ask the origin wt's own probe would ask, or a relocated
    # ollama (auth.base_url in registry.toml) is reported down while serving.
    registry = _registry()
    registry.providers[0].auth = AuthConfig(type="none", base_url="http://127.0.0.1:12345")
    state_path = _state_path(tmp_path, {})
    asked: list[str] = []

    def fake_answers(url: str, timeout: float = 2.0) -> bool:
        asked.append(url)
        return True

    with patch("modelman.local_control._http_answers", fake_answers):
        start_local_model(registry, "ollama/qwen3.8:27b-mlx", state_path)
    assert asked == ["http://127.0.0.1:12345/api/tags"]


def test_start_syncs_routes_once_and_writes_no_exposed(tmp_path, wt_calls):
    """#179: starting a model routes it via one `wt litellm sync`; modelman
    no longer flips an exposed flag or calls expose."""
    state_path = _state_path(tmp_path, {})
    with (
        patch("modelman.local_control.isolate_provider"),
        patch("modelman.local_control.stop_provider"),
        patch("modelman.local_control.stop_all_local_providers"),
    ):
        start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
    assert not [c for c in wt_calls if c[:1] in (["expose"], ["unexpose"])]
    assert "exposed" not in state_path.read_text()


def test_stop_all_syncs_routes_once(tmp_path, wt_calls):
    """#179: stop --all costs one sync for every model it stopped."""
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control.stop_all_local_providers"):
        result = stop_all_local_models(state_path)
    assert sorted(result.stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_start_already_running_model_resyncs_routes(tmp_path, wt_calls):
    # Re-running `modelman start` on a model that's already running is the
    # remediation path for a route that drifted: the probe confirms it's
    # healthy, so there's no teardown/restart — just one `wt litellm sync`.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})

    with patch("modelman.local_control._probe_running", return_value=True):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)

    assert result.already_running is True
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_start_succeeds_with_warning_when_sync_fails(tmp_path):
    # A failed `wt litellm sync` must degrade to a warning rather than
    # abort an otherwise-successful start: the running flag is still
    # persisted, and the warning names the command that fixes the routes.
    from modelman import wt_bridge

    state_path = _state_path(tmp_path, {})

    with patch.object(wt_bridge, "sync", side_effect=wt_bridge.WtBridgeError("wt not on PATH")):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)

    assert result.already_running is False
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True
    assert any("may not be synced" in w and "wt litellm sync" in w for w in result.warnings)


def test_stop_local_model_syncs_once_after_clearing_the_flag(tmp_path):
    # A stopped model must not stay routable through LiteLLM: stopping runs
    # one `wt litellm sync`, and only AFTER the process is stopped and the
    # running flag cleared — wt probes live state, so the sync must see the
    # model gone.
    from modelman import wt_bridge

    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    seen_running: list[bool] = []

    def recording_sync(**kwargs):
        seen_running.append(load_state(state_path).get("ollama/qwen3.8:27b-mlx").running)
        return wt_bridge.BridgeResult([], False, ["a sync warning"])

    with (
        patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama,
        patch.object(wt_bridge, "sync", side_effect=recording_sync),
    ):
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_called_once_with("qwen3.8:27b-mlx")

    assert seen_running == [False]
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is False
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert result.warnings == ["a sync warning"]


def test_start_marker_names_dead_model_clears_it_and_restarts(tmp_path):
    # Flag matches but the probe says nothing is serving (crash, reboot,
    # `omlx stop`): the flag must be cleared and the full start must run —
    # this is the recovery path wt's fatal message prescribes. Ollama is
    # flag-only, so "restart" here means neither stop-all nor isolate are
    # ever invoked — only the flag is re-set.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert result.already_running is False
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_start_leaves_unrelated_running_model_flagged_and_flips_ollama_flag_only(tmp_path):
    # Cross-provider concurrency: starting an ollama model while an
    # unrelated model is already flagged running leaves that flag
    # untouched (no more global stop-all-then-start), and ollama itself
    # is flag-only (never calls isolate_provider — it lazy-loads on
    # first request).
    state_path = _state_path(tmp_path, {"some/other-model": True})
    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    assert result.already_running is False
    assert result.direct_url is None
    assert result.other_running == ["some/other-model"]
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is True
    assert state.get("some/other-model").running is True


def test_start_isolate_failure_raises_and_does_not_write_marker(tmp_path):
    # No prior flag: a failed start must not fabricate one. Uses mtplx:
    # ollama is flag-only and omlx starts through wt (#213), so neither
    # calls isolate_provider and neither can exercise this failure path.
    state_path = _state_path(tmp_path, {})
    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        mock_isolate.return_value = IsolateResult(
            provider="mtplx", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_mtplx_registry(), "mtplx/org/model-a", state_path)
    assert load_state(state_path).get("mtplx/org/model-a").running is False


def test_start_omlx_failure_leaves_the_loaded_sibling_flagged(tmp_path, monkeypatch, wt_calls):
    # omlx is a pool (#213): a start no longer stops the model that is
    # loaded, so when wt refuses the new one the sibling is still loaded and
    # its flag stays. (modelman used to stop omlx and clear that flag before
    # the start was even attempted.) Routes are still synced, once, before
    # the failure is reported.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    calls = _stub_wt_start(monkeypatch, error=wt_bridge.WtBridgeError("warmup timed out"))
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
        pytest.raises(LocalControlError, match="warmup timed out"),
    ):
        start_local_model(_registry(), "omlx/model-b", state_path)
    assert calls == [("omlx/model-b", True)]
    mock_stop_one.assert_not_called()
    mock_isolate.assert_not_called()
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is True
    assert state.get("omlx/model-b").running is False
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


@pytest.mark.parametrize("fails_by", ["not_ok", "raises"])
def test_start_isolate_failure_after_occupant_stop_syncs_routes_once(tmp_path, wt_calls, fails_by):
    # The mtplx occupant is torn down INSIDE isolate; when the new model's
    # isolate then fails, routes must still follow live state (the
    # occupant's route must not keep pointing at a dead backend) — exactly
    # one sync, and the failure is still raised. (mtplx, not omlx: an omlx
    # start goes through wt and never reaches isolate_provider, #213.)
    from modelman.benchmark.errors import BenchmarkError

    state_path = _state_path(tmp_path, {"mtplx/org/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider"),
    ):
        if fails_by == "raises":
            mock_isolate.side_effect = BenchmarkError("warmup timed out")
        else:
            mock_isolate.return_value = IsolateResult(
                provider="mtplx", model="", direct_url="", ok=False, error="warmup timed out"
            )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_mtplx_registry(), "mtplx/org/model-b", state_path)
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_start_isolate_failure_includes_sync_warnings_in_error(tmp_path):
    # The failure branch raises, so a failed sync's warning travels in the
    # error text rather than being dropped. (mtplx: the isolate path; the
    # omlx start's twin is test_start_omlx_failure_includes_sync_warnings.)
    from modelman import wt_bridge

    state_path = _state_path(tmp_path, {"mtplx/org/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider"),
        patch.object(wt_bridge, "sync", side_effect=wt_bridge.WtBridgeError("wt not on PATH")),
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out") as excinfo:
            start_local_model(_mtplx_registry(), "mtplx/org/model-b", state_path)
    assert "may not be synced" in str(excinfo.value)


def test_start_successful_occupant_replacement_syncs_routes_once(tmp_path, wt_calls):
    # No double sync on the success path, occupant replacement included
    # (mtplx: omlx replaces nothing and starts through wt, #213).
    state_path = _state_path(tmp_path, {"mtplx/org/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider"),
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx", model="org/model-b", direct_url="http://x", ok=True, error=None
        )
        start_local_model(_mtplx_registry(), "mtplx/org/model-b", state_path)
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_start_running_flag_write_failure_keeps_sync_warnings(tmp_path):
    # If persisting running=True fails, the sync already ran: its warnings
    # must appear in the LocalControlError rather than being discarded.
    from modelman import wt_bridge

    state_path = _state_path(tmp_path, {})
    with (
        patch.object(wt_bridge, "sync", side_effect=wt_bridge.WtBridgeError("wt not on PATH")),
        patch("modelman.local_control.locked_state", side_effect=OSError(28, "No space left")),
        pytest.raises(LocalControlError, match="could not be") as excinfo,
    ):
        start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert "may not be synced" in str(excinfo.value)


def test_start_isolate_failure_does_not_clear_concurrent_writes(tmp_path):
    # _clear_stale_running_flag only ever touches the ONE id it's given
    # (per-model, not a shared marker) — a concurrent writer's flag on a
    # DIFFERENT model must survive a failed start of this one.
    state_path = _state_path(tmp_path, {})

    def concurrent_writer_and_fail(*args, **kwargs):
        with locked_state(state_path) as fresh:
            fresh.models["concurrent/new-model"] = ModelState(running=True)
        return IsolateResult(
            provider="mtplx", model="", direct_url="", ok=False, error="warmup timed out"
        )

    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider", side_effect=concurrent_writer_and_fail),
        pytest.raises(LocalControlError, match="warmup timed out"),
    ):
        start_local_model(_mtplx_registry(), "mtplx/org/model-a", state_path)
    state = load_state(state_path)
    assert state.get("concurrent/new-model").running is True
    assert state.get("mtplx/org/model-a").running is False


def test_stop_omlx_clears_flag_without_stopping_the_provider(tmp_path, monkeypatch):
    # An omlx model's stop goes to wt, which unloads that one model (#213);
    # stop_provider() — `omlx stop`, halting the whole service — is not run.
    # The model's flag is cleared on success.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    calls, _ = _stub_wt_stop(monkeypatch)
    with patch("modelman.local_control.stop_provider") as mock_stop:
        result = stop_local_model("omlx/model-a", state_path)
    assert calls == ["omlx/model-a"]
    mock_stop.assert_not_called()
    assert result.stopped_model_id == "omlx/model-a"
    assert load_state(state_path).get("omlx/model-a").running is False


def test_stop_noop_when_model_present_but_not_running(tmp_path):
    # A model present in state but explicitly flagged not-running (not just
    # absent from state entirely) must still no-op — the same
    # recovery-safety property as an unknown/never-seen model.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": False})
    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_not_called()
    assert result.stopped_model_id is None


def test_stop_does_not_clobber_concurrent_write_to_another_model(tmp_path, monkeypatch):
    # The stop (wt's, for omlx) runs OUTSIDE the state lock; a concurrent `modelman
    # start` that flags a DIFFERENT model running while our own stop is
    # mid-flight must not be clobbered by our own flag clear — locked_state
    # re-reads fresh from disk before mutating only our own key.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})

    def concurrent_writer(*args, **kwargs):
        with locked_state(state_path) as fresh:
            fresh.models["omlx/model-b"] = ModelState(running=True)

    monkeypatch.setattr(local_control.wt_bridge, "stop", concurrent_writer)
    result = stop_local_model("omlx/model-a", state_path)
    assert result.stopped_model_id == "omlx/model-a"
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is False
    assert state.get("omlx/model-b").running is True


def test_start_does_not_stop_a_different_provider(tmp_path, monkeypatch):
    # Cross-provider concurrency: starting an omlx model while an ollama
    # model is already flagged running must leave ollama's flag alone and
    # never call the global stop-everyone-else path.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    calls = _stub_wt_start(
        monkeypatch, wt_bridge.StartOutcome(id="omlx/model-a", status="started", unloaded=[])
    )
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop_all,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        result = start_local_model(_registry(), "omlx/model-a", state_path)
    assert calls == [("omlx/model-a", True)]
    mock_stop_all.assert_not_called()
    mock_stop_one.assert_not_called()
    mock_isolate.assert_not_called()  # an omlx start is wt's (#213)
    assert result.other_running == ["ollama/qwen3.8:27b-mlx"]
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is True
    assert state.get("ollama/qwen3.8:27b-mlx").running is True  # untouched


def test_start_omlx_clears_a_sibling_only_when_wt_reports_it_unloaded(
    tmp_path, monkeypatch, wt_calls
):
    # omlx used to be treated as single-model-per-process: starting a
    # DIFFERENT omlx model stopped the service first and cleared the old
    # model's flag. It is a pool (#213): the start goes to wt, the service is
    # never stopped, and the old model's flag goes only because wt reported
    # omlx unloaded it. One sync closes the start.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    calls = _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx/model-b", status="started", unloaded=["omlx/model-a"]),
    )
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        start_local_model(_registry(), "omlx/model-b", state_path)
    assert calls == [("omlx/model-b", True)]
    mock_stop_one.assert_not_called()
    mock_isolate.assert_not_called()
    state = load_state(state_path)
    assert state.get("omlx/model-b").running is True
    assert state.get("omlx/model-a").running is False
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


def test_start_replaces_same_provider_occupant_with_unrelated_provider_also_running(tmp_path):
    # Regression test for the exact bug the plan's preflight review caught
    # in _same_provider_occupant: an EARLIER implementation returned the
    # first running model found overall (scanning all of state.models) and
    # only afterward filtered by "is this id a member of provider_model_ids"
    # — so with an ollama model ALSO flagged running, dict iteration order
    # could hand that buggy version ollama's id first, its post-hoc filter
    # would reject it (not an mtplx id) and stop there, silently returning
    # None instead of continuing on to find the real mtplx occupant. The
    # correct implementation iterates provider_model_ids itself, so it can
    # never be distracted by an unrelated provider's running flag —
    # regardless of insertion/iteration order. (mtplx: omlx is a pool with
    # no single occupant since #213, so the lookup no longer runs for it.)
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "mtplx/org/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx",
            model="org/model-b",
            direct_url="http://localhost:8003/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(_mtplx_registry(), "mtplx/org/model-b", state_path)
    mock_stop_one.assert_not_called()  # mtplx replaces its occupant inside isolate
    state = load_state(state_path)
    assert state.get("mtplx/org/model-b").running is True
    assert state.get("mtplx/org/model-a").running is False  # the real occupant, cleared
    assert state.get("ollama/qwen3.8:27b-mlx").running is True  # unrelated, untouched


def test_start_clears_an_omlx_6bit_model_wt_unloaded_when_starting_plain_omlx(
    tmp_path, monkeypatch
):
    # omlx and omlx-6bit are two registry provider ids sharing ONE physical
    # port/process (8000), so one pool (#213): starting a plain-omlx model
    # can make omlx unload a model flagged under the omlx-6bit spelling. wt
    # reports it under that id and its flag must go — treating the two
    # provider ids as independent would leave the 6-bit model's flag stale.
    # The service is not stopped to get there, as it used to be.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none")
        )
    )
    registry.models.append(
        ModelEntry(
            id="omlx-6bit/model-c",
            family="model-c",
            provider_id="omlx-6bit",
            model_name="model-c",
            fetch=Fetch(repo="org/model-c"),
        )
    )
    state_path = _state_path(tmp_path, {"omlx-6bit/model-c": True})
    calls = _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx/model-a", status="started", unloaded=["omlx-6bit/model-c"]),
    )
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        result = start_local_model(registry, "omlx/model-a", state_path)
    assert calls == [("omlx/model-a", True)]
    mock_stop_one.assert_not_called()
    mock_isolate.assert_not_called()
    assert result.unloaded == ["omlx-6bit/model-c"]
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is True
    assert state.get("omlx-6bit/model-c").running is False  # cross-spelling sibling, cleared


def test_start_replaces_mlx_lm_server_occupant_and_clears_its_flag(tmp_path):
    # Regression test for the bug found in review: the occupant flag-clear
    # was previously nested INSIDE the omlx-only stop_provider() gate, so
    # an mlx_lm_server occupant (which self-replaces its process inside its
    # own start function, never via stop_provider()) was found but its
    # flag was never cleared — a permanently-stale "running" flag, since
    # mlx_lm_server's own probe only checks "is *anything* serving on port
    # 8001" and can't self-heal once a DIFFERENT pairing takes over the
    # port.
    from modelman.registry import DraftSpec

    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none")
        )
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/pairing-a",
            family="pair-a",
            provider_id="mlx_lm_server",
            model_name="pairing-a",
            fetch=Fetch(repo="org/target-a"),
            draft=DraftSpec(repo="org/draft-a"),
        )
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/pairing-b",
            family="pair-b",
            provider_id="mlx_lm_server",
            model_name="pairing-b",
            fetch=Fetch(repo="org/target-b"),
            draft=DraftSpec(repo="org/draft-b"),
        )
    )
    state_path = _state_path(tmp_path, {"mlx_lm_server/pairing-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server",
            model="org/target-b",
            direct_url="http://localhost:8001/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(registry, "mlx_lm_server/pairing-b", state_path)
    # mlx_lm_server self-replaces its occupant inside its own start
    # function — local_control must never call stop_provider() for it.
    mock_stop_one.assert_not_called()
    state = load_state(state_path)
    assert state.get("mlx_lm_server/pairing-b").running is True
    assert state.get("mlx_lm_server/pairing-a").running is False  # occupant flag cleared


def test_start_replaces_mtplx_occupant_and_clears_its_flag(tmp_path):
    # Regression test for the final-review finding: the occupant LOOKUP was
    # skipped entirely for mtplx (on the grounds that its own isolate()
    # replaces the PROCESS internally), so the replaced occupant's `running`
    # flag was never cleared here. Nothing in mtplx's process path writes
    # modelman.toml, so until some later probe self-healed it the TUI's
    # RUNNING column showed two mtplx models running at once and `modelman
    # start`'s other-running warning named an already-dead model. The
    # process stop must still stay mtplx-internal (no stop_provider() call).
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    for suffix in ("a", "b"):
        registry.models.append(
            ModelEntry(
                id=f"mtplx/org/model-{suffix}",
                family=f"mtplx-{suffix}",
                provider_id="mtplx",
                model_name=f"org/model-{suffix}",
            )
        )
    state_path = _state_path(tmp_path, {"mtplx/org/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx",
            model="org/model-b",
            direct_url="http://localhost:8003/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(registry, "mtplx/org/model-b", state_path)
    # mtplx tears down its predecessor inside isolate_provider(..., solo=True)
    # → providers/lifecycle.py's isolate(); local_control must not duplicate
    # that with a stop_provider() call.
    mock_stop_one.assert_not_called()
    state = load_state(state_path)
    assert state.get("mtplx/org/model-b").running is True
    assert state.get("mtplx/org/model-a").running is False  # occupant flag cleared


def test_start_isolate_failure_leaves_mtplx_occupant_flagged(tmp_path):
    # Regression test for the finding fixed in review: mtplx tears down its
    # predecessor INSIDE isolate_provider(..., solo=True), not before it —
    # so unlike omlx (which has its own confirmed stop_provider() call
    # first), the occupant's flag must only be cleared AFTER isolate_provider()
    # actually succeeds. If isolate fails, the occupant may still be
    # running (its teardown may itself be what failed), so its flag must
    # survive — clearing it early would falsely report it stopped.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    for suffix in ("a", "b"):
        registry.models.append(
            ModelEntry(
                id=f"mtplx/org/model-{suffix}",
                family=f"mtplx-{suffix}",
                provider_id="mtplx",
                model_name=f"org/model-{suffix}",
            )
        )
    state_path = _state_path(tmp_path, {"mtplx/org/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx", model="", direct_url="", ok=False, error="serve died"
        )
        with pytest.raises(LocalControlError, match="serve died"):
            start_local_model(registry, "mtplx/org/model-b", state_path)
    mock_stop_one.assert_not_called()
    state = load_state(state_path)
    assert state.get("mtplx/org/model-a").running is True  # occupant survives
    assert state.get("mtplx/org/model-b").running is False


def test_start_isolate_failure_leaves_mlx_lm_server_occupant_flagged(tmp_path):
    # Same regression as the mtplx case above, for mlx_lm_server: its
    # predecessor teardown also happens inside isolate_provider(solo=True),
    # so a failed isolate must leave the occupant's flag untouched rather
    # than clearing it on the unconfirmed assumption that teardown ran.
    from modelman.registry import DraftSpec

    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none")
        )
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/pairing-a",
            family="pair-a",
            provider_id="mlx_lm_server",
            model_name="pairing-a",
            fetch=Fetch(repo="org/target-a"),
            draft=DraftSpec(repo="org/draft-a"),
        )
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/pairing-b",
            family="pair-b",
            provider_id="mlx_lm_server",
            model_name="pairing-b",
            fetch=Fetch(repo="org/target-b"),
            draft=DraftSpec(repo="org/draft-b"),
        )
    )
    state_path = _state_path(tmp_path, {"mlx_lm_server/pairing-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server",
            model="",
            direct_url="",
            ok=False,
            error="port still answering",
        )
        with pytest.raises(LocalControlError, match="port still answering"):
            start_local_model(registry, "mlx_lm_server/pairing-b", state_path)
    mock_stop_one.assert_not_called()
    state = load_state(state_path)
    assert state.get("mlx_lm_server/pairing-a").running is True  # occupant survives
    assert state.get("mlx_lm_server/pairing-b").running is False


def test_start_ollama_is_flag_only(tmp_path):
    # Ollama never gets a process call on start - lazy-loads on first
    # request. Only the flag flips.
    state_path = _state_path(tmp_path, {})
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop_all,
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    mock_isolate.assert_not_called()
    mock_stop_one.assert_not_called()
    mock_stop_all.assert_not_called()
    assert result.already_running is False
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_stop_local_model_stops_one_and_clears_its_flag_only(tmp_path):
    # Stopping one model must never touch another model's flag — this is
    # what makes cross-provider concurrency safe: stopping omlx must not
    # silently kill an unrelated ollama flag.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_called_once_with("qwen3.8:27b-mlx")
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("omlx/model-a").running is True  # untouched


def test_stop_local_model_noop_when_not_running(tmp_path):
    # Stopping a model that was never started (absent from state
    # entirely) must no-op rather than raise or shell out.
    state_path = _state_path(tmp_path, {})
    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_stop_ollama.assert_not_called()
    assert result.stopped_model_id is None


def test_stop_local_model_mtplx_stops_via_lifecycle_and_clears_flag(tmp_path):
    # mtplx's stop goes through providers.lifecycle.stop("mtplx") (its
    # process is a plain backgrounded subprocess, not something
    # stop_provider()'s bash helper drives) — on success the model's flag
    # is cleared like any other provider's stop.
    from modelman.local_process import ProcessResult

    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    registry.models.append(
        ModelEntry(
            id="mtplx/org/model",
            family="mtplx-family",
            provider_id="mtplx",
            model_name="org/model",
        )
    )
    state_path = _state_path(tmp_path, {"mtplx/org/model": True})
    with patch("modelman.providers.lifecycle.stop") as mock_lifecycle_stop:
        mock_lifecycle_stop.return_value = ProcessResult(
            provider="mtplx", model="", direct_url="", ok=True, error=None
        )
        result = stop_local_model("mtplx/org/model", state_path)
    mock_lifecycle_stop.assert_called_once_with("mtplx")
    assert result.stopped_model_id == "mtplx/org/model"
    assert load_state(state_path).get("mtplx/org/model").running is False


def test_stop_local_model_mtplx_failure_raises_local_control_error(tmp_path):
    # A failed mtplx stop (process wouldn't die, binary missing) must
    # surface as a clean LocalControlError, not silently clear the flag —
    # the model may still actually be serving.
    from modelman.local_process import ProcessResult

    state_path = _state_path(tmp_path, {"mtplx/org/model": True})
    with patch("modelman.providers.lifecycle.stop") as mock_lifecycle_stop:
        mock_lifecycle_stop.return_value = ProcessResult(
            provider="mtplx", model="", direct_url="", ok=False, error="mtplx stop failed"
        )
        with pytest.raises(LocalControlError, match="mtplx stop failed"):
            stop_local_model("mtplx/org/model", state_path)
    mock_lifecycle_stop.assert_called_once_with("mtplx")
    # Failure: the model may still be serving, so its flag stays true.
    assert load_state(state_path).get("mtplx/org/model").running is True


def test_stop_all_local_models_stops_every_running_one(tmp_path):
    # `modelman stop --all` (or equivalent) must clear every running
    # model's flag in one pass, via the single stop-all isolation call —
    # not one stop_provider() call per model.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop_all:
        result = stop_all_local_models(state_path)
    mock_stop_all.assert_called_once()
    assert sorted(result.stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    assert result.warnings == []
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("omlx/model-a").running is False


def test_running_model_ids_filters_to_probe_verified(tmp_path):
    # The TUI/CLI's "what's actually running" view must self-heal: a
    # flagged-but-dead model (probe fails) is excluded from the result,
    # not just trusted from the on-disk flag.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    registry = _registry()
    state = load_state(state_path)
    with patch(
        "modelman.local_control._probe_running",
        side_effect=lambda provider_id, model_name, base: provider_id == "ollama",
    ):
        ids = running_model_ids(registry, state, state_path)
    assert ids == ["ollama/qwen3.8:27b-mlx"]


def test_stop_all_local_models_surfaces_sync_failure_as_warning(tmp_path):
    # A failed sync must not be silently swallowed: every process still
    # stops and every running flag still clears, but the caller (main.py's
    # --all branch) needs a warning to print, mirroring stop_local_model's
    # StopResult.warnings.
    from modelman import wt_bridge

    state_path = _state_path(tmp_path, {"ollama/a": True})

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch.object(wt_bridge, "sync", side_effect=wt_bridge.WtBridgeError("wt not on PATH")),
    ):
        result = stop_all_local_models(state_path)

    assert result.stopped == ["ollama/a"]
    assert any("may not be synced" in w for w in result.warnings)
    # The process is stopped either way — a failed sync must not block it.
    assert load_state(state_path).get("ollama/a").running is False


def test_running_model_ids_keeps_a_flag_the_probe_cannot_disprove(tmp_path, wt_calls):
    """#249: the probe's three answers are not two. A provider that is up but
    cannot say — an omlx pool with some models loaded, which only the
    key-protected status endpoint could resolve — must leave the flag alone.
    Reading "unknown" as "not running" cleared the flag of a model that was
    still being served, after which `modelman stop` no-opped and the process
    stayed resident with nothing recording it."""
    state_path = _state_path(tmp_path, {"omlx/model-a": True, "ollama/x:1b": True})
    registry = _registry()
    state = load_state(state_path)

    def probe(provider_id, model_name, base):
        return None if provider_id in ("omlx", "omlx-6bit") else False

    with patch("modelman.local_control._probe_running", side_effect=probe):
        ids = running_model_ids(registry, state, state_path)

    assert ids == ["omlx/model-a"]
    after = load_state(state_path)
    assert after.get("omlx/model-a").running is True
    assert after.get("ollama/x:1b").running is False


def test_start_listing_shows_a_model_the_probe_cannot_disprove_as_running(tmp_path):
    """The no-arg `modelman start` inventory lists what the probe confirms.
    For an answer of "cannot say" the flag is what there is to go on, and the
    flag says running — the same answer running_model_ids, and so the TUI's
    RUNNING column, gives. (`modelman start <id>` is the one reader that wants
    a definite yes: its idempotency check reloads on "cannot say".)"""
    from modelman.local_control import inventory_local_models

    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    registry = _registry()
    state = load_state(state_path)

    with (
        _patch_provider_local_models(
            {"omlx": [{"variant_id": "model-a", "path": "/m/a", "size_bytes": 7}]}
        ),
        patch("modelman.local_control._probe_running", return_value=None),
    ):
        inventory = inventory_local_models(registry, state)

    entry = next(e for e in inventory.downloaded if e.model_id == "omlx/model-a")
    assert entry.running is True


def test_running_model_ids_clears_a_stale_flag_without_touching_litellm(tmp_path, wt_calls):
    # A flagged model whose probe fails gets its running flag cleared (TUI
    # mount reconcile is the main caller of this path). Routing is not this
    # path's job any more (#179): wt's sync probes live state itself.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    registry = _registry()
    state = load_state(state_path)

    with patch("modelman.local_control._probe_running", return_value=False):
        ids = running_model_ids(registry, state, state_path)

    assert ids == []
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is False
    assert wt_calls == []


def test_start_mlx_lm_server_resolves_pairing_args(tmp_path):
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none")
        )
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/target-repo",
            family="pair",
            provider_id="mlx_lm_server",
            model_name="target-repo",
            fetch=Fetch(repo="org/target-repo"),
            draft=DraftSpec(repo="org/draft-repo"),
        )
    )
    state_path = _state_path(tmp_path)
    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server",
            model="org/target-repo",
            direct_url="http://localhost:8001/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(registry, "mlx_lm_server/target-repo", state_path)
    mock_isolate.assert_called_once_with(
        "mlx_lm_server", "org/target-repo", "org/draft-repo", env=None, solo=True
    )


def test_start_broken_mlx_lm_server_pairing_fails_before_teardown(tmp_path):
    # A missing target/draft pairing must fail fast: the previous model's
    # stop-all runs only after the isolate arguments resolve, so a pairing
    # error can never tear down a healthy model and then leave the GPU empty.
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none")
        )
    )
    registry.models.append(
        ModelEntry(
            id="mlx_lm_server/target-repo",
            family="pair",
            provider_id="mlx_lm_server",
            model_name="target-repo",
            fetch=Fetch(repo="org/target-repo"),
            draft=DraftSpec(repo="org/draft-repo"),
        )
    )
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        # Strip the draft so pairing resolution fails.
        registry.models[-1].draft = None
        with pytest.raises(LocalControlError, match="missing a target or"):
            start_local_model(registry, "mlx_lm_server/target-repo", state_path)
    mock_isolate.assert_not_called()
    # No teardown happened, so the previously running model's flag stays.
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True


def test_name_matches_lenient_prefix_strict_variant_tail():
    # Lenient on prefix: a server may report a path-ish spelling of the same
    # model. Strict on the tail: omlx's 4-bit and 6-bit variants share port
    # 8000 and differ exactly there — a different tail is a different model.
    assert _name_matches("qwen3.8:27b-mlx", "qwen3.8:27b-mlx")
    assert _name_matches("models/qwen3.8:27b-mlx", "qwen3.8:27b-mlx")
    assert _name_matches("org/repo", "repo")
    assert _name_matches("repo", "org/repo")
    assert not _name_matches("ornith-1.5-35b-a3b-mlx-4bit", "ornith-1.5-35b-a3b-mlx-6bit")
    assert not _name_matches("other-model", "qwen3.8:27b-mlx")


def _tags(*names, cloud=()):
    return {
        "models": [{"name": n} for n in names]
        + [{"name": n, "remote_host": "https://ollama.com"} for n in cloud]
    }


def test_probe_running_ollama_means_still_pulled():
    # #242. Ollama's start is flag-only (no warmup — it lazy-loads on first
    # request), so "loaded" (`ollama ps`) is the wrong question: it would
    # clear the flag of a model started and not yet requested. "Still
    # pulled" is the right one — a model that is gone cannot be served, and
    # nothing else would ever clear its flag (#233).
    seen = []

    def tags(url, timeout=2.0):
        seen.append(url)
        return _tags("here:8b", "plain:latest", cloud=["hosted:cloud"])

    with (
        patch("modelman.local_control._http_json", tags),
        patch("modelman.local_control._ollama_loaded_names", return_value=[]),
    ):
        assert _probe_running("ollama", "here:8b", None) is True
        assert _probe_running("ollama", "plain", None) is True  # implicit :latest
        assert _probe_running("ollama", "gone:8b", None) is False
        # An ollama.com stub is not a pulled model (as wt reads it).
        assert _probe_running("ollama", "hosted:cloud", None) is False
        # Asked at the provider row's origin, not the local CLI: with
        # base_url on another host that is the server the model is on.
        assert _probe_running("ollama", "here:8b", "http://elsewhere:11434") is True
    assert seen[0] == "http://localhost:11434/api/tags"
    assert seen[-1] == "http://elsewhere:11434/api/tags"


@pytest.mark.parametrize("answer", [None, {}, {"models": "nope"}], ids=["down", "empty", "junk"])
def test_probe_running_ollama_is_unknown_when_the_daemon_cannot_say(answer):
    # Only a listing that positively lacks the model reads False. A daemon
    # that is down or answers nonsense cannot say either way — None, which
    # every caller that keeps a flag reads as "leave it" (running_model_ids)
    # or "still running" (the start listing): restarting ollama must not stop
    # every started model.
    with patch("modelman.local_control._http_json", return_value=answer):
        assert _probe_running("ollama", "anything", None) is None


def test_running_model_ids_clears_an_unregistered_ollama_model_that_is_gone(tmp_path):
    # #242: a model started with no registry entry is flagged under its
    # discovered id. Reconcile walks the registry and never saw it, so after
    # `ollama rm` its flag stayed for good — in start's other-running warning
    # and in `stop --all`.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/somemodel:7b", ModelState(running=True))
    store.set("ollama/kept:1b", ModelState(running=True))
    save_state(store, state_path)
    registry = Registry(
        providers=[ProviderEntry(id="ollama", name="O", location="local")], models=[]
    )

    with patch("modelman.local_control._http_json", return_value=_tags("kept:1b")):
        verified = running_model_ids(registry, store, state_path)

    assert verified == ["ollama/kept:1b"]
    on_disk = load_state(state_path)
    assert on_disk.get("ollama/somemodel:7b").running is False
    assert on_disk.get("ollama/kept:1b").running is True


def test_start_mtplx_isolates_without_env_var(tmp_path):
    """start_local_model() must call isolate_provider('mtplx', ..., env=None)
    rather than mapping mtplx through an env-var override like ollama/omlx
    do, and must persist the running-model marker in modelman.toml's
    [local] table on success. Passing an env var here would misroute mtplx
    isolation through the wrong bash-shim mechanism; skipping the marker
    write would leave wt's local-model gate pointing at a stale model."""
    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="mtplx",
            name="MTPLX",
            location="local",
            auth=AuthConfig(type="none", base_url="http://localhost:8003/v1"),
        )
    )
    registry.models.append(
        ModelEntry(
            id="mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            family="qwen3.8",
            provider_id="mtplx",
            model_name="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        )
    )
    state_path = _state_path(tmp_path)
    with (
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx",
            model="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            direct_url="http://localhost:8003/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(
            registry, "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", state_path
        )
    mock_isolate.assert_called_once_with(
        "mtplx",
        "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        env=None,
        solo=True,
    )
    # mtplx's own isolate() handles replacing its occupant internally
    # (solo=True) — local_control must not also call stop_provider for it.
    mock_stop_one.assert_not_called()
    assert result.direct_url == "http://localhost:8003/v1/chat/completions"
    assert (
        load_state(state_path).get("mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality").running
        is True
    )


def test_start_discovered_artifact_runs_without_registering(tmp_path, wt_calls):
    # #179 Phase B: an on-disk artifact with no registry.toml entry is started
    # as-is — no family prompt, no registry entry, no ready flag — and the
    # start's single `wt litellm sync` is what routes it, under the id wt's
    # catalog gives it (<family>/<native name>). The running flag lives
    # under that same id so `modelman stop` can find it. Typing wt's id works
    # as well as the bare native name.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    before = registry_path.read_text()
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}
        ]
    }

    for typed in ("llama3.2:3b", "ollama/llama3.2:3b"):
        with (
            _patch_provider_local_models(mapping),
            patch("modelman.local_control.isolate_provider") as mock_isolate,
        ):
            result = start_local_model(registry, typed, state_path)
        mock_isolate.assert_not_called()  # ollama is flag-only
        assert result.model_id == "ollama/llama3.2:3b"

    assert registry_path.read_text() == before
    assert [m.id for m in registry.models] == [m.id for m in _registry().models]
    model_state = load_state(state_path).get("ollama/llama3.2:3b")
    assert model_state.running is True
    assert model_state.ready is False
    # One sync per start: the second start found the flag and probed it, and
    # the idempotent path re-syncs too.
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]] * 2


def test_start_discovered_omlx_artifact_drives_the_provider_by_its_name(tmp_path, monkeypatch):
    # An unregistered omlx artifact is started through wt under its
    # discovered id — the id wt itself gives it — resolved from the real
    # OMLXProvider listing (not the name-keyed stub) because omlx's artifact
    # names are directory basenames. registry.toml stays untouched.
    model_dir = _omlx_model_dir(tmp_path, basename="Qwen3.8-27B-4bit")
    registry = Registry(
        providers=[
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
    )
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    calls = _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx/Qwen3.8-27B-4bit", status="started", unloaded=[]),
    )
    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        result = start_local_model(registry, "Qwen3.8-27B-4bit", state_path)

    assert result.model_id == "omlx/Qwen3.8-27B-4bit"
    assert calls == [("omlx/Qwen3.8-27B-4bit", True)]
    mock_isolate.assert_not_called()
    assert load_registry(registry_path).models == []
    assert load_state(state_path).get("omlx/Qwen3.8-27B-4bit").running is True


def test_start_unregistered_name_ambiguous_across_providers_raises(tmp_path):
    # A discovered name matching on-disk artifacts from two different
    # providers is genuinely ambiguous — starting either one would silently
    # guess wrong, so this must raise and ask for the full <family>/<name>.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    mapping = {
        "ollama": [{"variant_id": "shared-name", "path": "ollama:shared-name", "size_bytes": None}],
        "mtplx": [{"variant_id": "shared-name", "path": "/mtplx/shared-name", "size_bytes": None}],
    }
    with (
        _patch_provider_local_models(mapping),
        pytest.raises(LocalControlError, match="multiple providers") as excinfo,
    ):
        start_local_model(registry, "shared-name")
    # It names the ids to type, not the provider rows.
    assert "(mtplx/shared-name, ollama/shared-name)" in str(excinfo.value)
    assert "<family>/<name>" in str(excinfo.value)


def test_start_native_name_resolves_existing_registered_model_without_reregistering(tmp_path):
    # A model already registered (from a TUI `+` row or curated) must
    # resolve by its native provider-side name too - a user who starts
    # "llama3.2:3b" by the bare name must not see "unknown model" just
    # because the id it is registered under is "ollama/llama3.2:3b".
    registry = _registry()
    registry.models.append(
        ModelEntry(
            id="ollama/llama3.2:3b",
            family="discovered",
            provider_id="ollama",
            model_name="llama3.2:3b",
            location="local",
            source="discovered",
        )
    )
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(registry, "llama3.2:3b", state_path)
    assert result.model_id == "ollama/llama3.2:3b"
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()  # ollama is flag-only
    assert load_state(state_path).get("ollama/llama3.2:3b").running is True


def _listing_registry() -> Registry:
    """Local models covering all three inventory buckets, plus one cloud
    model, for inventory_local_models tests."""
    return Registry(
        providers=[
            ProviderEntry(
                id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")
            ),
            ProviderEntry(
                id="openrouter",
                name="OpenRouter",
                location="cloud",
                auth=AuthConfig(type="api_key"),
            ),
        ],
        models=[
            ModelEntry(
                id="ollama/alpha-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="alpha-model",
            ),
            ModelEntry(
                id="ollama/zulu-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="zulu-model",
            ),
            ModelEntry(
                id="ollama/missing-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="missing-model",
            ),
            ModelEntry(
                id="openrouter/z-ai/glm-5.3-flash",
                family="glm",
                provider_id="openrouter",
                model_name="z-ai/glm-5.3-flash",
                location="cloud",
            ),
        ],
    )


def _listing_state(running_model: str | None = None) -> StateStore:
    store = StateStore()
    store.set(
        "ollama/alpha-model",
        ModelState(ready=True, running=(running_model == "ollama/alpha-model")),
    )
    store.set("ollama/zulu-model", ModelState(ready=True))
    store.set("openrouter/z-ai/glm-5.3-flash", ModelState(ready=False))
    return store


def _patch_provider_local_models(mapping: dict[str, list[dict]]):
    """Patch ProviderRegistry so local_control sees `mapping` (provider_id ->
    list of LocalModel dicts) without shelling out to any real provider CLI or
    scanning a real directory.

    The stub answers BOTH questions local_control asks a provider: what is on
    disk (`list_local`) and whether one registered variant is on disk
    (`resolve_local`/`is_downloaded`/`size_of`). The per-variant answers are
    keyed on the variant's `name` (i.e. ModelEntry.model_name), so this helper
    is ollama-shaped by construction — the provider spelling and the registry
    spelling agree. omlx, where they don't, is covered by the real-provider
    fixtures further down.
    """

    def get_class(name):
        return object if name in mapping else None

    def get(name, config):
        by_name = {lm["variant_id"]: lm for lm in mapping.get(name, [])}
        stub = MagicMock()
        stub.list_local.return_value = mapping.get(name, [])
        # None = no batch implementation; the caller falls back to the
        # per-variant methods below (a bare MagicMock would be a truthy
        # non-list and silently take the same fallback).
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = lambda spec, *a, **k: spec.get("name") in by_name
        stub.size_of.side_effect = lambda spec, *a, **k: by_name.get(spec.get("name"), {}).get(
            "size_bytes"
        )
        return stub

    return patch.multiple(
        "modelman.local_control.ProviderRegistry",
        get_class=MagicMock(side_effect=get_class),
        get=MagicMock(side_effect=get),
    )


def test_inventory_downloaded_bucket_includes_size_and_running_marker():
    # `modelman start`'s no-arg listing must surface the actually-verified
    # size and running status for a registered, on-disk model — not just
    # whatever modelman.toml happens to have cached.
    registry = _listing_registry()
    state = _listing_state(running_model="ollama/alpha-model")
    mapping = {
        "ollama": [
            {
                "variant_id": "alpha-model",
                "path": "ollama:alpha-model",
                "size_bytes": 4_900_000_000,
            },
        ]
    }
    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control._probe_running", return_value=True),
    ):
        inventory = inventory_local_models(registry, state)
    assert inventory.downloaded == [
        InventoryEntry(model_id="ollama/alpha-model", running=True, size_bytes=4_900_000_000)
    ]


def test_inventory_not_downloaded_bucket_lists_registered_missing_artifacts():
    # Registered models the live provider reports nothing for must be
    # listed as missing, whatever their ready or running state — this is what tells
    # a user which registry.toml entries need a real download.
    registry = _listing_registry()
    state = _listing_state()
    with _patch_provider_local_models({"ollama": []}):
        inventory = inventory_local_models(registry, state)
    assert inventory.not_downloaded == [
        "ollama/alpha-model",
        "ollama/missing-model",
        "ollama/zulu-model",
    ]


def test_inventory_discovered_bucket_excludes_already_registered():
    # An on-disk artifact the provider reports must appear in "discovered"
    # only when no registry.toml entry already names it — otherwise every
    # already-registered model would be re-offered for registration on
    # every `modelman start` listing.
    registry = _listing_registry()
    state = _listing_state()
    mapping = {
        "ollama": [
            {"variant_id": "alpha-model", "path": "ollama:alpha-model", "size_bytes": 1},
            {
                "variant_id": "brand-new-model",
                "path": "ollama:brand-new-model",
                "size_bytes": 2_000_000_000,
            },
        ]
    }
    with _patch_provider_local_models(mapping):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == [
        DiscoveredModel(
            provider_id="ollama",
            variant_id="brand-new-model",
            path="ollama:brand-new-model",
            size_bytes=2_000_000_000,
        )
    ]


def test_discover_unregistered_models_excludes_already_registered():
    # Mirrors inventory_local_models's discovered bucket, but through the
    # standalone entry point the TUI calls (it doesn't need the registered-
    # presence half of a full inventory, only the discovery half).
    registry = _listing_registry()
    mapping = {
        "ollama": [
            {"variant_id": "alpha-model", "path": "ollama:alpha-model", "size_bytes": 1},
            {
                "variant_id": "brand-new-model",
                "path": "ollama:brand-new-model",
                "size_bytes": 2_000_000_000,
            },
        ]
    }
    with _patch_provider_local_models(mapping):
        discovered = discover_unregistered_models(registry)
    assert discovered == [
        DiscoveredModel(
            provider_id="ollama",
            variant_id="brand-new-model",
            path="ollama:brand-new-model",
            size_bytes=2_000_000_000,
        )
    ]


def test_discover_unregistered_models_empty_when_nothing_new():
    # Every provider-reported artifact already has a registry.toml entry —
    # nothing should be offered for registration.
    registry = _listing_registry()
    mapping = {
        "ollama": [
            {"variant_id": "alpha-model", "path": "ollama:alpha-model", "size_bytes": 1},
        ]
    }
    with _patch_provider_local_models(mapping):
        discovered = discover_unregistered_models(registry)
    assert discovered == []


def test_inventory_tolerates_a_provider_list_local_failure():
    # A down ollama daemon fails every provider call: the inventory must
    # still be produced (no exception escaping to the CLI), with the
    # unanswerable models listed rather than dropped — the ambiguity is
    # surfaced separately via unqueryable_providers, not by crashing.
    registry = _listing_registry()
    state = _listing_state()

    def get(name, config):
        stub = MagicMock()
        stub.list_local.side_effect = RuntimeError("daemon unreachable")
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = RuntimeError("daemon unreachable")
        return stub

    with patch.multiple(
        "modelman.local_control.ProviderRegistry",
        get_class=MagicMock(return_value=object),
        get=MagicMock(side_effect=get),
    ):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == []
    assert inventory.not_downloaded == [
        "ollama/alpha-model",
        "ollama/missing-model",
        "ollama/zulu-model",
    ]


def test_inventory_skips_providers_with_no_registered_class():
    # A registry.toml entry for a provider id nothing registers under
    # ProviderRegistry (e.g. a hand-edited "omlx-6bit" row today) must be
    # skipped, not raise KeyError — and reported as unqueryable rather than
    # silently contributing an empty (i.e. "nothing on disk") answer.
    registry = _listing_registry()
    registry.providers.append(
        ProviderEntry(
            id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none")
        )
    )
    state = _listing_state()
    with _patch_provider_local_models({"ollama": []}):
        inventory = inventory_local_models(registry, state)
    assert inventory.discovered == []
    assert inventory.unqueryable_providers == ["omlx-6bit"]


# --- omlx join-mismatch regression tests -----------------------------------
#
# Every other fixture in this file is ollama-shaped, where a provider's
# list_local() variant_id is byte-identical to the registered
# ModelEntry.model_name. omlx is the counter-example that broke the original
# implementation: list_local() reports the model DIRECTORY BASENAME
# ("Qwen3.8-27B-4bit") while the registry entry holds the full HuggingFace
# repo id ("mlx-community/Qwen3.8-27B-4bit"). These tests drive the REAL
# OMLXProvider against a temp model dir so the mismatch is genuine rather
# than a stub's idea of it.


def _omlx_model_dir(tmp_path: Path, basename: str = "Qwen3.8-27B-4bit") -> Path:
    """Create ~/.omlx/models-style <model_dir>/<repo basename>/ with a file
    in it and return the model_dir. config.json is what makes omlx (and
    modelman's scan) read the directory as a model; it is empty so the
    size the tests assert stays that of the weights."""
    model_dir = tmp_path / "omlx-models"
    (model_dir / basename).mkdir(parents=True)
    (model_dir / basename / "config.json").write_text("")
    (model_dir / basename / "weights.safetensors").write_bytes(b"x" * 2048)
    return model_dir


_OMLX_MODEL_ID = "omlx/mlx-community--Qwen3.8-27B-4bit"


def _omlx_registry(model_dir: Path) -> Registry:
    return Registry(
        providers=[
            ProviderEntry(
                id="omlx",
                name="oMLX",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
        models=[
            ModelEntry(
                id=_OMLX_MODEL_ID,
                family="qwen3.8",
                provider_id="omlx",
                model_name="mlx-community/Qwen3.8-27B-4bit",
                location="local",
                fetch=Fetch(repo="mlx-community/Qwen3.8-27B-4bit"),
            )
        ],
    )


def test_inventory_registered_omlx_model_is_downloaded_and_not_rediscovered(tmp_path):
    # The bug this branch's final review found: an omlx model registered
    # under its full repo id but reported on disk under its directory
    # basename was bucketed BOTH as "registered, not downloaded" and as a
    # brand-new "discovered" artifact. It must be neither: it is registered
    # and present, with a real size (which list_local() never reports for
    # omlx, but size_of() does).
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    inventory = inventory_local_models(registry, StateStore())
    assert inventory.downloaded == [
        InventoryEntry(model_id=_OMLX_MODEL_ID, running=False, size_bytes=2048)
    ]
    assert inventory.not_downloaded == []
    assert inventory.discovered == []
    assert inventory.unqueryable_providers == []


def test_inventory_excludes_mlx_lm_server_artifacts_from_discovery(tmp_path):
    # mlx_lm_server's on-disk directories are individual target/draft
    # models, never independently startable — Provider.supports_discovery
    # = False on MLXLMServerProvider must keep them out of `modelman
    # start`'s discovered bucket even though list_local() happily
    # enumerates them (this replaced a hardcoded provider-id set in
    # local_control.py with the provider declaring its own capability).
    model_dir = tmp_path / "mlx-lm-server-models"
    (model_dir / "some-target-repo").mkdir(parents=True)
    (model_dir / "some-target-repo" / "weights.safetensors").write_bytes(b"x")
    registry = Registry(
        providers=[
            ProviderEntry(
                id="mlx_lm_server",
                name="mlx-lm server",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
    )
    inventory = inventory_local_models(registry, StateStore())
    assert inventory.discovered == []
    assert inventory.unqueryable_providers == []


def test_start_omlx_directory_basename_resolves_the_registered_full_repo_entry(
    tmp_path, monkeypatch
):
    # The duplicate-registration bug: the old "discovered" listing told the
    # user to type the on-disk basename, but the native-name match compared
    # it verbatim against model_name (the full repo id), missed, and fell
    # through to the discovered-artifact step — which then registered it a
    # second time, and today would start an already-registered artifact
    # under a second, discovered id.
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    calls = _stub_wt_start(
        monkeypatch, wt_bridge.StartOutcome(id=_OMLX_MODEL_ID, status="started", unloaded=[])
    )
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(
            registry,
            "Qwen3.8-27B-4bit",
            state_path,
            litellm_path=litellm_path,
        )

    assert result.model_id == _OMLX_MODEL_ID
    # wt is asked for the REGISTERED id, not a second, discovered one.
    assert calls == [(_OMLX_MODEL_ID, True)]
    mock_isolate.assert_not_called()
    # Singular registry entry and an untouched LiteLLM config: nothing was
    # re-registered or re-routed.
    assert [m.id for m in load_registry(registry_path).models] == [_OMLX_MODEL_ID]
    assert litellm_path.read_text() == "model_list: []\n"


def test_find_discovered_omlx_basename_does_not_rediscover_registered_model(tmp_path, monkeypatch):
    # The same join, exercised through start_local_model's resolution path:
    # an already-registered omlx model must never be started as a
    # discovered artifact just because its on-disk spelling differs.
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    calls = _stub_wt_start(
        monkeypatch, wt_bridge.StartOutcome(id=_OMLX_MODEL_ID, status="started", unloaded=[])
    )
    # The full repo id must keep resolving too (registry-id lookup misses
    # it; the native-name match is what catches it).
    result = start_local_model(registry, "mlx-community/Qwen3.8-27B-4bit", state_path)
    assert result.model_id == _OMLX_MODEL_ID
    assert calls == [(_OMLX_MODEL_ID, True)]


def test_inventory_flags_providers_whose_presence_check_raises():
    # "Couldn't ask the provider" must be distinguishable from "confirmed not
    # downloaded": a stopped ollama daemon makes is_downloaded() raise, and
    # silently relabelling every registered ollama model as missing would send
    # the user off to re-download models they already have.
    registry = _listing_registry()
    state = _listing_state()

    def get(name, config):
        stub = MagicMock()
        # Enumeration still works; only the per-model presence check fails.
        stub.list_local.return_value = [
            {"variant_id": "brand-new-model", "path": "ollama:brand-new-model", "size_bytes": 1}
        ]
        stub.resolve_local.return_value = None
        stub.is_downloaded.side_effect = RuntimeError("daemon unreachable")
        return stub

    with patch.multiple(
        "modelman.local_control.ProviderRegistry",
        get_class=MagicMock(return_value=object),
        get=MagicMock(side_effect=get),
    ):
        inventory = inventory_local_models(registry, state)
    assert inventory.downloaded == []
    assert inventory.unqueryable_providers == ["ollama"]
    # The discovery half is unaffected — the two questions are asked
    # separately, so one failing does not blind the other.
    assert [d.variant_id for d in inventory.discovered] == ["brand-new-model"]


def test_inventory_falls_back_to_cached_size_when_provider_reports_none():
    # A present model whose provider cannot size it (omlx's list_local(), any
    # provider without size_of()) must still show the size modelman.toml
    # already cached from an earlier reconcile, not "—".
    registry = _listing_registry()
    state = _listing_state()
    state.set("ollama/alpha-model", ModelState(ready=True, size_bytes=4_900_000_000))
    mapping = {
        "ollama": [{"variant_id": "alpha-model", "path": "ollama:alpha-model", "size_bytes": None}]
    }
    with _patch_provider_local_models(mapping):
        inventory = inventory_local_models(registry, state)
    assert (
        InventoryEntry(model_id="ollama/alpha-model", running=False, size_bytes=4_900_000_000)
        in inventory.downloaded
    )


def test_running_model_ids_verifies_a_discovered_model_by_probing_it(tmp_path):
    # #179 Phase B: a model started without a registry entry keeps its
    # running flag under its discovered id <family>/<native name>. The
    # verified-running view must probe it like a registered model — the TUI
    # mount reconcile clears every flag this function does not return, so
    # skipping unregistered ids would wipe a live model's flag on every
    # mount. A dead one is cleared; an id whose provider is not in the
    # registry has nothing to probe and is not verified.
    state_path = _state_path(
        tmp_path,
        {"ollama/llama3.2:3b": True, "omlx/gone-4bit": True, "ghost/x": True},
    )
    probed: list[tuple[str, str]] = []

    def probe(provider_id, model_name, base):
        probed.append((provider_id, model_name))
        return model_name == "llama3.2:3b"

    with patch("modelman.local_control._probe_running", side_effect=probe):
        ids = running_model_ids(_registry(), load_state(state_path), state_path)

    assert ids == ["ollama/llama3.2:3b"]
    assert sorted(probed) == [("ollama", "llama3.2:3b"), ("omlx", "gone-4bit")]
    state = load_state(state_path)
    assert state.get("ollama/llama3.2:3b").running is True
    assert state.get("omlx/gone-4bit").running is False


def test_same_provider_occupant_sees_a_flagged_discovered_model(tmp_path):
    # Starting a registered mtplx model replaces whatever mtplx serves, and a
    # model started without a registry entry is flagged only under its
    # discovered id: the occupant lookup must find it, or its flag would
    # outlive the replacement. omlx is a pool (#213): a flagged discovered
    # omlx model is no occupant, and nothing is replaced as a matter of course.
    state_path = _state_path(tmp_path, {"mtplx/org/stray": True, "omlx/stray-4bit": True})
    state = load_state(state_path)
    occupant = same_provider_occupant(_mtplx_registry(), state, "mtplx/org/model-a", "mtplx")
    assert occupant == "mtplx/org/stray"
    assert same_provider_occupant(_registry(), state, "omlx/model-a", "omlx") is None


def test_running_model_ids_verifies_a_discarded_registration_by_repo_id(tmp_path):
    # Registering a discovered mtplx artifact moves its running flag from
    # the discovered id ("mtplx/org/model") to the registered id
    # ("mtplx/org--model" — parse_model's "/" → "--"); a session Discard
    # rolls the registry entry back while the flag row stays on disk. The
    # server reports the repo id ("org/model"), which the registered
    # spelling can never name-match (`_name_matches` is separator-aware),
    # so the probe must retry the repo-id spelling before clearing — the
    # TUI's mount reconcile otherwise drops a serving model's flag and
    # `modelman stop` starts reading it dead. A genuinely dead one (neither
    # spelling answers) is still cleared.
    state_path = _state_path(tmp_path, {"mtplx/org--model": True, "mtplx/dead--model": True})
    registry = Registry(
        providers=[
            ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
        ],
        models=[],
    )
    probed: list[tuple[str, str]] = []

    def probe(provider_id, model_name, base):
        probed.append((provider_id, model_name))
        return model_name == "org/model"

    with patch("modelman.local_control._probe_running", side_effect=probe):
        ids = running_model_ids(registry, load_state(state_path), state_path)

    assert ids == ["mtplx/org--model"]
    assert sorted(probed) == [
        ("mtplx", "dead--model"),
        ("mtplx", "dead/model"),
        ("mtplx", "org--model"),
        ("mtplx", "org/model"),
    ]
    state = load_state(state_path)
    assert state.get("mtplx/org--model").running is True
    assert state.get("mtplx/dead--model").running is False


def test_stop_all_stops_a_running_discovered_model(tmp_path, wt_calls):
    # `modelman stop --all` works off the flags, so a model started without a
    # registry entry is stopped and its flag cleared like any other, with the
    # one closing sync.
    state_path = _state_path(tmp_path, {"ollama/llama3.2:3b": True})
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop_all:
        result = stop_all_local_models(state_path)
    mock_stop_all.assert_called_once()
    assert result.stopped == ["ollama/llama3.2:3b"]
    assert load_state(state_path).get("ollama/llama3.2:3b").running is False
    assert [c for c in wt_calls if c[:1] == ["sync"]] == [["sync", "--json"]]


# --- discovered ids equal wt's ---------------------------------------------
#
# wt names a discovered model config.DiscoveredModelID(localmodels.Family(
# providerID), artifact) (wt/internal/litellm/service.go DiscoveredModel,
# wt/internal/localmodels/inventory.go). modelman keeps the running flag
# under its own id for the same model and `modelman stop <id>` reads that
# flag, while the route wt writes is named by wt's id — so the two must be
# the same string, per provider family.


@pytest.mark.parametrize(
    ("provider_id", "variant_id", "wt_id"),
    [
        # ollama: the tag as `ollama list` / /api/tags spells it.
        ("ollama", "llama3.2:3b", "ollama/llama3.2:3b"),
        # omlx: the model directory's basename.
        ("omlx", "Qwen3.8-27B-4bit", "omlx/Qwen3.8-27B-4bit"),
        # omlx-6bit is the SAME physical server: wt's id is prefixed with the
        # family ("omlx"), never the registry provider row.
        ("omlx-6bit", "Qwen3.8-27B-6bit", "omlx/Qwen3.8-27B-6bit"),
        # mtplx: the repo id (org/name), "/" kept — not the registered-id
        # spelling "org--name".
        ("mtplx", "Youssofal/Qwen3.8-27B", "mtplx/Youssofal/Qwen3.8-27B"),
    ],
)
def test_discovered_entry_id_is_wts_family_prefixed_id(provider_id, variant_id, wt_id):
    entry = _discovered_entry(
        DiscoveredModel(provider_id=provider_id, variant_id=variant_id, path="/x", size_bytes=None)
    )
    assert entry.id == wt_id
    # The provider row is untouched: it selects the lifecycle backend and
    # env var (omlx-6bit's differ from omlx's).
    assert entry.provider_id == provider_id
    assert entry.model_name == variant_id


def test_start_discovered_mtplx_artifact_uses_the_repo_id_wt_routes(tmp_path):
    # The real MTPLXProvider over an on-disk `<org>--<name>` directory: wt
    # spells that artifact `org/name` (mtplxRepoID) and routes it as
    # `mtplx/org/name`. modelman must flag it under exactly that id, serve it
    # by the repo id, probe it by the repo id, and stop it by that id.
    from modelman.local_process import ProcessResult

    model_dir = tmp_path / "mtplx-models"
    (model_dir / "Youssofal--Qwen3.8-27B-MTPLX").mkdir(parents=True)
    (model_dir / "Youssofal--Qwen3.8-27B-MTPLX" / "weights.safetensors").write_bytes(b"x")
    registry = Registry(
        providers=[
            ProviderEntry(
                id="mtplx",
                name="MTPLX",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
    )
    state_path = _state_path(tmp_path)
    wt_id = "mtplx/Youssofal/Qwen3.8-27B-MTPLX"

    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        mock_isolate.return_value = IsolateResult(
            provider="mtplx",
            model="Youssofal/Qwen3.8-27B-MTPLX",
            direct_url="http://localhost:8003/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(registry, "Youssofal/Qwen3.8-27B-MTPLX", state_path)

    assert result.model_id == wt_id
    mock_isolate.assert_called_once_with(
        "mtplx", "Youssofal/Qwen3.8-27B-MTPLX", env=None, solo=True
    )
    assert load_state(state_path).get(wt_id).running is True

    probed: list[tuple[str, str]] = []

    def probe(provider_id, model_name, base):
        probed.append((provider_id, model_name))
        return True

    with patch("modelman.local_control._probe_running", side_effect=probe):
        assert running_model_ids(registry, load_state(state_path), state_path) == [wt_id]
    assert probed == [("mtplx", "Youssofal/Qwen3.8-27B-MTPLX")]

    with patch("modelman.providers.lifecycle.stop") as mock_lifecycle_stop:
        mock_lifecycle_stop.return_value = ProcessResult(
            provider="mtplx", model="", direct_url="", ok=True, error=None
        )
        stopped = stop_local_model(wt_id, state_path)
    mock_lifecycle_stop.assert_called_once_with("mtplx")
    assert stopped.stopped_model_id == wt_id
    assert load_state(state_path).get(wt_id).running is False


def _omlx_6bit_only_registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(
                id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none")
            )
        ],
        models=[
            ModelEntry(
                id="omlx-6bit/registered",
                family="registered",
                provider_id="omlx-6bit",
                model_name="org/registered-6bit",
                fetch=Fetch(repo="org/registered-6bit"),
            )
        ],
    )


def test_start_discovered_artifact_on_an_omlx_6bit_only_registry_uses_the_family_id(
    tmp_path, monkeypatch
):
    # A registry whose only omlx-family provider row is `omlx-6bit`: wt
    # still routes an unregistered artifact as `omlx/<name>` (family-
    # prefixed) with the omlx-6bit row as its provider. If modelman can
    # enumerate that row (stubbed here), wt must be asked to start wt's id
    # and the flag must sit under it — not `omlx-6bit/<name>`; the probe and
    # `modelman stop` must both resolve the family-prefixed id back to that
    # one server, and the pool has no occupant under either spelling (#213).
    registry = _omlx_6bit_only_registry()
    state_path = _state_path(tmp_path)
    mapping = {
        "omlx-6bit": [
            {"variant_id": "Qwen3.8-27B-6bit", "path": "/omlx/Qwen3.8-27B-6bit", "size_bytes": None}
        ]
    }
    start_calls = _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx/Qwen3.8-27B-6bit", status="started", unloaded=[]),
    )
    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(registry, "Qwen3.8-27B-6bit", state_path)

    assert result.model_id == "omlx/Qwen3.8-27B-6bit"
    assert start_calls == [("omlx/Qwen3.8-27B-6bit", True)]
    mock_isolate.assert_not_called()
    state = load_state(state_path)
    assert state.get("omlx/Qwen3.8-27B-6bit").running is True
    assert "omlx-6bit/Qwen3.8-27B-6bit" not in state.models

    # Verified-running view: the family prefix "omlx" is not a provider row
    # here, so the probe must go to the family's row that IS defined.
    probed: list[tuple[str, str]] = []

    def probe(provider_id, model_name, base):
        probed.append((provider_id, model_name))
        return True

    with patch("modelman.local_control._probe_running", side_effect=probe):
        assert running_model_ids(registry, state, state_path) == ["omlx/Qwen3.8-27B-6bit"]
    assert probed == [("omlx-6bit", "Qwen3.8-27B-6bit")]

    # The shared omlx server is a pool: no occupant, seen from a registered
    # omlx-6bit model or otherwise.
    assert same_provider_occupant(registry, state, "omlx-6bit/registered", "omlx-6bit") is None

    stop_calls, _ = _stub_wt_stop(monkeypatch)
    with patch("modelman.local_control.stop_provider") as mock_stop:
        stopped = stop_local_model("omlx/Qwen3.8-27B-6bit", state_path)
    assert stop_calls == ["omlx/Qwen3.8-27B-6bit"]
    mock_stop.assert_not_called()
    assert stopped.stopped_model_id == "omlx/Qwen3.8-27B-6bit"
    assert load_state(state_path).get("omlx/Qwen3.8-27B-6bit").running is False


def test_start_on_an_omlx_6bit_only_registry_with_the_real_providers(tmp_path, monkeypatch):
    # #194: no Provider class is registered under "omlx-6bit" — it is the
    # omlx server reached through a second registry row — so a registry whose
    # only omlx-family row is `omlx-6bit` could not be enumerated at all:
    # modelman reported the row unqueryable and an unregistered omlx artifact
    # was "unknown model", while wt listed and routed it as `omlx/<name>`.
    # The row is now asked through its family's class (OMLXProvider) with its
    # own settings, here the real provider scanning a real model directory.
    model_dir = _omlx_model_dir(tmp_path, basename="Qwen3.8-27B-6bit")
    registry = Registry(
        providers=[
            ProviderEntry(
                id="omlx-6bit",
                name="oMLX 6-bit",
                location="local",
                model_dir=str(model_dir),
                auth=AuthConfig(type="none"),
            )
        ],
    )
    state_path = _state_path(tmp_path)
    inventory = inventory_local_models(registry, StateStore())
    assert inventory.unqueryable_providers == []
    assert [(d.model_id, d.provider_id) for d in inventory.discovered] == [
        ("omlx/Qwen3.8-27B-6bit", "omlx-6bit")
    ]
    calls = _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx/Qwen3.8-27B-6bit", status="started", unloaded=[]),
    )
    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        result = start_local_model(registry, "Qwen3.8-27B-6bit", state_path)
    # wt's id (the family's), which is the id wt is asked to start.
    assert result.model_id == "omlx/Qwen3.8-27B-6bit"
    assert calls == [("omlx/Qwen3.8-27B-6bit", True)]
    mock_isolate.assert_not_called()
    assert load_state(state_path).get("omlx/Qwen3.8-27B-6bit").running is True


def test_a_provider_row_with_no_class_and_no_family_class_is_unqueryable():
    # The family fallback is for a second row of a known server. A row whose
    # id names nothing modelman has code for stays "cannot be asked" — never
    # "reports nothing", which would list its registered models as missing.
    registry = Registry(
        providers=[
            ProviderEntry(id="mystery", name="?", location="local", auth=AuthConfig(type="none"))
        ],
        models=[ModelEntry(id="mystery/m", family="f", provider_id="mystery", model_name="m")],
    )
    assert inventory_local_models(registry, StateStore()).unqueryable_providers == ["mystery"]


def test_start_full_discovered_id_disambiguates_a_name_two_providers_share(tmp_path):
    # The ambiguity error tells the user to type the full <family>/<name>
    # id. _find_discovered matches leniently on the name's tail, so that id
    # still matches BOTH artifacts — the exact discovered id must pick its own.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [{"variant_id": "shared-name", "path": "ollama:shared-name", "size_bytes": None}],
        "mtplx": [{"variant_id": "shared-name", "path": "/mtplx/shared-name", "size_bytes": None}],
    }
    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        result = start_local_model(registry, "ollama/shared-name", state_path)
    mock_isolate.assert_not_called()  # the ollama one: flag-only
    assert result.model_id == "ollama/shared-name"
    assert load_state(state_path).get("ollama/shared-name").running is True
    assert load_state(state_path).get("mtplx/shared-name").running is False


@pytest.mark.parametrize(
    ("typed", "has", "wrong_family"),
    [
        # Only ollama has `foo`; a single-port family's prefix must not
        # start it (and replace that server's occupant on the way).
        ("omlx/foo", "ollama", "omlx"),
        # The other direction: only mtplx has it, ollama's prefix typed.
        ("ollama/foo", "mtplx", "ollama"),
        # A provider row's own id that is not the family (omlx-6bit).
        ("omlx-6bit/foo", "ollama", "omlx-6bit"),
        # A cloud provider row's id is still a provider prefix, not a name.
        ("openrouter/foo", "ollama", "openrouter"),
    ],
)
def test_start_wrong_family_prefix_never_starts_another_providers_artifact(
    tmp_path, typed, has, wrong_family
):
    # _find_discovered matches on the name's tail, so `<other family>/foo`
    # matches the one artifact named `foo` wherever it lives. A typed
    # family/provider prefix is a claim about WHERE the model is: when no
    # candidate's discovered id equals what was typed, it is unknown — and
    # the error names the id that does exist so it can be retyped.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    state_path = _state_path(tmp_path)
    mapping = {has: [{"variant_id": "foo", "path": f"/{has}/foo", "size_bytes": None}]}
    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop,
        pytest.raises(LocalControlError, match="unknown model") as excinfo,
    ):
        start_local_model(registry, typed, state_path)
    assert f"{has}/foo" in str(excinfo.value)
    mock_isolate.assert_not_called()
    mock_stop.assert_not_called()
    assert load_state(state_path).models == {}


def test_ollama_name_matches_mirrors_wts_implicit_latest_rule():
    # wt/internal/localmodels/match.go OllamaNameMatches(have, want): exact,
    # or — only when the overlay's name carries no ":" tag — the artifact is
    # that name with ollama's implicit ":latest". One-directional, as in wt.
    assert _ollama_name_matches("llama3.2:latest", "llama3.2")
    assert _ollama_name_matches("llama3.2:3b", "llama3.2:3b")
    assert not _ollama_name_matches("llama3.2", "llama3.2:latest")
    assert not _ollama_name_matches("llama3.2:3b", "llama3.2")
    assert not _ollama_name_matches("org/llama3.2:3b", "llama3.2:3b")


def test_ollama_overlay_without_a_tag_owns_the_latest_artifact(tmp_path):
    # wt matches overlay model_name "llama3.2" to the pulled artifact
    # "llama3.2:latest" and routes it under the OVERLAY's id. modelman must
    # agree: the artifact is not listed as discovered, and starting it by its
    # listed name (bare or family-prefixed) resolves to the overlay — never a
    # second, discovered id `ollama/llama3.2:latest` wt does not route.
    registry = _registry()
    registry.models.append(
        ModelEntry(
            id="ollama/my-llama",
            family="llama",
            provider_id="ollama",
            model_name="llama3.2",
            location="local",
        )
    )
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {"variant_id": "llama3.2:latest", "path": "ollama:llama3.2:latest", "size_bytes": 7}
        ]
    }
    with _patch_provider_local_models(mapping):
        assert inventory_local_models(registry, StateStore()).discovered == []
        for typed in ("llama3.2:latest", "ollama/llama3.2:latest"):
            result = start_local_model(registry, typed, state_path)
            assert result.model_id == "ollama/my-llama"
    state = load_state(state_path)
    assert state.get("ollama/my-llama").running is True
    assert "ollama/llama3.2:latest" not in state.models


def test_ollama_overlay_with_another_tag_does_not_own_the_latest_artifact():
    # The rule is only the implicit tag: an overlay for "llama3.2:3b" leaves
    # "llama3.2:latest" discovered, exactly as wt lists it.
    registry = _registry()
    registry.models.append(
        ModelEntry(
            id="ollama/llama3.2:3b",
            family="llama",
            provider_id="ollama",
            model_name="llama3.2:3b",
            location="local",
        )
    )
    mapping = {
        "ollama": [
            {"variant_id": "llama3.2:latest", "path": "ollama:llama3.2:latest", "size_bytes": 7}
        ]
    }
    with _patch_provider_local_models(mapping):
        discovered = inventory_local_models(registry, StateStore()).discovered
    assert [d.model_id for d in discovered] == ["ollama/llama3.2:latest"]


def test_omlx_artifact_registered_on_the_other_family_row_is_not_discovered(tmp_path, monkeypatch):
    # omlx and omlx-6bit are one server and (for wt) one family: an overlay
    # on the omlx-6bit row consumes the artifact, so wt never lists it as
    # discovered. Matching per exact provider row re-discovered it through
    # the `omlx` row — listed as both registered and discovered, startable
    # under a second id.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(
            id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none")
        )
    )
    registry.models.append(
        ModelEntry(
            id="omlx-6bit/org--Qwen3.8-27B-6bit",
            family="qwen3.8",
            provider_id="omlx-6bit",
            model_name="org/Qwen3.8-27B-6bit",
            fetch=Fetch(repo="org/Qwen3.8-27B-6bit"),
        )
    )
    state_path = _state_path(tmp_path)
    mapping = {
        "omlx": [
            {"variant_id": "Qwen3.8-27B-6bit", "path": "/omlx/Qwen3.8-27B-6bit", "size_bytes": None}
        ]
    }
    calls = _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx-6bit/org--Qwen3.8-27B-6bit", status="started", unloaded=[]),
    )
    with _patch_provider_local_models(mapping):
        assert discover_unregistered_models(registry) == []
        result = start_local_model(registry, "Qwen3.8-27B-6bit", state_path)
    assert result.model_id == "omlx-6bit/org--Qwen3.8-27B-6bit"
    assert calls == [("omlx-6bit/org--Qwen3.8-27B-6bit", True)]


def test_start_refuses_a_discovered_id_that_is_a_registry_models_id(tmp_path):
    # Registry `omlx/foo` names a DIFFERENT artifact (model_name "bar");
    # an unregistered on-disk artifact `foo` would get the discovered id
    # `omlx/foo` too. Starting it would write the running flag onto the
    # registry model's row, and wt drops the colliding discovered entry, so
    # it would never be routed. Refuse, naming the registry model that owns
    # the id; nothing is started or flagged.
    registry = _registry()
    registry.models.append(
        ModelEntry(
            id="omlx/foo",
            family="foo",
            provider_id="omlx",
            model_name="bar",
            fetch=Fetch(repo="org/bar"),
        )
    )
    state_path = _state_path(tmp_path)
    mapping = {"omlx": [{"variant_id": "foo", "path": "/omlx/foo", "size_bytes": None}]}
    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        pytest.raises(LocalControlError, match="omlx/foo") as excinfo,
    ):
        start_local_model(registry, "foo", state_path)
    assert "already" in str(excinfo.value) and "bar" in str(excinfo.value)
    mock_isolate.assert_not_called()
    assert load_state(state_path).models == {}


def test_omlx_probe_sees_a_model_that_is_still_loading(monkeypatch):
    # #213 item 6: /health's loaded_count counts finished loads only, so a
    # pool with one model mid-load read as "nothing loaded" and modelman
    # cleared the flag of a model that was on its way up. Status reports
    # is_loading, so it is asked first.
    def fake_json(url):
        if url.endswith("/v1/models/status"):
            return {"models": [{"id": "A-4bit", "loaded": False, "is_loading": True}]}
        if url.endswith("/health"):
            return {"engine_pool": {"loaded_count": 0, "model_count": 1}}
        return None

    monkeypatch.setattr(local_control, "_http_json", fake_json)
    assert local_control._omlx_loaded("http://x", ["A-4bit"]) == ["A-4bit"]


def test_omlx_probe_asks_wt_when_status_is_refused(monkeypatch):
    # A keyed omlx refuses the keyless status read; wt holds the registry's
    # key, so it is asked. When wt cannot say either the answer is None
    # (unknown), which leaves a running flag alone (#249).
    def fake_json(url):
        if url.endswith("/health"):
            return {"engine_pool": {"loaded_count": 1, "model_count": 2}}
        return {"error": {"message": "API key required"}}

    monkeypatch.setattr(local_control, "_http_json", fake_json)
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda p, timeout=0.0: ["B-4bit"])
    assert local_control._omlx_loaded("http://x", ["A-4bit", "B-4bit"]) == ["B-4bit"]
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda p, timeout=0.0: None)
    assert local_control._omlx_loaded("http://x", ["A-4bit", "B-4bit"]) is None


def test_omlx_probe_reads_the_list_on_an_omlx_without_status(monkeypatch):
    # An omlx old enough to have neither /health counts nor a status endpoint:
    # its /health ANSWERS, with no pool counts, and its /v1/models list is all
    # there is, as before.
    def fake_json(url):
        return {"status": "healthy"} if url.endswith("/health") else None

    monkeypatch.setattr(local_control, "_http_json", fake_json)
    assert local_control._omlx_loaded("http://x", ["A-4bit"]) == ["A-4bit"]


def _omlx_answers_nothing(monkeypatch, refused):
    """An omlx whose status, `wt served` and /health reads all fail, with the
    connection to it refused or not. Returns the urls the refused check saw."""
    refused_urls: list[str] = []

    def fake_refused(url, timeout=0.0):
        refused_urls.append(url)
        return refused

    monkeypatch.setattr(local_control, "_http_json", lambda url: None)
    monkeypatch.setattr(local_control, "_http_models_ids", lambda url: [])
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda p, timeout=0.0: None)
    monkeypatch.setattr(local_control, "_connection_refused", fake_refused)
    return refused_urls


def test_omlx_probe_cannot_say_when_nothing_answered_at_all(tmp_path, monkeypatch):
    # Final review F2: status, `wt served` and /health all failed without the
    # connection being refused (an omlx too busy loading another model to
    # answer inside the probe timeout). The probe returned the /v1/models
    # list, which had failed too and was [], so it read "not running" and
    # running_model_ids cleared the flag of a model that was still resident.
    refused_urls = _omlx_answers_nothing(monkeypatch, refused=False)

    assert local_control._omlx_loaded("http://x", []) is None
    assert refused_urls == ["http://x/health"]
    assert _probe_running("omlx", "org/A-4bit", "http://x") is None

    state_path = _state_path(tmp_path, {"omlx/a": True})
    state = load_state(state_path)
    assert running_model_ids(_omlx_pool_registry(), state, state_path) == ["omlx/a"]
    assert load_state(state_path).get("omlx/a").running


def test_omlx_probe_reads_a_refused_connection_as_nothing_loaded(monkeypatch):
    # Nothing listens on omlx's port: no server, so nothing is loaded. That is
    # a positive answer, and the one failure that may clear a flag.
    refused_urls = _omlx_answers_nothing(monkeypatch, refused=True)

    assert local_control._omlx_loaded("http://x", []) == []
    assert refused_urls == ["http://x/health"]
    assert _probe_running("omlx", "org/A-4bit", "http://x") is False


@pytest.mark.parametrize(
    "models",
    [
        [{"id": "A"}],
        [{"id": "A", "loaded": "yes"}],
        [{"id": "A", "loaded": True}, {"id": "B", "is_loading": False}],
        [{"id": "A", "loaded": True}, "B"],
    ],
    ids=["no-loaded-key", "loaded-not-boolean", "one-entry-without", "entry-not-an-object"],
)
def test_omlx_status_with_an_entry_lacking_boolean_loaded_is_no_listing(monkeypatch, models):
    # Final review F5: an entry with no boolean `loaded` read as "not loaded".
    # Were omlx to rename the key, every status answer would say nothing is
    # loaded and every omlx flag would clear. A listing this code cannot read
    # is no listing.
    def fake_json(url):
        if url.endswith("/v1/models/status"):
            return {"models": models}
        return {"engine_pool": {"loaded_count": 1, "model_count": 2}}

    monkeypatch.setattr(local_control, "_http_json", fake_json)
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda p, timeout=0.0: None)
    assert local_control._omlx_status_loaded("http://x") is None
    assert _probe_running("omlx", "B", "http://x") is None


def test_omlx_status_with_no_models_is_an_answer(monkeypatch):
    # An empty pool is a listing that says nothing is loaded, not "no listing".
    monkeypatch.setattr(local_control, "_http_json", lambda url: {"models": []})
    assert local_control._omlx_status_loaded("http://x") == []


def test_the_omlx_probe_never_runs_the_real_wt_served_by_default(monkeypatch):
    # Final review F6: wt_bridge.served_ids shells out directly, past both
    # bridge seams, and the probe reaches it whenever status gives no listing
    # (the suite's default). conftest must stub it.
    def no_subprocess(*args, **kwargs):
        raise AssertionError("the probe ran `wt served`")

    monkeypatch.setattr(wt_bridge.shutil, "which", lambda name: "/usr/local/bin/wt")
    monkeypatch.setattr(wt_bridge.subprocess, "run", no_subprocess)
    assert local_control._omlx_loaded("http://x", []) is None


def _omlx_probe(listed, health, status=None):
    """_probe_running for omlx/A against a server listing `listed` on
    /v1/models, answering /health with `health` and /v1/models/status with
    `status` (None = no answer from either)."""

    def answer(url):
        return status if url.endswith("/v1/models/status") else health

    with (
        patch("modelman.local_control._http_models_ids", return_value=listed),
        patch("modelman.local_control._http_json", side_effect=answer),
    ):
        return _probe_running("omlx", "A", "http://localhost:8000")


def test_probe_running_omlx_listed_but_unloaded_is_not_running():
    # wt#201: omlx's /v1/models lists every model in its pool, loaded or not,
    # so a listed model read as "running" for as long as the service was up —
    # and `modelman start` on it was a no-op exactly when a load was needed.
    # /health carries the pool's counts with no key: nothing loaded means not
    # running, whatever the list says. Status is asked first and says the same.
    idle = {"status": "healthy", "engine_pool": {"model_count": 2, "loaded_count": 0}}
    none_loaded = {"models": [{"id": "A", "loaded": False}, {"id": "B", "loaded": False}]}
    assert _omlx_probe(["A", "B"], idle, none_loaded) is False


def test_probe_running_omlx_all_loaded_reads_the_list():
    # Every pool model loaded and every one listed: the list is the answer.
    full = {"engine_pool": {"model_count": 2, "loaded_count": 2}}
    both = {"models": [{"id": "A", "loaded": True}, {"id": "B", "loaded": True}]}
    assert _omlx_probe(["A", "B"], full, both) is True
    # A hidden model is counted but not listed; status reports the whole pool,
    # so A reads as loaded all the same.
    hidden = {"models": [{"id": "A", "loaded": True}, {"id": "H", "loaded": True}]}
    assert _omlx_probe(["A"], full, hidden) is True


def _omlx_probe_with_status(status):
    """_probe_running for omlx/A on a two-model pool with one loaded, whose
    /v1/models/status answers `status`."""
    mixed = {"engine_pool": {"model_count": 2, "loaded_count": 1}}

    def answer(url):
        return status if url.endswith("/v1/models/status") else mixed

    with (
        patch("modelman.local_control._http_models_ids", return_value=["A", "B"]),
        patch("modelman.local_control._http_json", side_effect=answer),
    ):
        return _probe_running("omlx", "A", "http://localhost:8000")


def test_probe_running_omlx_mixed_pool_asks_status_without_a_key():
    # Some loaded, not all: the counts cannot say which, and the status
    # endpoint can. An omlx with no API key answers it unasked-for, which
    # settles the normal state of a multi-model pool both ways — a flag left
    # on A after omlx was switched to B outside modelman is cleared, where
    # "unknown" kept it and `modelman stop A` then stopped the server under B.
    def models(**loaded):
        return {"models": [{"id": k, "loaded": v, "is_loading": False} for k, v in loaded.items()]}

    assert _omlx_probe_with_status(models(A=True, B=False)) is True
    assert _omlx_probe_with_status(models(A=False, B=True)) is False
    # Gone from the pool altogether is as definite as unloaded.
    assert _omlx_probe_with_status(models(B=True)) is False
    # Mid-load occupies the server as much as loaded does (wt's reading).
    loading = {"models": [{"id": "A", "loaded": False, "is_loading": True}]}
    assert _omlx_probe_with_status(loading) is True


def test_probe_running_omlx_asks_wt_when_status_wants_a_key():
    # An omlx with an API key refuses status without it, and modelman
    # resolves no secret_ref. wt does, so it is asked — and its answer
    # settles a keyed pool the same way the keyless status settles the rest.
    refused = {"detail": "Invalid API key"}
    with patch("modelman.local_control.wt_bridge.served_ids", return_value=["B"]) as wt_served:
        assert _omlx_probe_with_status(refused) is False
    wt_served.assert_called_once_with("omlx")
    with patch("modelman.local_control.wt_bridge.served_ids", return_value=["A"]):
        assert _omlx_probe_with_status(refused) is True
    # Nothing else shells out: a pool whose status answers never reaches wt.
    answered = {"models": [{"id": "A", "loaded": True}]}
    with patch("modelman.local_control.wt_bridge.served_ids") as wt_served:
        assert _omlx_probe_with_status(answered) is True
    wt_served.assert_not_called()


def test_probe_running_omlx_mixed_pool_is_unknown_when_nothing_can_say():
    # #249: status refuses and wt cannot say either (not installed, no
    # secret_ref on the row, timed out) — so this is unknown, and the callers
    # that guard a flag leave the flag alone. Reading it as "not running"
    # cleared the flag of a model the server was still serving: `modelman
    # stop` no-opped, `stop --all` skipped it, and the process stayed
    # resident with nothing in modelman.toml saying so.
    with patch("modelman.local_control.wt_bridge.served_ids", return_value=None):
        assert _omlx_probe_with_status({"detail": "Invalid API key"}) is None
        assert _omlx_probe_with_status(None) is None
        mixed = {"engine_pool": {"model_count": 2, "loaded_count": 1}}
        assert _omlx_probe(["A", "B"], mixed) is None


def test_probe_running_omlx_without_health_counts_falls_back_to_the_list():
    # An omlx whose /health answers with no pool counts predates the counts:
    # the list is all there is, as before.
    assert _omlx_probe(["A"], {}) is True
    assert _omlx_probe(["A"], {"status": "healthy"}) is True
    assert _omlx_probe(["B"], {"status": "healthy"}) is False
    # No /health answer at all is not that case (final review F2): nothing
    # answered, so nothing says the model is not loaded.
    assert _omlx_probe(["A"], None) is None


def test_probe_running_mtplx_does_not_consult_health():
    # Only omlx lists a pool; an mtplx process serves one model, so its list
    # is already what is loaded.
    with (
        patch("modelman.local_control._http_models_ids", return_value=["Org/Q"]),
        patch("modelman.local_control._http_json", side_effect=AssertionError("asked /health")),
    ):
        assert _probe_running("mtplx", "Org/Q", "http://localhost:8003") is True


def test_discovered_models_lists_an_artifact_once_per_discovered_id():
    # wt#195: omlx and omlx-6bit are two registry rows for one server and one
    # model directory, so both rows list every artifact in it. The discovered
    # id is per FAMILY ("omlx/<name>" for both), and the TUI's "+" rows,
    # `modelman start`'s listing and name resolution all key on it: without
    # the dedup one model directory shows as two rows and a bare name is
    # "ambiguous" between two spellings of the same id. One entry survives,
    # under the provider row that sorts first.
    registry = Registry(
        providers=[
            ProviderEntry(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="omlx-6bit", name="oMLX 6-bit", location="local", auth=AuthConfig(type="none")
            ),
        ]
    )
    local_map = {
        ("omlx-6bit", "Shared-4bit"): {"path": "/m/Shared-4bit", "size_bytes": 7},
        ("omlx", "Shared-4bit"): {"path": "/m/Shared-4bit", "size_bytes": 7},
        ("omlx-6bit", "Other-6bit"): {"path": "/m/Other-6bit", "size_bytes": 9},
    }
    found = discover_unregistered_models(registry, local_map)
    assert sorted(d.model_id for d in found) == ["omlx/Other-6bit", "omlx/Shared-4bit"]
    shared = next(d for d in found if d.variant_id == "Shared-4bit")
    assert shared.provider_id == "omlx"


# --- #194 follow-ups ---------------------------------------------------


def _ollama_model(model_id: str, name: str) -> ModelEntry:
    return ModelEntry(
        id=model_id, family="f", provider_id="ollama", model_name=name, location="local"
    )


# conftest replaces the pulled check with a no-op for the whole suite; these
# tests are about the check, so they put the real one back.
_REAL_REQUIRE_OLLAMA_PULLED = local_control._require_ollama_pulled


def _start_with_ollama_tags(registry, model_id, state_path, tags, asked=None):
    """start_local_model with the real pulled check and `/api/tags` answering
    `tags` (None = no usable answer)."""

    def fake_json(url: str, timeout: float = 2.0):
        if asked is not None:
            asked.append(url)
        return tags

    with (
        patch("modelman.local_control._http_json", fake_json),
        patch("modelman.local_control._require_ollama_pulled", _REAL_REQUIRE_OLLAMA_PULLED),
    ):
        return start_local_model(registry, model_id, state_path)


def test_start_refuses_a_registered_ollama_model_that_is_not_pulled(tmp_path):
    # #194 (#18): ollama's start is flag-only, so starting a registered model
    # that was never pulled printed "Started", flagged it running and routed
    # nothing — wt routes an ollama model only while it is pulled. Refuse it
    # and name the command that fixes it; nothing is flagged.
    registry = _registry()
    registry.models.append(_ollama_model("ollama/ghost:1b", "ghost:1b"))
    state_path = _state_path(tmp_path)
    with pytest.raises(LocalControlError, match="not pulled — ollama pull ghost:1b"):
        _start_with_ollama_tags(
            registry, "ollama/ghost:1b", state_path, {"models": [{"name": "other:1b"}]}
        )
    assert load_state(state_path).models == {}


@pytest.mark.parametrize(
    "tags",
    [
        None,  # refused, timed out, or not JSON
        {"error": "boom"},  # answered, but not a tags listing
        {"models": None},
    ],
)
def test_start_does_not_refuse_ollama_when_presence_is_unknown(tmp_path, tags):
    # The refusal needs a positive "not pulled". A tags read that gives no
    # usable listing is "unknown", and the start goes ahead as before rather
    # than blocking on a flaky probe.
    registry = _registry()
    registry.models.append(_ollama_model("ollama/ghost:1b", "ghost:1b"))
    state_path = _state_path(tmp_path)
    result = _start_with_ollama_tags(registry, "ollama/ghost:1b", state_path, tags)
    assert result.model_id == "ollama/ghost:1b"


def test_start_ollama_pull_check_asks_the_configured_origin(tmp_path):
    # The pull check used to run the local `ollama list` CLI while the daemon
    # check and wt read the provider row's base_url — so with ollama on
    # another host or port, a model pulled THERE was refused as "not pulled"
    # because the local daemon did not have it. Both checks ask the same
    # `/api/tags` now, and the local CLI is not consulted at all.
    registry = _registry()
    registry.providers[0].auth = AuthConfig(type="none", base_url="http://127.0.0.1:12345/v1")
    registry.models.append(_ollama_model("ollama/remote:1b", "remote:1b"))
    state_path = _state_path(tmp_path)
    asked: list[str] = []
    local_cli = {"ollama": [{"variant_id": "other:1b", "path": "ollama:other:1b", "size_bytes": 1}]}
    with _patch_provider_local_models(local_cli):
        result = _start_with_ollama_tags(
            registry,
            "ollama/remote:1b",
            state_path,
            {"models": [{"name": "remote:1b"}]},
            asked,
        )
    assert result.model_id == "ollama/remote:1b"
    assert asked == ["http://127.0.0.1:12345/api/tags"]


def test_start_ollama_pull_check_reads_tags_the_way_wt_does(tmp_path):
    # Mirrors wt's ollamaModelNames + OllamaNameMatches: an untagged overlay
    # owns the pulled ":latest", and an entry with a remote_host is an
    # ollama.com cloud model — nothing is on disk, so it is not "pulled".
    registry = _registry()
    registry.models.append(_ollama_model("ollama/llama3.2", "llama3.2"))
    registry.models.append(_ollama_model("ollama/cloudy:1b", "cloudy:1b"))
    state_path = _state_path(tmp_path)
    tags = {
        "models": [
            {"name": "llama3.2:latest"},
            {"name": "cloudy:1b", "remote_host": "https://ollama.com:443"},
        ]
    }
    assert _start_with_ollama_tags(registry, "ollama/llama3.2", state_path, tags).model_id == (
        "ollama/llama3.2"
    )
    with pytest.raises(LocalControlError, match="not pulled — ollama pull cloudy:1b"):
        _start_with_ollama_tags(registry, "ollama/cloudy:1b", state_path, tags)


def test_start_exact_discovered_id_beats_a_registered_models_name(tmp_path):
    # #194: with overlay ollama/qwen3:8b and a pulled someuser/qwen3:8b, the
    # listing prints the second as `ollama/someuser/qwen3:8b` — but typing
    # that started the REGISTERED model, because the registered-name step
    # matches on the name's tail and ran first. The exact discovered id wins.
    registry = _registry()
    registry.models.append(_ollama_model("ollama/qwen3:8b", "qwen3:8b"))
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {"variant_id": "qwen3:8b", "path": "ollama:qwen3:8b", "size_bytes": 1},
            {
                "variant_id": "someuser/qwen3:8b",
                "path": "ollama:someuser/qwen3:8b",
                "size_bytes": 2,
            },
        ]
    }
    with _patch_provider_local_models(mapping):
        assert (
            start_local_model(registry, "ollama/someuser/qwen3:8b", state_path).model_id
            == "ollama/someuser/qwen3:8b"
        )
        # The registered model is still reached by its own id and its name.
        assert start_local_model(registry, "qwen3:8b", state_path).model_id == "ollama/qwen3:8b"


def test_start_bare_repo_id_whose_org_is_a_cloud_provider_id(tmp_path):
    # #194: a typed "<prefix>/<rest>" is refused when <prefix> names a
    # provider, since the prefix then says WHERE the model is. That check
    # counted cloud provider ids too, so a bare repo id whose org happens to
    # equal one ("openrouter/…" here) was refused although no local model can
    # live on a cloud provider. Typed in full, such a name is the artifact's
    # name. (A cloud prefix in front of a SHORTER artifact name is still not
    # that artifact — test_start_wrong_family_prefix_never_starts_… pins it.)
    registry = _registry()
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {
                "variant_id": "openrouter/tiny:1b",
                "path": "ollama:openrouter/tiny:1b",
                "size_bytes": 1,
            }
        ]
    }
    with _patch_provider_local_models(mapping):
        result = start_local_model(registry, "openrouter/tiny:1b", state_path)
    assert result.model_id == "ollama/openrouter/tiny:1b"
    # A LOCAL provider prefix still says where: no silent cross-provider guess.
    mapping = {"ollama": [{"variant_id": "foo", "path": "ollama:foo", "size_bytes": 1}]}
    with (
        _patch_provider_local_models(mapping),
        pytest.raises(LocalControlError, match="unknown model"),
    ):
        start_local_model(registry, "omlx/foo", state_path)


def test_stop_of_a_discovered_model_leaves_no_state_row(tmp_path):
    # #194: a discovered model has no registry entry, so its modelman.toml row
    # exists only to carry the running flag. Stopping it left an all-default
    # `[model_state."<id>"]` row behind for good. A row that says nothing is
    # removed; one that still carries data (a cached size) is kept.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/disc:1b", ModelState(running=True))
    store.set("ollama/sized:1b", ModelState(running=True, size_bytes=5))
    save_state(store, state_path)
    with patch("modelman.local_control._stop_ollama_model"):
        stop_local_model("ollama/disc:1b", state_path)
        stop_local_model("ollama/sized:1b", state_path)
    models = load_state(state_path).models
    assert "ollama/disc:1b" not in models
    assert models["ollama/sized:1b"] == ModelState(running=False, size_bytes=5)


def test_stop_all_leaves_no_all_default_state_rows(tmp_path):
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/disc:1b", ModelState(running=True))
    save_state(store, state_path)
    with patch("modelman.local_control.stop_all_local_providers"):
        stop_all_local_models(state_path)
    assert load_state(state_path).models == {}


def test_listing_omits_an_artifact_whose_discovered_id_is_a_registry_id():
    # #194: registry `ollama/foo` names artifact "bar"; an artifact "foo" is
    # also on disk. Its discovered id would be `ollama/foo` — the registered
    # model's id — so it can be neither started (start refuses, naming the
    # clash) nor routed (wt drops it). Listing it under Discovered offered an
    # id that then failed, and the TUI's "+" row would register a duplicate id.
    registry = _registry()
    registry.models.append(_ollama_model("ollama/foo", "bar"))
    mapping = {
        "ollama": [
            {"variant_id": "foo", "path": "ollama:foo", "size_bytes": 1},
            {"variant_id": "free", "path": "ollama:free", "size_bytes": 2},
        ]
    }
    with _patch_provider_local_models(mapping):
        assert [d.model_id for d in discover_unregistered_models(registry)] == ["ollama/free"]
        listed = inventory_local_models(registry, StateStore()).discovered
        assert [d.model_id for d in listed] == ["ollama/free"]
        # Starting it by name still explains the clash instead of "unknown model".
        with pytest.raises(LocalControlError, match="already the id of a registered model"):
            start_local_model(registry, "foo", None)


def test_listing_omits_ollama_cloud_entries():
    # `ollama list` also prints the cloud models the account can reach, with
    # "-" for a size: nothing is on disk, and wt's inventory leaves them out.
    # modelman listed them as discovered local models that could be "started".
    from modelman.providers.ollama import OllamaProvider

    out = (
        "NAME                        ID            SIZE     MODIFIED\n"
        "qwen3.6:35b-mlx             e92a3e94bbca  23 GB    5 hours ago\n"
        "nemotron-3-nano:30b-cloud   2fd759a2eb63  -        3 days ago\n"
    )
    runner = MagicMock(return_value=MagicMock(stdout=out, returncode=0))
    names = [m["variant_id"] for m in OllamaProvider({}).list_local(runner=runner)]
    assert names == ["qwen3.6:35b-mlx"]


@pytest.mark.parametrize(
    ("registered_name", "typed", "want"),
    [
        # A pulled model's full name, namespace included, is that model — not
        # the registered one its tail happens to spell.
        ("qwen3:8b", "someuser/qwen3:8b", "ollama/someuser/qwen3:8b"),
        # ...also when the namespace is a cloud provider's id.
        ("qwen3:8b", "openrouter/qwen3:8b", "ollama/openrouter/qwen3:8b"),
        # And the reverse: a bare name is not a registered namespaced model.
        ("someuser/qwen3:8b", "qwen3:8b", "ollama/qwen3:8b"),
    ],
)
def test_start_ollama_native_name_is_exact_not_a_tail_match(tmp_path, registered_name, typed, want):
    # #194 made the exact discovered ID beat the registered-name step, which
    # compared name tails. The NAME the listing shows next to it still went
    # through that step: `modelman start someuser/qwen3:8b` started the
    # registered `qwen3:8b`. An ollama name is exact (wt's OllamaNameMatches;
    # _artifact_matches here) — a namespace is part of it.
    registry = _registry()
    registry.models.append(_ollama_model("ollama/registered", registered_name))
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {"variant_id": name, "path": f"ollama:{name}", "size_bytes": 1}
            for name in (registered_name, want.removeprefix("ollama/"))
        ]
    }
    with _patch_provider_local_models(mapping):
        assert start_local_model(registry, typed, state_path).model_id == want
        # The registered model is still reached by its name, bare or prefixed.
        for spelling in (registered_name, f"ollama/{registered_name}"):
            assert start_local_model(registry, spelling, state_path).model_id == "ollama/registered"


def test_start_prefix_of_a_legacy_location_provider_row_is_a_local_claim(tmp_path):
    # A provider row with no `location` is local (is_local_location) — the
    # legacy spelling. Its id as a typed prefix therefore says WHERE the model
    # is, exactly like a `location = "local"` row's: it must not fall into the
    # cloud-prefix exception and start another provider's artifact.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="legacybox", name="Legacy", location=None, auth=AuthConfig(type="none"))
    )
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [{"variant_id": "legacybox/foo", "path": "ollama:legacybox/foo", "size_bytes": 1}]
    }
    with (
        _patch_provider_local_models(mapping),
        pytest.raises(LocalControlError, match="unknown model"),
    ):
        start_local_model(registry, "legacybox/foo", state_path)
    assert load_state(state_path).models == {}


def test_stale_flag_clear_leaves_no_all_default_state_row(tmp_path):
    # The row-dropping clear covers every flag clear, not only the stops: a
    # discovered model whose process died (running_model_ids' probe), or that
    # a start on the same single-model provider replaced, lost its flag
    # through _clear_stale_running_flag and kept an empty row for good.
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none"))
    )
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("mtplx/Org/gone", ModelState(running=True))
    store.set("mtplx/Org/sized", ModelState(running=True, size_bytes=5))
    save_state(store, state_path)
    with patch("modelman.local_control._probe_running", return_value=False):
        assert running_model_ids(registry, load_state(state_path), state_path) == []
    models = load_state(state_path).models
    assert "mtplx/Org/gone" not in models
    assert models["mtplx/Org/sized"] == ModelState(running=False, size_bytes=5)


def test_start_bare_name_that_is_exactly_one_artifact_is_not_ambiguous(tmp_path):
    # #194 review: with `qwen3:8b` and `someuser/qwen3:8b` both pulled and
    # unregistered, the lenient tail match found both and `modelman start
    # qwen3:8b` failed as ambiguous — though one of them is named exactly.
    # An exact name beats a tail match, slash or no slash.
    registry = _registry()
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [
            {"variant_id": "qwen3:8b", "path": "ollama:qwen3:8b", "size_bytes": 1},
            {
                "variant_id": "someuser/qwen3:8b",
                "path": "ollama:someuser/qwen3:8b",
                "size_bytes": 1,
            },
        ]
    }
    with _patch_provider_local_models(mapping):
        result = start_local_model(registry, "qwen3:8b", state_path)
    assert result.model_id == "ollama/qwen3:8b"


def test_resolving_a_registered_repo_id_lists_no_provider():
    # #194 review: an exact discovered id is `<local family>/<name>`, so only
    # a typed name with such a prefix can be one. A registered model's repo id
    # ("org/model-a") has a "/" too, and used to pay for an `ollama list` plus
    # the omlx and mtplx directory scans before its registry match.
    registry = _registry()
    with (
        patch("modelman.local_control.model_has_local_artifact", return_value=True),
        patch(
            "modelman.local_control._find_discovered",
            side_effect=AssertionError("listed the providers"),
        ),
    ):
        model = local_control._resolve_local_model(registry, "org/model-a")
    assert model.id == "omlx/model-a"


def _omlx_pool_registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="omlx", name="oMLX", location="local", auth=AuthConfig(type="none"))
        ],
        models=[
            ModelEntry(
                id="omlx/a",
                family="f",
                provider_id="omlx",
                model_name="org/A-4bit",
                location="local",
            ),
            ModelEntry(
                id="omlx/b",
                family="f",
                provider_id="omlx",
                model_name="org/B-4bit",
                location="local",
            ),
        ],
    )


def _stub_wt_start(monkeypatch, outcome=None, error=None):
    calls: list[tuple[str, bool]] = []

    def fake(model_id, *, replace, timeout=0.0):
        calls.append((model_id, replace))
        if error is not None:
            raise error
        return outcome

    monkeypatch.setattr(local_control.wt_bridge, "start", fake)
    return calls


def test_start_omlx_delegates_to_wt_and_keeps_the_sibling_flag(tmp_path, monkeypatch):
    # #213: omlx is a pool. Starting a second omlx model must leave the first
    # one loaded and flagged; modelman used to stop the omlx service first,
    # taking down a model another session was using.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    calls = _stub_wt_start(
        monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="started", unloaded=[])
    )
    stop_provider = MagicMock()
    monkeypatch.setattr(local_control, "stop_provider", stop_provider)
    monkeypatch.setattr(
        local_control, "isolate_provider", MagicMock(side_effect=AssertionError("isolate"))
    )

    result = start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert calls == [("omlx/b", True)]
    stop_provider.assert_not_called()
    state = load_state(state_path)
    assert state.get("omlx/a").running and state.get("omlx/b").running
    assert (result.already_running, result.unloaded, result.other_running) == (
        False,
        [],
        ["omlx/a"],
    )


def test_start_omlx_clears_the_flags_of_models_wt_says_it_unloaded(tmp_path, monkeypatch):
    # When the new model does not fit, omlx unloads others and wt reports
    # them. Their flags must go, or they read as running forever and
    # `modelman stop` on them would try to unload a model that is not loaded.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_start(
        monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="started", unloaded=["omlx/a"])
    )

    result = start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    state = load_state(state_path)
    assert not state.get("omlx/a").running and state.get("omlx/b").running
    assert (result.unloaded, result.other_running) == (["omlx/a"], [])


def test_start_omlx_ignores_an_unloaded_model_with_no_flag(tmp_path, monkeypatch):
    # wt can unload a model modelman never flagged (one wt itself started).
    # That must not fail the start or leave an empty state row behind.
    state_path = _state_path(tmp_path)
    _stub_wt_start(
        monkeypatch,
        wt_bridge.StartOutcome(id="omlx/b", status="started", unloaded=["omlx/Stray-4bit"]),
    )

    start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert "omlx/Stray-4bit" not in load_state(state_path).models


def test_start_omlx_reports_already_running_from_wt(tmp_path, monkeypatch):
    # wt is the one that knows whether the model is loaded; a model it calls
    # already running gets its flag set (it may have been started by wt).
    state_path = _state_path(tmp_path)
    _stub_wt_start(
        monkeypatch, wt_bridge.StartOutcome(id="omlx/b", status="already_running", unloaded=[])
    )

    result = start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert result.already_running and load_state(state_path).get("omlx/b").running


def test_start_omlx_failure_changes_no_flag(tmp_path, monkeypatch):
    # A start wt refuses (no room, a refused key) must not flag the model, and
    # must not clear anyone else's flag on a guess.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_start(monkeypatch, error=wt_bridge.WtBridgeError("omlx has no room for B-4bit"))

    with pytest.raises(LocalControlError, match="failed to start omlx/b: omlx has no room"):
        start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    state = load_state(state_path)
    assert state.get("omlx/a").running and not state.get("omlx/b").running


def test_start_omlx_failure_includes_sync_warnings(tmp_path, monkeypatch):
    # The failure raises, so a failed sync's warning travels in the error
    # text rather than being dropped — as on the isolate path.
    state_path = _state_path(tmp_path)
    _stub_wt_start(monkeypatch, error=wt_bridge.WtBridgeError("omlx has no room for B-4bit"))
    monkeypatch.setattr(
        wt_bridge, "sync", MagicMock(side_effect=wt_bridge.WtBridgeError("wt not on PATH"))
    )

    with pytest.raises(LocalControlError, match="omlx has no room") as excinfo:
        start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert "may not be synced" in str(excinfo.value)


def test_start_omlx_timeout_says_the_model_may_still_be_loading(tmp_path, monkeypatch):
    # Final review F8: on a timeout the bridge kills wt, not omlx, so the load
    # may go on. The user has to hear that before retrying; no flag changes.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_start(
        monkeypatch, error=wt_bridge.WtBridgeTimeoutError("wt start timed out after 660s")
    )

    with pytest.raises(LocalControlError, match="failed to start omlx/b") as exc:
        start_local_model(_omlx_pool_registry(), "omlx/b", state_path)

    assert "wt start timed out after 660s" in str(exc.value)
    assert "may still be loading in omlx" in str(exc.value)
    state = load_state(state_path)
    assert state.get("omlx/a").running and not state.get("omlx/b").running


def test_is_omlx_family_covers_both_omlx_rows():
    # omlx and omlx-6bit are one server; every other provider is not it.
    assert local_control.is_omlx_family("omlx") and local_control.is_omlx_family("omlx-6bit")
    assert not local_control.is_omlx_family("mtplx")
    assert not local_control.is_omlx_family("ollama")


def _stub_wt_stop(monkeypatch, error=None, served=(), refused=False):
    """Stub wt's stop and its pool read, and the refused-connection check.
    Returns (stop targets, urls the refused check was made against)."""
    calls: list[str] = []
    refused_urls: list[str] = []

    def fake(target, timeout=0.0):
        calls.append(target)
        if error is not None:
            raise error

    def fake_refused(url, timeout=0.0):
        refused_urls.append(url)
        return refused

    monkeypatch.setattr(local_control.wt_bridge, "stop", fake)
    monkeypatch.setattr(local_control.wt_bridge, "served_ids", lambda provider, timeout=0.0: served)
    monkeypatch.setattr(local_control, "_connection_refused", fake_refused)
    return calls, refused_urls


def test_stop_omlx_unloads_one_model_through_wt(tmp_path, monkeypatch):
    # #213: stopping one omlx model must leave its siblings loaded and
    # flagged. modelman used to run `omlx stop`, halting every loaded model.
    state_path = _state_path(tmp_path, {"omlx/a": True, "omlx/b": True})
    calls, _ = _stub_wt_stop(monkeypatch)
    stop_provider = MagicMock()
    monkeypatch.setattr(local_control, "stop_provider", stop_provider)

    result = stop_local_model("omlx/a", state_path)

    assert calls == ["omlx/a"] and result.stopped_model_id == "omlx/a"
    stop_provider.assert_not_called()
    state = load_state(state_path)
    assert not state.get("omlx/a").running and state.get("omlx/b").running


def test_stop_omlx_clears_the_flag_of_a_model_wt_says_is_not_running(tmp_path, monkeypatch):
    # The model was evicted or timed out since it was flagged. wt can read
    # the pool and it is not there: the goal is met, so the flag goes.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(
        monkeypatch,
        error=wt_bridge.WtBridgeError('model "omlx/a" is not running'),
        served=["B-4bit"],
    )

    result = stop_local_model("omlx/a", state_path, registry=_omlx_pool_registry())

    assert result.stopped_model_id == "omlx/a"
    assert not load_state(state_path).get("omlx/a").running


_WT_SAYS_NOT_RUNNING = wt_bridge.WtBridgeError('model "omlx/a" is not running')


def test_stop_omlx_keeps_the_flag_when_the_pool_cannot_be_read(tmp_path, monkeypatch):
    # #249's rule: "cannot say" is not "stopped". A keyed omlx whose key the
    # registry does not name makes wt call the model not running while it is
    # loaded; clearing the flag would leave a resident model nothing records.
    # omlx is up here (its port accepts the connection), so nothing shows the
    # model is gone.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _, refused_urls = _stub_wt_stop(
        monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=None, refused=False
    )

    with pytest.raises(LocalControlError, match="secret_ref") as exc:
        stop_local_model("omlx/a", state_path, registry=_omlx_pool_registry())

    assert "modelman stop --all" in str(exc.value)
    assert refused_urls == ["http://localhost:8000/health"]  # it did look
    assert load_state(state_path).get("omlx/a").running


def test_stop_omlx_clears_the_flag_when_omlx_refuses_the_connection(tmp_path, monkeypatch):
    # Nothing listens on omlx's port: no server, so nothing is loaded, and a
    # flagged model is stopped. The user must be able to clear a flag left by
    # a crashed server. Asked at the origin the registry gives omlx — here
    # none, so the default.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _, refused_urls = _stub_wt_stop(
        monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=None, refused=True
    )

    result = stop_local_model("omlx/a", state_path, registry=_omlx_pool_registry())

    assert result.stopped_model_id == "omlx/a"
    assert refused_urls == ["http://localhost:8000/health"]
    assert not load_state(state_path).get("omlx/a").running


def test_stop_omlx_asks_the_registrys_origin_whether_omlx_is_down(tmp_path, monkeypatch):
    # An omlx on another host or port: a refused connection at localhost:8000
    # says nothing about it. Asking there cleared the flag of a loaded model
    # on every stop.
    registry = _omlx_pool_registry()
    registry.providers[0].auth = AuthConfig(type="none", base_url="http://10.0.0.5:9000/v1")
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _, refused_urls = _stub_wt_stop(
        monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=None, refused=False
    )

    with pytest.raises(LocalControlError, match="cannot tell whether omlx/a is loaded"):
        stop_local_model("omlx/a", state_path, registry=registry)

    assert refused_urls == ["http://10.0.0.5:9000/health"]
    assert load_state(state_path).get("omlx/a").running


def test_stop_omlx_origin_comes_from_the_omlx_6bit_row_when_it_is_the_only_one(
    tmp_path, monkeypatch
):
    # omlx and omlx-6bit are one server; a registry may define only the
    # omlx-6bit row, and a discovered model's id is `omlx/<name>` either way.
    registry = _omlx_6bit_only_registry()
    registry.providers[0].auth = AuthConfig(type="none", base_url="http://10.0.0.5:9000")
    state_path = _state_path(tmp_path, {"omlx/Stray-6bit": True})
    _, refused_urls = _stub_wt_stop(
        monkeypatch,
        error=wt_bridge.WtBridgeError('unknown model "omlx/Stray-6bit"'),
        served=None,
        refused=True,
    )

    assert stop_local_model("omlx/Stray-6bit", state_path, registry=registry).stopped_model_id
    assert refused_urls == ["http://10.0.0.5:9000/health"]


def test_stop_omlx_without_a_registry_never_takes_the_down_shortcut(tmp_path, monkeypatch):
    # No registry, no origin to ask: even a refused connection at the default
    # port proves nothing, so the flag stays and the user is told.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _, refused_urls = _stub_wt_stop(
        monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=None, refused=True
    )

    with pytest.raises(LocalControlError, match="cannot tell whether omlx/a is loaded"):
        stop_local_model("omlx/a", state_path)

    assert refused_urls == []
    assert load_state(state_path).get("omlx/a").running


def test_stop_omlx_clears_the_flag_of_a_model_wt_no_longer_knows(tmp_path, monkeypatch):
    # A discovered omlx model (no registry row) that is no longer loaded is
    # not "not running" to wt but unknown: it lists such a model only while
    # it runs. With the pool readable that is the same answer.
    state_path = _state_path(tmp_path, {"omlx/Stray-4bit": True})
    _stub_wt_stop(
        monkeypatch,
        error=wt_bridge.WtBridgeError('unknown model "omlx/Stray-4bit"'),
        served=["B-4bit"],
    )

    result = stop_local_model("omlx/Stray-4bit", state_path, registry=_omlx_pool_registry())

    assert result.stopped_model_id == "omlx/Stray-4bit"
    assert not load_state(state_path).get("omlx/Stray-4bit").running


def test_stop_omlx_keeps_the_flag_when_omlx_reports_the_model_loaded(tmp_path, monkeypatch):
    # Final review F1: wt refused the stop as "not running", but the pool it
    # then read holds the model (wt's own read was transiently unusable, or it
    # could not map the id). A readable pool is not an absent model: the flag
    # was cleared for a model omlx still had loaded.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _, refused_urls = _stub_wt_stop(
        monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=["A-4bit", "B-4bit"], refused=True
    )

    with pytest.raises(LocalControlError, match="omlx reports it loaded") as exc:
        stop_local_model("omlx/a", state_path, registry=_omlx_pool_registry())

    assert "wt refused to stop omlx/a" in str(exc.value)
    assert refused_urls == []  # a pool that answered is not asked whether it is down
    assert load_state(state_path).get("omlx/a").running


def test_stop_omlx_keeps_the_flag_of_a_discovered_model_omlx_reports_loaded(tmp_path, monkeypatch):
    # A discovered id is `omlx/<directory name>`, so its tail is the name omlx
    # serves it under — with or without a registry to say so.
    for registry in (None, _omlx_pool_registry()):
        state_path = _state_path(tmp_path, {"omlx/Stray-4bit": True})
        _stub_wt_stop(
            monkeypatch,
            error=wt_bridge.WtBridgeError('unknown model "omlx/Stray-4bit"'),
            served=["Stray-4bit"],
        )

        with pytest.raises(LocalControlError, match="omlx reports it loaded"):
            stop_local_model("omlx/Stray-4bit", state_path, registry=registry)

        assert load_state(state_path).get("omlx/Stray-4bit").running


def test_stop_omlx_clears_the_flag_when_the_pool_is_empty(tmp_path, monkeypatch):
    # Nothing is loaded, so this model is not: no name is needed to say so,
    # and so no registry either.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=[])

    result = stop_local_model("omlx/a", state_path)

    assert result.stopped_model_id == "omlx/a"
    assert not load_state(state_path).get("omlx/a").running


def test_stop_omlx_keeps_the_flag_when_the_models_omlx_name_is_not_known(tmp_path, monkeypatch):
    # No registry was given and the pool holds models. `omlx/a` is a registry
    # id here (model_name org/A-4bit), and its tail `a` is no directory name:
    # that `a` is not among the served ids does not say A-4bit is not. The
    # flag stays and the user is told.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(monkeypatch, error=_WT_SAYS_NOT_RUNNING, served=["A-4bit"])

    with pytest.raises(LocalControlError, match="cannot tell whether omlx/a is loaded") as exc:
        stop_local_model("omlx/a", state_path)

    assert "modelman stop --all" in str(exc.value)
    assert load_state(state_path).get("omlx/a").running


def test_stop_omlx_surfaces_any_other_wt_failure(tmp_path, monkeypatch):
    # A refused key or a failed unload is a real failure: the model may still
    # be loaded, so the flag stays and the user sees wt's reason.
    state_path = _state_path(tmp_path, {"omlx/a": True})
    _stub_wt_stop(
        monkeypatch,
        error=wt_bridge.WtBridgeError("omlx still has A-4bit loaded"),
        served=["A-4bit"],
    )

    with pytest.raises(LocalControlError, match="failed to stop omlx/a: omlx still has"):
        stop_local_model("omlx/a", state_path)

    assert load_state(state_path).get("omlx/a").running


def test_omlx_has_no_single_occupant(tmp_path):
    # The TUI asks this before a start to warn about a replacement. omlx
    # holds several models, so there is no occupant to name; what a start
    # would unload comes from wt's plan instead.
    state = load_state(_state_path(tmp_path, {"omlx/a": True}))
    assert same_provider_occupant(_omlx_pool_registry(), state, "omlx/b", "omlx") is None
