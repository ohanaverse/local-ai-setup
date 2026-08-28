"""Shared atomic TOML write + None-pruning helpers.

Both registry.py (registry.toml) and state.py (modelman.toml) write TOML
files that a single interactive process can still be interrupted mid-write
(crash, Ctrl-C) — never a concurrent-writer problem, since modelman is the
sole writer of both files. Atomic write (temp file + rename) is enough;
no locking needed.
"""

from __future__ import annotations

import contextlib
import os
import tempfile
from pathlib import Path
from typing import Any

import tomli_w


def drop_none(value: Any) -> Any:
    """Recursively strip None values/keys — TOML has no null type."""
    if isinstance(value, dict):
        return {k: drop_none(v) for k, v in value.items() if v is not None}
    if isinstance(value, list):
        return [drop_none(v) for v in value]
    return value


def unknown_keys(raw: dict[str, Any], known: set[str]) -> dict[str, Any]:
    """Return the subset of `raw` whose keys are not in `known`.

    Used to capture hand-edited fields that aren't part of the typed schema
    so they survive a load/save round-trip instead of being silently dropped.
    """
    return {k: v for k, v in raw.items() if k not in known}


def atomic_write_toml(payload: dict[str, Any], path: Path) -> None:
    """Write `payload` to `path` as TOML via temp file + rename.

    On any failure the temp file is removed and `path` is left untouched.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb") as f:
            tomli_w.dump(payload, f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise
