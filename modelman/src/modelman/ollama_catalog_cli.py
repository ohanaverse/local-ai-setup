"""`modelman ollama-catalog` — mirror ollama.com/pricing into ollama + modelman.

Exit codes: 0 ok, 1 a pull/rm/registry save failed (other steps still ran),
2 page fetch failed or `ollama list` unreadable, 3 page shape changed (raw
HTML saved; fix ollama_catalog.parse_pricing), 4 the plan would remove more
than half the ollama cloud entries and --force was not given; nothing was
changed.
"""

from __future__ import annotations

from pathlib import Path

import typer

from .ollama_catalog import (
    CatalogFetchError,
    CatalogParseError,
    apply_sync,
    fetch_pricing_html,
    format_plan,
    list_ollama_tags,
    parse_pricing,
    plan_sync,
    remove_ollama_tag,
    resolve_cloud_tags,
    save_failed_html,
)
from .queue import QueuedOps
from .registry import load_registry, locked_registry, model_entry_to_variant
from .state import load_state

ollama_catalog_app = typer.Typer(
    help="Sync ollama cloud models and prices from ollama.com/pricing.",
    no_args_is_help=True,
)


@ollama_catalog_app.command("sync")
def sync(
    dry_run: bool = typer.Option(False, "--dry-run", help="Print the plan; change nothing."),
    html: Path | None = typer.Option(  # noqa: B008
        None, "--html", help="Parse a saved pricing page instead of fetching it."
    ),
    yes: bool = typer.Option(False, "--yes", help="Apply the plan without confirming."),
    force: bool = typer.Option(
        False, "--force", help="Apply even if more than half the cloud entries would be removed."
    ),
) -> None:
    """Make ollama's pulled cloud models, modelman's ollama cloud entries and
    LiteLLM's routes match ollama.com/pricing: update prices (incl.
    off-peak), add/remove registry entries, `ollama pull` what's missing,
    `ollama rm` what the page no longer lists, and (re)expose every page
    model so its route carries current prices. Local (non-cloud) models
    are never touched.

    One confirmation covers the whole plan; --yes skips it (the skill's
    non-interactive path — its Bash tool cannot answer prompts)."""
    if html is not None:
        try:
            text = html.read_text()
        except OSError as exc:
            typer.echo(f"error: cannot read {html}: {exc}", err=True)
            raise typer.Exit(2) from exc
    else:
        try:
            text = fetch_pricing_html()
        except CatalogFetchError as exc:
            typer.echo(f"error: could not fetch ollama.com/pricing: {exc}", err=True)
            raise typer.Exit(2) from exc

    try:
        catalog = parse_pricing(text)
    except CatalogParseError as exc:
        saved = save_failed_html(text)
        typer.echo(f"error: could not parse ollama.com/pricing: {exc.check}", err=True)
        typer.echo(f"raw HTML saved to {saved} — update ollama_catalog.parse_pricing", err=True)
        raise typer.Exit(3) from exc

    # Without it, what to pull and rm is unknowable: refuse rather than
    # mirror half the plan.
    tags = list_ollama_tags()
    if tags is None:
        typer.echo(
            "error: could not run `ollama list` (is the ollama daemon up?); nothing was changed",
            err=True,
        )
        raise typer.Exit(2)

    resolved, tag_warnings = resolve_cloud_tags([cm.name for cm in catalog.models])
    if catalog.models and all(t is None for t in resolved.values()):
        typer.echo(
            "error: could not resolve a cloud tag for any model on ollama.com/library; "
            "nothing was changed",
            err=True,
        )
        for w in tag_warnings:
            typer.echo(f"  {w}", err=True)
        raise typer.Exit(2)

    plan = plan_sync(load_registry(), catalog, tags, resolved)
    plan.warnings += tag_warnings
    exposed = {mid for mid, st in load_state().models.items() if st.exposed}
    typer.echo(format_plan(plan, exposed))
    if dry_run:
        return
    if plan.mass_removal() and not force:
        typer.echo(
            f"error: {len(plan.removals)} of {plan.cloud_entries} ollama cloud entries would be "
            "removed — check the page parsed correctly, then re-run with --force. "
            "Nothing was changed.",
            err=True,
        )
        raise typer.Exit(4)
    if not (yes or typer.confirm("Apply these changes?", default=True)):
        return

    # Re-plan against the registry as it is now (the TUI's price-refresh
    # worker may have written since the plan was printed), and act on that.
    try:
        with locked_registry() as fresh:
            fresh_plan = plan_sync(fresh, catalog, tags, resolved)
            apply_sync(fresh, fresh_plan)
            deletes = {mid: model_entry_to_variant(fresh.model(mid)) for mid in fresh_plan.removals}
    except OSError as exc:
        typer.echo(f"error: failed to save registry: {exc}", err=True)
        raise typer.Exit(1) from exc
    typer.echo(f"Updated {len(fresh_plan.updates)} and added {len(fresh_plan.additions)} model(s).")

    failed = False
    # Routes for every page model, not just new ones: an existing row keeps
    # the prices it was exposed with until wt rebuilds it. wt restarts the
    # proxy only when config.yaml changed; removals unexpose via queue.py.
    ops = QueuedOps(
        ready=dict.fromkeys(fresh_plan.pulls, True),
        deletes=deletes,
        exposes=dict.fromkeys(fresh_plan.routes, True),
    )
    if ops.ready or ops.deletes or ops.exposes:
        from .main import run_queued_ops  # function-local: main imports this module

        failed = run_queued_ops(ops)
    for tag in fresh_plan.stray_tags:
        try:
            remove_ollama_tag(tag)
        except RuntimeError as exc:
            typer.echo(f"error: {exc}", err=True)
            failed = True
            continue
        typer.echo(f"Removed {tag}.")

    if failed:
        raise typer.Exit(1)
