"""Unit tests for modelman.local_control — the start/stop orchestration
behind `modelman start`/`modelman stop` (issue #65). Isolation subprocess
calls are mocked; these tests cover validation, probe-based idempotency,
stale-marker recovery, and marker mutation — not bin/llm-isolate-provider
itself (see tests/benchmark/test_isolation.py for that)."""

from dataclasses import replace
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from modelman.benchmark.isolation import IsolateResult
from modelman.litellm import ExposeError
from modelman.local_control import (
    DiscoveredModel,
    DiscoveredModelNeedsFamily,
    InventoryEntry,
    LocalControlError,
    _clear_stale_running_flag,
    _name_matches,
    _probe_running,
    discover_unregistered_models,
    inventory_local_models,
    running_model_ids,
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
    locked_registry,
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


def test_start_exposes_previously_unexposed_ready_model(tmp_path):
    # The motivating bug: a model started via `modelman start` but never
    # separately exposed showed as running in modelman's own TUI but
    # never appeared in wt's picker, because wt used to also require
    # exposed=true for local models. Under the 2026-09-15 design wt no
    # longer checks `exposed` for local models at all, so the flag's only
    # remaining job is keeping LiteLLM's model_list in sync for agents
    # whose route is forced through the proxy — a fresh start of an
    # already-ready, not-yet-exposed model must expose it too, with no
    # separate `modelman expose` step required.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=False))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    result = start_local_model(
        _registry(), "ollama/qwen3.8:27b-mlx", state_path, litellm_path=litellm_path
    )

    assert result.already_running is False
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is True
    assert state.get("ollama/qwen3.8:27b-mlx").exposed is True


def test_start_already_running_unexposed_model_gets_exposed(tmp_path):
    # Re-running `modelman start` on a model that's already running but
    # was never exposed (drifted state from before this feature, or a
    # manually-cleared exposed flag on a still-running model) is the
    # remediation path — it must expose the model without a full
    # teardown/restart, since the probe already confirms it's healthy.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    with patch("modelman.local_control._probe_running", return_value=True):
        result = start_local_model(
            _registry(), "ollama/qwen3.8:27b-mlx", state_path, litellm_path=litellm_path
        )

    assert result.already_running is True
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").exposed is True


def test_start_succeeds_with_warning_when_expose_fails(tmp_path):
    # A model that isn't `ready` yet can still be started — expose_model's
    # ready gate rejects it, but that must degrade to a warning rather
    # than aborting an otherwise-successful start: wt's local-model
    # visibility no longer depends on `exposed` at all, only on the
    # running probe, so a failed expose must not block getting the model
    # running.
    state_path = _state_path(tmp_path, {})  # ready defaults to False

    result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)

    assert result.already_running is False
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True
    assert any("could not be exposed" in w for w in result.warnings)


def test_start_succeeds_with_warning_when_expose_raises_oserror(tmp_path):
    # Regression test: expose_model()'s underlying LiteLLM config write can
    # raise a plain OSError (ENOSPC, EACCES, a read-only config dir) rather
    # than one of the two exception types _expose_for_start used to catch.
    # Before this fix an uncaught OSError here would propagate out of
    # start_local_model() on the fresh-start path — AFTER the local model's
    # process had already been isolate_provider()-started — leaving the
    # model genuinely running while its `running` flag never got persisted.
    # It must instead degrade to a warning, same as ExposeError/
    # LiteLLMConfigError.
    state_path = _state_path(tmp_path, {})  # ready defaults to False

    with patch(
        "modelman.local_control.expose_model", side_effect=OSError(28, "No space left on device")
    ):
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)

    assert result.already_running is False
    assert load_state(state_path).get("ollama/qwen3.8:27b-mlx").running is True
    assert any("could not be exposed" in w for w in result.warnings)


def test_stop_local_model_unexposes_a_previously_exposed_model(tmp_path):
    # A stopped model must not stay routable through LiteLLM: stopping
    # removes its model_list row and clears the exposed flag, so a stale
    # backend is never left configured. (Superseded the earlier "exposed
    # is sticky across stop/start" design.)
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text(
        "model_list:\n"
        "  - model_name: ollama/qwen3.8:27b-mlx\n"
        "    litellm_params:\n"
        "      model: ollama/qwen3.8:27b-mlx\n"
    )

    with patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama:
        result = stop_local_model("ollama/qwen3.8:27b-mlx", state_path, litellm_path=litellm_path)
    mock_stop_ollama.assert_called_once_with("qwen3.8:27b-mlx")

    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").running is False
    assert state.get("ollama/qwen3.8:27b-mlx").exposed is False
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert result.warnings == []


