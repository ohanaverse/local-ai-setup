"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman only. See registry.py for the canonical, shared model/
provider definitions this state is keyed against, and
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md for the
ownership split.

The `families` table is a modelman-only addition on top of that spec: it
holds a family's display name and marks a family "known" before it has any
models (mirroring the legacy per-family manifest file's existence). It is
NOT part of the shared registry.toml schema and is never read by
agent-worktree.
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
class FamilyState:
    display_name: str | None = None


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)
    families: dict[str, FamilyState] = field(default_factory=dict)

    def get(self, model_id: str) -> ModelState:
        return self.models.get(model_id, ModelState())

    def set(self, model_id: str, state: ModelState) -> None:
        self.models[model_id] = state

    def family_display_name(self, family: str) -> str:
        entry = self.families.get(family)
        if entry is not None and entry.display_name:
            return entry.display_name
        return family

    def touch_family(self, family: str, display_name: str | None = None) -> None:
        """Mark `family` as known, optionally setting its display name.

        Used both for "Add Family" (before any model exists) and to record
        a display name for a family that already has models. `display_name`
        of `None` preserves whatever name was set previously; an empty
        string clears it.
        """
        existing = self.families.get(family, FamilyState())
        self.families[family] = FamilyState(
            display_name=existing.display_name if display_name is None else display_name
        )

    def forget_family(self, family: str) -> None:
        self.families.pop(family, None)


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
    families = {
        family: FamilyState(display_name=entry.get("display_name"))
        for family, entry in raw.get("families", {}).items()
    }
    return StateStore(models=models, families=families)


def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {model_id: drop_none(asdict(s)) for model_id, s in store.models.items()},
        "families": {family: drop_none(asdict(s)) for family, s in store.families.items()},
    }
    atomic_write_toml(payload, state_path)
