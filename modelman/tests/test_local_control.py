"""Unit tests for modelman.local_control — the start/stop orchestration
behind `modelman start`/`modelman stop` (issue #65). Isolation subprocess
calls are mocked; these tests cover validation, probe-based idempotency,
stale-marker recovery, and marker mutation — not bin/llm-isolate-provider
itself (see tests/benchmark/test_isolation.py for that)."""

from pathlib import Path
from unittest.mock import patch

import pytest

from modelman.benchmark.isolation import IsolateResult
from modelman.local_control import (
    LocalControlError,
    _name_matches,
    start_local_model,
    stop_local_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import StateStore, load_state, save_state


def _registry() -> Registry:
    return Registry(
        providers=[
            ProviderEntry(id="ollama", name="Ollama", location="local", auth=AuthConfig(type="none")),
            ProviderEntry(
                id="openrouter", name="OpenRouter", location="cloud", auth=AuthConfig(type="api_key")
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


def _state_path(tmp_path: Path, running_model: str | None = None) -> Path:
    """Write an initial modelman.toml with the given marker and return its
    path — local_control now owns the marker's on-disk lifecycle."""
    path = tmp_path / "modelman.toml"
    store = StateStore()
    store.local.running_model = running_model
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
    # The marker alone is not enough to no-op: the marked model must also
    # answer its availability probe — `modelman start <id>` is the recovery
    # command wt's "not running" message prescribes, and a no-op on a dead
    # marker would deadlock that recovery loop.
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
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
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_marker_names_dead_model_clears_it_and_restarts(tmp_path):
    # Marker matches but the probe says nothing is serving (crash, reboot,
    # `omlx stop`): the marker must be cleared and the full start must run —
    # this is the recovery path wt's fatal message prescribes.
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with (
        patch("modelman.local_control._probe_running", return_value=False),
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama",
            model="qwen3.8:27b-mlx",
            direct_url="http://localhost:11434/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert result.already_running is False
    mock_stop.assert_called_once()
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_stops_current_then_starts_requested(tmp_path):
    state_path = _state_path(tmp_path, "some/other-model")
    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama",
            model="qwen3.8:27b-mlx",
            direct_url="http://localhost:11434/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    mock_stop.assert_called_once()
    mock_isolate.assert_called_once_with(
        "ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "qwen3.8:27b-mlx"}
    )
    assert result.already_running is False
    assert result.direct_url == "http://localhost:11434/v1/chat/completions"
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_isolate_failure_raises_and_does_not_write_marker(tmp_path):
    # No prior marker: a failed start must not fabricate one.
    state_path = _state_path(tmp_path)
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).local.running_model is None


def test_start_isolate_failure_after_stopall_clears_stale_marker(tmp_path):
    # stop-all succeeded (the previously marked model is gone), then the
    # start failed: leaving the old marker would make wt fatal on every
    # launch — including pure-cloud ones — pointing at a model that is no
    # longer serving. The failure path must clear it.
    state_path = _state_path(tmp_path, "some/other-model")
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="cleared the stale"):
            start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).local.running_model is None


def test_start_isolate_failure_does_not_clear_foreign_marker(tmp_path):
    # A concurrent writer moved the marker on between our failed start and
    # the cleanup: cleanup only clears markers naming what WE tore down,
    # never a marker a concurrent start just wrote.
    state_path = _state_path(tmp_path, "some/other-model")

    def concurrent_writer_and_fail(*args, **kwargs):
        concurrent = StateStore()
        concurrent.local.running_model = "concurrent/new-model"
        save_state(concurrent, state_path)
        return IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )

    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider", side_effect=concurrent_writer_and_fail),
        pytest.raises(LocalControlError, match="warmup timed out"),
    ):
        start_local_model(_registry(), "ollama/qwen3.8:27b-mlx", state_path)
    assert load_state(state_path).local.running_model == "concurrent/new-model"


def test_stop_clears_marker_and_stops(tmp_path):
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model(state_path)
    mock_stop.assert_called_once()
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert load_state(state_path).local.running_model is None


def test_stop_noop_when_nothing_running():
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model()
    mock_stop.assert_not_called()
    assert result.stopped_model_id is None


def test_stop_does_not_clobber_concurrent_new_marker(tmp_path):
    # stop-all is done; a concurrent `modelman start` already wrote a new
    # marker before our cleanup ran: the cleanup must not clear the new
    # model's (still-true) marker.
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")

    def concurrent_writer(*args, **kwargs):
        concurrent = StateStore()
        concurrent.local.running_model = "concurrent/new-model"
        save_state(concurrent, state_path)

    with patch("modelman.local_control.stop_all_local_providers", side_effect=concurrent_writer):
        result = stop_local_model(state_path)
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert load_state(state_path).local.running_model == "concurrent/new-model"


def test_start_mlx_lm_server_resolves_pairing_args(tmp_path):
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none"))
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
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server", model="org/target-repo", direct_url="http://localhost:8001/v1/chat/completions",
            ok=True, error=None,
        )
        start_local_model(registry, "mlx_lm_server/target-repo", state_path)
    mock_isolate.assert_called_once_with("mlx_lm_server", "org/target-repo", "org/draft-repo", env=None)


def test_start_broken_mlx_lm_server_pairing_fails_before_teardown(tmp_path):
    # A missing target/draft pairing must fail fast: the previous model's
    # stop-all runs only after the isolate arguments resolve, so a pairing
    # error can never tear down a healthy model and then leave the GPU empty.
    from modelman.registry import DraftSpec, Fetch

    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mlx_lm_server", name="mlx-lm server", location="local", auth=AuthConfig(type="none"))
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
    state_path = _state_path(tmp_path, "ollama/qwen3.8:27b-mlx")
    with (
        patch("modelman.local_control.stop_all_local_providers") as mock_stop,
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        # Strip the draft so pairing resolution fails.
        registry.models[-1].draft = None
        with pytest.raises(LocalControlError, match="missing a target or"):
            start_local_model(registry, "mlx_lm_server/target-repo", state_path)
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    # No teardown happened, so the previously marked model's marker stays.
    assert load_state(state_path).local.running_model == "ollama/qwen3.8:27b-mlx"


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


def test_start_mtplx_isolates_without_env_var(tmp_path):
    """start_local_model() must call isolate_provider('mtplx', ..., env=None)
    rather than mapping mtplx through an env-var override like ollama/omlx
    do, and must persist the running-model marker in modelman.toml's
    [local] table on success. Passing an env var here would misroute mtplx
    isolation through the wrong bash-shim mechanism; skipping the marker
    write would leave wt's local-model gate pointing at a stale model."""
    registry = _registry()
    registry.providers.append(
        ProviderEntry(id="mtplx", name="MTPLX", location="local", auth=AuthConfig(type="none", base_url="http://localhost:8003/v1"))
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
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mtplx",
            model="Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
            direct_url="http://localhost:8003/v1/chat/completions",
            ok=True,
            error=None,
        )
        result = start_local_model(registry, "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", state_path)
    mock_isolate.assert_called_once_with(
        "mtplx",
        "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
        env=None,
    )
    assert result.direct_url == "http://localhost:8003/v1/chat/completions"
    assert load_state(state_path).local.running_model == "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