def test_stop_local_model_preserves_concurrent_expose_when_nothing_to_unexpose(tmp_path):
    # Same race guard, single-stop path: stopping a model that was never
    # exposed must not clobber a concurrent expose landing in the
    # meantime back to the stale False snapshot.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)

    def _load_then_race(path=None):
        result = load_state(path)
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=True)
        return result

    with (
        patch("modelman.local_control.load_state", side_effect=_load_then_race),
        patch("modelman.local_control._stop_ollama_model") as mock_stop_ollama,
        patch("modelman.local_control.unexpose_model") as mock_unexpose,
    ):
        result = stop_local_model("ollama/a", state_path)

    mock_unexpose.assert_not_called()
    mock_stop_ollama.assert_called_once_with("a")
    assert result.stopped_model_id == "ollama/a"
    state = load_state(state_path)
    assert state.get("ollama/a").running is False
    assert state.get("ollama/a").exposed is True


def test_stop_local_model_skips_unexpose_when_not_exposed(tmp_path):
    # No point touching LiteLLM's config for a model that was never
    # exposed — this also means a stop with no litellm_path passed never
    # falls through to default_litellm_config_path() for the common case.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})

    with (
        patch("modelman.local_control._stop_ollama_model"),
        patch("modelman.local_control.unexpose_model") as mock_unexpose,
    ):
        stop_local_model("ollama/qwen3.8:27b-mlx", state_path)
    mock_unexpose.assert_not_called()


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
    # No prior flag: a failed start must not fabricate one. Uses omlx (not
    # ollama): ollama is flag-only and never calls isolate_provider, so it
    # cannot exercise this failure path any more.
    state_path = _state_path(tmp_path, {})
    with patch("modelman.local_control.isolate_provider") as mock_isolate:
        mock_isolate.return_value = IsolateResult(
            provider="omlx", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_registry(), "omlx/model-a", state_path)
    assert load_state(state_path).get("omlx/model-a").running is False


def test_start_isolate_failure_after_occupant_replaced_leaves_occupant_cleared(tmp_path):
    # A same-provider occupant (omlx is single-port) is stopped and its
    # flag cleared BEFORE isolate is attempted; if the new model's isolate
    # then fails, the occupant stays cleared (never restored) and the new
    # model's flag stays unset — the machine state truthfully reflects
    # that the occupant is gone and the replacement never came up.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_registry(), "omlx/model-b", state_path)
    mock_stop_one.assert_called_once_with("omlx")
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is False
    assert state.get("omlx/model-b").running is False


def test_start_isolate_failure_does_not_clear_concurrent_writes(tmp_path):
    # _clear_stale_running_flag only ever touches the ONE id it's given
    # (per-model, not a shared marker) — a concurrent writer's flag on a
    # DIFFERENT model must survive a failed start of this one.
    state_path = _state_path(tmp_path, {})

    def concurrent_writer_and_fail(*args, **kwargs):
        with locked_state(state_path) as fresh:
            fresh.models["concurrent/new-model"] = ModelState(running=True)
        return IsolateResult(
            provider="omlx", model="", direct_url="", ok=False, error="warmup timed out"
        )

    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider", side_effect=concurrent_writer_and_fail),
        pytest.raises(LocalControlError, match="warmup timed out"),
    ):
        start_local_model(_registry(), "omlx/model-a", state_path)
    state = load_state(state_path)
    assert state.get("concurrent/new-model").running is True
    assert state.get("omlx/model-a").running is False


def test_stop_clears_flag_and_stops_provider(tmp_path):
    # A non-ollama, non-mtplx provider's stop goes through stop_provider()
    # (the same-provider-only isolation helper), and the model's flag is
    # cleared on success.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    with patch("modelman.local_control.stop_provider") as mock_stop:
        result = stop_local_model("omlx/model-a", state_path)
    mock_stop.assert_called_once_with("omlx")
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


