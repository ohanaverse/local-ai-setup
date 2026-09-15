"""HTTP polling primitives shared by provider lifecycle backends.

Port/model/warmup polling loops generalized from `bin/lib/*.sh` and
today's mtplx-only helpers in `lifecycle/__init__.py`. Nothing here is
provider-specific: callers pass in the URLs to poll.
"""

from __future__ import annotations

import json
import re
import subprocess
import time
import urllib.error
import urllib.request

from ...local_process import http_models_ids
from .envelope import LifecycleError

# bash: wait_for_port_closed url 5 = 5 x (1s curl + 0.2s sleep)
STOP_WAIT_TIMEOUT = 6.0
# bash: poll_until_down ... 0.5 120 (mlx_lm_server pre-bind wait)
PREBIND_WAIT_TIMEOUT = 60.0
# bash: ollama's 90 x (2s health + <=120s chat + 1s sleep), as one deadline
WARMUP_TIMEOUT = 300.0
# today's _wait_for_model default, unchanged
MODEL_LOAD_TIMEOUT = 300.0
# bash: wait_for url name 30 = 30 x (2s + 1s)
RESTORE_WAIT_TIMEOUT = 90.0


def wait_for_port_closed(url: str, timeout: float = 6.0) -> None:
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


def port_closed_within(url: str, *, timeout: float) -> bool:
    """Same polling loop as `wait_for_port_closed`, but returns True/False
    instead of raising on timeout. Used by stop-and-wait helpers that want
    to warn rather than fail when a port doesn't close in time."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1.0) as resp:  # noqa: S310 — localhost probe
                resp.read()
        except urllib.error.HTTPError:
            pass  # server answered (with an error status) — still open
        except urllib.error.URLError as exc:
            if isinstance(exc.reason, TimeoutError):
                pass
            else:
                return True  # refused / reset / no route — port closed
        except TimeoutError:
            pass  # accepted the connection, then stalled mid-read — still open
        except ConnectionError:
            return True  # reset at read time — the listener is dying or gone
        time.sleep(0.2)
    return False


def wait_for_port_open(url: str, *, timeout: float = 90.0, interval: float = 1.0) -> bool:
    """Poll a localhost URL until it answers (any response, including an
    HTTP error status, counts as "up"), returning True once it does or
    False after `timeout` elapses. Mirrors bash's `poll_until_up`, which
    never raises — this matches that: non-raising, boolean return."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1.0) as resp:  # noqa: S310 — localhost probe
                resp.read()
            return True
        except urllib.error.HTTPError:
            return True  # server answered (with an error status) — up
        except OSError:
            pass  # not listening yet, or a transient connection failure
        time.sleep(interval)
    return False


def wait_for_model(
    models_url: str,
    model: str,
    *,
    proc: subprocess.Popen | None = None,
    timeout: float = 300.0,
) -> None:
    """Poll `models_url` until it lists `model`, the serve process dies
    (when `proc` is given), or the deadline passes."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if proc is not None and proc.poll() is not None:
            # The serve process died mid-load (OOM kill, missing weights
            # discovered late): without this check the HTTP poll below
            # runs the full timeout against a dead port while the real
            # crash cause stays buried in the log.
            raise LifecycleError(f"serve process exited during model load (exit {proc.returncode})")
        ids = http_models_ids(models_url)
        if any(served == model or served.endswith("/" + model) for served in ids):
            return
        time.sleep(1.0)
    raise LifecycleError(f"timed out waiting for {models_url} to serve {model}")


def serving_model(models_url: str, model: str) -> bool:
    """True when `models_url` already lists `model` (same lenient match as
    `wait_for_model`). `http_models_ids` returns [] on any error, so a down
    or wedged server reads as "not serving" — the safe fallback."""
    ids = http_models_ids(models_url)
    return any(served == model or served.endswith("/" + model) for served in ids)


def warmup(chat_url: str, model: str, *, health_url: str, timeout: float = 300.0) -> None:
    """Warm a model into GPU/RAM: poll `health_url` for liveness (matching
    bash's `curl -m 2` probe), then POST a 1-token chat completion to
    `chat_url` and check for the chat.completion marker in the body.

    Supports asymmetric health/chat URLs (e.g. ollama's health check hits
    `/api/tags` while its chat completions go to `/v1/chat/completions`) —
    today's mtplx-only `_warmup` conflated the two because mtplx's health
    check was baked in elsewhere.
    """
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
            urllib.request.urlopen(health_url, timeout=2.0)  # noqa: S310 — localhost probe
        except OSError:
            time.sleep(1.0)
            continue
        try:
            req = urllib.request.Request(
                chat_url,
                data=payload,
                headers={"Content-Type": "application/json"},
            )
            with urllib.request.urlopen(req, timeout=120) as resp:  # noqa: S310
                body = resp.read().decode()
            # MTPLX (and others) may serialize with spaces between keys and
            # values (e.g. `"object": "chat.completion"`); tolerate
            # arbitrary whitespace around the colon rather than requiring
            # compact JSON.
            if re.search(r'"object"\s*:\s*"chat\.completion"', body):
                return
        except OSError:
            pass
        time.sleep(1.0)
    raise LifecycleError(f"failed to warm up model {model} at {chat_url}")
