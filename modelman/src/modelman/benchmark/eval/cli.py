"""CLI for `modelman benchmark eval`."""

from __future__ import annotations

from pathlib import Path

import typer

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.eval.category import list_categories, load_category
from modelman.benchmark.eval.runner import (
    DEFAULT_RESULTS_DIR,
    RunSavedButRestoreFailed,
    rejudge_run,
    run_suite,
)
from modelman.benchmark.eval.suite import load_suite
from modelman.registry import load_registry
from modelman.state import load_state, save_state

eval_app = typer.Typer(
    help="Cross-category capability benchmark (reasoning/planning/coding/"
    "code_review/doc_summary), single-turn, coding graded by EvalPlus."
)

DEFAULT_CATEGORIES_ROOT = Path("benchmarks/tasks/eval")
DEFAULT_SUITES_DIR = Path("benchmarks/suites")


@eval_app.command("list-categories")
def list_categories_cmd(
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
) -> None:
    for category in list_categories(root):
        count = len(category.items) if category.rubric is not None else 0
        typer.echo(f"{category.name}  (items: {count})")


@eval_app.command("list-items")
def list_items_cmd(
    category: str = typer.Option(..., "--category"),
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
) -> None:
    try:
        loaded = load_category(root / category)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    for item in loaded.items:
        typer.echo(item.id)


@eval_app.command("run")
def run_cmd(
    suite: Path = typer.Option(..., "--suite"),  # noqa: B008
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
    category: list[str] = typer.Option([], "--category"),  # noqa: B008
    row: list[str] = typer.Option([], "--row"),  # noqa: B008
    results_dir: Path | None = typer.Option(None, "--results-dir"),  # noqa: B008
    dry_run: bool = typer.Option(False, "--dry-run"),
) -> None:
    registry = load_registry()
    try:
        loaded_suite = load_suite(suite, registry)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    all_categories = list_categories(root)
    categories = all_categories
    if category:
        wanted = set(category)
        categories = [c for c in categories if c.name in wanted]

    # A row's `categories =` must name real categories (validated against
    # the FULL set under --root, not the --category-filtered set: scoping
    # with --category coding must not reject a row that also legitimately
    # names reasoning): a typo would otherwise filter every category out
    # silently and the row would "run" with zero results.
    known_names = {c.name for c in all_categories}
    for r in loaded_suite.rows:
        if r.categories is not None:
            unknown = [c for c in r.categories if c not in known_names]
            if unknown:
                typer.echo(
                    f"error: row {r.label!r} names unknown category(ies): "
                    f"{', '.join(unknown)} (known: {', '.join(sorted(known_names))})",
                    err=True,
                )
                raise typer.Exit(1)

    # Same check for the CLI's own --category values: a typo there (or a
    # typo'd --root, which makes list_categories return []) would otherwise
    # produce a zero-category "run" that still isolates providers, writes
    # an empty summary, and repoints eval_last_run at it — the exact
    # silent-empty failure mode the row-level check above exists to prevent.
    if loaded_suite.rows and not all_categories:
        typer.echo(f"error: no categories found under {root} (check --root)", err=True)
        raise typer.Exit(1)
    unknown_cli = [c for c in category if c not in known_names]
    if unknown_cli:
        typer.echo(
            f"error: unknown category(ies): {', '.join(unknown_cli)} "
            f"(known: {', '.join(sorted(known_names))})",
            err=True,
        )
        raise typer.Exit(1)

    rows = loaded_suite.rows
    if row:
        wanted_rows = set(row)
        rows = [
            r
            for i, r in enumerate(rows, start=1)
            if r.label in wanted_rows or str(i) in wanted_rows
        ]

    if dry_run:
        # Print each selected row with its FULL-suite index — --row N
        # selects by full-suite position (run_suite matches the same way),
        # so renumbering the filtered list here would show "01" for a row
        # the user selected as 3 and defeat the dry run's purpose. Rows
        # whose own `categories` is disjoint from the --category selection
        # print as skipped — the real run skips them the same way (a
        # notice to stderr, never a silent zero-category execution).
        for i, r in enumerate(loaded_suite.rows, start=1):
            if not row or r.label in set(row) or str(i) in set(row):
                row_categories = r.categories or [c.name for c in categories]
                overlap = [c for c in row_categories if c in {cat.name for cat in categories}]
                note = "" if overlap else "  (skipped: no category overlap with selection)"
                typer.echo(
                    f"{i:02d}  {r.label}  model={r.model_id}  route={r.route}  "
                    f"categories={row_categories}{note}"
                )
        skipped = [
            r
            for r in rows
            if not (
                set(r.categories or [c.name for c in categories]) & {cat.name for cat in categories}
            )
        ]
        typer.echo(
            f"{len(rows) - len(skipped)} of {len(loaded_suite.rows)} row(s), "
            f"{len(categories)} categorie(s) resolved, dry run — nothing executed"
        )
        if skipped:
            typer.echo(
                f"note: {len(skipped)} row(s) would be skipped: "
                + ", ".join(r.label for r in skipped)
            )
        return

    try:
        run_dir, results = run_suite(
            loaded_suite, registry, categories, row_filter=row or None, results_dir=results_dir
        )
    except RunSavedButRestoreFailed as exc:
        _record_run(exc.run_dir)
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from None
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    _record_run(run_dir)
    ok = sum(1 for r in results if r.error is None)
    typer.echo(
        f"Eval benchmark complete: {len(results)} row(s), {ok} ran without an isolation error"
    )
    typer.echo(f"Results: {run_dir}")


def _record_run(run_dir: Path) -> None:
    state = load_state()
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["eval_last_run"] = str(run_dir)
    save_state(state)


@eval_app.command("show")
def show_cmd(
    latest: bool = typer.Option(False, "--latest"),
    run_id: str | None = typer.Option(None, "--run-id"),  # noqa: B008
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("eval_last_run")
        if not run_dir_str:
            typer.echo("error: no latest eval run recorded", err=True)
            raise typer.Exit(1)
        md_path = Path(run_dir_str) / "summary.md"
    else:
        md_path = results_dir / str(run_id) / "summary.md"
    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)
    typer.echo(md_path.read_text(encoding="utf-8"))


@eval_app.command("judge")
def judge_cmd(
    latest: bool = typer.Option(False, "--latest"),
    run_id: str | None = typer.Option(None, "--run-id"),  # noqa: B008
    root: Path = typer.Option(DEFAULT_CATEGORIES_ROOT, "--root"),  # noqa: B008
    row: list[str] = typer.Option([], "--row"),  # noqa: B008
    samples: int | None = typer.Option(None, "--samples"),
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("eval_last_run")
        if not run_dir_str:
            typer.echo("error: no latest eval run recorded", err=True)
            raise typer.Exit(1)
        target_dir = Path(run_dir_str)
    else:
        target_dir = results_dir / str(run_id)

    categories = list_categories(root)
    registry = load_registry()
    try:
        outcomes = rejudge_run(
            target_dir,
            categories,
            row_filter=row or None,
            samples_override=samples,
            registry=registry,
        )
    except (BenchmarkError, FileNotFoundError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    for outcome in outcomes:
        typer.echo(
            f"{outcome['row']}/{outcome['category']}/{outcome['item']}: total={outcome['total']}"
        )


__all__ = ["eval_app"]
