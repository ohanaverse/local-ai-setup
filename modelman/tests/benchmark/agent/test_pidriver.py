"""Tests for modelman.benchmark.agent.pidriver — route resolution and pi
config generation.

Route resolution is the part of the design most likely to silently
misaddress a model (spec: "a registry id sent under the wrong provider
could never be addressed and its bare form would be sent upstream, a 400
at the gateway") — these tests pin the litellm-vs-direct addressing rules
exactly as the spec's Route resolution section states them.
"""

import json
from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import (
    DirectRouteConfig,
    RowConfig,
    build_models_json,
    build_pi_command,
    resolve_pi_target,
    write_pi_config,
)
from modelman.benchmark.errors import BenchmarkError

LITELLM_ID = "ollama/qwen3.8:27b-mlx"


def _live_litellm_path(tmp_path: Path) -> Path:
    """A stand-in for ~/.pi/agent/models.json.

    Never read the real file (hermetic), and never pass a *missing* one to a
    litellm row: resolve_pi_target raises without an apiKey, so tests that
    only care about the config shape still have to supply a resolvable
    gateway entry.
    """
    path = tmp_path / "models.json"
    path.write_text(
        json.dumps(
            {
                "providers": {
                    "litellm": {
                        "baseUrl": "http://localhost:4000/v1",
                        "apiKey": "sk-test-key",
                        "models": [],
                    }
                }
            }
        ),
        encoding="utf-8",
    )
    return path


def _litellm_row() -> RowConfig:
    return RowConfig(
        label="r1", model_id=LITELLM_ID, thinking="off", route="litellm", provider_id="ollama"
    )


def test_litellm_route_keys_by_full_registry_id(tmp_path):
    """route=litellm addresses pi's litellm provider by the full registry
    model id — the LiteLLM model_list is keyed on it, not the bare name."""
    live = tmp_path / "models.json"
    live.write_text(
        json.dumps(
            {
                "providers": {
                    "litellm": {
                        "baseUrl": "http://localhost:4000/v1",
                        "apiKey": "sk-live-key",
                        "models": [
                            {"id": LITELLM_ID, "contextWindow": 131072, "reasoning": False}
                        ],
                    }
                }
            }
        ),
        encoding="utf-8",
    )

    target = resolve_pi_target(_litellm_row(), "qwen3.8:27b-mlx", routes_direct={}, live_models_path=live)

    assert target.pi_provider == "litellm"
    assert target.launch_id == LITELLM_ID
    assert target.model_arg == f"litellm/{LITELLM_ID}"
    assert target.api_key == "sk-live-key"
    assert target.context_window == 131072
    assert target.reasoning is False


def test_direct_route_keys_by_bare_model_name(tmp_path):
    """route=direct addresses the backend's own endpoint by its bare
    model_name, per [routes.direct.<provider>] in the suite."""
    row = RowConfig(
        label="r1",
        model_id="omlx/Ornith-1.5-35B-A3B-MLX-6bit",
        thinking="high",
        route="direct",
        provider_id="omlx",
    )
    routes_direct = {
        "omlx": DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")
    }

    target = resolve_pi_target(
        row, "Ornith-1.5-35B-A3B-MLX-6bit", routes_direct, live_models_path=tmp_path / "missing.json"
    )

    assert target.pi_provider == "omlx"
    assert target.launch_id == "Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.model_arg == "omlx/Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.base_url == "http://localhost:8000/v1"
    assert target.api_key == "ollama"  # placeholder — pi rejects an empty apiKey


def test_direct_route_can_override_the_launch_id(tmp_path):
    """direct_model names what the backend actually serves when the registry
    model_name does not — omlx registers `mlx-community/X` but serves `X`. A
    wrong launch id is not a 404 the harness can tell apart from a model that
    failed the task, so the escape hatch has to exist."""
    row = RowConfig(
        label="r1",
        model_id="omlx/mlx-community--Qwen3.8-27B-4bit",
        thinking="off",
        route="direct",
        provider_id="omlx",
        direct_model="Qwen3.8-27B-4bit",
    )
    routes_direct = {
        "omlx": DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")
    }

    target = resolve_pi_target(
        row, "mlx-community/Qwen3.8-27B-4bit", routes_direct, live_models_path=tmp_path / "missing.json"
    )

    assert target.launch_id == "Qwen3.8-27B-4bit"
    assert target.model_arg == "omlx/Qwen3.8-27B-4bit"


def test_direct_route_missing_config_block_raises(tmp_path):
    """A direct row with no matching [routes.direct.<provider>] block fails
    at resolution with the provider name, not a KeyError deep in dict access."""
    row = RowConfig(label="r1", model_id="omlx/x", thinking="off", route="direct", provider_id="omlx")
    with pytest.raises(BenchmarkError, match="omlx"):
        resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")


def test_litellm_route_missing_key_raises(tmp_path):
    """No working LiteLLM apiKey anywhere means the row can't be judged or
    run through the gateway — fail clearly instead of sending an empty key."""
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    with pytest.raises(BenchmarkError, match="apiKey"):
        resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")


def test_build_models_json_matches_wt_pi_models_shape(tmp_path):
    """Provider/entry shape mirrors wt/internal/agents/pi_models.go exactly
    (id/contextWindow/input/reasoning/_launch) so an agent row behaves like
    a wt-launched pi session."""
    target = resolve_pi_target(
        _litellm_row(), "qwen3.8:27b-mlx", routes_direct={}, live_models_path=_live_litellm_path(tmp_path)
    )
    doc = build_models_json(target)
    provider = doc["providers"]["litellm"]
    entry = provider["models"][0]
    assert entry["_launch"] is True
    assert entry["id"] == LITELLM_ID
    assert entry["input"] == ["text", "image"]
    assert provider["apiKey"] == "sk-test-key"  # written per-run, never into the user's file
    assert provider["baseUrl"] == "http://localhost:4000/v1"
    assert provider["api"] == "openai-completions"


def test_write_pi_config_writes_models_json(tmp_path):
    target = resolve_pi_target(
        _litellm_row(), "qwen3.8:27b-mlx", routes_direct={}, live_models_path=_live_litellm_path(tmp_path)
    )
    config_dir = tmp_path / "run-config"
    path = write_pi_config(target, config_dir)
    assert path == config_dir / "models.json"
    assert json.loads(path.read_text(encoding="utf-8"))["providers"]["litellm"]["models"][0]["id"] == LITELLM_ID


def test_build_pi_command_shape(tmp_path):
    row = RowConfig(
        label="r1", model_id="ollama/x", thinking="high", route="litellm", provider_id="ollama"
    )
    target = resolve_pi_target(
        row, "x", routes_direct={}, live_models_path=_live_litellm_path(tmp_path)
    )
    cmd = build_pi_command(target, "high", tmp_path / "session", "do the task")
    assert cmd[0] == "pi"
    assert "--mode" in cmd and "json" in cmd
    assert cmd[cmd.index("--model") + 1] == "litellm/ollama/x"
    assert cmd[cmd.index("--thinking") + 1] == "high"
    assert cmd[-1] == "do the task"
    assert "--no-approve" in cmd
