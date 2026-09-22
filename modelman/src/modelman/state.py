"""modelman.toml — modelman's per-machine mutable state overlay.

Owner: modelman (the only writer). wt reads this file read-only — the
`exposed` and `ready` flags, to filter its model picker. A `[litellm]`
table here is only a legacy read-only fallback wt copies once (wt owns the
routing state); modelman round-trips it verbatim and never edits it. wt does NOT read the per-model `running` flag: running
state comes from wt's own live probes of the providers (see
wt/internal/config/modelman.go; the shared
contract fixture is docs/contracts/modelman.sample.toml). See registry.py for the
canonical, shared model/provider/family definitions this state is keyed
against, and
`docs/superpowers/specs/2026-08-27-shared-model-registry-design.md` for the
ownership split.

The `families` table is a legacy read-side fallback: family display names
now live in registry.toml's first-class [[families]] entries (see
docs/superpowers/specs/2026-08-29-modelman-first-class-families-design.md).
Writers no longer create entries here; they drain them via promotion.
Entries stay loadable so pre-existing modelman.toml files keep working.
"""

from __future__ import annotations

import contextlib
import os
import threading
import tomllib
from collections.abc import Generator
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from ._toml_io import atomic_write_toml, drop_none, unknown_keys

_PRICE_REFRESH_LAST_RUN_KEY = "price_refresh_last_run"


def get_price_refresh_last_run(state: StateStore) -> str | None:
    return state.extra.get(_PRICE_REFRESH_LAST_RUN_KEY)


def set_price_refresh_last_run(state: StateStore, date: str | None) -> None:
    if date is None:
        state.extra.pop(_PRICE_REFRESH_LAST_RUN_KEY, None)
    else:
        state.extra[_PRICE_REFRESH_LAST_RUN_KEY] = date


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
    ready: bool = False
    disk_path: str | None = None
    size_bytes: int | None = None
    exposed: bool = False  # was litellm_exposed
    running: bool = False
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class FamilyState:
    display_name: str | None = None
    extra: dict[str, Any] = field(default_factory=dict, repr=False)


@dataclass
class StateStore:
    models: dict[str, ModelState] = field(default_factory=dict)
    families: dict[str, FamilyState] = field(default_factory=dict)
    # NOTE: a legacy [litellm] table (wt owns routing state since 2026-09-21)
    # lives untouched in `extra`; modelman never reads, invents or mutates it.
    extra: dict[str, Any] = field(default_factory=dict, repr=False)

    def get(self, model_id: str) -> ModelState:
        return self.models.get(model_id, ModelState())

    def set(self, model_id: str, state: ModelState) -> None:
        self.models[model_id] = state

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
            ready=entry.get("ready", entry.get("downloaded", False)),
            disk_path=entry.get("disk_path"),
            size_bytes=entry.get("size_bytes"),
            exposed=entry.get("exposed", entry.get("litellm_exposed", False)),
            running=entry.get("running", False),
            extra=unknown_keys(
                entry,
                {
                    "ready",
                    "downloaded",
                    "disk_path",
                    "size_bytes",
                    "exposed",
                    "litellm_exposed",
                    "running",
                },
            ),
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
        extra=unknown_keys(raw, {"model_state", "families", "local"}),
    )


def save_state(store: StateStore, path: Path | None = None) -> None:
    state_path = Path(path) if path else _default_state_path()
    payload = {
        "model_state": {
            model_id: drop_none(
                {
                    **s.extra,
                    "ready": s.ready,
                    "disk_path": s.disk_path,
                    "size_bytes": s.size_bytes,
                    "exposed": s.exposed,
                    "running": s.running,
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


_STATE_LOCK = threading.Lock()


@contextlib.contextmanager
def locked_state(path: Path | None = None) -> Generator[StateStore]:
    """Atomically read-modify-write modelman.toml.

    save_state() alone is a whole-file overwrite of whatever StateStore
    it's given, with no merge — safe only when a single writer holds the
    only in-memory copy. Within one modelman process, a background
    worker thread (e.g. app.py's daily price-refresh worker) and the
    main thread can each independently call locked_state() around the
    same on-disk file; without serializing them, both could load a
    stale snapshot and silently stomp each other's changes. This
    acquires a process-wide
    lock, loads the current on-disk StateStore, yields it for the caller
    to mutate in place, and saves it back before releasing the lock —
    every writer's mutation is always applied on top of the latest
    on-disk state.
    """
    with _STATE_LOCK:
        store = load_state(path)
        yield store
        save_state(store, path)
