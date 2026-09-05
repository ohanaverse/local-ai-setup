"""Suite orchestration: phase 0 (preflight) and phase 1 (execute)."""

from __future__ import annotations

import itertools
import json
import os
import shutil
import subprocess
import tempfile
import time
import tomllib
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

from modelman.benchmark import isolation
from modelman.benchmark.agent import judge, pidriver, report
from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.gates import evaluate as evaluate_gates
from modelman.benchmark.agent.suite import JudgeConfig, RowConfig, Suite, preflight
from modelman.benchmark.agent.task import TaskBundle, load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import DEFAULT_PROVIDER_IDS, Registry

DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"


@dataclass
class RowRunResult:
    row: RowConfig
    pass_number: int
    row_dir: Path
    gates: GatesReport | None
    metrics: pidriver.AgentMetrics | None
    diff_raw: str
    events: list[dict] = field(default_factory=list)
    seed_contents: dict[str, str] = field(default_factory=dict)
    closing_message: str = ""
    judge: judge.JudgeOutcome | None = None
    composite: int | None = None
    error: str | None = None


def _row_dir(run_dir: Path, index: int, row: RowConfig, pass_number: int) -> Path:
    return run_dir / f"{index:02d}--{row.label}--p{pass_number}"


def _prompt_for(task: TaskBundle) -> str:
    return (
        f"{task.task_md}\n\n"
        "Work only inside this repository. When you believe the bug is fixed, "
        "add a regression test under tests/ and stop."
    )


def _closing_message(events: list[dict]) -> str:
    """The text blocks of the last assistant `message_end`.

    pi's `message_end` "contains the final authoritative message", nested at
    message_end.message.content as typed blocks; the thinking block is excluded
    because only `text` blocks are the closing message. Reading the final
    message rather than concatenating deltas is what keeps this correct when a
    provider reports content only at completion.

    Distinct from `metrics.final_text` on purpose: that is the streamed view,
    this is the authoritative one, and the judge scores against this."""
    last_text = ""
    for event in events:
        if event.get("type") != "message_end":
            continue
        message = event.get("message") or {}
        if message.get("role") != "assistant":
            continue  # pi emits the echoed prompt as a message_end too
        blocks = message.get("content")
        if isinstance(blocks, list):
            last_text = "".join(
                b.get("text", "") for b in blocks if isinstance(b, dict) and b.get("type") == "text"
            )
    return last_text


def _build_judge_transport(judge_cfg: JudgeConfig, live_models_path: Path) -> judge.JudgeTransport:
    try:
        live = json.loads(live_models_path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        live = {}
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError(
            "no LiteLLM apiKey found in ~/.pi/agent/models.json for the judge transport"
        )
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return judge.LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)


def _judge_all(
    suite: Suite,
    task: TaskBundle,
    results: list[RowRunResult],
    live_models_path: Path,
    factory: Callable[[JudgeConfig, Path], judge.JudgeTransport],
) -> None:
    """Phase 2. Runs after restore_providers(): judging is a cloud call and
    must not hold the local GPU while it works."""
    transport = factory(suite.judge, live_models_path)
    for result in results:
        if result.error is not None or result.gates is None or result.metrics is None:
            continue
        prompt = judge.build_prompt(
            task_md=task.task_md,
            seed_contents=result.seed_contents,
            diff_text=judge.anonymize_diff(result.diff_raw),
            closing_message=judge.anonymize_message(result.closing_message),
            rubric_md=task.rubric_md,
        )
        outcome = judge.judge_row(
            transport,
            prompt,
            temperature=suite.judge.temperature,
            samples=suite.judge.samples,
            max_attempts=suite.judge.max_attempts,
        )
        result.judge = outcome
        result.composite = (
            judge.apply_cap(outcome.combined.total, result.gates.cap)
            if outcome.status == "scored" and outcome.combined is not None
            else None
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
        # what the agent touched, paired with what those files held at the
        # baseline: the judge needs the before-state to read a diff at all
        touched = workspace.modified_or_deleted_since_baseline() + workspace.new_files_since_baseline()
        seed_contents: dict[str, str] = {}
        for path in touched:
            rel = str(path.relative_to(workspace.root))
            content = workspace.file_at_baseline(rel)
            if content is not None:
                seed_contents[rel] = content
        closing_message = _closing_message(events)
        return RowRunResult(
            row=row,
            pass_number=pass_number,
            row_dir=row_dir,
            gates=gates,
            metrics=metrics,
            diff_raw=diff_raw,
            events=events,
            seed_contents=seed_contents,
            closing_message=closing_message,
        )
    finally:
        shutil.rmtree(config_dir, ignore_errors=True)
        destroy_workspace(workspace)


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    return [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]


def _git_sha() -> str:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=False
        )
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


