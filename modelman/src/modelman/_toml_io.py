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
from collections.abc import Callable
from pathlib import Path
from typing import IO, Any

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


def atomic_write(
    path: Path,
    write_fn: Callable[[IO[Any]], None],
    *,
    binary: bool = False,
    preserve_mode: bool = False,
) -> None:
    """Write to `path` atomically via temp file + rename.

    `write_fn` receives the open temp file handle and writes the desired
    content to it (the caller picks the serialization — TOML, YAML, ...).
    On any failure the temp file is removed and `path` is left untouched.

    `preserve_mode`, when True, carries `path`'s existing permission bits
    onto the replacement file: `tempfile.mkstemp` always creates 0600, and
    `os.replace` preserves that mode, so without this a config readable by
    another user/service would silently tighten to 0600 on first write.
    Off by default — TOML callers (registry.toml, modelman.toml) are
    modelman's own private config with no such cross-process readers.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    existing_mode: int | None = None
    if preserve_mode:
        with contextlib.suppress(OSError):
            existing_mode = path.stat().st_mode & 0o777
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        if existing_mode is not None:
            os.fchmod(fd, existing_mode)
        with os.fdopen(fd, "wb" if binary else "w") as f:
            write_fn(f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise


def atomic_write_toml(payload: dict[str, Any], path: Path) -> None:
    """Write `payload` to `path` as TOML via temp file + rename.

    On any failure the temp file is removed and `path` is left untouched.
    """
    atomic_write(path, lambda f: tomli_w.dump(payload, f), binary=True)