def test_stop_does_not_clobber_concurrent_write_to_another_model(tmp_path):
    # stop_provider() runs OUTSIDE the state lock; a concurrent `modelman
    # start` that flags a DIFFERENT model running while our own stop is
    # mid-flight must not be clobbered by our own flag clear — locked_state
    # re-reads fresh from disk before mutating only our own key.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})

    def concurrent_writer(*args, **kwargs):
        with locked_state(state_path) as fresh:
            fresh.models["omlx/model-b"] = ModelState(running=True)

    with patch("modelman.local_control.stop_provider", side_effect=concurrent_writer):
        result = stop_local_model("omlx/model-a", state_path)
    assert result.stopped_model_id == "omlx/model-a"
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is False
    assert state.get("omlx/model-b").running is True


def test_start_does_not_stop_a_different_provider(tmp_path):
    # Cross-provider concurrency: starting an omlx model while an ollama
    # model is already flagged running must leave ollama's flag alone and
    # never call the global stop-everyone-else path.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_all_local_providers") as mock_stop_all,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="model-a",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(_registry(), "omlx/model-a", state_path)
    mock_stop_all.assert_not_called()
    mock_stop_one.assert_not_called()  # nothing else was running on omlx
    assert mock_isolate.call_args.kwargs["solo"] is True
    assert result.other_running == ["ollama/qwen3.8:27b-mlx"]
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is True
    assert state.get("ollama/qwen3.8:27b-mlx").running is True  # untouched


def test_start_replaces_same_provider_occupant(tmp_path):
    # omlx/mtplx/mlx_lm_server are single-model-per-process: starting a
    # DIFFERENT model on the same provider must stop the old one first.
    state_path = _state_path(tmp_path, {"omlx/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="model-b",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(_registry(), "omlx/model-b", state_path)
    mock_stop_one.assert_called_once_with("omlx")
    state = load_state(state_path)
    assert state.get("omlx/model-b").running is True
    assert state.get("omlx/model-a").running is False


def test_start_replaces_same_provider_occupant_with_unrelated_provider_also_running(tmp_path):
    # Regression test for the exact bug the plan's preflight review caught
    # in _same_provider_occupant: an EARLIER implementation returned the
    # first running model found overall (scanning all of state.models) and
    # only afterward filtered by "is this id a member of provider_model_ids"
    # — so with an ollama model ALSO flagged running, dict iteration order
    # could hand that buggy version ollama's id first, its post-hoc filter
    # would reject it (not an omlx id) and stop there, silently returning
    # None instead of continuing on to find the real omlx occupant. The
    # correct implementation iterates provider_model_ids itself, so it can
    # never be distracted by an unrelated provider's running flag —
    # regardless of insertion/iteration order.
    state_path = _state_path(tmp_path, {"ollama/qwen3.8:27b-mlx": True, "omlx/model-a": True})
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="model-b",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(_registry(), "omlx/model-b", state_path)
    mock_stop_one.assert_called_once_with("omlx")
    state = load_state(state_path)
    assert state.get("omlx/model-b").running is True
    assert state.get("omlx/model-a").running is False  # the real occupant, cleared
    assert state.get("ollama/qwen3.8:27b-mlx").running is True  # unrelated, untouched


def test_start_replaces_omlx_6bit_occupant_when_starting_plain_omlx(tmp_path):
    # omlx and omlx-6bit are two registry provider ids sharing ONE physical
    # port/process (8000): starting a plain-omlx model while an omlx-6bit
    # model is flagged running must still find and replace that occupant —
    # treating the two provider ids as independent occupancy domains would
    # leave the 6-bit model's flag permanently stale (nothing else would
    # ever displace it to trigger a probe-based self-heal).
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
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control.stop_provider") as mock_stop_one,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="model-a",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(registry, "omlx/model-a", state_path)
    mock_stop_one.assert_called_once_with("omlx")
    state = load_state(state_path)
    assert state.get("omlx/model-a").running is True
    assert state.get("omlx-6bit/model-c").running is False  # cross-spelling occupant, cleared


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


def test_stop_all_local_models_unexposes_each_stopped_model(tmp_path):
    # `--all` must not leave a stopped model's LiteLLM row behind either —
    # same expose/running symmetry as a single stop_local_model() call,
    # just applied to every model the batch stops, in a single batched
    # config write (see test_stop_all_local_models_unexposes_in_one_batch
    # for the call-count assertion).
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=True, running=True))
    store.set("omlx/model-a", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text(
        "model_list:\n"
        "  - model_name: ollama/qwen3.8:27b-mlx\n"
        "    litellm_params:\n"
        "      model: ollama/qwen3.8:27b-mlx\n"
    )

    with patch("modelman.local_control.stop_all_local_providers"):
        result = stop_all_local_models(state_path, litellm_path=litellm_path)

    assert sorted(result.stopped) == ["ollama/qwen3.8:27b-mlx", "omlx/model-a"]
    assert result.warnings == []
    state = load_state(state_path)
    assert state.get("ollama/qwen3.8:27b-mlx").exposed is False
    assert state.get("omlx/model-a").exposed is False  # was never exposed; stays False


def test_stop_all_local_models_unexposes_in_one_batched_call(tmp_path):
    # Two exposed models being stopped together must go through ONE
    # apply_unexpose_queue call (one config load/save/restart), not one
    # unexpose_model() call per model — the efficiency half of the
    # code-review finding this task fixes.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    store.set("ollama/b", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.apply_unexpose_queue", return_value=[]) as mock_batch,
    ):
        result = stop_all_local_models(state_path)

    mock_batch.assert_called_once()
    (_, called_ids, _), _ = mock_batch.call_args
    assert sorted(called_ids) == ["ollama/a", "ollama/b"]
    assert sorted(result.stopped) == ["ollama/a", "ollama/b"]


