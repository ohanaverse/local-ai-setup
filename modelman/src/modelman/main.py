"""modelman CLI entry point."""

from __future__ import annotations

from pathlib import Path

import typer

# Import providers package to trigger registration of all providers.
from . import providers  # noqa: F401
from .benchmark.cli import benchmark_app
from .config import default_config_path
from .formatting import format_size
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
from .providers.registry import ProviderRegistry
from .queue import PendingChanges, QueuedOps
from .registry import (
    _default_registry_path,
    load_registry,
    model_entry_to_variant,
    provider_config,
    save_registry,
)
from .state import _default_state_path, load_state, locked_state
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


def print_event(tag: str) -> None:
    """Print one thin, human-readable line for a queue.py lifecycle tag
    (see queue.py's module docstring for the tag format). Replaces
    StatusScreen's RichLog rendering now that apply() runs after the TUI
    has exited, in a plain terminal."""
    parts = tag.split("|", 3)
    verb = parts[0]
    if verb == "delete:start":
        typer.echo(f"Deleting {parts[2]}...")
    elif verb == "delete:done":
        typer.echo(f"  done: deleted {parts[2]}")
    elif verb == "delete:fail":
        typer.echo(f"  FAILED: delete {parts[2]}: {parts[3]}")
    elif verb == "download:start":
        typer.echo(f"Downloading {parts[2]}...")
    elif verb == "download:done":
        suffix = f" ({parts[3]})" if len(parts) >= 4 and parts[3] else ""
        typer.echo(f"  done: downloaded {parts[2]}{suffix}")
    elif verb == "download:fail":
        typer.echo(f"  FAILED: download {parts[2]}: {parts[3]}")
    elif verb == "download:cancelled":
        typer.echo(f"  cancelled: {parts[2]}")
    elif verb == "ready:start":
        typer.echo(f"Marking {parts[2]} ready...")
    elif verb == "ready:done":
        typer.echo(f"  done: {parts[2]} ready")
    elif verb == "ready:fail":
        typer.echo(f"  FAILED: ready {parts[2]}: {parts[3]}")
    elif verb == "move:start":
        typer.echo(f"Moving {parts[2]} -> {parts[3]}...")
    elif verb == "move:done":
        typer.echo(f"  done: moved {parts[2]} -> {parts[3]}")
    elif verb == "move:fail":
        typer.echo(f"  FAILED: move {parts[2]}: {parts[3]}")
    elif verb in ("expose:start", "unexpose:start"):
        action = "Exposing" if verb == "expose:start" else "Unexposing"
        typer.echo(f"{action} {parts[2]}...")
    elif verb in ("expose:done", "unexpose:done"):
        action = "exposed" if verb == "expose:done" else "unexposed"
        typer.echo(f"  done: {action} {parts[2]}")
    elif verb in ("expose:fail", "unexpose:fail"):
        action = verb.split(":")[0]
        typer.echo(f"  FAILED: {action} {parts[2]}: {parts[3]}")
    elif verb == "expose:warning":
        typer.echo(f"  warning: {parts[1]}")
    elif verb == "save:done":
        typer.echo("Saved.")
    elif verb == "save:fail":
        typer.echo(f"  FAILED: save: {parts[1]}")
    # apply:done / apply:cancelled / save:start: no line — run_queued_ops
    # prints its own summary once apply() returns.


def print_error_summary(failures: list[str], total: int) -> bool:
    """Print the "Completed with errors" block when `failures` is
    non-empty. Returns True iff there were failures, so the caller can
    decide the process exit code. Clean runs print nothing."""
    if not failures:
        return False
    typer.echo("Completed with errors:")
    for failure in failures:
        typer.echo(f"  - {failure}")
    typer.echo(f"{len(failures)} of {total} operations failed.")
    return True


