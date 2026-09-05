"""CLI for `modelman benchmark agent`."""

from __future__ import annotations

from pathlib import Path

import typer

from modelman.benchmark.agent.runner import run_suite
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.agent.task import list_task_bundles
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import load_registry
from modelman.state import load_state, save_state

agent_app = typer.Typer(help="Agentic coding benchmark harness (real coding task, gates + judge).")

DEFAULT_TASKS_DIR = Path("benchmarks/tasks")
DEFAULT_SUITES_DIR = Path("benchmarks/suites")


@agent_app.command("list-tasks")
def list_tasks_cmd(
    root: Path = typer.Option(DEFAULT_TASKS_DIR, "--root", help="Directory containing task bundles"),  # noqa: B008
) -> None:
    """List task bundles under root."""
    for task in list_task_bundles(root):
        hidden_count = len(task.gates_config.get("hidden", {}).get("files", []))
        typer.echo(f"{task.task_id}  (hidden files: {hidden_count})")


@agent_app.command("list-suites")
def list_suites_cmd(
    root: Path = typer.Option(DEFAULT_SUITES_DIR, "--root", help="Directory containing suite TOML files"),  # noqa: B008
) -> None:
    """List suite files under root, with their expanded row count."""
    if not root.is_dir():
        return
    registry = load_registry()
    for path in sorted(root.glob("*.toml")):
        try:
            suite = load_suite(path, registry)
        except BenchmarkError as exc:
            typer.echo(f"{path.name}: error: {exc}", err=True)
            continue
        typer.echo(f"{path.name}  ({suite.name}, {len(suite.rows)} rows)")


@agent_app.command("run")
def run_cmd(
    suite: Path = typer.Option(..., "--suite", help="Path to a suite TOML file"),  # noqa: B008
    row: list[str] = typer.Option([], "--row", help="Row label or index to run (repeatable)"),  # noqa: B008
    results_dir: Path | None = typer.Option(  # noqa: B008
        None, "--results-dir", help="Directory for run artifacts"
    ),
    dry_run: bool = typer.Option(False, "--dry-run", help="Resolve and print the row matrix; run nothing"),
) -> None:
    """Run a suite against a real coding task."""
    registry = load_registry()
    try:
        loaded_suite = load_suite(suite, registry)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    rows = loaded_suite.rows
    if row:
        wanted = set(row)
        rows = [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]

    if dry_run:
        for i, r in enumerate(rows, start=1):
            typer.echo(
                f"{i:02d}  {r.label}  model={r.model_id}  thinking={r.thinking}  "
                f"route={r.route}  provider={r.provider_id}"
            )
        typer.echo(f"{len(rows)} row(s) resolved, dry run — nothing executed")
        return

    try:
        run_dir, results = run_suite(loaded_suite, registry, row_filter=row or None, results_dir=results_dir)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    state = load_state()
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["agent_last_run"] = str(run_dir)
    save_state(state)

    ok = sum(1 for r in results if r.error is None)
    typer.echo(f"Agent benchmark complete: {len(results)} row(s), {ok} ran without an isolation error")
    typer.echo(f"Results: {run_dir}")


__all__ = ["agent_app"]
