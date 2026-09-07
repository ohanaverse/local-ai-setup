"""Tests for DownloadScreen: rendering DownloadManager's states and
per-row cancel."""

from __future__ import annotations

import pytest
from textual.widgets import DataTable

from modelman.app import ModelmanApp
from modelman.downloads import DownloadState
from modelman.registry import AuthConfig, FamilyEntry, ProviderEntry, Registry, save_registry
from modelman.screens.downloads import DownloadScreen
from modelman.state import StateStore, save_state


def _seed(tmp_path, monkeypatch):
    reg_path = tmp_path / "registry.toml"
    state_path = tmp_path / "modelman.toml"
    save_registry(
        Registry(
            providers=[ProviderEntry(id="ollama", name="O", auth=AuthConfig(type="none"))],
            families=[FamilyEntry(name="f")],
        ),
        reg_path,
    )
    save_state(StateStore(), state_path)
    monkeypatch.setenv("MODELMAN_REGISTRY", str(reg_path))
    monkeypatch.setenv("MODELMAN_STATE", str(state_path))


@pytest.mark.asyncio
async def test_download_screen_renders_active_downloads(tmp_path, monkeypatch):
    # Opening the screen must show whatever DownloadManager already knows
    # about, without requiring a download to actually be running.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        assert table.row_count == 1
        row = list(table.rows.keys())[0]
        assert str(row.value) == "ollama/x"


@pytest.mark.asyncio
async def test_cancel_key_cancels_the_row_under_cursor(tmp_path, monkeypatch):
    # 'c' on a downloading row must call DownloadManager.cancel() with
    # that row's model id.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="downloading"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("c")
        await pilot.pause()
        assert cancelled == ["ollama/x"]


@pytest.mark.asyncio
async def test_reload_preserves_cursor_on_the_selected_row(tmp_path, monkeypatch):
    # Regression for a review finding: _reload() hand-rolled its own
    # cursor-preserving clear/repopulate instead of routing through the
    # shared reload_preserving_cursor() helper both other list screens use
    # (see modelman/CLAUDE.md: "the single DataTable-clear helper... Both
    # list screens route through it"). Pin the cursor-preservation
    # behavior itself so a future change can't silently drop it.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/a"] = DownloadState(
            model_id="ollama/a", variant_id="ollama/a", provider="ollama", status="downloading"
        )
        app.downloads._states["ollama/b"] = DownloadState(
            model_id="ollama/b", variant_id="ollama/b", provider="ollama", status="downloading"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        keys = [k.value for k in table.rows]
        table.move_cursor(row=keys.index("ollama/b"))

        app.screen._reload()  # simulates the 1s poll's periodic call
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        cursor_key = list(table.rows.keys())[table.cursor_row].value
        assert cursor_key == "ollama/b"


@pytest.mark.asyncio
async def test_cancel_key_is_a_noop_on_a_finished_row(tmp_path, monkeypatch):
    # Cancelling a row that already finished (done/failed/cancelled) must
    # not call DownloadManager.cancel() at all — nothing to cancel.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/x"] = DownloadState(
            model_id="ollama/x", variant_id="ollama/x", provider="ollama", status="done"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        cancelled = []
        monkeypatch.setattr(app.downloads, "cancel", lambda mid: cancelled.append(mid))
        await pilot.press("c")
        await pilot.pause()
        assert cancelled == []


@pytest.mark.asyncio
async def test_clear_finished_removes_non_downloading_states(tmp_path, monkeypatch):
    # 'C' should remove all finished/failed/cancelled downloads but keep
    # active ones visible.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        app.downloads._states["ollama/active"] = DownloadState(
            model_id="ollama/active", variant_id="ollama/active", provider="ollama", status="downloading"
        )
        app.downloads._states["ollama/done"] = DownloadState(
            model_id="ollama/done", variant_id="ollama/done", provider="ollama", status="done"
        )
        app.downloads._states["ollama/failed"] = DownloadState(
            model_id="ollama/failed", variant_id="ollama/failed", provider="ollama", status="failed"
        )
        app.downloads._states["ollama/cancelled"] = DownloadState(
            model_id="ollama/cancelled", variant_id="ollama/cancelled", provider="ollama", status="cancelled"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        assert table.row_count == 4

        await pilot.press("C")
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        assert table.row_count == 1
        row = list(table.rows.keys())[0]
        assert str(row.value) == "ollama/active"


@pytest.mark.asyncio
async def test_discard_clears_download_states(tmp_path, monkeypatch):
    # When discarding changes, downloads added this session should be
    # cleared from DownloadManager so they don't persist in DownloadScreen.
    _seed(tmp_path, monkeypatch)
    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        # Simulate a download that was added this session
        app.downloads._states["ollama/new"] = DownloadState(
            model_id="ollama/new", variant_id="ollama/new", provider="ollama", status="downloading"
        )
        app.push_screen(DownloadScreen())
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        assert table.row_count == 1

        # Simulate the download being cancelled (as would happen on discard)
        app.downloads._states["ollama/new"].status = "cancelled"
        app.downloads.clear_state("ollama/new")
        app.screen._reload()
        await pilot.pause()

        table = app.screen.query_one(DataTable)
        assert table.row_count == 0