def test_stop_all_local_models_surfaces_unexpose_failure_as_warning(tmp_path):
    # A batch unexpose failure (disk full, unwritable config) must not be
    # silently swallowed: every process still stops and every running
    # flag still clears, but the caller (main.py's --all branch) needs a
    # warning to print, mirroring stop_local_model's StopResult.warnings.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch(
            "modelman.local_control.apply_unexpose_queue",
            side_effect=OSError(28, "No space left on device"),
        ),
    ):
        result = stop_all_local_models(state_path)

    assert result.stopped == ["ollama/a"]
    assert any("could not be un-exposed" in w for w in result.warnings)
    state = load_state(state_path)
    # The process is stopped either way — a failed unexpose must not block it.
    assert state.get("ollama/a").running is False


def test_stop_all_local_models_failed_unexpose_does_not_clobber_concurrent_exposed_write(
    tmp_path,
):
    # Race guard: stop_all_local_models() loads `state` once up front, then
    # (in this test) a concurrent process flips `exposed` on disk while the
    # batch unexpose is failing. The flag write at the end must fall back
    # to the freshly-locked value, not the stale pre-attempt snapshot —
    # the same guard stop_local_model() already has.
    from modelman.state import locked_state

    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)

    def _concurrent_write_then_fail(*args, **kwargs):
        # Simulate another process (e.g. `modelman unexpose ollama/a`)
        # racing this stop_all_local_models() call.
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=False)
        raise OSError(28, "No space left on device")

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch(
            "modelman.local_control.apply_unexpose_queue",
            side_effect=_concurrent_write_then_fail,
        ),
    ):
        stop_all_local_models(state_path)

    # Must reflect the concurrent write (False), not the stale pre-attempt
    # snapshot (True) that stop_all_local_models loaded before the race.
    assert load_state(state_path).get("ollama/a").exposed is False


def test_stop_all_local_models_preserves_concurrent_expose_when_nothing_to_unexpose(tmp_path):
    # Same race guard, --all path: a model with nothing to unexpose (it
    # was never exposed) must not have a concurrent expose clobbered
    # back to the stale False snapshot either.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)

    def _load_then_race(path=None):
        result = load_state(path)
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=True)
        return result

    with (
        patch("modelman.local_control.load_state", side_effect=_load_then_race),
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.apply_unexpose_queue") as mock_apply,
    ):
        result = stop_all_local_models(state_path)

    mock_apply.assert_not_called()
    assert result.stopped == ["ollama/a"]
    state = load_state(state_path)
    assert state.get("ollama/a").running is False
    assert state.get("ollama/a").exposed is True


