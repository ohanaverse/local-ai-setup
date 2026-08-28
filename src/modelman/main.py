"""modelman CLI entry point."""

from __future__ import annotations

from pathlib import Path

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .app import ModelmanApp
from .config import default_config_path
from .litellm import (
    ExposeError,
    LiteLLMConfigError,
    default_litellm_config_path,
    expose_model,
    unexpose_model,
)
from .manifest import get_family_dir
from .migrate import migrate as run_migration
from .registry import load_registry, save_registry
from .state import load_state, save_state
from .sync import SyncError
from .sync import sync as run_sync

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


@app.command()
def sync() -> None:
    """Reconcile configured models against their providers."""
    registry = load_registry()
    state = load_state()
    try:
        result = run_sync(registry, state)
    except SyncError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    if result.providers_added:
        try:
            save_registry(registry)
        except OSError as exc:
            # The state sync already succeeded; report the registry repair
            # failure cleanly instead of a traceback. The repair is idempotent
            # and re-runs on the next sync.
            typer.echo(f"error: failed to save registry: {exc}", err=True)
            raise typer.Exit(1) from exc
        typer.echo(f"Added provider entries: {', '.join(result.providers_added)}")
    typer.echo(
        f"Synced: {len(result.downloaded)} downloaded, {len(result.not_downloaded)} not downloaded."
    )


@app.command()
def expose(
    model_id: str = typer.Argument(..., help="Registry model id to expose"),
) -> None:
    """Expose a model through LiteLLM (writes a model_list entry)."""
    registry = load_registry()
    state = load_state()
    try:
        expose_model(registry, state, model_id, default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(f"Exposed {model_id} through LiteLLM.")


@app.command()
def unexpose(
    model_id: str = typer.Argument(..., help="Registry model id to stop exposing"),
) -> None:
    """Remove a model's LiteLLM model_list entry."""
    state = load_state()
    try:
        unexpose_model(state, model_id, default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(f"Unexposed {model_id}.")


if __name__ == "__main__":
    app()
