"""Atomic TOML write (temp file + rename).

llmbench writes a run's `run.toml` and the latest-run pointers. A crash or
Ctrl-C mid-write must leave the previous file intact, never a truncated one.
"""

from __future__ import annotations

import contextlib
import os
import tempfile
from collections.abc import Callable
from pathlib import Path
from typing import IO, Any

import tomli_w


def atomic_write(path: Path, write_fn: Callable[[IO[Any]], None], *, binary: bool = False) -> None:
    """Write to `path` atomically via temp file + rename.

    `write_fn` receives the open temp file handle and writes the content. On
    any failure the temp file is removed and `path` is left untouched.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb" if binary else "w") as f:
            write_fn(f)
        os.replace(tmp_name, path)
    except BaseException:
        with contextlib.suppress(OSError):
            os.unlink(tmp_name)
        raise


def atomic_write_toml(payload: dict[str, Any], path: Path) -> None:
    """Write `payload` to `path` as TOML via temp file + rename."""
    atomic_write(path, lambda f: tomli_w.dump(payload, f), binary=True)
