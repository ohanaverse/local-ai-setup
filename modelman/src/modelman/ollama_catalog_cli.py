"""`modelman ollama-catalog` — sync ollama cloud entries with ollama.com/pricing.

Exit codes: 0 ok, 1 save/delete failure, 2 page fetch failed, 3 page
shape changed (raw HTML saved; fix ollama_catalog.parse_pricing).
"""

from __future__ import annotations

from pathlib import Path

import typer

from .ollama_catalog import (
    CatalogFetchError,
    CatalogParseError,
    DeleteCandidate,
    apply_sync,
    fetch_pricing_html,
    format_plan,
    list_ollama_tags,
    parse_pricing,
    plan_sync,
    remove_ollama_tag,
    save_failed_html,
)
from .queue import QueuedOps
from .registry import load_registry, locked_registry, model_entry_to_variant

ollama_catalog_app = typer.Typer(
    help="Sync ollama cloud models and prices from ollama.com/pricing.",
    no_args_is_help=True,
)


def _delete(candidate: DeleteCandidate) -> bool:
    """Delete one pulled cloud stub. Returns True on failure."""
    if candidate.model_id is None:
        try:
            remove_ollama_tag(candidate.tag)
        except RuntimeError as exc:
            typer.echo(f"error: {exc}", err=True)
            return True
        typer.echo(f"Removed {candidate.tag}.")
        return False
    from .main import run_queued_ops  # function-local: main imports this module

    entry = load_registry().model(candidate.model_id)
    return run_queued_ops(QueuedOps(deletes={entry.id: model_entry_to_variant(entry)}))


@ollama_catalog_app.command("sync")
def sync(
    dry_run: bool = typer.Option(False, "--dry-run", help="Print the plan; change nothing."),
    html: Path | None = typer.Option(  # noqa: B008
        None, "--html", help="Parse a saved pricing page instead of fetching it."
    ),
    yes: bool = typer.Option(
        False, "--yes", help="Apply registry changes without confirming (never deletes)."
    ),
    delete: list[str] | None = typer.Option(  # noqa: B008
        None,
        "--delete",
        help="Delete this pulled cloud stub without prompting (repeatable). "
        "When given, delete candidates not named here are kept.",
    ),
    no_deletes: bool = typer.Option(
        False, "--no-deletes", help="Keep every delete candidate without prompting."
    ),
) -> None:
    """Add/update ollama cloud entries (incl. off-peak prices) and offer to
    delete pulled cloud stubs that ollama.com/pricing no longer lists.

    Deletes are prompted per model (default no) unless --delete/--no-deletes
    decide them up front — the non-interactive path the ollama-catalog skill
    uses, since it cannot answer prompts."""
    if no_deletes and delete:
        typer.echo("error: --no-deletes and --delete are mutually exclusive", err=True)
        raise typer.Exit(2)
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

    tags = list_ollama_tags()
    if tags is None:
        typer.echo("warning: could not run `ollama list`; skipping the delete check", err=True)

    plan = plan_sync(load_registry(), catalog, tags)
    typer.echo(format_plan(plan))
    if dry_run:
        return

    failed = False
    if plan.has_registry_changes() and (
        yes or typer.confirm("Apply these registry changes?", default=True)
    ):
        try:
            with locked_registry() as fresh:
                fresh_plan = plan_sync(fresh, catalog, tags)
                apply_sync(fresh, fresh_plan)
        except OSError as exc:
            typer.echo(f"error: failed to save registry: {exc}", err=True)
            raise typer.Exit(1) from exc
        typer.echo(
            f"Updated {len(fresh_plan.updates)} and added {len(fresh_plan.additions)} model(s)."
        )

    if delete:
        candidate_tags = {c.tag for c in plan.delete_candidates}
        for tag in delete:
            if tag not in candidate_tags:
                typer.echo(f"warning: {tag} is not a delete candidate; left as-is", err=True)
    for candidate in plan.delete_candidates:
        if no_deletes:
            continue
        if delete:
            chosen = candidate.tag in delete
        else:
            prompt = f"{candidate.tag} is pulled but no longer on ollama.com/pricing. Delete it?"
            chosen = typer.confirm(prompt, default=False)
        if chosen:
            failed = _delete(candidate) or failed

    if failed:
        raise typer.Exit(1)
