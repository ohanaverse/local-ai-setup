"""Suite orchestration: phase 0 (preflight) and phase 1 (execute)."""

from __future__ import annotations

import itertools
import os
import shutil
import tempfile
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

from modelman.benchmark import isolation
from modelman.benchmark.agent import pidriver
from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.gates import evaluate as evaluate_gates
from modelman.benchmark.agent.suite import RowConfig, Suite, preflight
from modelman.benchmark.agent.task import TaskBundle, load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry

DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"


@dataclass
class RowRunResult:
    row: RowConfig
    pass_number: int
    row_dir: Path
    gates: GatesReport | None
    metrics: pidriver.AgentMetrics | None
    diff_raw: str
    error: str | None = None


def _row_dir(run_dir: Path, index: int, row: RowConfig, pass_number: int) -> Path:
    return run_dir / f"{index:02d}--{row.label}--p{pass_number}"


def _prompt_for(task: TaskBundle) -> str:
    return (
        f"{task.task_md}\n\n"
        "Work only inside this repository. When you believe the bug is fixed, "
        "add a regression test under tests/ and stop."
    )


def _run_single_row(
    row: RowConfig,
    pass_number: int,
    task: TaskBundle,
    suite: Suite,
    registry: Registry,
    row_dir: Path,
    live_models_path: Path,
) -> RowRunResult:
    row_dir.mkdir(parents=True, exist_ok=True)
    model = registry.model(row.model_id)
    target = pidriver.resolve_pi_target(row, model.model_name, suite.routes_direct, live_models_path=live_models_path)

    workspace = create_workspace(task)
    config_dir = Path(tempfile.mkdtemp(prefix="agent-bench-config-"))
    try:
        pidriver.write_pi_config(target, config_dir)
        cmd = pidriver.build_pi_command(target, row.thinking, row_dir, _prompt_for(task))
        env = {**os.environ, "PI_CODING_AGENT_DIR": str(config_dir)}
        # start_wall/end_wall bracket the subprocess only; metrics.gen_seconds
        # and the TTFTs come from the _ts stamps the reader thread applies as it
        # reads each line, relative to the same run.
        start_wall = time.monotonic()
        events, run_result = pidriver.run_pi_process(
            cmd,
            workspace_path=workspace.root,
            timeout_seconds=suite.agent_timeout_s,
            env=env,
        )
        end_wall = time.monotonic()
        metrics = pidriver.compute_metrics(
            events,
            start_wall=start_wall,
            end_wall=end_wall,
            thinking=row.thinking,
            log_fn=lambda msg: pidriver._log(msg, row_dir / "metrics.log"),
        )
        # pi writes its own session file into --session-dir (== row_dir), so
        # the presence of a *.jsonl there is evidence the session really
        # happened. Glob the extension rather than "anything in the dir":
        # gates.json/metrics.json land in the same directory later, so a bare
        # iterdir() would read as present on a re-run of a finished row.
        session_present = any(row_dir.glob("*.jsonl"))
        gates = evaluate_gates(
            workspace, task, run_result, events=events, session_file_present=session_present
        )
        diff_raw = workspace.diff()
        return RowRunResult(
            row=row, pass_number=pass_number, row_dir=row_dir, gates=gates, metrics=metrics, diff_raw=diff_raw
        )
    finally:
        shutil.rmtree(config_dir, ignore_errors=True)
        destroy_workspace(workspace)


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    return [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]


def run_suite(
    suite: Suite,
    registry: Registry,
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    live_models_path: Path = pidriver.LIVE_PI_MODELS_PATH,
) -> tuple[Path, list[RowRunResult]]:
    """Phase 0 (preflight) + phase 1 (execute): group rows by provider,
    isolate once per group, run each row's agent + gates, restore at the
    end. Judging (phase 2) and the rendered report (phase 3) are added by
    Tasks 19-23."""
    task = load_task(suite.task_path)
    preflight(suite, registry, task)

    rows = _select_rows(suite.rows, row_filter)
    results_dir = results_dir or DEFAULT_RESULTS_DIR
    run_id = datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run_dir = results_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    results: list[RowRunResult] = []
    index = 0
    ordered = sorted(rows, key=lambda r: r.provider_id)
    for provider_id, group in itertools.groupby(ordered, key=lambda r: r.provider_id):
        group_rows = list(group)
        try:
            isolation.isolate_provider(provider_id)
        except BenchmarkError as exc:
            for row in group_rows:
                index += 1
                for pass_number in range(1, suite.passes + 1):
                    results.append(
                        RowRunResult(
                            row=row,
                            pass_number=pass_number,
                            row_dir=_row_dir(run_dir, index, row, pass_number),
                            gates=None,
                            metrics=None,
                            diff_raw="",
                            error=str(exc),
                        )
                    )
            continue

        for row in group_rows:
            index += 1
            for pass_number in range(1, suite.passes + 1):
                row_dir = _row_dir(run_dir, index, row, pass_number)
                results.append(_run_single_row(row, pass_number, task, suite, registry, row_dir, live_models_path))
                if pass_number < suite.passes:
                    time.sleep(suite.cooldown_s)

    isolation.restore_providers()
    return run_dir, results

