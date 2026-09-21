"""Subprocess bridge to `wt litellm ...`.

wt owns LiteLLM management (config.yaml routes, proxy restart, routing
state); modelman calls these primitives instead of keeping its own copy of
the logic. See docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path


class WtBridgeError(Exception):
    """wt could not process the request (nothing was changed)."""


class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


@dataclass(frozen=True)
class BridgeOutcome:
    id: str
    action: str | None
    error: str | None


@dataclass(frozen=True)
class BridgeResult:
    outcomes: list[BridgeOutcome]
    changed: bool
    warnings: list[str]


@dataclass(frozen=True)
class LitellmStatus:
    enabled: bool
    url: str
    api_key_set: bool


def ensure_wt() -> None:
    """Raise WtNotFoundError unless the wt binary is on PATH."""
    if shutil.which("wt") is None:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`")


_DEFAULT_TIMEOUT = 120.0


def _run(
    args: list[str], env: dict[str, str] | None = None, timeout: float = _DEFAULT_TIMEOUT
) -> subprocess.CompletedProcess[str]:
    ensure_wt()
    # Error messages deliberately name only the subcommand: argv may carry
    # `--api-key <secret>` and must never reach a traceback or the TUI.
    sub = args[0] if args else ""
    try:
        return subprocess.run(
            ["wt", "litellm", *args],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
            env={**os.environ, **(env or {})},
        )
    except subprocess.TimeoutExpired:
        raise WtBridgeError(f"wt litellm {sub} timed out after {timeout:g}s") from None
    except FileNotFoundError:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`") from None
    except OSError as e:
        raise WtBridgeError(f"wt litellm {sub} could not run: {type(e).__name__}") from None


def _env(litellm_path: Path | None) -> dict[str, str]:
    return {"WT_LITELLM_CONFIG": str(litellm_path)} if litellm_path is not None else {}


def _msg(proc: subprocess.CompletedProcess[str], fallback: str) -> str:
    """One clean line for the user from wt's output.

    wt (cobra) prints both `Error: <msg>` and its own `wt: <msg>`; drop blank
    lines, strip those prefixes, de-duplicate and join with "; ". Only wt's
    own output is used (never argv), so no api key can leak through here.
    """
    raw = (proc.stderr or "").strip() or (proc.stdout or "").strip()
    lines: list[str] = []
    for line in raw.splitlines():
        line = line.strip()
        for prefix in ("Error: ", "wt: "):
            if line.startswith(prefix):
                line = line[len(prefix) :].strip()
        if line and line not in lines:
            lines.append(line)
    return "; ".join(lines) or fallback


def parse_change_result(stdout: str) -> BridgeResult:
    doc = json.loads(stdout)
    return BridgeResult(
        outcomes=[
            BridgeOutcome(o["id"], o.get("action") or None, o.get("error") or None)
            for o in doc.get("outcomes", [])
        ],
        changed=bool(doc.get("changed")),
        warnings=list(doc.get("warnings") or []),
    )


def parse_status(stdout: str) -> LitellmStatus:
    doc = json.loads(stdout)
    return LitellmStatus(bool(doc["enabled"]), doc.get("url") or "", bool(doc["api_key_set"]))


def parse_routed(stdout: str) -> list[str]:
    return [str(x) for x in json.loads(stdout)["routed"]]


def parse_providers(stdout: str) -> dict[str, bool]:
    return {pid: bool(v["cloud"]) for pid, v in json.loads(stdout)["providers"].items()}


def _change(args: list[str], litellm_path: Path | None) -> BridgeResult:
    proc = _run(args, _env(litellm_path))
    try:
        json.loads(proc.stdout)
    except (ValueError, TypeError):
        # No JSON on stdout: a file-level failure (missing/invalid config.yaml,
        # unreadable registry). Nothing was applied.
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}")) from None
    try:
        # Parse regardless of exit code: a partial batch exits 1 with JSON.
        return parse_change_result(proc.stdout)
    except (ValueError, KeyError, TypeError, AttributeError):
        raise WtBridgeError(
            f"unparseable wt output (exit {proc.returncode}); changes may have been applied"
        ) from None


def expose(
    ids: list[str], *, litellm_path: Path | None = None, skip_ready_gate: bool = True
) -> BridgeResult:
    args = ["expose", "--json"] + (["--skip-ready-gate"] if skip_ready_gate else []) + ["--", *ids]
    return _change(args, litellm_path)


def unexpose(ids: list[str], *, litellm_path: Path | None = None) -> BridgeResult:
    return _change(["unexpose", "--json", "--", *ids], litellm_path)


def routed_ids(*, litellm_path: Path | None = None) -> list[str]:
    proc = _run(["list", "--json"], _env(litellm_path))
    try:
        return parse_routed(proc.stdout)
    except (ValueError, KeyError, TypeError):
        raise WtBridgeError(_msg(proc, "wt litellm list failed")) from None


_PROVIDER_FAILURE_TTL = 30.0
_PROVIDER_TIMEOUT = 5.0
# Short bound for the TUI's status read (runs on the UI thread at mount).
STATUS_TIMEOUT = 5.0
_provider_cache: dict[str, bool] | None = None
_provider_failed_at: float | None = None
_provider_warned = False


def _reset_provider_cache() -> None:
    """Forget the cached provider table, failure window and one-shot warning (tests)."""
    global _provider_cache, _provider_failed_at, _provider_warned
    _provider_cache = None
    _provider_failed_at = None
    _provider_warned = False


def provider_cloud_flags() -> dict[str, bool]:
    """provider id -> is cloud, for every LiteLLM-mapped provider (wt owns the table).

    Called from TUI render paths, so it never raises: if wt is missing or
    fails, warn once per process on stderr and return {}. A failure is
    remembered for _PROVIDER_FAILURE_TTL seconds (no re-spawn per model row),
    then retried.
    """
    global _provider_cache, _provider_failed_at, _provider_warned
    if _provider_cache is not None:
        return _provider_cache
    if (
        _provider_failed_at is not None
        and time.monotonic() - _provider_failed_at < _PROVIDER_FAILURE_TTL
    ):
        return {}
    try:
        proc = _run(["providers", "--json"], timeout=_PROVIDER_TIMEOUT)
        try:
            flags = parse_providers(proc.stdout)
        except (ValueError, KeyError, TypeError, AttributeError):
            raise WtBridgeError(_msg(proc, "wt litellm providers failed")) from None
    except WtBridgeError as e:
        _provider_failed_at = time.monotonic()
        if not _provider_warned:
            _provider_warned = True
            print(
                f"modelman: cannot read wt's LiteLLM provider table ({e}); "
                "cloud/mapping checks are degraded",
                file=sys.stderr,
            )
        return {}
    _provider_failed_at = None
    _provider_cache = flags
    return flags


def litellm_status(timeout: float | None = None) -> LitellmStatus:
    """wt's routing state. `timeout` (seconds) overrides _run's 120 s default;
    the TUI passes a short one so a hung wt cannot freeze it."""
    proc = _run(["status", "--json"], timeout=_DEFAULT_TIMEOUT if timeout is None else timeout)
    try:
        return parse_status(proc.stdout)
    except (ValueError, KeyError, TypeError):
        raise WtBridgeError(_msg(proc, "wt litellm status failed")) from None


def litellm_status_text(timeout: float | None = None) -> str:
    """wt's human-readable status (key masking lives in wt)."""
    proc = _run(["status"], timeout=_DEFAULT_TIMEOUT if timeout is None else timeout)
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, "wt litellm status failed"))
    return proc.stdout


def litellm_set_enabled(on: bool) -> None:
    proc = _run(["on" if on else "off"])
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))


def litellm_set(url: str | None, api_key: str | None) -> None:
    args = (
        ["set"]
        + (["--url", url] if url is not None else [])
        + (["--api-key", api_key] if api_key is not None else [])
    )
    proc = _run(args)
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))
