"""Resolve a registry `secret_ref` the way wt does.

Forms: `exec:<command args...>` (stdout, stripped), a bare ENV_NAME or
`os.environ/NAME` (the variable, "" when unset), anything else is the literal.
Errors name the reference, never any output or value: the secret is a bearer key.
"""

from __future__ import annotations

import os
import re
import subprocess

from .lifecycle.envelope import LifecycleError

EXEC_TIMEOUT_S: float = 15.0

_ENV_NAME = re.compile(r"^[A-Z][A-Z0-9_]*$")


class SecretError(LifecycleError):
    """A secret reference could not be resolved."""


def resolve_secret(ref: str) -> str:
    if ref.startswith("exec:"):
        return _run(ref)
    if ref.startswith("os.environ/"):
        return os.environ.get(ref.removeprefix("os.environ/"), "")
    if _ENV_NAME.match(ref):
        return os.environ.get(ref, "")
    return ref


def _run(ref: str) -> str:
    argv = ref.removeprefix("exec:").split()
    if not argv:
        raise SecretError(f"secret reference {ref!r} has an empty command")
    try:
        result = subprocess.run(
            argv,
            capture_output=True,
            text=True,
            timeout=EXEC_TIMEOUT_S,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise SecretError(f"secret command {ref!r} timed out") from exc
    except OSError as exc:
        raise SecretError(f"secret command {ref!r} could not run ({type(exc).__name__})") from exc
    if result.returncode != 0:
        raise SecretError(f"secret command {ref!r} failed (exit {result.returncode})")
    value = result.stdout.strip()
    if not value:
        raise SecretError(f"secret command {ref!r} produced no output")
    return value
