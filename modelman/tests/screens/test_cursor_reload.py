"""Tests for the shared reload_preserving_cursor helper.

The helper is used by both FamilyScreen.reload and ModelScreen._load_models
to clear+repopulate their DataTables without resetting the cursor to row 0
every refresh.
"""

import pytest
from textual.widgets import DataTable

from modelman.app import ModelmanApp
from modelman.screens import reload_preserving_cursor


@pytest.mark.asyncio
async def test_reload_preserving_cursor_restores_same_key():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = DataTable(cursor_type="row")
        app.screen.mount(table)
        await pilot.pause()
        table.add_columns("NAME")
        table.add_row("alpha", key="alpha")
        table.add_row("beta", key="beta")
        table.add_row("gamma", key="gamma")
        table.move_cursor(row=1)
        assert table.cursor_row == 1

        def repopulate():
            table.clear()
            table.add_row("alpha", key="alpha")
            table.add_row("beta", key="beta")
            table.add_row("gamma", key="gamma")

        reload_preserving_cursor(table, repopulate)
        await pilot.pause()
        assert table.cursor_row == 1
        assert table.get_row_at(table.cursor_row)[0] == "beta"


@pytest.mark.asyncio
async def test_reload_preserving_cursor_falls_back_when_key_missing():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = DataTable(cursor_type="row")
        app.screen.mount(table)
        await pilot.pause()
        table.add_columns("NAME")
        table.add_row("alpha", key="alpha")
        table.add_row("beta", key="beta")
        table.move_cursor(row=1)

        def repopulate():
            table.clear()
            table.add_row("alpha", key="alpha")

        reload_preserving_cursor(table, repopulate)
        await pilot.pause()
        assert table.cursor_row == 0


@pytest.mark.asyncio
async def test_reload_preserving_cursor_noop_on_empty_table():
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        table = DataTable(cursor_type="row")
        app.screen.mount(table)
        await pilot.pause()
        table.add_columns("NAME")

        def repopulate():
            table.clear()
            table.add_row("alpha", key="alpha")

        reload_preserving_cursor(table, repopulate)
        await pilot.pause()
        assert table.cursor_row == 0
        assert table.row_count == 1
