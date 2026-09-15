"""Local-provider lifecycle: start/stop/warmup for on-demand local models.

This module is the first step toward consolidating local-provider lifecycle
logic in Python (issue #79). For now it dispatches MTPLX to
`backends.mtplx.MTPLX` (a `Backend` subclass) and delegates ollama/omlx/
mlx_lm_server to the existing bash helper (bin/llm-isolate-provider) during
the transition period.

The CLI entry point (`python3 -m modelman.providers.lifecycle isolate mtplx`)
prints a single JSON object on stdout — the same envelope bin/llm-isolate-
provider emits — so the bash shim can delegate its `mtplx` case here and pass
the JSON straight through.
"""

from __future__ import annotations

import contextlib
import json
import os
import shutil
import subprocess
import sys
from dataclasses import asdict

from ...local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from .backends.mtplx import MTPLX, MTPLX_DIRECT_URL
from .envelope import LifecycleError, LifecycleResult


def _stop_others(keep: str = "") -> None:
    """Stop every local provider except the one about to start, by
    delegating to the bash helper's stop-all mode (transition period).
    `keep` names one provider whose models stay loaded — mirroring the
    per-provider branches' stop_all_local keep arg (the helper's own
    helper path resolution note from Task 2 stays as written).

    The helper's own absolute path (exported by the bash shim as
    LLM_ISOLATE_HELPER) wins over PATH: the shim is routinely invoked by
    absolute path with bin/ absent from PATH, where shutil.which() would
    return None and fail an isolate that already stopped everything."""
    helper = os.environ.get("LLM_ISOLATE_HELPER") or shutil.which("llm-isolate-provider")
    if helper is None:
        raise LifecycleError("isolation helper 'llm-isolate-provider' not found on PATH")
    cmd = [helper, "stop-all"] + ([keep] if keep else [])
    result = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise LifecycleError(
            f"failed to stop other providers: {result.stderr.strip() or result.stdout.strip()}"
        )


