"""Parse `ollama show` output for LiteLLM-compatible model_info fields."""

from __future__ import annotations

import subprocess
from typing import Any, Protocol


class _Runner(Protocol):
    def __call__(self, *args: Any, **kwargs: Any) -> Any: ...


_CAPABILITY_MAP = {
    "tools": "supports_function_calling",
    "vision": "supports_vision",
}


def parse_ollama_show(stdout: str) -> dict[str, Any]:
    """Extract a model_info dict from `ollama show <model>` text output.

    Only populates keys we know how to map today; everything else is ignored.
    """
    info: dict[str, Any] = {}
    in_caps = False
    for line in stdout.splitlines():
        stripped = line.strip()
        if not stripped:
            in_caps = False
            continue
        if stripped.lower() == "capabilities":
            in_caps = True
            continue
        if not in_caps:
            continue
        key = _CAPABILITY_MAP.get(stripped)
        if key and key not in info:
            info[key] = True
    return info


def _default_runner(args: list[str], **kwargs: Any):
    return subprocess.run(args, **kwargs)


def auto_detect_model_info(name: str, runner: _Runner | None = None) -> dict[str, Any]:
    """Run `ollama show <name>` and return its parsed model_info. {} on failure."""
    r = (runner or _default_runner)(["ollama", "show", name], capture_output=True, text=True)
    if r.returncode != 0:
        return {}
    return parse_ollama_show(r.stdout)