def test_running_model_ids_unexposes_a_stale_flagged_model(tmp_path):
    # A model modelman no longer believes is running (probe fails) must
    # also stop being routable through LiteLLM — the same self-heal that
    # already clears `running` now clears `exposed` too, wherever a stale
    # flag is found (TUI mount reconcile is the main caller of this path).
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/qwen3.8:27b-mlx", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text(
        "model_list:\n"
        "  - model_name: ollama/qwen3.8:27b-mlx\n"
        "    litellm_params:\n"
        "      model: ollama/qwen3.8:27b-mlx\n"
    )
    registry = _registry()
    state = load_state(state_path)

    with patch("modelman.local_control._probe_running", return_value=False):
        ids = running_model_ids(registry, state, state_path, litellm_path=litellm_path)

    assert ids == []
    on_disk = load_state(state_path)
    assert on_disk.get("ollama/qwen3.8:27b-mlx").running is False
    assert on_disk.get("ollama/qwen3.8:27b-mlx").exposed is False


def test_clear_stale_running_flag_failed_unexpose_does_not_clobber_concurrent_write(tmp_path):
    # Same race guard as stop_all_local_models: if unexpose_model() fails
    # while this function is clearing a stale flag, the exposed field it
    # writes back must reflect whatever a concurrent process wrote in the
    # meantime, not the stale pre-attempt snapshot this function loaded
    # for itself at the top.
    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=True, running=True))
    save_state(store, state_path)
    litellm_path = tmp_path / "config.yaml"  # missing -> unexpose_model raises

    def _concurrent_write_then_fail(*args, **kwargs):
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=False)
        raise OSError(28, "No space left on device")

    with patch("modelman.local_control.unexpose_model", side_effect=_concurrent_write_then_fail):
        _clear_stale_running_flag("ollama/a", state_path, litellm_path)

    state = load_state(state_path)
    assert state.get("ollama/a").running is False
    # Must reflect the concurrent write, not the stale True snapshot this
    # function loaded before the race.
    assert state.get("ollama/a").exposed is False


def test_clear_stale_running_flag_preserves_concurrent_expose_when_nothing_to_unexpose(tmp_path):
    # When the model was already not-exposed at read time, this function
    # never attempts an unexpose — but the final write used to overwrite
    # `exposed` unconditionally with the stale pre-attempt snapshot
    # anyway. A concurrent `modelman expose <id>` landing in the window
    # between this function's initial load_state() and its own
    # locked_state() call must survive, not get clobbered back to False.
    from modelman.local_control import _clear_stale_running_flag
    from modelman.state import load_state as real_load_state
    from modelman.state import locked_state

    state_path = tmp_path / "modelman.toml"
    store = StateStore()
    store.set("ollama/a", ModelState(ready=True, exposed=False, running=True))
    save_state(store, state_path)

    def _load_then_race(path=None):
        result = real_load_state(path)
        with locked_state(state_path) as fresh:
            fresh.models["ollama/a"] = replace(fresh.models["ollama/a"], exposed=True)
        return result

    with (
        patch("modelman.local_control.load_state", side_effect=_load_then_race),
        patch("modelman.local_control.unexpose_model") as mock_unexpose,
    ):
        _clear_stale_running_flag("ollama/a", state_path)

    mock_unexpose.assert_not_called()
    state = load_state(state_path)
    assert state.get("ollama/a").running is False
    assert state.get("ollama/a").exposed is True


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


def test_probe_running_ollama_is_exempt_from_live_verification():
    # Ollama's start is deliberately flag-only (no warmup — it lazy-loads
    # on first request), so `ollama ps` (which lists only currently-LOADED
    # models) would read a freshly-flagged-but-never-requested model as
    # not-running the moment anything probes it, permanently self-clearing
    # a flag that was never wrong. Ollama must always probe as running,
    # regardless of what `ollama ps` (_ollama_loaded_names) reports.
    with patch("modelman.local_control._ollama_loaded_names", return_value=[]):
        assert _probe_running("ollama", "anything", None) is True
    with patch("modelman.local_control._ollama_loaded_names", side_effect=RuntimeError("boom")):
        assert _probe_running("ollama", "anything", None) is True


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


