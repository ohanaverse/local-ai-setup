"""Benchmark-specific errors."""

from pathlib import Path
from typing import Any


class BenchmarkError(Exception):
    """Raised when a benchmark step cannot complete."""


class RunSavedButRestoreFailed(BenchmarkError):
    """Every row completed and is on disk; only putting the backends back
    failed. Carries `run_dir` and `results` so the CLI can still record the
    `--latest` pointer and report the row count — otherwise a host whose
    provider restore fails turns a finished, fully persisted sweep into an
    exit code with nothing to show for it. Shared by the agent and eval
    benchmarks (each passes its own row-result list)."""

    def __init__(self, message: str, *, run_dir: Path, results: list[Any]) -> None:
        super().__init__(message)
        self.run_dir = run_dir
        self.results = results
