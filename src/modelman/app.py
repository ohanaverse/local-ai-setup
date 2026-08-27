"""Textual application root for modelman."""

from __future__ import annotations

import contextlib

from textual.app import App

from .config import load_config
from .manifest import get_family_dir, load_manifest
from .screens.families import FamilyScreen
from .screens.models import ModelScreen
from .settings import Settings, load_settings, save_settings


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
        # Compute configured providers once and forward to whichever
        # screens need them. Keeps ModelScreen's provider pane showing
        # every entry from config.yaml, even when the family on disk
        # has zero variants. Order is config-file insertion order.
        try:
            configured = list(load_config().providers.keys())
        except Exception:
            configured = []
        self.push_screen(FamilyScreen())
        if self._initial_family is not None:
            manifest = load_manifest(self._initial_family)
            path = get_family_dir() / f"{self._initial_family}.yaml"
            self.push_screen(ModelScreen(manifest, path, configured))

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
        # Persistence is best-effort: don't crash the TUI on a disk
        # error. The user can still see and use their theme for the
        # current session.
        with contextlib.suppress(Exception):
            save_settings(current)
