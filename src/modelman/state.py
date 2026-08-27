"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman only. See registry.py for the canonical, shared model/
provider definitions this state is keyed against, and
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md for the
ownership split.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import asdict, dataclass, field
from pathlib import Path

from ._toml_io import atomic_write_toml, drop_none


def _default_state_path() -> Path:
    """Compute the state path lazily so env overrides work in tests."""
    return Path(os.environ.get("MODELMAN_STATE", "~/.config/local-ai/modelman.toml")).expanduser()


@dataclass
class ModelState:
    downloaded: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    litellm_exposed: bool = False


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)

    def get(self, model_id: str) -> ModelState:
        return self.models.get(model_id, ModelState())

    def set(self, model_id: str, state: ModelState) -> None:
        self.models[model_id] = state


def load_state(path: Path | None = None) -> StateStore:
    """Load modelman.toml. Missing file returns an empty store — this file
    is optional, unlike registry.toml, since a fresh install has no
    per-machine download state yet."""
    state_path = Path(path) if path else _default_state_path()
    if not state_path.exists():
        return StateStore()
    with open(state_path, "rb") as f:
        raw = tomllib.load(f)
    models = {
        model_id: ModelState(
            downloaded=entry.get("downloaded", False),
            disk_path=entry.get("disk_path"),
            size_bytes=entry.get("size_bytes"),
            litellm_exposed=entry.get("litellm_exposed", False),
        )
        for model_id, entry in raw.get("model_state", {}).items()
    }
    return StateStore(models=models)


def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {model_id: drop_none(asdict(s)) for model_id, s in store.models.items()}
    }
    atomic_write_toml(payload, state_path)
