"""Local-provider lifecycle: start/stop/warmup for on-demand local models.

This module is the first step toward consolidating local-provider lifecycle
logic in Python (issue #79). For now it owns MTPLX directly and delegates
ollama/omlx/mlx_lm_server to the existing bash helper (bin/llm-isolate-
provider) during the transition period.

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
import time
import urllib.error
import urllib.request
from dataclasses import asdict

from ..local_process import ENV_VAR_BY_PROVIDER as _ENV_VAR_BY_PROVIDER
from ..local_process import ProcessResult as LifecycleResult
from ..local_process import http_models_ids as _http_models_ids
from ..registry import load_registry
from .mtplx import MTPLX_BASE, MTPLX_PORT, MTPLX_V1_BASE

MTPLX_DIRECT_URL = f"{MTPLX_V1_BASE}/chat/completions"
MTPLX_PIDFILE = "/tmp/local-ai-setup-mtplx.pid"
MTPLX_LOG = "/tmp/local-ai-setup-mtplx.log"


class LifecycleError(Exception):
    """Raised for internal lifecycle failures that map to ok=False."""


def _log_tail(max_bytes: int = 1024) -> str:
    """The last ~512 chars of the mtplx serve log — the crash cause for
    'serve died' error messages (empty string when unreadable)."""
    try:
        with open(MTPLX_LOG, "rb") as f:
            f.seek(0, 2)
            size = f.tell()
            f.seek(max(0, size - max_bytes))
            return f.read().decode(errors="replace")[-512:]
    except OSError:
        return ""


def _resolve_mtplx_model(model: str | None) -> str:
    """The MTPLX repo id to serve: the explicit `model`, else the single
    mtplx model in the registry. With no explicit model and more than one
    mtplx entry, refuse rather than guess — a caller that failed to
    forward the model name would otherwise silently serve (and
    benchmark) the wrong weights with no error."""
    if model:
        return model
    registry = load_registry()
    matches = [m.model_name for m in registry.models if m.provider_id == "mtplx"]
    if len(matches) == 1:
        return matches[0]
    if not matches:
        raise LifecycleError("no mtplx model in the registry")
    raise LifecycleError(
        f"model required: registry holds {len(matches)} mtplx models "
        f"({', '.join(matches)})"
    )


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


def _wait_for_port_closed(url: str, timeout: float = 10.0) -> None:
    """Poll a localhost URL until it stops responding, or raise on timeout.

    A response — success or HTTP error status — means something is still
    listening. `HTTPError` is a `URLError`/`OSError` subclass, so it must
    be caught before the `URLError` catch below, or a server merely
    answering with a non-2xx status (e.g. 404 for a not-yet-ready path)
    would be misread as the port having closed. A read `TimeoutError` is
    also NOT a closed port: the listener accepted the connection and then
    stalled (hung, or draining under load) — exactly the "still holds the
    port" case this poll exists to catch. Only a connection-level failure
    (refused, reset, no route) means the port actually closed.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1.0) as resp:  # noqa: S310 — localhost probe
                resp.read()
        except urllib.error.HTTPError:
            pass  # server answered (with an error status) — still open
        except urllib.error.URLError as exc:
            # urlopen wraps connection-level failures in URLError. A
            # connect timeout (reason is a TimeoutError) is ambiguous —
            # treat it as still open rather than risk spawning into a
            # held port.
            if isinstance(exc.reason, TimeoutError):
                pass
            else:
                return  # refused / reset / no route — port closed
        except TimeoutError:
            pass  # accepted the connection, then stalled mid-read — still open
        except ConnectionError:
            return  # reset at read time — the listener is dying or gone
        time.sleep(0.2)
    raise LifecycleError(f"port still answering at {url} after {timeout}s")


