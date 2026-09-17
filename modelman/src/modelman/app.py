"""Textual application root for modelman."""

from __future__ import annotations

import contextlib
from typing import Any

from textual.app import App

from .queue import QueuedOps
from .registry import (
    Registry,
    RegistryError,
    _default_registry_path,
    load_registry,
    sync_agent_providers,
)
from .screens.forms import ConfirmForceQuitDialog
from .screens.models import ModelScreen
from .settings import Settings, load_settings, save_settings
from .state import _default_state_path, load_state


class ModelmanApp(App[QueuedOps | None]):
    TITLE = "modelman"

    def __init__(self) -> None:
        super().__init__()
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
            )
        )
        self.run_worker(
            self._run_price_refresh,
            exclusive=True,
            thread=True,
            name="price-refresh",
            description="Refreshing token prices",
        )

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
        """ctrl+q's entry point: delegate to the top ModelScreen's Escape
        handling (the apply/discard/cancel confirmation dialog when a
        queue is pending, or an immediate exit when it's empty) instead
        of quitting out from under an unapplied queue. If the force-quit
        dialog is already up (a background worker is still running from
        a prior quit attempt), a second ctrl+q dismisses it as "Force
        quit" instead of falling through to a plain self.exit() — which
        would hang the same way the dialog exists to prevent. Falls
        through to a direct exit for any other modal."""
        top = self.screen
        if isinstance(top, ModelScreen):
            top.action_back()
            return
        if isinstance(top, ConfirmForceQuitDialog):
            top.dismiss(True)
            return
        self.exit()

    async def action_quit(self) -> None:
        """Override Textual's default (self.exit()) to route through
        request_quit(), so ctrl+q shows ModelScreen's apply/discard/cancel
        confirm dialog when a queue is pending, instead of quitting
        immediately from any screen (a pre-existing quirk this fixes)."""
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
