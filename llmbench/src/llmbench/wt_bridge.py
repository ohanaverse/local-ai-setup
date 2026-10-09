"""Subprocess bridge to wt: `wt warm`, the one wt call llmbench makes (#256).

wt resolves the registry's secret_ref, which an omlx with an API key wants
before it loads a model.
"""

from __future__ import annotations

import shutil
import subprocess


class WtBridgeError(Exception):
    """wt could not process the request (nothing was changed)."""


class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


class WtBridgeTimeoutError(WtBridgeError):
    """wt timed out before it answered."""


def ensure_wt() -> None:
    """Raise WtNotFoundError unless the wt binary is on PATH."""
    if shutil.which("wt") is None:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`")


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


# Above wt's own warmup budget (lifecycle's warmupTimeout, 600s), so a model
# that never loads is wt's failure to report, not a kill from here.
WARM_TIMEOUT = 630.0


def warm(provider: str, model: str, timeout: float = WARM_TIMEOUT) -> None:
    """Have wt load `model` into `provider`'s running server (`wt warm`).

    wt is asked because it resolves the registry's secret_ref: an omlx with
    an API key refuses the keyless warmup llmbench sends itself (#256).
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
