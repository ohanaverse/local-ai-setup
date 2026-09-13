"""Textual application root for modelman."""

from __future__ import annotations

import contextlib
from typing import Any

from textual.app import App

from .downloads import DownloadManager
from .registry import (
    Registry,
    RegistryError,
    _default_registry_path,
    load_registry,
    sync_agent_providers,
)
from .screens.forms import QuitBlockedModal
from .screens.models import ModelScreen
from .settings import Settings, load_settings, save_settings
from .state import _default_state_path, load_state


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
        try:
            registry = load_registry()
        except RegistryError:
            # No registry.toml yet (fresh install): an empty Registry still
            # gives the user an Add dialog to create the first model.
            registry = Registry()
        sync_agent_providers(registry)
        self.push_screen(
            ModelScreen(
                registry=registry,
                state=load_state(),
                registry_path=_default_registry_path(),
                state_path=_default_state_path(),
                scroll_to_family=self._initial_family,
            )
        )
        self.run_worker(self._run_price_refresh, exclusive=True, thread=True)

    def _run_price_refresh(self) -> None:
        """Daily-gated background refresh of OpenRouter prices.

        Runs on a worker thread at startup. Checks the daily gate, fetches
        prices, applies them under the registry lock, and records the refresh
        date — all fail-soft. Sets ``_price_refresh_skipped_or_failed`` so the
        exit dialog can show a stale-pricing reminder.

        The fetch happens *before* the lock is taken, so the network round
        trip never blocks a main-thread registry save (an add/edit the user
        makes while the API is slow).
        """
        from datetime import date

        from .pricing import apply_prices, fetch_openrouter_pricing, should_run_price_refresh
        from .registry import _default_registry_path, load_registry, locked_registry
        from .state import StateStore, load_state, locked_state, set_price_refresh_last_run

        # Skip when the canonical registry file is absent — load_registry()
        # would fall back to the legacy ~/.config path and locked_registry()
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

        try:
            api_models = fetch_openrouter_pricing()
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(self.notify, f"Token price refresh failed: {exc}")
            self._price_refresh_skipped_or_failed = True
            return

        api_by_id: dict[str, dict[str, Any]] = {}
        for item in api_models:
            if isinstance(item, dict) and "id" in item:
                api_by_id[item["id"]] = item

        try:
            with locked_registry() as disk_registry:
                result = apply_prices(disk_registry, api_by_id)
        except Exception as exc:  # noqa: BLE001
            self.call_from_thread(
                self.notify, f"Token prices refreshed but could not save registry: {exc}"
            )
            self._price_refresh_skipped_or_failed = True
            return

        # A refresh that updated nothing (cloud candidates existed, but no
        # OpenRouter entry matched or carried pricing) is not a success worth
        # gating on: surface the stale-pricing reminder and do NOT record
        # today's date, so the daily gate allows a same-day retry instead of
        # suppressing it.
        if result.updated == 0:
            self._price_refresh_skipped_or_failed = True
        else:
            try:
                with locked_state() as disk_state:
                    set_price_refresh_last_run(disk_state, date.today().isoformat())
            except Exception as exc:  # noqa: BLE001
                self.call_from_thread(
                    self.notify,
                    f"Token prices refreshed but could not record refresh date: {exc}",
                )

        for warning in result.warnings:
            self.call_from_thread(self.notify, warning)
        self.call_from_thread(self.notify, f"Token prices refreshed for {result.updated} model(s)")

    def request_quit(self) -> None:
        """The single quit entry point every binding routes through
        (ctrl+q's default action_quit, and ModelScreen's Escape when it
        has no pending changes — it's the app's root screen now, so
        Escape-with-nothing-queued means quit). Blocks quitting while a
        download is active instead of exiting
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
        apply-on-exit confirm — it's now gated by request_quit()."""
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
