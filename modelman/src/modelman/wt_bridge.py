"""Subprocess bridge to wt: `wt litellm ...`, `wt served`, `wt warm`, and `wt start` / `wt stop` for the omlx pool (#213).

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

# One exception hierarchy for both packages: llmbench's lifecycle raises these
# from its own `wt warm` call, and modelman's callers catch them by these
# names. WtBridgeTimeoutError on a `sync` means the write may have already
# applied to config.yaml before the kill.
from llmbench.wt_bridge import WtBridgeError as WtBridgeError
from llmbench.wt_bridge import WtBridgeTimeoutError as WtBridgeTimeoutError
from llmbench.wt_bridge import WtNotFoundError as WtNotFoundError


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


# Above wt's own warmup budget (lifecycle's warmupTimeout, 600s), so a model
# that never loads is wt's failure to report, not a kill from here.
WARM_TIMEOUT = 630.0


def warm(provider: str, model: str, timeout: float = WARM_TIMEOUT) -> None:
    """Have wt load `model` into `provider`'s running server (`wt warm`).

    wt is asked because it resolves the registry's secret_ref: an omlx with
    an API key refuses the keyless warmup modelman sends itself (#256).
    Raises WtBridgeError with wt's own message when the model did not load.
    """
    ensure_wt()
    try:
        proc = subprocess.run(
            ["wt", "warm", provider, model],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise WtBridgeTimeoutError(f"wt warm timed out after {timeout:g}s") from None
    except FileNotFoundError:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`") from None
    except OSError as e:
        raise WtBridgeError(f"wt warm could not run: {type(e).__name__}") from None
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))


# Above wt's whole start budget, so a model that never loads is wt's failure
# to report, not a kill from here: bringing a stopped omlx up (its
# portUpTimeout, 90s), the load (warmupTimeout, 600s), then the route write
# and proxy wait (10s + 30s).
START_TIMEOUT = 780.0
# A dry run: one inventory probe round.
PLAN_TIMEOUT = 30.0
# Above wt's unload budget (its loadTimeout, 300s) plus the route write and
# proxy wait that follow it (10s + 30s).
STOP_TIMEOUT = 360.0

_PLAN_STATUSES = frozenset({"running", "fits", "would_unload", "unknown"})
_OUTCOME_STATUSES = frozenset({"started", "already_running"})


@dataclass(frozen=True)
class PlanUnload:
    id: str
    sessions: int


@dataclass(frozen=True)
class StartPlan:
    """`wt start <id> --plan --json`: what a start would do, nothing changed."""

    id: str
    status: str  # running | fits | would_unload | unknown
    would_unload: list[PlanUnload]


@dataclass(frozen=True)
class StartOutcome:
    """`wt start <id> --json` after a start."""

    id: str
    status: str  # started | already_running
    unloaded: list[str]


def _json_object(stdout: str) -> dict:
    try:
        doc = json.loads(stdout)
    except ValueError as exc:
        raise ValueError("not JSON") from exc
    if not isinstance(doc, dict):
        raise ValueError("not a JSON object")
    return doc


def parse_start_plan(stdout: str) -> StartPlan:
    """Parse a plan, raising ValueError for any shape this code does not know
    (pinned by docs/contracts/wt-start-cli.sample.json)."""
    doc = _json_object(stdout)
    status, model_id, rows = doc.get("status"), doc.get("id"), doc.get("would_unload")
    # isinstance first: a non-string status (a list, an object) is unhashable,
    # and the set lookup would raise TypeError past every ValueError handler.
    if (
        not isinstance(status, str)
        or status not in _PLAN_STATUSES
        or not isinstance(model_id, str)
        or not isinstance(rows, list)
    ):
        raise ValueError("not a wt start plan")
    unload: list[PlanUnload] = []
    for row in rows:
        if not isinstance(row, dict) or not isinstance(row.get("id"), str):
            raise ValueError("not a wt start plan")
        sessions = row.get("sessions", 0)
        unload.append(
            PlanUnload(id=row["id"], sessions=sessions if isinstance(sessions, int) else 0)
        )
    return StartPlan(id=model_id, status=status, would_unload=unload)


def parse_start_outcome(stdout: str) -> StartOutcome:
    """Parse a start result, raising ValueError for any unknown shape."""
    doc = _json_object(stdout)
    status, model_id, unloaded = doc.get("status"), doc.get("id"), doc.get("unloaded")
    if (
        not isinstance(status, str)
        or status not in _OUTCOME_STATUSES
        or not isinstance(model_id, str)
        or not isinstance(unloaded, list)
        or not all(isinstance(u, str) for u in unloaded)
    ):
        raise ValueError("not a wt start result")
    return StartOutcome(id=model_id, status=status, unloaded=list(unloaded))


def _run_wt(argv: list[str], timeout: float) -> subprocess.CompletedProcess[str]:
    """Run `wt <argv>` and return the finished process. The one seam the
    start/stop calls share, so the suite can keep the real binary out
    (conftest stubs it). Only the subcommand is named in errors."""
    ensure_wt()
    sub = argv[0] if argv else ""
    try:
        return subprocess.run(
            ["wt", *argv],
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        raise WtBridgeTimeoutError(f"wt {sub} timed out after {timeout:g}s") from None
    except FileNotFoundError:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`") from None
    except OSError as e:
        raise WtBridgeError(f"wt {sub} could not run: {type(e).__name__}") from None


# cobra's refusal of a flag the installed wt does not have.
_UNKNOWN_FLAG = "unknown flag"


def _raise_if_wt_predates(proc: subprocess.CompletedProcess[str], sub: str) -> None:
    """Raise the reinstall hint when wt failed on a flag it does not know.

    An older wt does not print prose and exit 0: cobra exits non-zero with
    `unknown flag: --json` on stderr. Passing that on bare leaves the user
    with a flag error about a command they never typed."""
    if proc.returncode == 0:
        return
    # Only the line that names the flag: cobra may print its usage after it.
    for line in (proc.stderr or "").splitlines():
        if _UNKNOWN_FLAG in line:
            flag_error = line[line.index(_UNKNOWN_FLAG) :].strip()
            raise WtBridgeError(
                f"{flag_error}: this wt predates the `wt {sub}` flags modelman uses for "
                "omlx — reinstall it with `make install`"
            )


def start_plan(model_id: str, timeout: float = PLAN_TIMEOUT) -> StartPlan | None:
    """What `wt start <model_id>` would do, or None when wt gives no usable
    answer (not installed, timed out, an id wt does not know).

    wt is asked because the answer needs omlx's sizes and the registry's key,
    which only wt reads. A read, so a failure is not raised: None means
    "could not tell", which no caller may read as "nothing would be
    unloaded". The one exception is a wt too old to know `--plan`
    (_raise_if_wt_predates): the start would be refused the same way, so that
    raises WtBridgeError with the reinstall hint."""
    try:
        proc = _run_wt(["start", model_id, "--plan", "--json"], timeout)
    except WtBridgeError:
        return None
    _raise_if_wt_predates(proc, "start")
    try:
        return parse_start_plan(proc.stdout)
    except ValueError:
        return None


def start(model_id: str, *, replace: bool, timeout: float = START_TIMEOUT) -> StartOutcome:
    """Start `model_id` through wt (`wt start <id> --json`), which loads it
    into omlx's pool beside what is loaded and writes its LiteLLM route.

    replace=True lets wt go ahead when the load would unload other models;
    their ids come back in the outcome's `unloaded`. Raises WtBridgeError with
    wt's own message when the model did not start."""
    argv = ["start", model_id, "--json"]
    if replace:
        argv.append("--replace")
    proc = _run_wt(argv, timeout)
    _raise_if_wt_predates(proc, "start")
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))
    try:
        return parse_start_outcome(proc.stdout)
    except ValueError:
        raise WtBridgeError(
            "wt start gave no usable answer; this wt may predate `wt start --json` "
            "— reinstall it with `make install`"
        ) from None


def stop(target: str, timeout: float = STOP_TIMEOUT) -> None:
    """Stop through wt: a model id unloads that one model from omlx's pool,
    a bare provider (`omlx`) stops the service. `--yes` because the caller
    has already asked the user. Raises WtBridgeError with wt's message."""
    proc = _run_wt(["stop", target, "--yes"], timeout)
    _raise_if_wt_predates(proc, "stop")
    if proc.returncode != 0:
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}"))
