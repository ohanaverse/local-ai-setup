"""Textual application root for modelman."""

from __future__ import annotations

import contextlib

from textual.app import App

from .registry import load_registry
from .screens.families import FamilyScreen
from .screens.models import ModelScreen
from .settings import Settings, load_settings, save_settings
from .state import load_state


def _configured_providers() -> list[str]:
    """Read provider names from registry.toml; on any failure return [].

    Kept here (rather than in registry.py) so `app.py` doesn't grow a
    hard dependency on a modelman-side parser for legacy config.yaml.
    """
    try:
        return [p.id for p in load_registry().providers]
    except Exception:
        return []


class ModelmanApp(App[None]):
    TITLE = "modelman"

    def __init__(self, family: str | None = None) -> None:
        super().__init__()
        self._initial_family = family
        # Load user preferences (theme, etc.) before any widget
        # mounts so the first frame uses the right colors. A missing
        # file returns defaults; a corrupted file falls back to
        # defaults so the user isn't locked out of their TUI.
        try:
            settings = load_settings()
        except Exception:
            settings = Settings()
        if settings.theme:
            self.theme = settings.theme

    def on_mount(self) -> None:
        configured = _configured_providers()
        self.push_screen(FamilyScreen())
        if self._initial_family is not None:
            try:
                registry = load_registry()
            except Exception:
                registry = None
            if registry is None:
                return
            from .registry import _default_registry_path
            from .state import _default_state_path

            self.push_screen(
                ModelScreen(
                    registry=registry,
                    state=load_state(),
                    family=self._initial_family,
                    registry_path=_default_registry_path(),
                    state_path=_default_state_path(),
                    available_providers=configured,
                )
            )

    def watch_theme(self, old_theme: str | None, new_theme: str) -> None:
        """Persist the theme whenever the user picks a new one via
        Ctrl+P (or any other Textual mechanism that mutates self.theme).
        """
        del old_theme  # unused; required by the watch_* signature
        # Skip saving when the new value already matches what's on
        # disk (e.g. watch fires once at startup with the loaded
        # theme). Avoids touching disk on every launch.
        try:
            current = load_settings()
        except Exception:
            current = Settings()
        if current.theme == new_theme:
            return
        current.theme = new_theme
        with contextlib.suppress(Exception):
            save_settings(current)
