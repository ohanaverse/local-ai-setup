"""CLI for modelman benchmark subcommand."""

from __future__ import annotations

import os
from pathlib import Path

import typer

from modelman.benchmark.errors import BenchmarkError
from modelman.benchmark.runner import DEFAULT_RESULTS_DIR, run_benchmark
from modelman.benchmark.workloads import get_workload, list_workloads
from modelman.registry import load_registry
from modelman.state import load_state, save_state

benchmark_app = typer.Typer(help="Benchmark local LLM models.")


@benchmark_app.command("list-workloads")
def list_workloads_cmd() -> None:
    """List built-in benchmark workloads."""
    for name in list_workloads():
        typer.echo(name)


@benchmark_app.command("run")
def run_cmd(
    workload: str = typer.Option("chat", "--workload", help="Workload name"),
    model: list[str] = typer.Option([], "--model", help="Registry model id(s) to benchmark"),  # noqa: B008
    family: str | None = typer.Option(None, "--family", help="Benchmark all models in a family"),
    direct: bool = typer.Option(False, "--direct", help="Only benchmark direct backend access"),
    litellm: bool = typer.Option(False, "--litellm", help="Only benchmark via LiteLLM"),
    passes: int = typer.Option(1, "--passes", min=1, help="Number of passes per target"),
    cooldown: float = typer.Option(15.0, "--cooldown", help="Seconds between passes"),
    results_dir: Path | None = typer.Option(  # noqa: B008
        None, "--results-dir", help="Directory for result artifacts"
    ),
) -> None:
    """Run a benchmark workload against local models."""
    routes = ["direct", "litellm"]
    if direct and litellm:
        typer.echo("error: --direct and --litellm are mutually exclusive", err=True)
        raise typer.Exit(1)
    if direct:
        routes = ["direct"]
    if litellm:
        routes = ["litellm"]

    workload_name = os.environ.get("MODELMAN_BENCHMARK_WORKLOAD", workload)
    try:
        workload_obj = get_workload(workload_name)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    registry = load_registry()
    state = load_state()
    try:
        run = run_benchmark(
            registry,
            state,
            workload_obj,
            model_ids=model or None,
            family=family,
            passes=passes,
            cooldown_seconds=cooldown,
            routes=routes,
            results_dir=results_dir,
        )
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    # Record latest run pointer in state.
    output_dir = (results_dir or DEFAULT_RESULTS_DIR) / run.run_id
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["last_run"] = run.started_at.isoformat()
    benchmarks["last_run_dir"] = str(output_dir)
    save_state(state)

    typer.echo(f"Benchmark complete: {run.run_id}")
    typer.echo(f"Results: {output_dir}")


@benchmark_app.command("show-results")
def show_results_cmd(
    latest: bool = typer.Option(False, "--latest", help="Show the latest run"),
    run_id: str | None = typer.Option(None, "--run-id", help="Run id to show"),
) -> None:
    """Print the Markdown summary for a benchmark run."""
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)

    if latest:
        state = load_state()
        info = state.extra.get("benchmarks", {})
        run_dir = info.get("last_run_dir")
        if not run_dir:
            typer.echo("error: no latest run recorded", err=True)
            raise typer.Exit(1)
        md_path = Path(run_dir) / "summary.md"
    else:
        assert run_id is not None
        md_path = DEFAULT_RESULTS_DIR / run_id / "summary.md"

    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)

    typer.echo(md_path.read_text(encoding="utf-8"))


__all__ = ["benchmark_app"]
