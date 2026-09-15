"""Shared lifecycle result/error types.

Single source of truth for the two types every lifecycle module returns or
raises: `LifecycleResult` (the JSON envelope contract shared with the bash
shim and `modelman/benchmark/isolation.py`) and `LifecycleError` (the one
exception type lifecycle code raises for internal failures that map to
`ok=False`).
"""

from __future__ import annotations

from ...local_process import ProcessResult as LifecycleResult

__all__ = ["LifecycleError", "LifecycleResult"]


class LifecycleError(Exception):
    """Raised for internal lifecycle failures that map to ok=False."""
