"""The latest-run pointers behind `--latest`.

One flat TOML file beside the results it points at:
`~/.config/local-ai/benchmarks/latest.toml` (override: LLMBENCH_LATEST), with
the keys `last_run`, `last_run_dir`, `agent_last_run` and `eval_last_run`.

`StateStore` keeps the pointers under `extra["benchmarks"]`, which is how the
three CLIs read and write them. latest.toml is the only file read: no pointer
comes from anywhere else.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._toml_io import atomic_write_toml

_POINTER_KEYS = ("last_run", "last_run_dir", "agent_last_run", "eval_last_run")


@dataclass
class StateStore:
    extra: dict[str, Any] = field(default_factory=dict)


def latest_path() -> Path:
    override = os.environ.get("LLMBENCH_LATEST")
    if override:
        return Path(override).expanduser()
    return Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"


def _read_toml(path: Path) -> dict[str, Any]:
    """The file's table, or {} when it is absent or cannot be read. A pointer
    file is disposable: a broken one must not fail the run that replaces it."""
    try:
        with open(path, "rb") as f:
            return tomllib.load(f)
    except (OSError, ValueError):  # ValueError: TOMLDecodeError and UnicodeDecodeError
        return {}


def _pointers(table: dict[str, Any]) -> dict[str, str]:
    """The pointer keys of `table` that hold a string. Anything else in a
    file a person can edit by hand (another key, `last_run_dir = 7`) is not
    a pointer, and the CLIs pass these values straight to Path()."""
    return {k: table[k] for k in _POINTER_KEYS if isinstance(table.get(k), str)}


def load_state(path: Path | None = None) -> StateStore:
    """The recorded pointers; an empty store when none are recorded."""
    pointers = _pointers(_read_toml(Path(path) if path else latest_path()))
    return StateStore(extra={"benchmarks": pointers} if pointers else {})


def save_state(store: StateStore, path: Path | None = None) -> None:
    atomic_write_toml(store.extra.get("benchmarks", {}), Path(path) if path else latest_path())