def _delegate_stop_all() -> LifecycleResult:
    helper = shutil.which("llm-isolate-provider")
    if helper is None:
        return LifecycleResult("stop-all", "", "", False, "isolation helper not found on PATH")
    result = subprocess.run([helper, "stop-all"], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        return LifecycleResult("stop-all", "", "", False, result.stderr.strip() or result.stdout.strip())
    return LifecycleResult("stop-all", "", "", True, None)


def isolate(
    provider_id: str, model: str | None = None, *, extra_args: tuple[str, ...] = (), solo: bool = False
) -> LifecycleResult:
    """Stop every other local provider and start the requested one —
    unless solo=True, in which case only THIS provider's own occupant (if
    serving a different model) is stopped, and every sibling provider is
    left alone. solo is used by modelman's same-provider-only local-model
    lifecycle (local_control.py); never passed by modelman benchmark."""
    if provider_id != "mtplx":
        # Transition: delegate non-mtplx providers to the bash helper.
        return _delegate_isolate(provider_id, model, extra_args, solo=solo)
    started = False
    try:
        plan = MTPLX.resolve(model, extra_args)
        if MTPLX.already_serving(plan):
            # Keep semantics, matching the bash providers (stop_all_local
            # $provider skips the target): re-isolating the model mtplx
            # is already serving pays only warmup, not a full stop +
            # respawn + multi-minute reload. mtplx is
            # single-model-per-process, so only the SAME model is
            # keepable — a different model takes the full restart below.
            if not solo:
                _stop_others(keep="mtplx")
            # From here, `started` means "a live mtplx process exists that
            # this call is responsible for tearing down on failure" — not
            # literally "this call spawned it". A wedged server (answers
            # /v1/models but hangs or errors on chat completions) must not
            # be left running for the next isolate() call to retry warmup
            # against forever with no path to recovery. Set only after
            # _stop_others succeeds: a failure there stopped the OTHER
            # providers, not mtplx, so it must not trigger mtplx teardown.
            started = True
            MTPLX.warm(plan)
        else:
            if solo:
                # MTPLX.replace_own_occupant() calls stop_and_wait() and
                # raises immediately on a non-None warning, rather than
                # falling through to start()'s wait_for_port_closed poll,
                # which would fail ~10s later with a generic "port still
                # answering" message that hides why the port never closed.
                MTPLX.replace_own_occupant(plan)
            else:
                _stop_others()
            MTPLX.start(plan)
            started = True
            MTPLX.wait_ready(plan)
            MTPLX.warm(plan)
    except Exception as exc:  # noqa: BLE001 — envelope contract, never a traceback
        # RegistryError/TOMLDecodeError from load_registry() are NOT
        # LifecycleError; anything escaping here must still return the
        # JSON envelope the bash shim and benchmark isolation parse.
        if started:
            # Teardown: serve is up but load/warmup failed — a running
            # orphan holding port 8003 and GPU/RAM breaks the one-local-
            # model invariant this module exists to enforce. Best-effort:
            # a cleanup failure must not mask the original error.
            with contextlib.suppress(Exception):  # noqa: BLE001
                MTPLX.stop_and_wait()
        return LifecycleResult("mtplx", model or "", MTPLX_DIRECT_URL, False, str(exc))
    return LifecycleResult("mtplx", plan.model, MTPLX_DIRECT_URL, True, None)


def _delegate_isolate(
    provider_id: str, model: str | None, extra_args: tuple[str, ...], *, solo: bool = False
) -> LifecycleResult:
    helper = shutil.which("llm-isolate-provider")
    if helper is None:
        return LifecycleResult(provider_id, model or "", "", False, "isolation helper not found on PATH")
    env = None
    if model and provider_id in _ENV_VAR_BY_PROVIDER:
        # Only set the env var for providers that read it in the bash helper.
        # MTPLX receives the model as a positional arg; mlx_lm_server receives
        # target/draft as positional args — both are forwarded via extra_args.
        env = {**os.environ, _ENV_VAR_BY_PROVIDER[provider_id]: model}
    argv = [helper, *(["--solo"] if solo else []), provider_id, *extra_args]
    result = subprocess.run(argv, capture_output=True, text=True, check=False, env=env)
    if result.returncode != 0:
        return LifecycleResult(provider_id, model or "", "", False, result.stderr.strip() or result.stdout.strip())
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError:
        return LifecycleResult(provider_id, model or "", "", False, "invalid JSON from isolation helper")
    return LifecycleResult(
        provider=data.get("provider", provider_id),
        model=data.get("model", ""),
        direct_url=data.get("direct_url", ""),
        ok=data.get("ok", False),
        error=data.get("error"),
    )


def stop(provider_id: str) -> LifecycleResult:
    """Stop one provider."""
    if provider_id == "mtplx":
        warning = MTPLX.stop_and_wait()
        return LifecycleResult("mtplx", "", MTPLX_DIRECT_URL, warning is None, warning)
    return LifecycleResult(provider_id, "", "", False, f"stop not implemented for {provider_id}")


def stop_all() -> LifecycleResult:
    """Stop every local provider.

    Delegates entirely to the bash helper's stop-all mode, which already
    tears down mtplx via the shared `mtplx_stop` bash function as part of
    the same call — calling `stop("mtplx")` here too would double-stop it
    (paying a second full `mtplx stop --grace-seconds 10`) and, on a
    machine without the `mtplx` binary installed, would misreport overall
    failure even though every real provider was torn down cleanly.
    """
    return _delegate_stop_all()


def _main(argv: list[str]) -> int:
    if not argv:
        print("usage: lifecycle <isolate|stop|stop-all> [provider] [model] ...", file=sys.stderr)
        return 1
    cmd = argv[0]
    try:
        if cmd == "isolate":
            provider = argv[1] if len(argv) > 1 else ""
            rest = argv[2:]
            solo = "--solo" in rest
            rest = [a for a in rest if a != "--solo"]
            model = rest[0] if rest else None
            extra_args = tuple(rest[1:])
            result = isolate(provider, model, extra_args=extra_args, solo=solo)
        elif cmd == "stop":
            provider = argv[1] if len(argv) > 1 else ""
            result = stop(provider)
        elif cmd == "stop-all":
            result = stop_all()
        else:
            print(f"unknown command: {cmd}", file=sys.stderr)
            return 1
    except Exception as exc:  # noqa: BLE001 — CLI contract: one JSON envelope or nothing
        result = LifecycleResult(cmd, "", "", False, f"{type(exc).__name__}: {exc}")
    print(json.dumps(asdict(result)))
    return 0 if result.ok else 1


if __name__ == "__main__":
    sys.exit(_main(sys.argv[1:]))
