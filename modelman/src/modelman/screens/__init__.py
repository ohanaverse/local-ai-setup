"""Shared TUI screen helpers."""

from __future__ import annotations

from collections.abc import Callable

from textual.widgets import DataTable


def reload_preserving_cursor(table: DataTable, repopulate: Callable[[], None]) -> None:
    """Clear and repopulate `table` without resetting the cursor to row 0.

    DataTable.clear() resets the cursor to (0, 0). This helper snapshots
    the row key under the cursor, runs the caller's repopulate function,
    then restores the cursor onto that key. If the key no longer exists
    (row deleted elsewhere), it falls back to row 0. Empty tables before
    or after are safe no-ops.
    """
    if table.row_count == 0:
        repopulate()
        return

    row_key = list(table.rows.keys())[table.cursor_row]
    repopulate()

    if table.row_count == 0:
        return

    # Textual's DataTable.get_row_index raises RowDoesNotExist (a
    # Textual-internal exception, not a stdlib KeyError) when the key
    # is gone, so catch the broad Exception family and fall back.
    try:
        new_index = table.get_row_index(row_key)
    except Exception:
        new_index = 0
    table.move_cursor(row=new_index)
