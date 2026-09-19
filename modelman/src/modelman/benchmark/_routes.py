"""Shared credential/endpoint helpers for the benchmark subsystem's
route resolution: the OpenRouter key (environment or the LiteLLM
LaunchAgent plist) and the LiteLLM apiKey/baseUrl from pi's
~/.pi/agent/models.json.

agent/ and eval/ each parse their own suites (deliberately parallel —
their row shapes differ), but the CREDENTIALS those routes need are the
same machine-level facts in both benchmarks; this module is the single
source so a plist/models.json format change is fixed exactly once."""

from __future__ import annotations

import json
import os
import plistlib
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError

LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"
OPENROUTER_BASE_URL = "https://openrouter.ai/api/v1"


def openrouter_key(plist_path: Path = LITELLM_PLIST) -> str | None:
    """The OpenRouter key, from the environment or the LiteLLM LaunchAgent.

    Same two places preflight looks; a judge on route=openrouter needs the
    value, not just the knowledge that one exists."""
    env_key = os.environ.get("OPENROUTER_API_KEY")
    if env_key:
        return env_key
    if not plist_path.exists():
        return None
    try:
        with plist_path.open("rb") as f:
            data = plistlib.load(f)
    except Exception:
        return None
    key = data.get("EnvironmentVariables", {}).get("OPENROUTER_API_KEY")
    return str(key) if key else None


def load_live_models(path: Path = LIVE_PI_MODELS_PATH) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def litellm_credentials(
    live_models_path: Path = LIVE_PI_MODELS_PATH,
) -> tuple[str, str]:
    """(base_url, api_key) for the local LiteLLM gateway, from pi's
    models.json litellm provider entry. Raises when the apiKey is absent —
    every caller (judge transports on both benchmarks, eval's litellm
    route) hard-requires it."""
    live = load_live_models(live_models_path)
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError(
            "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
            "pi session in litellm mode at least once to seed it"
        )
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return base_url, api_key


__all__ = [
    "LITELLM_PLIST",
    "LIVE_PI_MODELS_PATH",
    "OPENROUTER_BASE_URL",
    "litellm_credentials",
    "load_live_models",
    "openrouter_key",
]
