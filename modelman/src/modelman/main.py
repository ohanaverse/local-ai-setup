"""modelman CLI entry point."""

from __future__ import annotations

import hashlib
from dataclasses import replace
from pathlib import Path

import typer

# Import providers package to trigger registration of all providers.
from . import (
    providers,  # noqa: F401
    wt_bridge,
)
from .benchmark.cli import benchmark_app
from .config import default_config_path
from .formatting import format_size
from .litellm import sync_routes
from .local_control import (
    LocalControlError,
    LocalModelInventory,
    inventory_local_models,
    start_local_model,
    stop_all_local_models,
    stop_local_model,
)
from .manifest import get_family_dir
from .migrate import migrate as run_migration
from .migrate import migrate_wt_gateway_to_litellm
from .ollama_catalog_cli import ollama_catalog_app
from .providers.base import VariantSpec
from .providers.lifecycle.cli import provider_app
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
from .sync import SyncError, _ensure_provider_entries
from .sync import sync as run_sync
from .usage.cli import usage_app

app = typer.Typer(help="Manage local LLM model families across providers.")
app.add_typer(benchmark_app, name="benchmark")
app.add_typer(usage_app, name="usage")
app.add_typer(ollama_catalog_app, name="ollama-catalog")
app.add_typer(provider_app, name="provider")

litellm_app = typer.Typer(
    help="Control whether wt routes agents through LiteLLM or dials providers directly. "
    "Never starts or stops the proxy service."
)
app.add_typer(litellm_app, name="litellm")


def _warn_if_litellm_incomplete(consequence: str) -> None:
    """Emit the shared 'url/api_key not set' warning if wt reports either missing.

    `on` and `off` differ only in how they describe the consequence. The
    status read is best-effort: a failure here never masks a successful change.
    """
    try:
        status = wt_bridge.litellm_status()
    except wt_bridge.WtBridgeError:
        return
    if not status.url or not status.api_key_set:
        typer.echo(
            f"warning: litellm.url or litellm.api_key is not set — "
            f"{consequence}; run 'modelman litellm set --url ... --api-key ...'",
            err=True,
        )


def _wt_call(fn, *args):
    """Run a wt_bridge call; a bridge failure becomes `error: ...` + exit 1
    (never a traceback; bridge messages never contain the api key)."""
    try:
        return fn(*args)
    except wt_bridge.WtBridgeError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from None


@litellm_app.command("status")
def litellm_status():
    """Show wt's LiteLLM routing state. Does not touch the proxy."""
    typer.echo(_wt_call(wt_bridge.litellm_status_text), nl=False)


@litellm_app.command("on")
def litellm_on():
    """Route non-native models through LiteLLM. Does not start the proxy."""
    _wt_call(wt_bridge.litellm_set_enabled, True)
    typer.echo("litellm: on")
    _warn_if_litellm_incomplete("wt will fail at launch time")


@litellm_app.command("off")
def litellm_off():
    """Dial providers directly where possible. Does not stop the proxy."""
    _wt_call(wt_bridge.litellm_set_enabled, False)
    typer.echo("litellm: off")
    _warn_if_litellm_incomplete(
        "agent/model pairs with no direct protocol overlap (e.g. claude+openrouter) "
        "still route through litellm regardless of this setting and will fail to launch"
    )


