"""The latest-run pointers behind `--latest`.

One flat TOML file beside the results it points at:
`~/.config/local-ai/benchmarks/latest.toml` (override: LLMBENCH_LATEST), with
the keys `last_run`, `last_run_dir`, `agent_last_run` and `eval_last_run`.

`StateStore` keeps the pointers under `extra["benchmarks"]`, the shape they
had as modelman.toml's `[benchmarks]` table, so the three CLIs read and write
them as they always did.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._toml_io import atomic_write_toml


@dataclass
class StateStore:
    extra: dict[str, Any] = field(default_factory=dict)


def latest_path() -> Path:
    override = os.environ.get("LLMBENCH_LATEST")
    if override:
        return Path(override).expanduser()
    return Path.home() / ".config" / "local-ai" / "benchmarks" / "latest.toml"


def load_state(path: Path | None = None) -> StateStore:
    """The recorded pointers; an empty store when none are recorded."""
    latest = Path(path) if path else latest_path()
    if not latest.exists():
        return StateStore()
    with open(latest, "rb") as f:
        return StateStore(extra={"benchmarks": tomllib.load(f)})


def save_state(store: StateStore, path: Path | None = None) -> None:
    atomic_write_toml(store.extra.get("benchmarks", {}), Path(path) if path else latest_path())
