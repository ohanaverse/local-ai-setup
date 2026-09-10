"""Textual application root for modelman."""

from __future__ import annotations

import contextlib

from textual.app import App

from .downloads import DownloadManager
from .registry import load_registry, sync_agent_providers
from .screens.families import FamilyScreen
from .screens.forms import QuitBlockedModal
from .screens.models import ModelScreen
from .settings import Settings, load_settings, save_settings
from .state import load_state


def _configured_providers() -> list[str]:
    """Read provider names from registry.toml; on any failure return [].

    Kept here (rather than in registry.py) so `app.py` doesn't grow a
    hard dependency on a modelman-side parser for legacy config.yaml.
    """
    try:
        registry = load_registry()
        sync_agent_providers(registry)
        return [p.id for p in registry.providers]
    except Exception:
        return []


class ModelmanApp(App[None]):
    TITLE = "modelman"

    def __init__(self, family: str | None = None) -> None:
        super().__init__()
        self._initial_family = family
        # One DownloadManager for the app's lifetime, created before any
        # screen mounts so every screen can reference self.app.downloads
        # the instant it might need it (quit guard, glyph, routing).
        self.downloads = DownloadManager(self)
        # Set by the startup price-refresh worker when it skips or fails; read
        # by ModelScreen.action_back to decide whether to show a stale-pricing
        # reminder in the exit confirmation dialog.
        self._price_refresh_skipped_or_failed = False
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
                sync_agent_providers(registry)
            except Exception:
                registry = None
            if registry is None:
                self.run_worker(self._run_price_refresh, exclusive=True, thread=True)
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
        self.run_worker(self._run_price_refresh, exclusive=True, thread=True)

    def _run_price_refresh(self) -> None:
        """Daily-gated background refresh of OpenRouter prices.

        Runs on a worker thread at startup. Loads the registry, checks the
        daily gate, fetches prices, saves the registry, and records the
        refresh date — all fail-soft. Sets ``_price_refresh_skipped_or_failed``
        so the exit dialog can show a stale-pricing reminder.
        """
        from datetime import date

        from .pricing import refresh_prices, should_run_price_refresh
        from .registry import _default_registry_path, load_registry, save_registry
        from .state import StateStore, load_state, locked_state, set_price_refresh_last_run

        # Skip when the canonical registry file is absent — load_registry()
        # would fall back to the legacy ~/.config path and save_registry()
        # would then write to the canonical (env-overridden) path, creating
        # a stray file. The legacy fallback is a migration concern, not a
        # refresh concern.
        if not _default_registry_path().exists():
            return

        try:
            registry = load_registry()
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(self.notify, f"Could not load registry for price refresh: {exc}")
            self._price_refresh_skipped_or_failed = True
            return

        try:
            state = load_state()
        except Exception:  # noqa: BLE001
            state = StateStore()

        if not should_run_price_refresh(state, registry):
            return

        result = refresh_prices(registry)
        if result.error is not None:
            self.call_from_thread(self.notify, f"Token price refresh failed: {result.error}")
            self._price_refresh_skipped_or_failed = True
            return

        try:
            save_registry(registry)
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(
                self.notify, f"Token prices refreshed but could not save registry: {exc}"
            )
            self._price_refresh_skipped_or_failed = True
            return

        try:
            with locked_state() as disk_state:
                set_price_refresh_last_run(disk_state, date.today().isoformat())
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(
                self.notify, f"Token prices refreshed but could not record refresh date: {exc}"
            )

        for warning in result.warnings:
            self.call_from_thread(self.notify, warning)
        self.call_from_thread(self.notify, f"Token prices refreshed for {result.updated} model(s)")

    def request_quit(self) -> None:
        """The single quit entry point every binding routes through
        (ctrl+q's default action_quit, and FamilyScreen's 'q' — Task 9).
        Blocks quitting while a download is active instead of exiting
        out from under it, and — if the top screen is a ModelScreen with
        an unapplied queue (delete/ready/move/expose) — routes through
        its own action_back() so ctrl+q gets the same apply/discard/
        cancel confirmation Escape would give, instead of silently
        dropping the queue."""
        if self.downloads.has_active():

            def _on_choice(review: bool | None) -> None:
                if review:
                    from .screens.downloads import DownloadScreen

                    self.push_screen(DownloadScreen())

            self.push_screen(QuitBlockedModal(), _on_choice)
            return

        top = self.screen
        if isinstance(top, ModelScreen) and top.has_pending_changes():
            top.action_back()
            return

        self.exit()

    async def action_quit(self) -> None:
        """Override Textual's default (self.exit()) to route through the
        download quit guard. This also fixes a pre-existing quirk where
        ctrl+q quit immediately from any screen, bypassing ModelScreen's
        apply-on-exit confirm — it's now gated by request_quit() the same
        way FamilyScreen's 'q' binding is."""
        self.request_quit()

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