def _start_mtplx_serve(model: str) -> subprocess.Popen:
    """Start `mtplx serve` in the background, tracked by a pidfile, and
    return the Popen handle so the caller can watch for process death
    during the model-load poll.

    Assumes the caller (isolate()) has already stopped any prior mtplx
    instance via _stop_others() — stopping it again here would pay a second
    full `mtplx stop --grace-seconds 10` for no benefit, since _stop_others()
    already tore it down as part of the same isolate call."""
    bin_path = shutil.which("mtplx")
    if bin_path is None:
        raise LifecycleError("mtplx binary not found on PATH")
    # Wait for the OS to reclaim port 8003 before spawning the new process.
    # Spawning into a still-held port can leave the old process answering
    # warmup, or the new process may die immediately and leave a stale
    # pidfile. This is a fast poll (returns immediately once the port is
    # closed, which _stop_others() should have already achieved) kept as a
    # safety net: the bash stop-all's own port-closed poll only retries 5
    # times before warning-and-continuing, rather than blocking until closed.
    _wait_for_port_closed(f"{MTPLX_BASE}/v1/models")
    with open(MTPLX_LOG, "ab") as log:
        proc = subprocess.Popen(
            [
                bin_path,
                "serve",
                "--model",
                model,
                "--port",
                str(MTPLX_PORT),
                "--host",
                "127.0.0.1",
                # Without --model-id, mtplx serves under a slug it derives
                # from the artifact (e.g. "mtplx-qwen38-27b-optimized-
                # quality"), not the org/model repo id — /v1/models would
                # never list `model`, so _wait_for_model would time out
                # even though the server is healthy (confirmed live:
                # 2026-09-10). Pinning --model-id to the resolved repo id
                # makes /v1/models report exactly what we poll for.
                "--model-id",
                model,
            ],
            stdout=log,
            stderr=log,
        )
    with open(MTPLX_PIDFILE, "w") as f:
        f.write(str(proc.pid))
    # Give the process a moment to fail (missing weights, port conflict,
    # bad CLI flag) before committing to a full model-load poll.
    time.sleep(0.2)
    if proc.poll() is not None:
        log_tail = _log_tail()
        raise LifecycleError(
            f"mtplx serve exited immediately (exit {proc.poll()})"
            + (f"; log tail: {log_tail}" if log_tail else "")
        )
    return proc


