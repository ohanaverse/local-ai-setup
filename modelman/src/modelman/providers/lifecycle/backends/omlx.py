"""oMLX backend — ported from `bin/llm-isolate-provider`'s `omlx)` and
`omlx-6bit)` case arms, `start_omlx()`, and `stop_omlx_and_wait()`.

Both the 4-bit and 6-bit variants are served by ONE physical `omlx` daemon
on ONE port (8000) — there is no per-variant process or port, only whichever
model the daemon was last told to load. `OmlxBackend` is therefore a single
class parameterized by constructor args (id/env var/default model/restore
action), with two module-level instances below rather than two subclasses,
since the behavior is otherwise identical.

Both instances set `occupancy_key = "omlx"` (not their own `id`) so
`orchestrate.py` treats them as one shared occupant when deduplicating
stop-all/restore fan-out (`_distinct_backends`) — see the class docstring
and module-level instances below.

Live: registered in `backends.BACKENDS`, which `orchestrate.py` reads to
drive `isolate()`/`stop()`/`stop_all()`/`restore()`, reached from
`modelman provider ...`, `modelman benchmark`, and `local_control.py`.
"""

from __future__ import annotations

import shutil
import subprocess
import urllib.request

from .. import probe
from ..envelope import LifecycleError
from .base import Backend, StartPlan

OMLX_PORT = 8000
OMLX_BASE = f"http://localhost:{OMLX_PORT}"
OMLX_HEALTH_URL = f"{OMLX_BASE}/v1/models"
OMLX_CHAT_URL = f"{OMLX_BASE}/v1/chat/completions"
DEFAULT_4BIT_MODEL = "Ornith-1.5-35B-A3B-MLX-4bit"
DEFAULT_6BIT_MODEL = "Ornith-1.5-35B-A3B-MLX-6bit"
ENV_VAR_4BIT = "LLM_ISOLATE_OMLX_4BIT_MODEL"
ENV_VAR_6BIT = "LLM_ISOLATE_OMLX_6BIT_MODEL"


class OmlxBackend(Backend):
    """One omlx-served variant (4-bit or 6-bit). Both instances share the
    single physical omlx daemon/port, hence the shared `occupancy_key`."""

    occupancy_key = "omlx"
    health_url = OMLX_HEALTH_URL
    chat_url = OMLX_CHAT_URL

    def __init__(self, id: str, env_var: str, default_model: str, restore_action: str) -> None:
        self.id = id
        self.env_var = env_var
        self.default_model = default_model
        self.restore_action = restore_action

    def check_available(self) -> str | None:
        # bash: `command -v omlx >/dev/null 2>&1 || { echo "omlx binary not
        # found on PATH" >&2; exit 1; }` — match this exact message.
        if shutil.which("omlx") is None:
            return "omlx binary not found on PATH"
        return None

    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        return StartPlan(model=self._resolve_model(model), direct_url=OMLX_CHAT_URL)

    def start(self, plan: StartPlan) -> None:
        # bash: `omlx start >/dev/null 2>&1 || true` — output discarded,
        # failure swallowed; the daemon is idempotent to start when already
        # running.
        subprocess.run(["omlx", "start"], capture_output=True, check=False)
        self.warm(plan)

    def stop_and_wait(self) -> str | None:
        # bash: `silence_stdout omlx stop || true` — output discarded,
        # failure swallowed.
        subprocess.run(["omlx", "stop"], capture_output=True, check=False)
        if probe.port_closed_within(OMLX_HEALTH_URL, timeout=probe.STOP_WAIT_TIMEOUT):
            return None
        # Literal port number, matching bash's hardcoded message — there is
        # only ever one omlx port.
        return "omlx still listening on port 8000"

    def restore(self) -> None:
        if self.restore_action != "restart":
            return
        # bash: `curl -s -m 2 http://localhost:8000/v1/models >/dev/null
        # 2>&1 && return 0` — a single one-shot 2s probe, not a poll loop.
        try:
            urllib.request.urlopen(OMLX_HEALTH_URL, timeout=2.0)  # noqa: S310 — localhost probe
            return
        except OSError:
            pass
        subprocess.run(["omlx", "start"], capture_output=True, check=False)
        if not probe.wait_for_port_open(OMLX_HEALTH_URL, timeout=probe.RESTORE_WAIT_TIMEOUT):
            raise LifecycleError(f"omlx did not come back up ({OMLX_HEALTH_URL})")


# Only the 4-bit instance restores — starting it twice (once via each
# instance) during a restore fan-out would be redundant since they share one
# daemon.
OMLX_4BIT = OmlxBackend("omlx", ENV_VAR_4BIT, DEFAULT_4BIT_MODEL, restore_action="restart")
OMLX_6BIT = OmlxBackend("omlx-6bit", ENV_VAR_6BIT, DEFAULT_6BIT_MODEL, restore_action="skip")
