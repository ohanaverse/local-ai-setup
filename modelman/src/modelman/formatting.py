"""Shared display-formatting helpers used by both the CLI and the TUI."""

from __future__ import annotations


def format_size(n: int | None) -> str:
    """Human-readable byte size (e.g. "4.9 GB"), or "—" when unknown."""
    if n is None:
        return "—"
    value = float(n)
    if value < 1024:
        return f"{int(value)} B"
    for unit in ("KB", "MB", "GB", "TB"):
        value /= 1024
        if value < 1024:
            return f"{value:.1f} {unit}"
    return f"{value:.1f} PB"