def _pi_version() -> str:
    try:
        result = subprocess.run(["pi", "--version"], capture_output=True, text=True, check=False)
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


def _suite_to_dict(suite: Suite) -> dict:
    return {
        "name": suite.name,
        "task": str(suite.task_path),
        "passes": suite.passes,
        "cooldown_s": suite.cooldown_s,
        "agent_timeout_s": suite.agent_timeout_s,
        "judge": {
            "model": suite.judge.model,
            "thinking": suite.judge.thinking,
            "temperature": suite.judge.temperature,
            "samples": suite.judge.samples,
            "max_attempts": suite.judge.max_attempts,
            "route": suite.judge.route,
        },
        "routes_direct": {
            pid: {"base_url": c.base_url, "api": c.api} for pid, c in suite.routes_direct.items()
        },
        "rows": [
            {
                "label": row.label,
                "model_id": row.model_id,
                "thinking": row.thinking,
                "route": row.route,
                "provider_id": row.provider_id,
            }
            for row in suite.rows
        ],
    }


def _to_row_report(result: RowRunResult) -> report.RowReport:
    return report.RowReport(
        label=result.row.label,
        model_id=result.row.model_id,
        thinking=result.row.thinking,
        route=result.row.route,
        gates=result.gates,
        metrics=result.metrics,
        judge=result.judge,
        composite=result.composite,
        closing_message=result.closing_message,
        error=result.error,
    )


def _persist_row_artifacts(result: RowRunResult) -> None:
    """Write a row's artifacts. Called twice in run_suite — once before
    judging so a row's raw stream survives a judge crash, once after so
    judge.json exists — and it is idempotent for everything except judge.json."""
    if result.error is not None or result.gates is None or result.metrics is None:
        return
    report.write_row_artifacts(
        result.row_dir,
        events=result.events,
        diff_raw=result.diff_raw,
        gates=result.gates,
        metrics=result.metrics,
        judge_outcome=result.judge,
        seed_contents=result.seed_contents,
        closing_message=result.closing_message,
        label=result.row.label,
        model_id=result.row.model_id,
        thinking=result.row.thinking,
        route=result.row.route,
    )


def rejudge_run(
    run_dir: Path,
    *,
    row_filter: list[str] | None = None,
    samples_override: int | None = None,
    judge_transport_factory: Callable[[JudgeConfig, Path], judge.JudgeTransport] | None = None,
) -> list[dict]:
    """Re-score every row's persisted diff/row.json against the current rubric,
    without re-running any agent. Rewrites each row's judge.json; does not
    regenerate summary.md or metrics.jsonl."""
    with (run_dir / "run.toml").open("rb") as f:
        run_data = tomllib.load(f)
    suite_data = run_data["suite"]
    task = load_task(Path(suite_data["task"]))
    judge_cfg = JudgeConfig(**suite_data["judge"])
    if samples_override is not None:
        judge_cfg.samples = samples_override

    transport = (judge_transport_factory or _build_judge_transport)(
        judge_cfg, pidriver.LIVE_PI_MODELS_PATH
    )

    outcomes = []
    for row_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        if row_filter and row_dir.name not in row_filter:
            continue
        row_json_path = row_dir / "row.json"
        diff_path = row_dir / "diff.patch"
        if not (row_json_path.exists() and diff_path.exists()):
            continue
        row_info = json.loads(row_json_path.read_text(encoding="utf-8"))
        prompt = judge.build_prompt(
            task_md=task.task_md,
            seed_contents=row_info["seed_contents"],
            diff_text=diff_path.read_text(encoding="utf-8"),
            closing_message=row_info["closing_message"],
            rubric_md=task.rubric_md,
        )
        outcome = judge.judge_row(
            transport,
            prompt,
            temperature=judge_cfg.temperature,
            samples=judge_cfg.samples,
            max_attempts=judge_cfg.max_attempts,
        )
        gates_path = row_dir / "gates.json"
        gates_data = json.loads(gates_path.read_text(encoding="utf-8")) if gates_path.exists() else {"cap": 0.0}
        combined = outcome.combined
        composite = (
            judge.apply_cap(combined.total, gates_data["cap"])
            if outcome.status == "scored" and combined is not None
            else None
        )
        # write_judge_json, NOT write_row_artifacts: see that helper's docstring.
        report.write_judge_json(row_dir, outcome)
        outcomes.append(
            {
                "label": row_info["label"],
                "rubric_total": combined.total if outcome.status == "scored" and combined else None,
                "composite": composite,
                "verdict": combined.verdict if outcome.status == "scored" and combined else "JUDGE_FAIL",
            }
        )
    return outcomes



