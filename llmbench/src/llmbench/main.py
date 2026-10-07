"""llmbench CLI entry point."""

from __future__ import annotations

import typer

from .benchmark.cli import benchmark_app
from .providers.lifecycle.cli import provider_app

app = typer.Typer(help="Benchmark local LLM models.")
# No name: the benchmark commands (run, list-workloads, show-results, agent,
# eval) sit at the top level here. modelman mounts the same sub-app under
# `benchmark` until it is retired.
app.add_typer(benchmark_app)
app.add_typer(provider_app, name="provider")
