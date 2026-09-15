"""mlx_lm_server backend — ported from `bin/llm-isolate-provider`'s
`mlx_lm_server)` case arm and `bin/lib/mlx-lm-server.sh`'s
`mlx_lm_server_start`/`mlx_lm_server_stop`.

Unlike ollama/omlx, `mlx_lm.server` is a plain backgrounded subprocess
bound to exactly one target+draft speculative-decoding pairing for its
whole lifetime — there is no always-on daemon to point at a different
model, and no default pairing baked into this repo. All target/draft
resolution and validation happens in `resolve()`, which is pure (no
subprocess/pidfile/probe calls) — bash's start path has a real bug where
`stop_all_local mlx_lm_server` (tearing down every OTHER local provider)
runs BEFORE validating that target+draft were supplied, so an invalid
invocation still tears everything else down before failing. Keeping
`resolve()` pure and side-effect-free is what lets `orchestrate.isolate()`
call it before any teardown, structurally closing that bug.

Live: registered in `backends.BACKENDS`, which `orchestrate.py` reads to
drive `isolate()`/`stop()`/`stop_all()`/`restore()`, reached from
`modelman provider ...`, `modelman benchmark`, and `local_control.py`.
"""

from __future__ import annotations

import os

from .. import binaries, probe
from ..envelope import LifecycleError
from ..pidproc import PidfileProcess
from .base import Backend, StartPlan

MLX_LM_SERVER_PORT = 8001
MLX_LM_SERVER_BASE = f"http://localhost:{MLX_LM_SERVER_PORT}"
MLX_LM_SERVER_HEALTH_URL = f"{MLX_LM_SERVER_BASE}/v1/models"
MLX_LM_SERVER_CHAT_URL = f"{MLX_LM_SERVER_BASE}/v1/chat/completions"
PIDFILE = "/tmp/local-ai-setup-mlx-lm-server.pid"
LOGFILE = "/tmp/local-ai-setup-mlx-lm-server.log"
TARGET_ENV_VAR = "LLM_ISOLATE_MLXLM_MODEL"
DRAFT_ENV_VAR = "LLM_ISOLATE_MLXLM_DRAFT_MODEL"

_PROC = PidfileProcess(name="mlx_lm_server", pidfile=PIDFILE, logfile=LOGFILE)


class MlxLmServerBackend(Backend):
    id = "mlx_lm_server"
    occupancy_key = "mlx_lm_server"
    env_var = None  # two separate env vars (target/draft), not one — see resolve()
    default_model = None
    health_url = MLX_LM_SERVER_HEALTH_URL
    chat_url = MLX_LM_SERVER_CHAT_URL
    restore_action = "stop"  # never part of the standing baseline

    def check_available(self) -> str | None:
        # check_available()'s contract (backends/base.py) is "reason string
        # or None", never raising — resolve_mlx_lm_bin raises LifecycleError
        # on a missing omlx install, so that must be caught here rather than
        # left to propagate.
        try:
            binaries.resolve_mlx_lm_bin("server")
        except LifecycleError as exc:
            return str(exc)
        return None

    def resolve(self, model: str | None, extra_args: tuple[str, ...]) -> StartPlan:
        """Resolve target+draft and validate them — PURE, no subprocess
        calls, no side effects of any kind. This is the bug-fix-critical
        method: bash's start path tears down every other local provider
        BEFORE checking that target+draft were supplied, so an invalid
        invocation still tore everything else down before failing. By
        keeping all validation here, with zero interaction with any other
        provider or process, a caller (the orchestration layer) can call
        resolve() first and reject a bad request before touching anything
        else.
        """
        positional_target = extra_args[0] if extra_args and extra_args[0] else ""
        target = model or positional_target or os.environ.get(TARGET_ENV_VAR, "")

        positional_draft = extra_args[1] if len(extra_args) > 1 and extra_args[1] else ""
        draft = positional_draft or os.environ.get(DRAFT_ENV_VAR, "")
        if not target or not draft:
            raise LifecycleError(
                "mlx_lm_server requires target+draft: run `modelman provider "
                "isolate mlx_lm_server <target> --draft <draft>`, or set "
                "LLM_ISOLATE_MLXLM_MODEL/LLM_ISOLATE_MLXLM_DRAFT_MODEL "
                "(in-process callers forward the pair as extra_args=(target, draft))"
            )

        bin_path = binaries.resolve_mlx_lm_bin("server")

        return StartPlan(
            model=f"{target} (+draft {draft})",
            direct_url=MLX_LM_SERVER_CHAT_URL,
            argv=[
                bin_path,
                "--model",
                target,
                "--draft-model",
                draft,
                "--port",
                str(MLX_LM_SERVER_PORT),
            ],
            extra={"target": target},
        )

    def start(self, plan: StartPlan) -> None:
        """Stop any prior instance, then FATALLY wait for the port to
        close before spawning the replacement.

        `mlx_lm.server` is a plain subprocess with no daemon-style
        awareness of a second spawn: `_PROC.stop()` above only sends
        SIGTERM, it doesn't wait for the process to exit, and freeing a
        multi-GB MLX model from GPU/RAM takes the old server many
        seconds. If the new process is spawned while the port is still
        bound, it loses the bind race and dies, but warmup then polls the
        port and the STILL-RUNNING old server answers — isolation reports
        success while the benchmark silently measures the previous
        (possibly wrong) pairing, and the pidfile names the dead new
        process so a later stop is a no-op. Proceeding on a timed-out
        wait is worse than failing, so the timeout is fatal here: refuse
        to spawn rather than spawn into a port we cannot have.
        """
        _PROC.stop()
        if not probe.port_closed_within(
            MLX_LM_SERVER_HEALTH_URL, timeout=probe.PREBIND_WAIT_TIMEOUT
        ):
            raise LifecycleError(
                f"port {MLX_LM_SERVER_PORT} still in use after stopping "
                "prior mlx_lm_server — refusing to spawn a replacement "
                "that cannot bind (is something else listening on "
                f"{MLX_LM_SERVER_PORT}?)"
            )
        if plan.argv is None:  # resolve() always populates argv
            raise LifecycleError("mlx_lm_server start plan has no argv")
        _PROC.spawn(plan.argv)

    def warm(self, plan: StartPlan) -> None:
        # Base Backend.warm() would pass plan.model (the "<target> (+draft
        # <draft>)" display string) as the chat payload's model field,
        # which mlx_lm.server would not recognize — the warmup payload
        # must name the bare target repo id instead.
        probe.warmup(
            plan.direct_url,
            plan.extra["target"],
            health_url=self.health_url,
            timeout=probe.WARMUP_TIMEOUT,
        )

    def stop_and_wait(self) -> str | None:
        _PROC.stop()
        if probe.port_closed_within(MLX_LM_SERVER_HEALTH_URL, timeout=probe.STOP_WAIT_TIMEOUT):
            return None
        return "mlx_lm_server still listening on port 8001"


MLX_LM_SERVER = MlxLmServerBackend()
