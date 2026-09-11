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

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
from dataclasses import asdict

from ..benchmark.isolation import IsolateResult as LifecycleResult
from ..registry import load_registry

MTPLX_PORT = 8003
MTPLX_BASE = "http://localhost:8003"
MTPLX_DIRECT_URL = "http://localhost:8003/v1/chat/completions"
MTPLX_PIDFILE = "/tmp/local-ai-setup-mtplx.pid"
MTPLX_LOG = "/tmp/local-ai-setup-mtplx.log"

# Provider ids that use an LLM_ISOLATE_*_MODEL env var in bin/llm-isolate-provider.
# mlx_lm_server is deliberately absent: it takes target+draft as positional args.
_ENV_VAR_BY_PROVIDER = {
    "ollama": "LLM_ISOLATE_OLLAMA_MODEL",
    "omlx": "LLM_ISOLATE_OMLX_4BIT_MODEL",
    "omlx-6bit": "LLM_ISOLATE_OMLX_6BIT_MODEL",
}


class LifecycleError(Exception):
    """Raised for internal lifecycle failures that map to ok=False."""


def _resolve_mtplx_model(model: str | None) -> str:
    """The MTPLX repo id to serve: the explicit `model`, else the single
    mtplx model in the registry."""
    if model:
        return model
    registry = load_registry()
    for m in registry.models:
        if m.provider_id == "mtplx":
            return m.model_name
    raise LifecycleError("no mtplx model in the registry")


def _stop_others() -> None:
    """Stop every local provider except the one about to start, by
    delegating to the bash helper's stop-all mode (transition period)."""
    helper = shutil.which("llm-isolate-provider")
    if helper is None:
        raise LifecycleError("isolation helper 'llm-isolate-provider' not found on PATH")
    result = subprocess.run([helper, "stop-all"], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise LifecycleError(
            f"failed to stop other providers: {result.stderr.strip() or result.stdout.strip()}"
        )


def _wait_for_port_closed(url: str, timeout: float = 10.0) -> None:
    """Poll a localhost URL until it stops responding, or raise on timeout."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            urllib.request.urlopen(url, timeout=1.0)  # noqa: S310 — localhost probe
        except OSError:
            return
        time.sleep(0.2)
    raise LifecycleError(f"port still answering at {url} after {timeout}s")


def _start_mtplx_serve(model: str) -> None:
    """Start `mtplx serve` in the background, tracked by a pidfile.

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
        log_tail = ""
        try:
            with open(MTPLX_LOG, "rb") as f:
                f.seek(0, 2)
                size = f.tell()
                if isinstance(size, int):
                    f.seek(max(0, size - 1024))
                    log_tail = f.read().decode(errors="replace")[-512:]
        except OSError:
            pass
        raise LifecycleError(
            f"mtplx serve exited immediately (exit {proc.poll()})"
            + (f"; log tail: {log_tail}" if log_tail else "")
        )


def _http_models_ids(url: str, timeout: float = 2.0) -> list[str]:
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


def _wait_for_model(model: str, timeout: float = 300.0) -> None:
    """Poll /v1/models until it lists `model` (or the deadline passes)."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        ids = _http_models_ids(f"{MTPLX_BASE}/v1/models")
        if any(served == model or served.endswith("/" + model) for served in ids):
            return
        time.sleep(1.0)
    raise LifecycleError(f"timed out waiting for mtplx to serve {model}")


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
    if shutil.which("mtplx") is None:
        return LifecycleResult("mtplx", "", MTPLX_DIRECT_URL, False, "mtplx binary not found on PATH")
    result = subprocess.run(
        ["mtplx", "stop", "--port", str(MTPLX_PORT), "--grace-seconds", "10"],
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
    try:
        resolved = _resolve_mtplx_model(model)
        _stop_others()
        _start_mtplx_serve(resolved)
        _wait_for_model(resolved)
        _warmup(resolved)
    except LifecycleError as exc:
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
    """Stop every local provider."""
    mtplx_result = stop("mtplx")
    others = _delegate_stop_all()
    ok = mtplx_result.ok and others.ok
    error = None if ok else (mtplx_result.error or others.error)
    return LifecycleResult("stop-all", "", "", ok, error)


def _main(argv: list[str]) -> int:
    if not argv:
        print("usage: lifecycle <isolate|stop|stop-all> [provider] [model] ...", file=sys.stderr)
        return 1
    cmd = argv[0]
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
    print(json.dumps(asdict(result)))
    return 0 if result.ok else 1


if __name__ == "__main__":
    sys.exit(_main(sys.argv[1:]))
