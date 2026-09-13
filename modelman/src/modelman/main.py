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
from .local_control import (
    DiscoveredModelNeedsFamily,
    LocalControlError,
    LocalModelInventory,
    inventory_local_models,
    start_local_model,
    stop_local_model,
)
from .manifest import get_family_dir
from .migrate import migrate as run_migration
from .migrate import migrate_wt_gateway_to_litellm
from .registry import load_registry, save_registry
from .state import load_state, locked_state
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
    # Merge what sync reconciled onto a freshly-loaded store instead of
    # overwriting modelman.toml wholesale from the snapshot taken before
    # the (potentially slow) provider scans — a whole-file save would
    # revert [local].running_model (or any other key) written concurrently
    # (same merge shape migrate uses).
    with locked_state() as fresh:
        fresh.models.update(state.models)
        fresh.families.update(state.families)
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
    # Merge only the one model row this command reconciled: expose_model can
    # spend up to the LiteLLM-restart timeout inside its config write, and a
    # whole-file overwrite from the pre-restart snapshot would revert
    # [local].running_model (or anything else) written concurrently.
    with locked_state() as fresh:
        if model_id in state.models:
            fresh.models[model_id] = state.models[model_id]
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
    # See expose above: merge only this command's model row.
    with locked_state() as fresh:
        if model_id in state.models:
            fresh.models[model_id] = state.models[model_id]
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


def _format_size(n: int | None) -> str:
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


def _echo_inventory_caveats(inventory: LocalModelInventory) -> None:
    """Name the providers the inventory could not ask.

    Without this, "not downloaded"/"nothing discovered" for an unreachable
    provider (a stopped ollama daemon, a registry.toml provider id modelman
    has no Provider class for) is indistinguishable from a confirmed-absent
    artifact — the user would be told to re-download models they already have.
    """
    for provider_id in inventory.unqueryable_providers:
        typer.echo(
            f"{provider_id}: could not be queried — entries above may be inaccurate",
            err=True,
        )


@app.command()
def start(
    model_id: str | None = typer.Argument(
        None,
        help="Registry model id, or a provider-native model name, to run locally. "
        "Omit to list local models.",
    ),
) -> None:
    """Stop any running local model and start model_id, recording it as
    the single local model wt's picker may offer. Idempotent when
    model_id's marker still matches a probe of the running process.

    model_id may be a registry id, an existing model's native
    provider-side name, or the native name of a model a provider has on
    disk but that has no registry.toml entry yet — the last case prompts
    for a family, then registers, exposes, and starts it in one step.

    Omit model_id to print a live inventory: models registered and on
    disk, models registered but missing their artifact, and on-disk
    models with no registry.toml entry yet.
    """
    registry = load_registry()
    if model_id is None:
        state = load_state()
        inventory = inventory_local_models(registry, state)
        if not (inventory.downloaded or inventory.not_downloaded or inventory.discovered):
            typer.echo("No local models found. `modelman start <name>` will register one it finds on disk.")
            _echo_inventory_caveats(inventory)
            return
        if inventory.downloaded:
            typer.echo("Registered, on disk:")
            for entry in inventory.downloaded:
                marker = "*" if entry.running else " "
                suffix = " (running)" if entry.running else ""
                typer.echo(f"{marker} {entry.model_id}\t{_format_size(entry.size_bytes)}{suffix}")
            typer.echo()
        if inventory.not_downloaded:
            typer.echo("Registered, not downloaded:")
            for model_id_str in inventory.not_downloaded:
                typer.echo(f"  {model_id_str}")
            typer.echo()
        if inventory.discovered:
            typer.echo("Discovered (not in registry.toml — `modelman start <name>` to add):")
            for disc in inventory.discovered:
                typer.echo(f"  {disc.provider_id}:{disc.variant_id}\t{_format_size(disc.size_bytes)}")
            typer.echo()
        _echo_inventory_caveats(inventory)
        typer.echo("Run `modelman start <model_id>` to start one.")
        return

    family: str | None = None
    while True:
        try:
            # start_local_model owns the marker read/write (short locked_state
            # transactions around it); the stop-all/warmup subprocesses must run
            # outside any state lock.
            result = start_local_model(registry, model_id, family=family)
            break
        except DiscoveredModelNeedsFamily as exc:
            hint = f" (existing: {', '.join(exc.suggested_families)})" if exc.suggested_families else ""
            typer.echo(f"{exc.provider_id}/{exc.variant_id} was found on disk but isn't registered yet.{hint}")
            # default="" stops click's own prompt() from silently re-looping
            # on blank input (its built-in retry never returns an empty
            # string when no default is set) so this loop can print its own
            # "cannot be empty" message and re-prompt.
            answer = typer.prompt("Family name for this model", default="", show_default=False)
            while not answer.strip():
                typer.echo("Family name cannot be empty.", err=True)
                answer = typer.prompt("Family name for this model", default="", show_default=False)
            family = answer.strip()
        except LocalControlError as exc:
            typer.echo(f"error: {exc}", err=True)
            raise typer.Exit(1) from exc

    if result.already_running:
        typer.echo(f"{result.model_id} is already running.")
    else:
        typer.echo(f"Started {result.model_id}.")
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)


@app.command()
def stop() -> None:
    """Stop the currently-running local model and clear the marker.
    No-op when nothing is running."""
    try:
        result = stop_local_model()
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.stopped_model_id is None:
        typer.echo("No local model is running.")
    else:
        typer.echo(f"Stopped {result.stopped_model_id}.")


if __name__ == "__main__":
    app()
