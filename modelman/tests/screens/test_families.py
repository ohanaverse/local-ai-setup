"""Tests for FamilyScreen refresh locking and cursor preservation."""

from __future__ import annotations

import threading
from unittest.mock import MagicMock

import pytest
from textual.widgets import DataTable, Static

from modelman.app import ModelmanApp
from modelman.registry import (
    AuthConfig,
    FamilyEntry,
    ModelEntry,
    ProviderEntry,
    Registry,
    save_registry,
)
from modelman.state import ModelState, StateStore, save_state


def _seed(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="alpha"), FamilyEntry(name="beta")],
        models=[
            ModelEntry(id="ollama/a", family="alpha", provider_id="ollama", model_name="a"),
            ModelEntry(id="ollama/b", family="beta", provider_id="ollama", model_name="b"),
        ],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/a", ModelState(ready=True, disk_path="/tmp/a"))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))
    return reg_path, state_path


@pytest.mark.asyncio
async def test_family_screen_table_focused_after_reconcile(tmp_path, monkeypatch):
    """After the initial refresh completes, focus must be back on the family
    table without the user pressing Tab first. Disabling the focused table
    (Textual blurs a widget when it's disabled) must not leave the screen
    with nothing focused once the table is re-enabled."""
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    gate = threading.Event()
    stub.is_downloaded.side_effect = lambda v: gate.wait(timeout=2.0) or True
    stub.size_of.return_value = 1
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("#family-table", DataTable)
        # While the refresh runs the table is disabled and blurred...
        assert app.screen._reconciling is True
        assert table.disabled is True
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        assert app.screen._reconciling is False
        # ...but once the refresh completes the table must hold focus again.
        assert app.screen.focused is table


@pytest.mark.asyncio
async def test_family_screen_table_disabled_while_reconciling(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"

    gate = threading.Event()

    def slow_is_downloaded(v):
        gate.wait(timeout=2.0)
        return True

    stub.is_downloaded.side_effect = slow_is_downloaded
    stub.size_of.return_value = 1
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("#family-table", DataTable)
        assert app.screen._reconciling is True
        assert table.disabled is True
        indicator = app.screen.query_one("#refresh-indicator", Static)
        assert indicator.display is True
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        assert app.screen._reconciling is False
        assert table.disabled is False
        assert indicator.display is False


@pytest.mark.asyncio
async def test_family_screen_actions_noop_while_reconciling(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry
    from modelman.screens.forms import AddFamilyModal, ConfirmModal, EditFamilyModal

    stub = MagicMock()
    stub.name = "ollama"
    gate = threading.Event()
    stub.is_downloaded.side_effect = lambda v: gate.wait(timeout=2.0) or True
    stub.size_of.return_value = 1
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    captured = []
    original_push = app.push_screen

    def tracking_push(screen, *args, **kwargs):
        if isinstance(screen, (AddFamilyModal, EditFamilyModal, ConfirmModal)):
            captured.append(screen)
        return original_push(screen, *args, **kwargs)

    monkeypatch.setattr(app, "push_screen", tracking_push)

    async with app.run_test() as pilot:
        await pilot.pause()
        await pilot.press("a")
        await pilot.press("e")
        await pilot.press("d")
        await pilot.press("enter")
        await pilot.press("r")
        await pilot.pause()
        assert captured == []
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break


@pytest.mark.asyncio
async def test_family_screen_superseded_reconcile_does_not_clear_flag(tmp_path, monkeypatch):
    """A superseded reconcile worker must not clear _reconciling — only the
    latest worker does. on_screen_resume fires on initial mount too, so two
    workers start on mount; the first is cancelled by exclusive=True but its
    finally block still runs _reconcile_done. Without the generation check,
    that stale completion would unlock the table while the second worker is
    still scanning."""
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Let the initial reconcile(s) settle so we control the state below.
        for _ in range(30):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        screen = app.screen
        # Simulate a newer worker having started: bump the generation and
        # mark reconciling.
        screen._reconciling = True
        screen._reconcile_generation += 1
        current = screen._reconcile_generation
        # A stale worker (previous generation) finishing is a no-op.
        screen._reconcile_done(current - 1)
        assert screen._reconciling is True
        # The current worker finishing clears the flag.
        screen._reconcile_done(current)
        assert screen._reconciling is False


@pytest.mark.asyncio
async def test_family_screen_cursor_restored_after_reconcile(tmp_path, monkeypatch):
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    # Block the worker long enough that we can observe the in-progress
    # reconciling state.
    gate = threading.Event()
    stub.is_downloaded.side_effect = lambda v: gate.wait(timeout=2.0) or True
    stub.size_of.return_value = 1
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Let the initial reconcile complete.
        gate.set()
        for _ in range(30):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        # Reset gate for the user-triggered reconcile.
        gate.clear()
        table = app.screen.query_one("#family-table", DataTable)
        table.move_cursor(row=1)
        assert table.cursor_row == 1
        app.screen.action_reconcile()
        await pilot.pause()
        # Worker is now running and gated.
        assert app.screen._reconciling is True
        gate.set()
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        assert app.screen._reconciling is False
        assert table.cursor_row == 1
        assert str(list(table.rows.keys())[table.cursor_row].value) == "beta"


@pytest.mark.asyncio
async def test_family_screen_column_headers(tmp_path, monkeypatch):
    """The family table's fourth column is DOWNLOADED (count of local
    models on disk), not READY."""
    _seed(tmp_path, monkeypatch)

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = False
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = app.screen.query_one("#family-table", DataTable)
        labels = [str(col.label) for col in table.columns.values()]
        assert labels == ["FAMILY", "DISPLAY", "VARIANTS", "DOWNLOADED", "SIZE"]


@pytest.mark.asyncio
async def test_family_screen_downloaded_excludes_cloud(tmp_path, monkeypatch):
    """DOWNLOADED counts only local models. A cloud entry (location='cloud')
    holds no disk weights and must not inflate the count, even when the
    provider reports it as downloaded — `ollama show <model>:cloud` exits 0."""
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    reg = Registry(
        providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
        families=[FamilyEntry(name="alpha")],
        models=[
            ModelEntry(
                id="ollama/cloudy",
                family="alpha",
                provider_id="ollama",
                model_name="cloudy:cloud",
                location="cloud",
            ),
            ModelEntry(id="ollama/local-a", family="alpha", provider_id="ollama", model_name="a"),
        ],
    )
    save_registry(reg, reg_path)
    state = StateStore()
    state.set("ollama/local-a", ModelState(ready=True, disk_path="/tmp/a"))
    save_state(state, state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))

    from modelman.providers import registry as prov_registry

    stub = MagicMock()
    stub.name = "ollama"
    stub.is_downloaded.return_value = True  # cloud tags resolve too
    stub.size_of.return_value = None
    monkeypatch.setattr(prov_registry.ProviderRegistry, "get", staticmethod(lambda name, cfg: stub))

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Let the initial reconcile complete so reload() uses provider truth.
        for _ in range(50):
            await pilot.pause()
            if not app.screen._reconciling:
                break
        table = app.screen.query_one("#family-table", DataTable)
        # Row key is the family name (key=family in add_row); cells are
        # FAMILY, DISPLAY, VARIANTS, DOWNLOADED, SIZE.
        row_key = next(k for k in table.rows if k.value == "alpha")
        assert str(table.get_row(row_key)[3]) == "1"
        assert str(table.get_row(row_key)[4]) == "—"
