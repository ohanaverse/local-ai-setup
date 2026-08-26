"""modelman CLI entry point."""
from __future__ import annotations

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .commands import download as download_cmd

app = typer.Typer(help="Manage local LLM model families across providers.")
app.add_typer(download_cmd.app, name="download")


if __name__ == "__main__":
    app()