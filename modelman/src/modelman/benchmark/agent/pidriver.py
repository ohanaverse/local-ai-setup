"""pi agent driver: route resolution, models.json generation, process
execution, and metrics extraction from pi's `--mode json` event stream."""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError

DEFAULT_CONTEXT_WINDOW = 262144
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"

PI_BASE_ARGS = [
    "--mode", "json",
    "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files",
    "--no-approve",
]


@dataclass
class DirectRouteConfig:
    base_url: str
    api: str


@dataclass
class RowConfig:
    label: str
    model_id: str
    thinking: str
    route: str  # "direct" | "litellm"
    provider_id: str
    direct_model: str | None = None  # overrides the launch id for route=direct


@dataclass
class PiTarget:
    pi_provider: str
    launch_id: str
    base_url: str
    api: str
    api_key: str
    context_window: int
    reasoning: bool

    @property
    def model_arg(self) -> str:
        return f"{self.pi_provider}/{self.launch_id}"


def _load_live_models(path: Path) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def _lookup_live_entry(live: dict, pi_provider: str, launch_id: str) -> dict | None:
    provider = live.get("providers", {}).get(pi_provider, {})
    for entry in provider.get("models", []):
        if entry.get("id") == launch_id:
            return entry
    return None


def resolve_pi_target(
    row: RowConfig,
    model_name: str,
    routes_direct: dict[str, DirectRouteConfig],
    live_models_path: Path = LIVE_PI_MODELS_PATH,
) -> PiTarget:
    """Map (model, route) to a pi provider/launch id.

    litellm route keys by the full registry model id (the LiteLLM
    model_list is keyed on it); direct route keys by the backend's own bare
    model_name — mirrors wt's addressing convention exactly, since pi
    splits --model on the first slash and a registry id under the wrong
    provider "could never be addressed" (spec, Verified against the live
    setup).
    """
    live = _load_live_models(live_models_path)

    if row.route == "litellm":
        pi_provider = "litellm"
        launch_id = row.model_id
        litellm_entry = live.get("providers", {}).get("litellm", {})
        base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
        api = "openai-completions"
        api_key = litellm_entry.get("apiKey")
        if not api_key:
            raise BenchmarkError(
                "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
                "pi session in litellm mode at least once to seed it"
            )
    elif row.route == "direct":
        direct_cfg = routes_direct.get(row.provider_id)
        if direct_cfg is None:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )
        pi_provider = row.provider_id
        # Registry model_name is the launch id only when the backend serves
        # that exact string (true for ollama, whose model_names are bare).
        # omlx registers `mlx-community/Qwen3.8-27B-4bit` while serving the
        # basename, so a direct omlx row needs direct_model. A misaddressed
        # agent row is indistinguishable from a model that failed the task,
        # which is the one confusion this harness must not allow.
        launch_id = row.direct_model or model_name
        base_url = direct_cfg.base_url
        api = direct_cfg.api
        api_key = "ollama"  # pi rejects an empty apiKey even for keyless local backends
    else:
        raise BenchmarkError(f"row {row.label!r} has unknown route: {row.route!r}")

    live_entry = _lookup_live_entry(live, pi_provider, launch_id) or {}
    context_window = live_entry.get("contextWindow", DEFAULT_CONTEXT_WINDOW)
    reasoning = live_entry.get("reasoning", True)

    return PiTarget(
        pi_provider=pi_provider,
        launch_id=launch_id,
        base_url=base_url,
        api=api,
        api_key=api_key,
        context_window=context_window,
        reasoning=reasoning,
    )


def build_models_json(target: PiTarget) -> dict:
    """Provider ids and entry shape mirror wt/internal/agents/pi_models.go
    exactly (id/contextWindow/input/reasoning/_launch) so an agent row
    behaves like a wt-launched session."""
    return {
        "providers": {
            target.pi_provider: {
                "api": target.api,
                "apiKey": target.api_key,
                "baseUrl": target.base_url,
                "models": [
                    {
                        "_launch": True,
                        "contextWindow": target.context_window,
                        "id": target.launch_id,
                        "input": ["text", "image"],
                        "reasoning": target.reasoning,
                    }
                ],
            }
        }
    }


def write_pi_config(target: PiTarget, config_dir: Path) -> Path:
    """Write models.json into a per-run PI_CODING_AGENT_DIR. The caller
    removes config_dir in a finally block — it contains the LiteLLM key."""
    config_dir.mkdir(parents=True, exist_ok=True)
    path = config_dir / "models.json"
    path.write_text(json.dumps(build_models_json(target), indent=2), encoding="utf-8")
    return path


def build_pi_command(target: PiTarget, thinking: str, session_dir: Path, prompt: str) -> list[str]:
    return [
        "pi",
        *PI_BASE_ARGS,
        "--session-dir", str(session_dir),
        "--model", target.model_arg,
        "--thinking", thinking,
        "-p", prompt,
    ]