def test_start_unregistered_name_with_no_family_raises_needs_family(tmp_path):
    # A bare provider-native name matching an on-disk, unregistered artifact
    # can't be auto-registered without a family — this is the interactive
    # prompt path the CLI's `start` command catches and retries. Also
    # verifies the failure has no side effects: nothing is written before
    # the family is known.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    mapping = {
        "ollama": [
            {"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}
        ]
    }
    with (
        _patch_provider_local_models(mapping),
        pytest.raises(DiscoveredModelNeedsFamily) as excinfo,
    ):
        start_local_model(registry, "llama3.2:3b", registry_path=registry_path)
    assert excinfo.value.provider_id == "ollama"
    assert excinfo.value.variant_id == "llama3.2:3b"
    # No side effects: nothing is written and nothing is started when the
    # call can't proceed without a family.
    assert load_registry(registry_path).models == registry.models


def test_start_unregistered_name_with_family_registers_exposes_and_starts(tmp_path):
    # The full discover -> register -> expose -> start pipeline for a
    # provider-native name with no registry.toml entry: this is the
    # single most consequential path this branch adds, so it asserts on
    # every artifact it should produce — the registry.toml entry (family/
    # provider/name/source/location), the in-memory registry the caller
    # keeps using for the rest of the call, the ready+exposed+sized state,
    # and the running marker.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")
    mapping = {
        "ollama": [
            {"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 2_000_000_000}
        ]
    }

    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama",
            model="llama3.2:3b",
            direct_url="http://localhost:11434/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(
            registry,
            "llama3.2:3b",
            state_path,
            family="discovered",
            registry_path=registry_path,
            litellm_path=litellm_path,
        )

    mock_stop.assert_not_called()  # ollama is flag-only, never stop-all'd on start
    mock_isolate.assert_not_called()
    assert result.already_running is False
    assert result.model_id == "ollama/llama3.2:3b"

    on_disk = load_registry(registry_path)
    entry = on_disk.model("ollama/llama3.2:3b")
    assert entry.family == "discovered"
    assert entry.provider_id == "ollama"
    assert entry.model_name == "llama3.2:3b"
    assert entry.source == "discovered"
    assert entry.location == "local"
    # The in-memory registry the caller passed in must reflect the write too
    # (start_local_model keeps using it for the rest of this call, and a CLI
    # process only has this one in-memory copy for the whole invocation).
    assert registry.model("ollama/llama3.2:3b") == entry

    state_on_disk = load_state(state_path)
    model_state = state_on_disk.get("ollama/llama3.2:3b")
    assert model_state.ready is True
    assert model_state.exposed is True
    assert model_state.size_bytes == 2_000_000_000
    assert load_state(state_path).get("ollama/llama3.2:3b").running is True


def test_start_unregistered_name_registry_write_failure_raises_local_control_error(tmp_path):
    # A registry.toml write failure while registering a discovered model
    # (full disk, read-only filesystem) must surface as a clean
    # LocalControlError the CLI already knows how to print, not an unguarded
    # OSError/traceback — mirrors the OSError handling main.py's `sync`
    # command already applies around its own registry/state saves.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    mapping = {
        "ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 7}]
    }

    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.locked_registry", side_effect=OSError("disk full")),
        pytest.raises(LocalControlError, match="failed to register"),
    ):
        start_local_model(registry, "llama3.2:3b", family="discovered", registry_path=registry_path)


def test_start_discovers_and_registers_omlx_artifact_resolvable_afterward(tmp_path):
    # Regression test for a bug this branch's review found: auto-registering
    # a discovered omlx artifact without ModelEntry.fetch left OMLXProvider
    # unable to re-derive the artifact's on-disk directory afterward
    # (_target_dir() only reads variant['repo']/['local_path'], both sourced
    # from fetch, never the model name) — the model modelman just started
    # and registered would show up as "not downloaded" on the very next
    # `modelman start` inventory. Uses the real OMLXProvider (like the
    # omlx join-mismatch tests below) rather than the name-keyed
    # _patch_provider_local_models stub, since the stub can't reproduce a
    # bug that only exists in OMLXProvider's own path derivation.
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
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
        patch("modelman.local_control._probe_running", return_value=False),
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="Qwen3.8-27B-4bit",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        start_local_model(
            registry,
            "Qwen3.8-27B-4bit",
            state_path,
            family="qwen3.8",
            registry_path=registry_path,
            litellm_path=litellm_path,
        )

    inventory = inventory_local_models(load_registry(registry_path), load_state(state_path))
    assert inventory.downloaded == [
        InventoryEntry(model_id="omlx/Qwen3.8-27B-4bit", running=False, size_bytes=2048)
    ]
    assert inventory.not_downloaded == []


