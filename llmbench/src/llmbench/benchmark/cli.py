"""The workload benchmark commands (`llmbench run`, `list-workloads`, `show-results`)."""

from __future__ import annotations

from pathlib import Path

import typer

from llmbench._env import env_first
from llmbench.benchmark.agent.cli import agent_app
from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.eval.cli import eval_app
from llmbench.benchmark.results import BenchmarkRun
from llmbench.benchmark.results import run_dir as resolve_run_dir
from llmbench.benchmark.runner import (
    DEFAULT_RESULTS_DIR,
    NoTargetSelection,
    WorkloadRunSavedButRestoreFailed,
    run_benchmark,
)
from llmbench.benchmark.workloads import get_workload, list_workloads
from llmbench.registry import load_registry
from llmbench.state import load_state, save_state

benchmark_app = typer.Typer(help="Benchmark local LLM models.")
benchmark_app.add_typer(agent_app, name="agent")
benchmark_app.add_typer(eval_app, name="eval")


def _record_latest(run: BenchmarkRun, run_dir: Path) -> None:
    """Record the --latest pointer for a completed run in state.

    `run_dir` is the run's OWN directory (`<results-dir>/<run_id>`), the one
    holding `summary.md` — not the results base dir. `show-results --latest`
    reads it back verbatim and appends `/summary.md`, so a base dir here makes
    `--latest` fail with "results not found" for every completed run."""
    state = load_state()
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["last_run"] = run.started_at.isoformat()
    benchmarks["last_run_dir"] = str(run_dir)
    save_state(state)


@benchmark_app.command("list-workloads")
def list_workloads_cmd() -> None:
    """List built-in benchmark workloads."""
    for name in list_workloads():
        typer.echo(name)


@benchmark_app.command("run")
def run_cmd(
    workload: str = typer.Option(
        "chat",
        "--workload",
        help="Workload name (LLMBENCH_WORKLOAD, when set, overrides this flag)",
    ),
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

    workload_name = env_first("LLMBENCH_WORKLOAD", "MODELMAN_BENCHMARK_WORKLOAD") or workload
    try:
        workload_obj = get_workload(workload_name)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    registry = load_registry()
    try:
        run = run_benchmark(
            registry,
            workload_obj,
            model_ids=model or None,
            family=family,
            passes=passes,
            cooldown_seconds=cooldown,
            routes=routes,
            results_dir=results_dir,
        )
    except WorkloadRunSavedButRestoreFailed as exc:
        _record_latest(exc.run, exc.run_dir)
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from None
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc
    except NoTargetSelection as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(2) from exc

    results_base = results_dir or DEFAULT_RESULTS_DIR
    _record_latest(run, results_base / run.run_id)

    typer.echo(f"Benchmark complete: {run.run_id}")
    typer.echo(f"Results: {results_base / run.run_id}")


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
        md_path = resolve_run_dir(DEFAULT_RESULTS_DIR, run_id) / "summary.md"

    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)

    typer.echo(md_path.read_text(encoding="utf-8"))


__all__ = ["benchmark_app"]
