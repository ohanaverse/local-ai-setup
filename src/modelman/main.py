"""modelman CLI entry point."""
from __future__ import annotations

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .commands.download import _do_download

app = typer.Typer(help="Manage local LLM model families across providers.")


@app.callback()
def _main():
    """Manage local LLM model families across providers."""
    pass


@app.command()
def download(
    family: str = typer.Argument(..., help="Family name (filename under ~/.config/local-ai/families/)"),
    all_missing: bool = typer.Option(False, "--all", help="Download all missing variants without prompting"),
    yes: bool = typer.Option(False, "-y", "--yes", help="Skip confirmation (used with --all)"),
):
    """Download missing model variants for a family."""
    _do_download(family, all_missing, yes)


if __name__ == "__main__":
    app()