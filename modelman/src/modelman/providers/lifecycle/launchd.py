"""Raw launchd primitives (kickstart/load/unload) used by provider lifecycle.

Separate from `modelman.litellm.restart_litellm_proxy`: that one is
config-write driven and honors `MODELMAN_LITELLM_RESTART_CMD`, while this
module is the raw `launchctl` primitive provider lifecycle code calls
directly (e.g. to bounce a LaunchAgent-managed provider).
"""

from __future__ import annotations

import os
import subprocess
from pathlib import Path

OLLAMA_LABEL = "com.ollama.ollama"
LITELLM_PLIST = Path.home() / "Library/LaunchAgents/local.litellm.proxy.plist"
LITELLM_PORT = 4000
LITELLM_HEALTH_URL = f"http://localhost:{LITELLM_PORT}/v1/models"
LLAMACPP_PLIST = Path.home() / "Library/LaunchAgents/local.llamacpp.server.plist"


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
