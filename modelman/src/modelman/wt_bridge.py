"""Subprocess bridge to `wt litellm ...` (and the `wt served` probe).

wt owns LiteLLM management (config.yaml routes, proxy restart, routing
state); modelman calls these primitives instead of keeping its own copy of
the logic. Its one route write is `sync` (#179: what is configured is
routed); the rest read wt's state (`list`, `status`) or toggle routing
(`on`/`off`/`set`). See
docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path


class WtBridgeError(Exception):
    """wt could not process the request (nothing was changed)."""


class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


class WtBridgeTimeoutError(WtBridgeError):
    """wt timed out; a write it was doing (`sync`) may have already applied
    to config.yaml before the kill."""


class WtRegistryRedirectedError(WtBridgeError):
    """wt refused to write routes: the registry is redirected but nothing
    names config.yaml. Final until WT_LITELLM_CONFIG is set — re-running the
    command is refused the same way."""


# The text of wt's ErrRegistryRedirected (wt/internal/litellm/configfile.go),
# which leads its refusal message.
_REGISTRY_REDIRECTED = "LiteLLM routes not touched"


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
        raise WtBridgeTimeoutError(f"wt litellm {sub} timed out after {timeout:g}s") from None
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


def _change(args: list[str], litellm_path: Path | None) -> BridgeResult:
    proc = _run(args, _env(litellm_path))
    try:
        json.loads(proc.stdout)
    except (ValueError, TypeError):
        # No JSON on stdout: a file-level failure (missing/invalid config.yaml,
        # unreadable registry). Nothing was applied.
        msg = _msg(proc, f"wt exited {proc.returncode}")
        if msg.startswith(_REGISTRY_REDIRECTED):
            raise WtRegistryRedirectedError(msg) from None
        raise WtBridgeError(msg) from None
    try:
        # Parse regardless of exit code: a partial batch exits 1 with JSON.
        return parse_change_result(proc.stdout)
    except (ValueError, KeyError, TypeError, AttributeError):
        raise WtBridgeError(
            f"unparseable wt output (exit {proc.returncode}); changes may have been applied"
        ) from None


def sync(*, litellm_path: Path | None = None) -> BridgeResult:
    """`wt litellm sync --json`: reconcile config.yaml with the registry's
    cloud models and the running local models (#179)."""
    return _change(["sync", "--json"], litellm_path)


def routed_ids(*, litellm_path: Path | None = None, timeout: float | None = None) -> list[str]:
    """`wt litellm list --json`: the model ids routed in config.yaml.

    No modelman command calls this since #179 (routes are synced, not read
    back); it stays as the bridge for wt's `list` verb, whose JSON shape the
    contract fixture still pins. `timeout` (seconds) overrides _run's 120s
    default for a caller that must not block on a hung wt."""
    proc = _run(
        ["list", "--json"],
        _env(litellm_path),
        timeout=_DEFAULT_TIMEOUT if timeout is None else timeout,
    )
    try:
        return parse_routed(proc.stdout)
    except (ValueError, KeyError, TypeError):
        raise WtBridgeError(_msg(proc, "wt litellm list failed")) from None


# Short bound for the TUI's status read (runs on the UI thread at mount).
STATUS_TIMEOUT = 5.0


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


# Above wt's own bound on an exec: secret_ref helper (15s) plus its probes, so
# a slow helper is wt's failure to report, not a kill from here.
SERVED_TIMEOUT = 20.0


def served_ids(provider: str, timeout: float = SERVED_TIMEOUT) -> list[str] | None:
    """The model ids `wt served <provider>` reports the provider's server is
    serving now, or None when wt gives no answer — not installed, timed out,
    or the server would not say (wt exits 1 rather than print an empty list).

    wt is asked because it resolves the registry's secret_ref, which a keyed
    omlx wants before it says which model of a partly loaded pool is loaded.
    A read, so unlike the `wt litellm` calls nothing is raised: every failure
    is "unknown", which no caller may read as "nothing is serving".
    """
    if shutil.which("wt") is None:
        return None
    try:
        proc = subprocess.run(
            ["wt", "served", provider, "--json"],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
        )
    except (subprocess.TimeoutExpired, OSError):
        return None
    if proc.returncode != 0:
        return None
    try:
        doc = json.loads(proc.stdout)
    except ValueError:
        return None
    served = doc.get("served") if isinstance(doc, dict) else None
    if not isinstance(served, list):
        return None
    return [s for s in served if isinstance(s, str)]
