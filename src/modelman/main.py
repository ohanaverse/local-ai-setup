"""modelman CLI entry point."""

from __future__ import annotations

from pathlib import Path

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .app import ModelmanApp
from .config import default_config_path
from .manifest import get_family_dir
from .migrate import migrate as run_migration
from .registry import save_registry
from .state import save_state

app = typer.Typer(help="Manage local LLM model families across providers.")


def run_tui(family: str | None) -> None:
    """Launch the Textual TUI, optionally starting at a family's model screen."""
    ModelmanApp(family=family).run()


@app.callback(invoke_without_command=True)
def _main(ctx: typer.Context) -> None:
    """Run `modelman` with no args to open the TUI."""
    if ctx.invoked_subcommand is None:
        run_tui(None)


@app.command()
def download(
    family: str = typer.Argument(..., help="Family name (filename under families dir)"),
):
    """Open the TUI at a family's model screen (queued downloads on exit)."""
    run_tui(family)


@app.command()
def migrate(
    wt_config: str = typer.Option(
        "~/.config/agent-wt/config.toml",
        envvar="MODELMAN_WT_CONFIG",
        help="Path to agent-worktree's config.toml to import from (skipped if missing)",
    ),
) -> None:
    """One-time import of legacy config.yaml + families/*.yaml (and,
    optionally, agent-worktree's config.toml) into registry.toml +
    modelman.toml."""
    result = run_migration(
        default_config_path(),
        get_family_dir(),
        wt_config_path=Path(wt_config).expanduser(),
    )

    save_registry(result.registry)
    save_state(result.state)

    for warning in result.warnings:
        typer.echo(f"warning: {warning}")
    typer.echo(
        f"Migrated {len(result.registry.providers)} providers and "
        f"{len(result.registry.models)} models."
    )


if __name__ == "__main__":
    app()
