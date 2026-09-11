"""Shared local-provider process types and HTTP probe helper.

Neutral home for code that both `modelman.benchmark.isolation` (isolating a
provider for a benchmark run) and `modelman.providers.lifecycle` (isolating
a provider for `modelman start`/`modelman stop`) need. Neither concept
belongs to one side more than the other, so living under `benchmark/` or
`providers/` made the other package's import backwards — this module has no
dependency on either.
"""

from __future__ import annotations

import json
import urllib.request
from dataclasses import dataclass


@dataclass
class ProcessResult:
    provider: str
    model: str
    direct_url: str
    ok: bool
    error: str | None


# Provider ids that use an LLM_ISOLATE_*_MODEL env var in bin/llm-isolate-provider.
# mlx_lm_server is deliberately absent: it takes target+draft as positional args.
ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


def http_models_ids(url: str, timeout: float = 2.0) -> list[str]:
    """Model ids from an OpenAI-compatible /v1/models response, or [] on any
    error (connection refused, timeout, non-JSON body)."""
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:  # noqa: S310 — localhost probe
            data = json.loads(resp.read().decode())
    except (OSError, ValueError):
        return []
    items = data.get("data") if isinstance(data, dict) else None
    if not isinstance(items, list):
        return []
    return [
        item["id"]
        for item in items
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    ]
