"""Shared helpers for subcommands."""
from __future__ import annotations

import os
from pathlib import Path
from typing import Iterable

from rich.console import Console
from rich.table import Table

from ..manifest import FamilyManifest
from ..providers.base import Provider, VariantSpec

console = Console()


def render_picker(manifest: FamilyManifest, statuses: dict[str, bool]) -> Table:
    """Render a table of variants with download status."""
    table = Table(title=f"Family: {manifest.display_name or manifest.family}")
    table.add_column("ID", style="cyan")
    table.add_column("Provider", style="magenta")
    table.add_column("Name", style="white")
    table.add_column("Status", justify="right")

    for v in manifest.variants:
        is_present = statuses.get(v["id"], False)
        status_str = "[green]✓ present[/green]" if is_present else "[red]✗ missing[/red]"
        table.add_row(v["id"], v["provider"], v["name"], status_str)
    return table


def relpath(p: str | Path) -> str:
    """Show path relative to home when under home, else absolute."""
    p = Path(p).expanduser()
    home = Path.home()
    try:
        return "~/" + str(p.relative_to(home))
    except ValueError:
        return str(p)