def test_start_unregistered_name_ambiguous_across_providers_raises(tmp_path):
    # A discovered name matching on-disk artifacts from two different
    # providers is genuinely ambiguous — auto-registering either one would
    # silently guess wrong, so this must raise and require the user to
    # register manually instead.
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
        pytest.raises(LocalControlError, match="multiple providers"),
    ):
        start_local_model(registry, "shared-name", registry_path=registry_path)


def test_start_native_name_resolves_existing_registered_model_without_reregistering(tmp_path):
    # A model already registered (auto or curated) under its native
    # provider-side name must resolve idempotently by that name too - a
    # user who auto-registered "llama3.2:3b" and starts it again by the
    # same bare name must not see "unknown model" just because the id it
    # was registered under is "ollama/llama3.2:3b", not the bare name.
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
        result = start_local_model(registry, "llama3.2:3b", state_path, registry_path=registry_path)
    assert result.model_id == "ollama/llama3.2:3b"
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()  # ollama is flag-only
    assert load_state(state_path).get("ollama/llama3.2:3b").running is True


def _listing_registry() -> Registry:
    """Local models covering all three inventory buckets, plus one exposed
    cloud model, for inventory_local_models tests."""
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
                id="ollama/exposed-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="exposed-model",
            ),
            ModelEntry(
                id="ollama/unexposed-model",
                family="qwen3.8",
                provider_id="ollama",
                model_name="unexposed-model",
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
        "ollama/exposed-model",
        ModelState(ready=True, exposed=True, running=(running_model == "ollama/exposed-model")),
    )
    store.set("ollama/unexposed-model", ModelState(ready=True, exposed=False))
    store.set("openrouter/z-ai/glm-5.3-flash", ModelState(ready=False, exposed=True))
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
    state = _listing_state(running_model="ollama/exposed-model")
    mapping = {
        "ollama": [
            {
                "variant_id": "exposed-model",
                "path": "ollama:exposed-model",
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
        InventoryEntry(model_id="ollama/exposed-model", running=True, size_bytes=4_900_000_000)
    ]


def test_inventory_not_downloaded_bucket_lists_registered_missing_artifacts():
    # Registered models the live provider reports nothing for must be
    # listed as missing, regardless of exposed status — this is what tells
    # a user which registry.toml entries need a real download.
    registry = _listing_registry()
    state = _listing_state()
    with _patch_provider_local_models({"ollama": []}):
        inventory = inventory_local_models(registry, state)
    assert inventory.not_downloaded == [
        "ollama/exposed-model",
        "ollama/missing-model",
        "ollama/unexposed-model",
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
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 1},
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
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 1},
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
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": 1},
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
        "ollama/exposed-model",
        "ollama/missing-model",
        "ollama/unexposed-model",
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
    in it and return the model_dir."""
    model_dir = tmp_path / "omlx-models"
    (model_dir / basename).mkdir(parents=True)
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


def test_start_omlx_directory_basename_resolves_the_registered_full_repo_entry(tmp_path):
    # The duplicate-registration bug: the old "discovered" listing told the
    # user to type the on-disk basename, but the native-name match compared
    # it verbatim against model_name (the full repo id), missed, and fell
    # through to auto-registration — writing a SECOND registry.toml entry and
    # a SECOND LiteLLM model_list row for an already-registered, already-
    # exposed artifact.
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    litellm_path = tmp_path / "config.yaml"
    litellm_path.write_text("model_list: []\n")

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="mlx-community/Qwen3.8-27B-4bit",
            direct_url="http://localhost:8000/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(
            registry,
            "Qwen3.8-27B-4bit",
            state_path,
            registry_path=registry_path,
            litellm_path=litellm_path,
        )

    assert result.model_id == _OMLX_MODEL_ID
    mock_isolate.assert_called_once_with(
        "omlx", env={"LLM_ISOLATE_OMLX_4BIT_MODEL": "mlx-community/Qwen3.8-27B-4bit"}, solo=True
    )
    # Singular registry entry and an untouched LiteLLM config: nothing was
    # re-registered or re-exposed.
    assert [m.id for m in load_registry(registry_path).models] == [_OMLX_MODEL_ID]
    assert litellm_path.read_text() == "model_list: []\n"


def test_find_discovered_omlx_basename_does_not_rediscover_registered_model(tmp_path):
    # The same join, exercised through start_local_model's resolution path
    # with no family: an already-registered omlx model must never raise
    # DiscoveredModelNeedsFamily just because its on-disk spelling differs.
    registry = _omlx_registry(_omlx_model_dir(tmp_path))
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="omlx",
            model="mlx-community/Qwen3.8-27B-4bit",
            direct_url=None,
            ok=True,
            error=None,
        )
        # The full repo id must keep resolving too (registry-id lookup misses
        # it; the native-name match is what catches it).
        result = start_local_model(
            registry, "mlx-community/Qwen3.8-27B-4bit", state_path, registry_path=registry_path
        )
    assert result.model_id == _OMLX_MODEL_ID


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
    state.set(
        "ollama/exposed-model", ModelState(ready=True, exposed=True, size_bytes=4_900_000_000)
    )
    mapping = {
        "ollama": [
            {"variant_id": "exposed-model", "path": "ollama:exposed-model", "size_bytes": None}
        ]
    }
    with _patch_provider_local_models(mapping):
        inventory = inventory_local_models(registry, state)
    assert (
        InventoryEntry(model_id="ollama/exposed-model", running=False, size_bytes=4_900_000_000)
        in inventory.downloaded
    )


def test_register_discovered_model_keeps_registry_and_state_when_expose_fails(tmp_path):
    # expose_model() runs AFTER the registry entry and ready=True state are
    # persisted, so an ExposeError leaves a registered-but-unexposed model.
    # That partial state is deliberate (a retry resolves the entry by its
    # native name instead of registering a duplicate) — this test pins both
    # the persistence and the error.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 7}]
    }

    with (
        _patch_provider_local_models(mapping),
        patch("modelman.local_control.expose_model", side_effect=ExposeError("no litellm config")),
        pytest.raises(LocalControlError, match="registered"),
    ):
        start_local_model(
            registry,
            "llama3.2:3b",
            state_path,
            family="discovered",
            registry_path=registry_path,
        )

    entry = load_registry(registry_path).model("ollama/llama3.2:3b")
    assert entry.model_name == "llama3.2:3b"
    persisted = load_state(state_path).get("ollama/llama3.2:3b")
    assert persisted.ready is True
    assert persisted.exposed is False
    # Nothing was started: the failure happens during resolution.
    assert load_state(state_path).get("ollama/llama3.2:3b").running is False


def test_register_discovered_model_refuses_an_id_that_already_exists(tmp_path):
    # Defense-in-depth against a race or a hand-edited registry.toml: the id
    # is derived from (provider, native name), so a colliding id means the
    # model is already registered — appending a second entry would duplicate
    # it in registry.toml and in LiteLLM's model_list.
    registry = _registry()
    registry_path = tmp_path / "registry.toml"
    save_registry(registry, registry_path)
    # On-disk registry already has the entry; the in-memory copy passed in
    # does not (the race this guards against).
    with locked_registry(registry_path) as fresh:
        fresh.models.append(
            ModelEntry(
                id="ollama/llama3.2:3b",
                family="other",
                provider_id="ollama",
                model_name="llama3.2:3b",
                location="local",
                source="discovered",
            )
        )
    state_path = _state_path(tmp_path)
    mapping = {
        "ollama": [{"variant_id": "llama3.2:3b", "path": "ollama:llama3.2:3b", "size_bytes": 7}]
    }

    with (
        _patch_provider_local_models(mapping),
        pytest.raises(LocalControlError, match="already registered"),
    ):
        start_local_model(
            registry,
            "llama3.2:3b",
            state_path,
            family="discovered",
            registry_path=registry_path,
        )
    assert [m.id for m in load_registry(registry_path).models].count("ollama/llama3.2:3b") == 1