def _wait_for_model(model: str, proc: subprocess.Popen, timeout: float = 300.0) -> None:
    """Poll /v1/models until it lists `model`, the serve process dies, or
    the deadline passes."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            # The serve process died mid-load (OOM kill, missing weights
            # discovered late): without this check the HTTP poll below
            # runs the full 300s against a dead port while the real
            # crash cause stays buried in the log.
            log_tail = _log_tail()
            raise LifecycleError(
                f"mtplx serve exited during model load (exit {proc.returncode})"
                + (f"; log tail: {log_tail}" if log_tail else "")
            )
        ids = _http_models_ids(f"{MTPLX_BASE}/v1/models")
        if any(served == model or served.endswith("/" + model) for served in ids):
            return
        time.sleep(1.0)
    raise LifecycleError(f"timed out waiting for mtplx to serve {model}")


def _serving_model(resolved: str) -> bool:
    """True when the mtplx instance on MTPLX_PORT already lists
    `resolved` (same lenient match as _wait_for_model): a same-model
    re-isolate can keep the loaded weights instead of paying a full
    stop + reload. http_models_ids returns [] on any error, so a down
    or wedged server reads as "not serving" — the safe fallback."""
    ids = _http_models_ids(f"{MTPLX_BASE}/v1/models")
    return any(served == resolved or served.endswith("/" + resolved) for served in ids)


def _warmup(model: str, timeout: float = 120.0) -> None:
    """Send a 1-token chat completion to force the model into GPU/RAM."""
    payload = json.dumps(
        {
            "model": model,
            "messages": [{"role": "user", "content": "hi"}],
            "max_tokens": 1,
            "temperature": 0,
            "stream": False,
        }
    ).encode()
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            req = urllib.request.Request(
                MTPLX_DIRECT_URL,
                data=payload,
                headers={"Content-Type": "application/json"},
            )
            with urllib.request.urlopen(req, timeout=120) as resp:  # noqa: S310
                body = resp.read().decode()
            if '"chat.completion"' in body:
                return
        except OSError:
            pass
        time.sleep(1.0)
    raise LifecycleError(f"failed to warm up mtplx model {model}")


def _stop_mtplx() -> LifecycleResult:
    """Stop MTPLX via `mtplx stop --port 8003 --grace-seconds 10`. Safe to
    call when nothing is running (mtplx stop exits 0)."""
    bin_path = shutil.which("mtplx")
    if bin_path is None:
        return LifecycleResult("mtplx", "", MTPLX_DIRECT_URL, False, "mtplx binary not found on PATH")
    result = subprocess.run(
        [bin_path, "stop", "--port", str(MTPLX_PORT), "--grace-seconds", "10"],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        return LifecycleResult(
            "mtplx", "", MTPLX_DIRECT_URL, False,
            result.stderr.strip() or result.stdout.strip() or "mtplx stop failed",
        )
    return LifecycleResult("mtplx", "", MTPLX_DIRECT_URL, True, None)


def _delegate_stop_all() -> LifecycleResult:
    helper = shutil.which("llm-isolate-provider")
    if helper is None:
        return LifecycleResult("stop-all", "", "", False, "isolation helper not found on PATH")
    result = subprocess.run([helper, "stop-all"], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        return LifecycleResult("stop-all", "", "", False, result.stderr.strip() or result.stdout.strip())
    return LifecycleResult("stop-all", "", "", True, None)


def isolate(provider_id: str, model: str | None = None, *, extra_args: tuple[str, ...] = ()) -> LifecycleResult:
    """Stop every other local provider and start the requested one."""
    if provider_id != "mtplx":
        # Transition: delegate non-mtplx providers to the bash helper.
        return _delegate_isolate(provider_id, model, extra_args)
    started = False
    try:
        resolved = _resolve_mtplx_model(model)
        if _serving_model(resolved):
            # Keep semantics, matching the bash providers (stop_all_local
            # $provider skips the target): re-isolating the model mtplx
            # is already serving pays only warmup, not a full stop +
            # respawn + multi-minute reload. mtplx is
            # single-model-per-process, so only the SAME model is
            # keepable — a different model takes the full restart below.
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
            _warmup(resolved)
        else:
            _stop_others()
            proc = _start_mtplx_serve(resolved)
            started = True
            _wait_for_model(resolved, proc)
            _warmup(resolved)
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
                _stop_mtplx()
        return LifecycleResult("mtplx", model or "", MTPLX_DIRECT_URL, False, str(exc))
    return LifecycleResult("mtplx", resolved, MTPLX_DIRECT_URL, True, None)


def _delegate_isolate(provider_id: str, model: str | None, extra_args: tuple[str, ...]) -> LifecycleResult:
    helper = shutil.which("llm-isolate-provider")
    if helper is None:
        return LifecycleResult(provider_id, model or "", "", False, "isolation helper not found on PATH")
    env = None
    if model and provider_id in _ENV_VAR_BY_PROVIDER:
        # Only set the env var for providers that read it in the bash helper.
        # MTPLX receives the model as a positional arg; mlx_lm_server receives
        # target/draft as positional args — both are forwarded via extra_args.
        env = {**os.environ, _ENV_VAR_BY_PROVIDER[provider_id]: model}
    result = subprocess.run([helper, provider_id, *extra_args], capture_output=True, text=True, check=False, env=env)
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
        return _stop_mtplx()
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
            model = argv[2] if len(argv) > 2 else None
            extra_args = tuple(argv[3:])
            result = isolate(provider, model, extra_args=extra_args)
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