@litellm_app.command("set")
def litellm_set(
    url: str = typer.Option(None, help="LiteLLM proxy base URL"),
    api_key: str = typer.Option(None, "--api-key", help="LiteLLM proxy API key"),
):
    """Set the proxy URL/key wt will use when routing through LiteLLM."""
    _wt_call(wt_bridge.litellm_set, url, api_key)
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
    elif verb == "save:done":
        typer.echo("Saved.")
    elif verb == "save:fail":
        typer.echo(f"  FAILED: save: {parts[1]}")
    elif verb in ("apply:done", "apply:cancelled", "save:start"):
        pass  # run_queued_ops prints its own summary once apply() returns.
    else:
        typer.echo(f"  (unhandled event: {tag})", err=True)


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
    deletes/moves/ready have not. Returns True iff the run
    should exit non-zero (failures, or a Ctrl+C cancellation).
    """
    registry = load_registry()
    state = load_state()
    models_by_id = {m.id: m for m in registry.models}
    ready_specs = {}
    missing_ready = []
    for mid in queued.ready:
        model_entry = models_by_id.get(mid)
        if model_entry is None:
            missing_ready.append(mid)
        else:
            ready_specs[mid] = model_entry_to_variant(model_entry)

    provider_instances: dict[str, object] = {}
    unavailable_providers: dict[str, str] = {}
    provider_ids = {
        spec["provider"] for spec in list(ready_specs.values()) + list(queued.deletes.values())
    }
    for provider_id in provider_ids:
        try:
            entry = registry.provider(provider_id)
        except KeyError:
            # Not in the registry at all: PendingChanges treats this as
            # flag-only (native/unmapped).
            continue
        if ProviderRegistry.get_class(provider_id) is None:
            # In the registry, but no Provider class registered for it
            # (e.g. openrouter): also flag-only, same as above.
            continue
        try:
            provider_instances[provider_id] = ProviderRegistry.get(
                provider_id, provider_config(entry)
            )
        except Exception as exc:  # noqa: BLE001
            # A provider WITH a registered class that fails to instantiate
            # with a real error (bad config, a broken constructor) must not
            # crash the whole run, and must not be silently treated as
            # flag-only either — that would flip ready=True without ever
            # downloading anything. A delete against this provider still
            # falls through to the flag-only path in queue.py (provider is
            # None there too) and cleans up the registry/state rows without
            # removing the on-disk artifact — the same degraded-but-safe
            # behavior a genuinely flag-only provider already gets, not an
            # oversight.
            unavailable_providers[provider_id] = str(exc)

    provider_ready_failures: list[tuple[str, str]] = []
    ready_items: list[tuple[str, VariantSpec, bool]] = []
    for mid, target in queued.ready.items():
        spec = ready_specs.get(mid)
        if spec is None:
            continue  # already recorded in missing_ready
        reason = unavailable_providers.get(spec["provider"])
        if reason is not None:
            provider_ready_failures.append((mid, reason))
        else:
            ready_items.append((mid, spec, target))

    pending = PendingChanges(
        registry=registry,
        state=state,
        registry_path=_default_registry_path(),
        state_path=_default_state_path(),
        providers=provider_instances,
        ready=ready_items,
        deletes=list(queued.deletes.items()),
        moves=list(queued.moves.items()),
    )
    total = (
        len(pending.ready)
        + len(missing_ready)
        + len(provider_ready_failures)
        + len(pending.deletes)
        + len(pending.moves)
    )
    completed = 0
    done_verbs = {
        "delete:done",
        "download:done",
        "ready:done",
        "move:done",
    }

    def on_event(tag: str) -> None:
        nonlocal completed
        verb = tag.split("|")[0]
        print_event(tag)
        if verb in done_verbs:
            completed += 1

    # A model id queued while the TUI was open but missing from a
    # freshly-loaded registry (deleted out-of-band, or a hand-edited
    # registry.toml) must not take down the whole run — every other op
    # (deletes/moves) already degrades to a per-item failure on
    # a missing id with a live event, via queue.py's own emit() calls;
    # ready needs the same treatment, done here since its lookup happens
    # before PendingChanges even exists.
    for mid in missing_ready:
        pending.failures.append(f"ready {mid}: Unknown model: {mid}")
        on_event(f"ready:fail|{mid}|{mid}|Unknown model")
    for mid, reason in provider_ready_failures:
        pending.failures.append(f"ready {mid}: provider unavailable: {reason}")
        on_event(f"ready:fail|{mid}|{mid}|provider unavailable: {reason}")

    try:
        pending.apply(on_event=on_event, on_progress=typer.echo)
    except KeyboardInterrupt:
        pending.cancel()
        typer.echo(
            f"\nCancelled: {completed} steps completed, {total - completed} remaining skipped."
        )
        return True
    except Exception as exc:  # noqa: BLE001
        # apply() normally captures per-step failures itself; an exception
        # reaching all the way out here is a genuine bug, not a per-item
        # failure — its own exception safety net (_persist) has already
        # saved whatever completed before this point. Report it like any
        # other failure instead of crashing with a raw traceback (mirrors
        # the deleted StatusScreen's equivalent except Exception).
        pending.failures.append(f"unexpected error: {exc}")
        _sync_routes_and_warn()
        return print_error_summary(pending.failures, total)

    _sync_routes_and_warn()
    return print_error_summary(pending.failures, total)


def _sync_routes_and_warn() -> None:
    """Bring LiteLLM's routes in line with what is now on disk (#179).

    wt reads registry.toml and the live providers itself, so every caller
    runs this once, after its writes are saved: run_queued_ops (on success
    and after an unexpected exception, since its safety net persisted the
    completed work — but not after a Ctrl+C, a request to stop; the next
    sync converges), a TUI exit that changed registry.toml, migrate, sync
    and refresh-prices. Warnings go to stderr; it never fails the command.
    """
    for warning in sync_routes():
        typer.echo(f"warning: {warning}", err=True)


def run_tui() -> None:
    """Launch the Textual TUI. If the user queues changes and exits via
    Apply, run them against fresh on-disk state now that the TUI has
    closed — provider progress and lifecycle events print straight to
    stdout instead of a StatusScreen."""
    # Imported lazily so non-TUI subcommands (sync, benchmark,
    # usage, migrate) don't pay the Textual import cost at CLI startup.
    from .app import ModelmanApp

    before = _file_digest(_default_registry_path())
    app = ModelmanApp()
    queued = app.run()
    if queued is None:
        # Add/edit write registry.toml immediately and queue nothing; route
        # what they changed (#179). run_queued_ops syncs on its own. A Discard
        # that put back a registry the session had written leaves the file as
        # it was and says so instead (ModelmanApp.route_sync_owed, #194).
        if _file_digest(_default_registry_path()) != before or getattr(
            app, "route_sync_owed", False
        ):
            _sync_routes_and_warn()
        return
    if run_queued_ops(queued):
        raise typer.Exit(1)


def _file_digest(path: Path) -> bytes | None:
    try:
        return hashlib.sha256(path.read_bytes()).digest()
    except OSError:
        return None


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

    # A fresh machine has nothing to import, and the registry would be written
    # with no providers at all; give the installed local providers their rows
    # (the same repair `modelman sync` makes).
    providers_added = _ensure_provider_entries(result.registry)
    save_registry(result.registry)
    # Merge rather than overwrite: `migrate` is re-run as a repair step (see
    # wt/CLAUDE.md's "unknown provider" note), and result.state is a fresh
    # StateStore that's empty except for whatever this run's legacy
    # family-manifest import produced. Overwriting modelman.toml with it
    # outright would wipe [litellm] and every other model's ready/running
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
    if providers_added:
        typer.echo(f"Added provider entries: {', '.join(providers_added)}")
    # registry.toml was rewritten; route what it now configures (#179).
    _sync_routes_and_warn()


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
    #
    # And merge only what sync observed, only for the models it reconciled:
    # ready, disk_path and size_bytes. `state` is as old as the scans, so
    # writing its rows back whole undid whatever happened meanwhile — a model
    # stopped during the sync was recorded running again, one started was
    # recorded stopped, and a discovered model's row (dropped by its stop)
    # came back (#231).
    #
    # Families are left alone: run_sync never touches them, so its snapshot
    # holds nothing to write — and merging `state.families` back whole would
    # resurrect a legacy family row a concurrent `delete-family` just dropped,
    # the same stale-snapshot hazard the model merge above exists to avoid
    # (#231). `locked_state` already round-trips whatever families are on disk.
    try:
        with locked_state() as fresh:
            for mid in (*result.downloaded, *result.not_downloaded):
                seen = state.get(mid)
                fresh.models[mid] = replace(
                    fresh.get(mid),
                    ready=seen.ready,
                    disk_path=seen.disk_path,
                    size_bytes=seen.size_bytes,
                )
    except OSError as exc:
        # No registry repair is saved either — the state write must land first,
        # and the repair is idempotent and re-runs on the next sync.
        typer.echo(f"error: failed to save state: {exc}", err=True)
        raise typer.Exit(1) from exc
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
    # The registry save above may have repaired a provider (backfilled
    # base_url, added entry); route what it now configures (#179).
    _sync_routes_and_warn()


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
        # Merge-only write: don't overwrite modelman.toml wholesale from a
        # snapshot that may be stale by now.
        with locked_state() as fresh:
            fresh.forget_family(name)
    typer.echo(f"Deleted family '{name}'.")


@app.command()
def refresh_prices() -> None:
    """Refresh per-token pricing for OpenRouter-priced models from OpenRouter."""
    from .pricing import refresh_prices as run_refresh
    from .state import stamp_price_refresh_today

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
    # Stamp the refresh date like the TUI's background refresh does (both
    # call state.stamp_price_refresh_today): wt's stale-pricing notice reads
    # it, and without the stamp it told the user to run this very command
    # forever (#151). But only when the refresh actually updated something —
    # the TUI's rule (app.py): a zero-update refresh is not a success worth
    # gating on, and stamping it (no OpenRouter-priced candidates, or no
    # candidate matched) would suppress a same-day retry and the pricing of
    # a model added later the same day.
    if result.updated > 0:
        stamp_price_refresh_today()
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)
    typer.echo(f"Refreshed prices for {result.updated} model(s).")
    # wt writes a route's prices from the registry, so new prices reach
    # LiteLLM only through a sync (#179).
    _sync_routes_and_warn()


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
    """Start model_id as a running local model, alongside any already
    running. Only a model on the same single-model provider (omlx, mtplx,
    mlx_lm_server) is stopped to make room. Idempotent when model_id is
    already running and a probe confirms it.

    model_id may be a registry id, an existing model's native
    provider-side name, or the native name of a model a provider has on
    disk with no registry.toml entry — that one starts without being
    registered (#179 Phase B), and wt routes it under its discovered id
    `<family>/<name>` (the id the no-arg listing prints).

    Omit model_id to print a live inventory: models registered and on
    disk, models registered but missing their artifact, and on-disk
    models with no registry.toml entry yet.
    """
    registry = load_registry()
    if model_id is None:
        state = load_state()
        inventory = inventory_local_models(registry, state)
        if not (inventory.downloaded or inventory.not_downloaded or inventory.discovered):
            typer.echo(
                "No local models found. `modelman start <name>` starts one it finds on disk."
            )
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
            typer.echo("Discovered (not in registry.toml — `modelman start <id>` runs one):")
            for disc in inventory.discovered:
                # The discovered id, not `<provider row>:<name>`: it is what
                # `modelman start` and `modelman stop` take.
                marker = "*" if disc.running else " "
                suffix = " (running)" if disc.running else ""
                typer.echo(f"{marker} {disc.model_id}\t{format_size(disc.size_bytes)}{suffix}")
            typer.echo()
        _echo_inventory_caveats(inventory)
        typer.echo("Run `modelman start <model_id>` to start one.")
        return

    try:
        # start_local_model owns the marker read/write (short locked_state
        # transactions around it); the stop-all/warmup subprocesses must run
        # outside any state lock.
        result = start_local_model(registry, model_id)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    if result.already_running:
        typer.echo(f"{result.model_id} is already running.")
    else:
        typer.echo(f"Started {result.model_id}.")
    if result.other_running:
        typer.echo(
            f"warning: {len(result.other_running)} other local model(s) already running: "
            f"{', '.join(result.other_running)}",
            err=True,
        )
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)


@app.command()
def stop(
    model_id: str | None = typer.Argument(
        None,
        help="Id of the running model to stop: a registry id, or the discovered id "
        "`<family>/<name>` of a model started without a registry entry.",
    ),
    all_: bool = typer.Option(False, "--all", help="Stop every currently-running local model."),
) -> None:
    """Stop one running local model (by id), or every one of them with
    --all. Bare `modelman stop` with neither is a usage error."""
    if model_id is None and not all_:
        typer.echo("error: pass a model id, or --all to stop every running local model", err=True)
        raise typer.Exit(1)
    if model_id is not None and all_:
        typer.echo("error: pass either a model id or --all, not both", err=True)
        raise typer.Exit(1)

    if all_:
        try:
            stop_all_result = stop_all_local_models()
        except LocalControlError as exc:
            typer.echo(f"error: {exc}", err=True)
            raise typer.Exit(1) from exc
        if not stop_all_result.stopped:
            typer.echo("No local model is running.")
        else:
            typer.echo(
                f"Stopped {len(stop_all_result.stopped)} model(s): "
                f"{', '.join(stop_all_result.stopped)}."
            )
        for warning in stop_all_result.warnings:
            typer.echo(f"warning: {warning}", err=True)
        return

    # Reached only when model_id is not None: the two checks above rule out
    # both (None, not all_) and (not None, all_), and the `all_` branch just
    # returned — so the sole remaining case is (not None, not all_). Spelled
    # out for mypy, which can't infer that from the two independent ifs.
    assert model_id is not None
    try:
        result = stop_local_model(model_id)
    except LocalControlError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    if result.stopped_model_id is None:
        typer.echo(f"{model_id} is not running.")
    else:
        typer.echo(f"Stopped {result.stopped_model_id}.")
    for warning in result.warnings:
        typer.echo(f"warning: {warning}", err=True)


if __name__ == "__main__":
    app()