def run_queued_ops(queued: QueuedOps) -> bool:
    """Apply a QueuedOps returned by the TUI against fresh on-disk state.

    Builds a PendingChanges the same way ModelScreen._run_apply used to,
    but against a Registry/StateStore just loaded from disk: adds/edits
    already persisted immediately while the TUI was open, while queued
    deletes/moves/ready/exposes have not. Returns True iff the run
    should exit non-zero (failures, or a Ctrl+C cancellation).
    """
    registry = load_registry()
    state = load_state()
    ready_specs = {}
    missing_ready = []
    for mid in queued.ready:
        try:
            ready_specs[mid] = model_entry_to_variant(registry.model(mid))
        except KeyError:
            missing_ready.append(mid)

    provider_instances: dict[str, object] = {}
    for spec in list(ready_specs.values()) + list(queued.deletes.values()):
        try:
            entry = registry.provider(spec["provider"])
            provider_instances[spec["provider"]] = ProviderRegistry.get(spec["provider"], provider_config(entry))
        except KeyError:
            # Not in the registry or not mapped to a Provider class:
            # PendingChanges treats this as flag-only (native/unmapped).
            continue

    pending = PendingChanges(
        registry=registry,
        state=state,
        registry_path=_default_registry_path(),
        state_path=_default_state_path(),
        providers=provider_instances,
        ready=[
            (mid, ready_specs[mid], target)
            for mid, target in queued.ready.items()
            if mid in ready_specs
        ],
        deletes=list(queued.deletes.items()),
        moves=list(queued.moves.items()),
        exposes=list(queued.exposes.items()),
        litellm_path=default_litellm_config_path(),
    )
    total = (
        len(pending.ready)
        + len(missing_ready)
        + len(pending.deletes)
        + len(pending.moves)
        + len(pending.exposes)
    )
    completed = 0
    done_verbs = {
        "delete:done",
        "download:done",
        "ready:done",
        "move:done",
        "expose:done",
        "unexpose:done",
    }

    def on_event(tag: str) -> None:
        nonlocal completed
        print_event(tag)
        if tag.split("|", 1)[0] in done_verbs:
            completed += 1

    # A model id queued while the TUI was open but missing from a
    # freshly-loaded registry (deleted out-of-band, or a hand-edited
    # registry.toml) must not take down the whole run — every other op
    # (deletes/moves/exposes) already degrades to a per-item failure on
    # a missing id with a live event, via queue.py's own emit() calls;
    # ready needs the same treatment, done here since its lookup happens
    # before PendingChanges even exists.
    for mid in missing_ready:
        pending.failures.append(f"ready {mid}: Unknown model: {mid}")
        on_event(f"ready:fail|{mid}|{mid}|Unknown model")

    try:
        pending.apply(on_event=on_event, on_progress=typer.echo)
    except KeyboardInterrupt:
        pending.cancel()
        typer.echo(
            f"\nCancelled: {completed} steps completed, {total - completed} remaining skipped."
        )
        return True

    return print_error_summary(pending.failures, total)


def run_tui() -> None:
    """Launch the Textual TUI. If the user queues changes and exits via
    Apply, run them against fresh on-disk state now that the TUI has
    closed — provider progress and lifecycle events print straight to
    stdout instead of a StatusScreen."""
    # Imported lazily so non-TUI subcommands (expose, sync, benchmark,
    # usage, migrate) don't pay the Textual import cost at CLI startup.
    from .app import ModelmanApp

    queued = ModelmanApp().run()
    if queued is None:
        return
    if run_queued_ops(queued):
        raise typer.Exit(1)


@app.callback(invoke_without_command=True)
def _main(ctx: typer.Context) -> None:
    """Run `modelman` with no args to open the TUI."""
    if ctx.invoked_subcommand is None:
        run_tui()


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


@app.command("delete-family")
def delete_family(
    name: str = typer.Argument(..., help="Family name to delete"),
) -> None:
    """Remove an empty family's lingering [[families]] registry entry.

    queue.py's apply() deliberately leaves a family entry behind once its
    last model is deleted or moved out ("stickiness" — see queue.py), and
    nothing removes it automatically. This is the only remaining way to
    clear one now that FamilyScreen (the old family-list screen, which
    used to offer family deletion) is gone. Refuses if the family still
    has models — move or delete them first.
    """
    registry = load_registry()
    models = registry.models_by_family(name)
    if models:
        typer.echo(
            f"error: family '{name}' has {len(models)} model(s); "
            "move or delete them before deleting the family",
            err=True,
        )
        raise typer.Exit(1)
    entry = registry.family(name)
    state = load_state()
    had_legacy = name in state.families
    if entry is None and not had_legacy:
        typer.echo(f"error: no family entry named '{name}'", err=True)
        raise typer.Exit(1)
    if entry is not None:
        registry.families.remove(entry)
        try:
            save_registry(registry)
        except OSError as exc:
            typer.echo(f"error: failed to save registry: {exc}", err=True)
            raise typer.Exit(1) from exc
    if had_legacy:
        # Merge-only write (see expose/unexpose above): don't overwrite
        # modelman.toml wholesale from a snapshot that may be stale by now.
        with locked_state() as fresh:
            fresh.forget_family(name)
    typer.echo(f"Deleted family '{name}'.")


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
                typer.echo(f"{marker} {entry.model_id}\t{format_size(entry.size_bytes)}{suffix}")
            typer.echo()
        if inventory.not_downloaded:
            typer.echo("Registered, not downloaded:")
            for model_id_str in inventory.not_downloaded:
                typer.echo(f"  {model_id_str}")
            typer.echo()
        if inventory.discovered:
            typer.echo("Discovered (not in registry.toml — `modelman start <name>` to add):")
            for disc in inventory.discovered:
                typer.echo(f"  {disc.provider_id}:{disc.variant_id}\t{format_size(disc.size_bytes)}")
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