# What bin/llm-isolate-provider can actually isolate. DEFAULT_PROVIDER_IDS is
# the registry's local set (ollama, llamacpp, omlx); omlx-6bit is only ever a
# row-level provider override, so it is absent from that constant and must be
# named here or the 6-bit variant — the one row that most needs isolation, since
# 4-bit and 6-bit share a process — silently stops being isolated.
ISOLATABLE_PROVIDERS = set(DEFAULT_PROVIDER_IDS) | {"omlx-6bit"}


def run_suite(
    suite: Suite,
    registry: Registry,
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    live_models_path: Path = pidriver.LIVE_PI_MODELS_PATH,
    skip_judge: bool = False,
    judge_transport_factory: Callable[[JudgeConfig, Path], judge.JudgeTransport] | None = None,
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
    isolated_any = False
    for provider_id, group in itertools.groupby(
        sorted(rows, key=lambda r: r.provider_id), key=lambda r: r.provider_id
    ):
        group_rows = list(group)
        if provider_id in ISOLATABLE_PROVIDERS:
            # A cloud row contends with nothing on this machine, and running the
            # helper for it fails outright — bin/llm-isolate-provider knows only
            # the local backends — which used to mark every cloud row
            # ISOLATION_ERROR before a single request was made.
            try:
                isolation.isolate_provider(provider_id)
                isolated_any = True
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
                results.append(
                    _run_single_row(row, pass_number, task, suite, registry, row_dir, live_models_path)
                )
                if pass_number < suite.passes:
                    time.sleep(suite.cooldown_s)

    # A failed restore must not discard a completed sweep: the local backends
    # are the least valuable thing in play here, the row data is not, and on
    # this host `llm-restore-providers` can time out on llama.cpp while every
    # row's data is perfectly good. Persist first, then surface the failure.
    restore_error: str | None = None
    if isolated_any:
        try:
            isolation.restore_providers()
        except BenchmarkError as exc:
            restore_error = str(exc)

    # before judging, so a judge crash still leaves a row's raw stream on disk
    for result in results:
        _persist_row_artifacts(result)

    if not skip_judge:
        _judge_all(
            suite, task, results, live_models_path, judge_transport_factory or _build_judge_transport
        )
        for result in results:
            _persist_row_artifacts(result)

    row_reports = [_to_row_report(r) for r in results]
    (run_dir / "summary.md").write_text(report.render_summary(run_id, row_reports), encoding="utf-8")
    report.write_metrics_jsonl(run_dir / "metrics.jsonl", row_reports)
    report.write_run_toml(
        run_dir / "run.toml",
        _suite_to_dict(suite),
        git_sha=_git_sha(),
        pi_version=_pi_version(),
    )

    if restore_error is not None:
        raise BenchmarkError(
            f"providers failed to restore after the run (all results were saved "
            f"to {run_dir}): {restore_error}"
        )
    return run_dir, results

