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


def provider_key(provider_id: str) -> str | None:
    """The bearer key the registry's `provider_id` row names in
    `auth.secret_ref`, resolved now; None when the registry or the row is
    absent, the row names no reference, or it resolves to "". A reference
    that cannot be resolved raises SecretError naming the provider and the
    reference, never a value. The benchmarks' direct routes send it, as the
    lifecycle probes do (rec refuses every route without it)."""
    from ..registry import RegistryError, load_registry

    try:
        row = load_registry().provider(provider_id)
    except (RegistryError, KeyError):
        return None
    if not row.secret_ref:
        return None
    try:
        key = resolve_secret(row.secret_ref)
    except SecretError as exc:
        raise SecretError(f"{provider_id}: cannot read the provider key: {exc}") from None
    return key or None


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
