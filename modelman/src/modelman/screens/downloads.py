"""DownloadScreen — live view of DownloadManager's in-flight and
finished downloads, opened with 'g' from FamilyScreen and ModelScreen."""

from __future__ import annotations

from textual.app import ComposeResult
from textual.screen import Screen
from textual.widgets import DataTable, Footer, Header

from . import reload_preserving_cursor


class DownloadScreen(Screen[None]):
    BINDINGS = [
        ("escape", "back", "Back"),
        ("c", "cancel_selected", "Cancel"),
        ("C", "clear_finished", "Clear finished"),
    ]

    def compose(self) -> ComposeResult:
        yield Header()
        yield DataTable(id="downloads-table", cursor_type="row")
        yield Footer()

    def on_mount(self) -> None:
        table = self.query_one("#downloads-table", DataTable)
        table.add_columns("MODEL", "PROVIDER", "STATUS", "PROGRESS")
        self._reload()
        self.set_interval(1.0, self._reload)

    def _reload(self) -> None:
        table = self.query_one("#downloads-table", DataTable)
        n_active = 0

        def _repopulate() -> None:
            nonlocal n_active
            table.clear()
            for state in self.app.downloads.states():  # type: ignore[attr-defined]
                if state.status == "downloading":
                    n_active += 1
                table.add_row(
                    state.model_id,
                    state.provider,
                    state.status,
                    state.progress or state.error or "",
                    key=state.model_id,
                )

        reload_preserving_cursor(table, _repopulate)
        self.title = f"⏳ {n_active} downloading" if n_active else "Downloads"

    def action_back(self) -> None:
        self.app.pop_screen()

    def action_cancel_selected(self) -> None:
        table = self.query_one("#downloads-table", DataTable)
        if table.row_count == 0:
            return
        row_key = list(table.rows.keys())[table.cursor_row]
        model_id = str(row_key.value)
        state = next(  # type: ignore[attr-defined]
            (s for s in self.app.downloads.states() if s.model_id == model_id),  # type: ignore[attr-defined]
            None,
        )
        if state is None or state.status != "downloading":
            return
        self.app.downloads.cancel(model_id)  # type: ignore[attr-defined]

    def action_clear_finished(self) -> None:
        """Clear all non-downloading states from the DownloadManager.
        This removes finished/failed/cancelled downloads from the list."""
        self.app.downloads.clear_finished()  # type: ignore[attr-defined]
        self._reload()
