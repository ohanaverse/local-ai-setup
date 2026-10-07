"""MTPLX backend — moved from `lifecycle/__init__.py`, which held mtplx's
original, unmoved Python implementation from before this port began (the
ONE backend already native in Python, unlike ollama/omlx/mlx_lm_server,
which were ported from bash by earlier tasks in this series).

This move also wires mtplx onto the shared `probe.py`/`pidproc.py`
primitives instead of the private helpers `__init__.py` used to carry
(`_wait_for_port_closed`, `_wait_for_model`, `_serving_model`, `_warmup`,
`_start_mtplx_serve`, `_log_tail`) — those primitives were generalized FROM
this exact mtplx code by an earlier task, without wiring `__init__.py` to
use them yet. This module closes that gap.

One deliberate behavior change versus today's `_stop_mtplx`: `stop_and_wait`
now polls for the port to actually close and reports a warning if it
doesn't, instead of unconditionally reporting success once the `mtplx
stop` subprocess exits 0. See `stop_and_wait`'s docstring.
"""

from __future__ import annotations

import subprocess

from ....registry import load_registry
from ...mtplx import MTPLX_BASE, MTPLX_PORT, MTPLX_V1_BASE
from .. import binaries, probe
from ..envelope import LifecycleError
from ..pidproc import PidfileProcess
from .base import PidfileTrackedBackend, StartPlan

MTPLX_DIRECT_URL = f"{MTPLX_V1_BASE}/chat/completions"
MTPLX_PIDFILE = "/tmp/local-ai-setup-mtplx.pid"
MTPLX_LOG = "/tmp/local-ai-setup-mtplx.log"

_PROC = PidfileProcess(name="mtplx", pidfile=MTPLX_PIDFILE, logfile=MTPLX_LOG)


class MtplxBackend(PidfileTrackedBackend):
    id = "mtplx"
    occupancy_key = "mtplx"
    env_var = None
    default_model = None
    health_url = f"{MTPLX_BASE}/v1/models"
    chat_url = MTPLX_DIRECT_URL
    cleanup_on_failure = True  # the ONE backend that sets this True
    restore_action = "stop"  # never part of the standing baseline

    def check_available(self) -> str | None:
        try:
            binaries.require_binary("mtplx")
        except LifecycleError as exc:
            return str(exc)
        return None

    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        """The MTPLX repo id to serve: the explicit `model`, else the first
        positional `extra_args` entry (how local_control.py forwards it —
        isolate_provider() only ever populates `model` from an env-var
        lookup, and mtplx is deliberately excluded from that mapping), else
        the single mtplx model in the registry. With no explicit model and
        more than one mtplx entry, refuse rather than guess — a caller that
        failed to forward the model name would otherwise silently serve
        (and benchmark) the wrong weights with no error."""
        resolved = model or self._positional_arg(extra_args, 0)
        if not resolved:
            registry = load_registry()
            matches = [m.model_name for m in registry.models if m.provider_id == "mtplx"]
            if len(matches) == 1:
                resolved = matches[0]
            elif not matches:
                raise LifecycleError("no mtplx model in the registry")
            else:
                raise LifecycleError(
                    f"model required: registry holds {len(matches)} mtplx models "
                    f"({', '.join(matches)})"
                )
        return StartPlan(model=resolved, direct_url=MTPLX_DIRECT_URL)

    def already_serving(self, plan: StartPlan) -> bool:
        return probe.serving_model(self.health_url, plan.model)

    def replace_own_occupant(self, plan: StartPlan) -> None:
        stop_warning = self.stop_and_wait()
        if stop_warning is not None:
            # Surface the real cause here rather than falling through to
            # start()'s wait_for_port_closed poll, which would fail ~10s
            # later with a generic "port still answering" message that
            # hides why the port never closed.
            raise LifecycleError(stop_warning)

    def start(self, plan: StartPlan) -> None:
        bin_path = binaries.require_binary("mtplx")
        # Wait for the OS to reclaim port 8003 before spawning the new
        # process. Spawning into a still-held port can leave the old
        # process answering warmup, or the new process may die immediately
        # and leave a stale pidfile. This is a fast poll (returns
        # immediately once the port is closed, which the caller's own
        # teardown should have already achieved) kept as a safety net: the
        # bash stop-all's own port-closed poll only retries 5 times before
        # warning-and-continuing, rather than blocking until closed.
        probe.wait_for_port_closed(self.health_url, timeout=10.0)
        self._proc = _PROC.spawn(
            [
                bin_path,
                "serve",
                "--model",
                plan.model,
                "--port",
                str(MTPLX_PORT),
                "--host",
                "127.0.0.1",
                # Without --model-id, mtplx serves under a slug it derives
                # from the artifact (e.g. "mtplx-qwen38-27b-optimized-
                # quality"), not the org/model repo id — wait_ready would
                # never see /v1/models list `plan.model`, so it would time
                # out even though the server is healthy (confirmed live:
                # 2026-09-10). Pinning --model-id to the resolved repo id
                # makes /v1/models report exactly what we poll for.
                "--model-id",
                plan.model,
            ]
        )

    def wait_ready(self, plan: StartPlan) -> None:
        probe.wait_for_model(
            self.health_url, plan.model, proc=self._proc, timeout=probe.MODEL_LOAD_TIMEOUT
        )

    def warm(self, plan: StartPlan) -> None:
        # mtplx's warmup uses a shorter timeout (120s) than the base
        # class's default probe.WARMUP_TIMEOUT (300s) — preserved
        # unchanged from today's _warmup.
        probe.warmup(self.chat_url, plan.model, health_url=self.health_url, timeout=120.0)

    def stop_and_wait(self) -> str | None:
        """Stop MTPLX via `mtplx stop --port 8003 --grace-seconds 10`, then
        confirm the port actually closed.

        Bug fix versus today's `_stop_mtplx`: today's code returns success
        as soon as the `mtplx stop` subprocess exits 0, with no check that
        port 8003 actually stopped answering — bash's `stop mtplx` case arm
        never captured a warning either, so a stop that reported success
        while mtplx kept the port bound went completely unnoticed. This now
        polls `probe.port_closed_within` and reports a warning if the port
        is still open.

        The port check runs regardless of `mtplx stop`'s exit code, not
        only after a clean exit — `mtplx stop` itself exits non-zero
        whenever nothing is listening on the port (see orchestrate.py's
        module docstring), which is exactly the state on a machine's first
        `modelman start` of an mtplx model. Short-circuiting on that exit
        code alone (the original shape of this fix) treated "nothing to
        stop" as a hard failure and made `replace_own_occupant()` raise on
        a start that had nothing to replace. Checking the port first makes
        "stop command failed, but the port is already closed" resolve to
        success, while still surfacing the command's own stderr — more
        informative than the generic "still listening" message below — when
        the port genuinely never closes.
        """
        try:
            bin_path = binaries.require_binary("mtplx")
        except LifecycleError:
            return "mtplx binary not found on PATH"
        result = subprocess.run(
            [bin_path, "stop", "--port", str(MTPLX_PORT), "--grace-seconds", "10"],
            capture_output=True,
            text=True,
            check=False,
        )
        if probe.port_closed_within(self.health_url, timeout=probe.STOP_WAIT_TIMEOUT):
            return None
        if result.returncode != 0:
            return result.stderr.strip() or result.stdout.strip() or "mtplx stop failed"
        return "mtplx still listening on port 8003"


MTPLX = MtplxBackend()
