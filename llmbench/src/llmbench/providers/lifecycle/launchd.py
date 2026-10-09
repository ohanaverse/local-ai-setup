"""Raw launchd primitives (kickstart/load/unload) for provider lifecycle.

Separate from the LiteLLM proxy restart, which wt owns: since 2026-09-21
wt bounces the proxy itself after every route change
(`wt/internal/litellm/restart.go`, honoring `WT_LITELLM_RESTART_CMD` and
the legacy `MODELMAN_LITELLM_RESTART_CMD`). This module is the raw
`launchctl` primitive provider lifecycle code calls directly.

Two of the three have a caller: `kickstart` (the ollama backend's restart)
and `load` (orchestrate._restore_litellm, for the LiteLLM proxy plist).
`unload` lost its only one when the retired llamacpp backend was removed,
and is kept deliberately: docs/reference/provider-artifacts.md's re-enable
steps restore that backend, whose `stop_and_wait` is its one user. Remove it
only together with that runbook.
"""

from __future__ import annotations

import os
import subprocess
from pathlib import Path

OLLAMA_LABEL = "com.ollama.ollama"
LITELLM_PLIST = Path.home() / "Library/LaunchAgents/local.litellm.proxy.plist"
LITELLM_PORT = 4000
LITELLM_HEALTH_URL = f"http://localhost:{LITELLM_PORT}/v1/models"


def _run_launchctl(args: list[str]) -> bool:
    """Run `launchctl <args>`, swallowing failure (matches bash's
    `2>/dev/null || true`) and returning whether it exited zero.
    `FileNotFoundError` is an `OSError` subclass, so one except suffices."""
    try:
        result = subprocess.run(["launchctl", *args], capture_output=True, check=False)
    except OSError:
        return False
    return result.returncode == 0


def kickstart(label: str) -> bool:
    """`launchctl kickstart -k gui/<uid>/<label>`."""
    return _run_launchctl(["kickstart", "-k", f"gui/{os.getuid()}/{label}"])


def load(plist: Path) -> bool:
    """`launchctl load -w <plist>`."""
    return _run_launchctl(["load", "-w", str(plist)])


def unload(plist: Path) -> bool:
    """`launchctl unload <plist>` (no -w flag, matching bash's unload usage)."""
    return _run_launchctl(["unload", str(plist)])
