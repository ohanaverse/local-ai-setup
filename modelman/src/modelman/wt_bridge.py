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


def _run(args: list[str], env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    ensure_wt()
    return subprocess.run(
        ["wt", "litellm", *args],
        capture_output=True,
        text=True,
        timeout=120,
        env={**os.environ, **(env or {})},
    )


def _env(litellm_path: Path | None) -> dict[str, str]:
    return {"WT_LITELLM_CONFIG": str(litellm_path)} if litellm_path is not None else {}


def _msg(proc: subprocess.CompletedProcess[str], fallback: str) -> str:
    return (proc.stderr or proc.stdout).strip() or fallback


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


def _change(args: list[str], litellm_path: Path | None) -> BridgeResult:
    proc = _run(args, _env(litellm_path))
    try:
        # Parse regardless of exit code: a partial batch exits 1 with JSON.
        return parse_change_result(proc.stdout)
    except (ValueError, KeyError, TypeError, AttributeError):
        # No JSON on stdout: a file-level failure (missing/invalid config.yaml,
        # unreadable registry). Nothing was applied.
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}")) from None


def expose(
    ids: list[str], *, litellm_path: Path | None = None, skip_ready_gate: bool = True
) -> BridgeResult:
    args = ["expose", "--json"] + (["--skip-ready-gate"] if skip_ready_gate else []) + ids
    return _change(args, litellm_path)


def unexpose(ids: list[str], *, litellm_path: Path | None = None) -> BridgeResult:
    return _change(["unexpose", "--json", *ids], litellm_path)


def routed_ids(*, litellm_path: Path | None = None) -> list[str]:
    proc = _run(["list", "--json"], _env(litellm_path))
    try:
        return list(json.loads(proc.stdout)["routed"])
    except (ValueError, KeyError, TypeError):
        raise WtBridgeError(_msg(proc, "wt litellm list failed")) from None


_provider_cache: dict[str, bool] | None = None
_provider_warned = False


def _reset_provider_cache() -> None:
    """Forget the cached provider table and the one-shot warning (tests)."""
    global _provider_cache, _provider_warned
    _provider_cache = None
    _provider_warned = False


def provider_cloud_flags() -> dict[str, bool]:
    """provider id -> is cloud, for every LiteLLM-mapped provider (wt owns the table).

    Called from TUI render paths, so it never raises: if wt is missing or
    fails, warn once per process on stderr and return {} (not cached, so a
    later call can recover).
    """
    global _provider_cache, _provider_warned
    if _provider_cache is not None:
        return _provider_cache
    try:
        proc = _run(["providers", "--json"])
        try:
            flags = {
                pid: bool(v["cloud"]) for pid, v in json.loads(proc.stdout)["providers"].items()
            }
        except (ValueError, KeyError, TypeError, AttributeError):
            raise WtBridgeError(_msg(proc, "wt litellm providers failed")) from None
    except WtBridgeError as e:
        if not _provider_warned:
            _provider_warned = True
            print(
                f"modelman: cannot read wt's LiteLLM provider table ({e}); "
                "cloud/mapping checks are degraded",
                file=sys.stderr,
            )
        return {}
    _provider_cache = flags
    return flags


def litellm_status() -> LitellmStatus:
    proc = _run(["status", "--json"])
    try:
        return parse_status(proc.stdout)
    except (ValueError, KeyError, TypeError):
        raise WtBridgeError(_msg(proc, "wt litellm status failed")) from None


def litellm_status_text() -> str:
    """wt's human-readable status (key masking lives in wt)."""
    proc = _run(["status"])
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
