"""modelman CLI entry point."""

from __future__ import annotations

from pathlib import Path

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .benchmark.cli import benchmark_app
from .config import default_config_path
from .litellm import (
    ExposeError,
    LiteLLMConfigError,
    default_litellm_config_path,
    expose_model,
    unexpose_model,
)
from .local_control import LocalControlError, start_local_model, stop_local_model
from .manifest import get_family_dir
from .migrate import migrate as run_migration
from .migrate import migrate_wt_gateway_to_litellm
from .registry import load_registry, save_registry
from .state import load_state, locked_state, save_state
from .sync import SyncError
from .sync import sync as run_sync
from .usage.cli import usage_app

app = typer.Typer(help="Manage local LLM model families across providers.")
app.add_typer(benchmark_app, name="benchmark")
app.add_typer(usage_app, name="usage")

litellm_app = typer.Typer(
    help="Control whether wt routes agents through LiteLLM or dials providers directly. "
    "Never starts or stops the proxy service."
)
app.add_typer(litellm_app, name="litellm")


@litellm_app.command("status")
def litellm_status():
    """Show the current [litellm] routing state. Does not touch the proxy."""
    state = load_state()
    mode = "on" if state.litellm.enabled else "off"
    key_display = "(unset)" if not state.litellm.api_key else "***" + state.litellm.api_key[-4:]
    typer.echo(f"litellm: {mode}")
    typer.echo(f"  url: {state.litellm.url or '(unset)'}")
    typer.echo(f"  api_key: {key_display}")


@litellm_app.command("on")
def litellm_on():
    """Route non-native models through LiteLLM. Does not start the proxy."""
    with locked_state() as state:
        state.litellm.enabled = True
        incomplete = not state.litellm.url or not state.litellm.api_key
    typer.echo("litellm: on")
    if incomplete:
        typer.echo(
            "warning: litellm.url or litellm.api_key is not set — "
            "wt will fail at launch time; run 'modelman litellm set --url ... --api-key ...'",
            err=True,
        )


@litellm_app.command("off")
def litellm_off():
    """Dial providers directly where possible. Does not stop the proxy."""
    with locked_state() as state:
        state.litellm.enabled = False
    typer.echo("litellm: off")


@litellm_app.command("set")
def litellm_set(
    url: str = typer.Option(None, help="LiteLLM proxy base URL"),
    api_key: str = typer.Option(None, "--api-key", help="LiteLLM proxy API key"),
):
    """Set the proxy URL/key wt will use when routing through LiteLLM."""
    with locked_state() as state:
        if url is not None:
            state.litellm.url = url
        if api_key is not None:
            state.litellm.api_key = api_key
    typer.echo("litellm: updated")


def run_tui(family: str | None) -> None:
    """Launch the Textual TUI, optionally starting at a family's model screen."""
    # Imported lazily so non-TUI subcommands (expose, sync, benchmark,
    # usage, migrate) don't pay the Textual import cost at CLI startup.
    from .app import ModelmanApp

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
        help="Path to wt's config.toml to import from (skipped if missing)",
    ),
) -> None:
    """One-time import of legacy config.yaml + families/*.yaml (and,
    optionally, wt's config.toml) into registry.toml +
    modelman.toml."""
    result = run_migration(
        default_config_path(),
        get_family_dir(),
        wt_config_path=Path(wt_config).expanduser(),
    )

    save_registry(result.registry)
    # Merge rather than overwrite: `migrate` is re-run as a repair step (see
    # wt/CLAUDE.md's "unknown provider" note), and result.state is a fresh
    # StateStore that's empty except for whatever this run's legacy
    # family-manifest import produced. Overwriting modelman.toml with it
    # outright would wipe [litellm] and every other model's ready/exposed
    # state on every repair re-run.
    with locked_state() as state:
        state.models.update(result.state.models)
        state.families.update(result.state.families)

    # One-time import of wt's legacy [gateway] block into modelman's
    # [litellm] table (routing policy only — never touches the proxy).
    if migrate_wt_gateway_to_litellm(wt_config_path=Path(wt_config).expanduser()):
        typer.echo("Imported wt's [gateway] into modelman.toml's [litellm] table.")

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
    # Always persist the registry, not just when providers_added is non-empty:
    # run_sync also calls backfill_provider_defaults, which mutates existing
    # provider entries in place (e.g. filling a missing auth.base_url) even
    # when no provider was added — gating the save on providers_added dropped
    # that repair on the floor.
    try:
        save_registry(registry)
    except OSError as exc:
        # The state sync already succeeded; report the registry repair
        # failure cleanly instead of a traceback. The repair is idempotent
        # and re-runs on the next sync.
        typer.echo(f"error: failed to save registry: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.providers_added:
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
        warnings = expose_model(registry, state, model_id, default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(f"Exposed {model_id} through LiteLLM.")
    for warning in warnings:
        typer.echo(f"warning: {warning}", err=True)


@app.command()
def unexpose(
    model_id: str = typer.Argument(..., help="Registry model id to stop exposing"),
) -> None:
    """Remove a model's LiteLLM model_list entry."""
    state = load_state()
    try:
        warnings = unexpose_model(state, model_id, default_litellm_config_path())
    except (ExposeError, LiteLLMConfigError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    save_state(state)
    typer.echo(f"Unexposed {model_id}.")
    for warning in warnings:
        typer.echo(f"warning: {warning}", err=True)


@app.command()
def refresh_prices() -> None:
    """Refresh per-token pricing for cloud models from OpenRouter."""
    from .pricing import refresh_prices as run_refresh

    registry = load_registry()
    result = run_refresh(registry)
    if result.error is not None:
        typer.echo(f"error: {result.error}", err=True)
        raise typer.Exit(1)
    try:
        save_registry(registry)
    except OSError as exc:
        typer.echo(f"error: failed to save registry: {exc}", err=True)
        raise typer.Exit(1) from exc
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)
    typer.echo(f"Refreshed prices for {result.updated} model(s).")


@app.command()
def start(
    model_id: str = typer.Argument(..., help="Registry model id to run locally (<provider>/<name>)"),
) -> None:
    """Stop any running local model and start model_id, recording it as
    the single local model wt's picker may offer. Idempotent if model_id
    is already running."""
    registry = load_registry()
    try:
        with locked_state() as state:
            result = start_local_model(registry, state, model_id)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.already_running:
        typer.echo(f"{model_id} is already running.")
    else:
        typer.echo(f"Started {model_id}.")


@app.command()
def stop() -> None:
    """Stop the currently-running local model and clear the marker.
    No-op when nothing is running."""
    try:
        with locked_state() as state:
            result = stop_local_model(state)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.stopped_model_id is None:
        typer.echo("No local model is running.")
    else:
        typer.echo(f"Stopped {result.stopped_model_id}.")


if __name__ == "__main__":
    app()
