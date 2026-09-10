"""Unit tests for modelman.local_control — the start/stop orchestration
behind `modelman start`/`modelman stop` (issue #65). Isolation subprocess
calls are mocked; these tests cover validation, idempotency, and marker
mutation, not bin/llm-isolate-provider itself (see
tests/benchmark/test_isolation.py for that)."""

from unittest.mock import patch

import pytest

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.isolation import IsolateResult
from modelman.local_control import (
    LocalControlError,
    start_local_model,
    stop_local_model,
)
from modelman.registry import AuthConfig, ModelEntry, ProviderEntry, Registry
from modelman.state import StateStore


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


def test_start_unknown_model_raises():
    with pytest.raises(LocalControlError, match="unknown model"):
        start_local_model(_registry(), StateStore(), "ollama/not-in-registry")


def test_start_cloud_model_rejected():
    # A cloud model can never be "running locally" - modelman start only
    # manages the local-process lifecycle.
    with pytest.raises(LocalControlError, match="cloud model"):
        start_local_model(_registry(), StateStore(), "openrouter/z-ai/glm-5.3-flash")


def test_start_unsupported_provider_rejected():
    # llamacpp is local but retired (issue #33) - not in SUPPORTED_PROVIDER_IDS,
    # so bin/llm-isolate-provider can't isolate it.
    with pytest.raises(LocalControlError, match="cannot be started"):
        start_local_model(_registry(), StateStore(), "llamacpp/retired-model")


def test_start_already_running_is_idempotent():
    state = StateStore()
    state.local.running_model = "ollama/qwen3.8:27b-mlx"
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop, patch(
        "modelman.local_control.isolate_provider"
    ) as mock_isolate:
        result = start_local_model(_registry(), state, "ollama/qwen3.8:27b-mlx")
    assert result.already_running is True
    mock_stop.assert_not_called()
    mock_isolate.assert_not_called()
    assert state.local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_stops_current_then_starts_requested():
    state = StateStore()
    state.local.running_model = "some/other-model"
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
        result = start_local_model(_registry(), state, "ollama/qwen3.8:27b-mlx")
    mock_stop.assert_called_once()
    mock_isolate.assert_called_once_with(
        "ollama", env={"LLM_ISOLATE_OLLAMA_MODEL": "qwen3.8:27b-mlx"}
    )
    assert result.already_running is False
    assert result.direct_url == "http://localhost:11434/v1/chat/completions"
    assert state.local.running_model == "ollama/qwen3.8:27b-mlx"


def test_start_isolate_failure_raises_and_does_not_write_marker():
    state = StateStore()
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="ollama", model="", direct_url="", ok=False, error="warmup timed out"
        )
        with pytest.raises(LocalControlError, match="warmup timed out"):
            start_local_model(_registry(), state, "ollama/qwen3.8:27b-mlx")
    assert state.local.running_model is None


def test_stop_clears_marker_and_stops():
    state = StateStore()
    state.local.running_model = "ollama/qwen3.8:27b-mlx"
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model(state)
    mock_stop.assert_called_once()
    assert result.stopped_model_id == "ollama/qwen3.8:27b-mlx"
    assert state.local.running_model is None


def test_stop_noop_when_nothing_running():
    state = StateStore()
    with patch("modelman.local_control.stop_all_local_providers") as mock_stop:
        result = stop_local_model(state)
    mock_stop.assert_not_called()
    assert result.stopped_model_id is None


def test_start_mlx_lm_server_resolves_pairing_args():
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
    state = StateStore()
    with (
        patch("modelman.local_control.stop_all_local_providers"),
        patch("modelman.local_control.isolate_provider") as mock_isolate,
    ):
        mock_isolate.return_value = IsolateResult(
            provider="mlx_lm_server", model="org/target-repo", direct_url="http://localhost:8001/v1/chat/completions",
            ok=True, error=None,
        )
        start_local_model(registry, state, "mlx_lm_server/target-repo")
    mock_isolate.assert_called_once_with("mlx_lm_server", "org/target-repo", "org/draft-repo", env=None)
