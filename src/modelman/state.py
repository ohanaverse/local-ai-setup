"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman only. See registry.py for the canonical, shared model/
provider/family definitions this state is keyed against, and
docs/superpowers/specs/2026-08-27-shared-model-registry-design.md for the
ownership split.

The `families` table is a legacy read-side fallback: family display names
now live in registry.toml's first-class [[families]] entries (see
docs/superpowers/specs/2026-08-29-modelman-first-class-families-design.md).
Writers no longer create entries here; they drain them via promotion.
Entries stay loadable so pre-existing modelman.toml files keep working.
"""

from __future__ import annotations

import os
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._toml_io import atomic_write_toml, drop_none, unknown_keys


def _default_state_path() -> Path:
    """Compute the state path lazily so env overrides work in tests.

    Precedence: MODELMAN_STATE > XDG_CONFIG_HOME > ~/.config, matching
    registry.py's `_default_registry_path` so registry.toml and modelman.toml
    land in the same directory (migrate/sync write them together).
    """
    override = os.environ.get("MODELMAN_STATE")
    if override:
        return Path(override).expanduser()
    base = os.environ.get("XDG_CONFIG_HOME") or "~/.config"
    return Path(base, "local-ai", "modelman.toml").expanduser()


@dataclass
class ModelState:
    downloaded: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    litellm_exposed: bool = False
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class FamilyState:
    display_name: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)
    families: dict[str, FamilyState] = field(default_factory=dict)
    extra: dict[str, Any] = field(default_factory=dict, repr=False)

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
            extra=unknown_keys(entry, {"downloaded", "disk_path", "size_bytes", "litellm_exposed"}),
        )
        for model_id, entry in raw.get("model_state", {}).items()
    }
    families = {
        family: FamilyState(
            display_name=entry.get("display_name"),
            extra=unknown_keys(entry, {"display_name"}),
        )
        for family, entry in raw.get("families", {}).items()
    }
    return StateStore(
        models=models,
        families=families,
        extra=unknown_keys(raw, {"model_state", "families"}),
    )


def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {
            model_id: drop_none(
                {
                    **s.extra,
                    "downloaded": s.downloaded,
                    "disk_path": s.disk_path,
                    "size_bytes": s.size_bytes,
                    "litellm_exposed": s.litellm_exposed,
                }
            )
            for model_id, s in store.models.items()
        },
        "families": {
            family: drop_none({**s.extra, "display_name": s.display_name})
            for family, s in store.families.items()
        },
    }
    atomic_write_toml({**store.extra, **payload}, state_path)
