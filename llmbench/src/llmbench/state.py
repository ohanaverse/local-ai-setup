"""The latest-run pointers behind `--latest`.

One flat TOML file beside the results it points at:
`~/.config/local-ai/benchmarks/latest.toml` (override: LLMBENCH_LATEST), with
the keys `last_run`, `last_run_dir`, `agent_last_run` and `eval_last_run`.

`StateStore` keeps the pointers under `extra["benchmarks"]`, the shape they
had as modelman.toml's `[benchmarks]` table, so the three CLIs read and write
them as they always did. Until latest.toml exists, that table is where the
pointers are read from: the first recorded run carries them over.
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


def _modelman_state_path() -> Path:
    """Where modelman kept modelman.toml: MODELMAN_STATE > XDG_CONFIG_HOME >
    ~/.config (modelman/state.py's _default_state_path)."""
    override = os.environ.get("MODELMAN_STATE")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "modelman.toml").expanduser()


def _read_toml(path: Path) -> dict[str, Any]:
    """The file's table, or {} when it is absent or cannot be read. A pointer
    file is disposable: a broken one must not fail the run that replaces it."""
    try:
        with open(path, "rb") as f:
            return tomllib.load(f)
    except (OSError, ValueError):  # ValueError: TOMLDecodeError and UnicodeDecodeError
        return {}


def _modelman_pointers() -> dict[str, str]:
    """The pointers modelman.toml's `[benchmarks]` table still holds."""
    table = _read_toml(_modelman_state_path()).get("benchmarks")
    if not isinstance(table, dict):
        return {}
    return {k: table[k] for k in _POINTER_KEYS if isinstance(table.get(k), str)}


def load_state(path: Path | None = None) -> StateStore:
    """The recorded pointers; an empty store when none are recorded.

    While latest.toml does not exist, the pointers come from modelman.toml,
    which held them before llmbench was carved out. Read-only: the next
    save_state writes them to latest.toml, and modelman.toml is not consulted
    again."""
    latest = Path(path) if path else latest_path()
    if latest.exists():
        pointers = _read_toml(latest)
    elif path is None:
        pointers = _modelman_pointers()
    else:
        pointers = {}
    return StateStore(extra={"benchmarks": pointers} if pointers else {})


def save_state(store: StateStore, path: Path | None = None) -> None:
    atomic_write_toml(store.extra.get("benchmarks", {}), Path(path) if path else latest_path())
