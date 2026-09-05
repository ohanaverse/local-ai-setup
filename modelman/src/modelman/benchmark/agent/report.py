"""Per-row artifact writes and the run summary report."""

from __future__ import annotations

import gzip
import json
from dataclasses import asdict, dataclass
from pathlib import Path
from statistics import median
from typing import Any

import tomli_w

from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.judge import (
    JudgeOutcome,
    JudgeScore,
    anonymize_diff,
    detect_overclaim,
)
from modelman.benchmark.agent.pidriver import AgentMetrics

_MASKED_KEY_NAMES = {"apiKey", "api_key", "resolved_api_key"}
_MASK = "***"


@dataclass
class RowReport:
    label: str
    model_id: str
    thinking: str
    route: str
    gates: GatesReport | None
    metrics: AgentMetrics | None
    judge: JudgeOutcome | None
    composite: int | None
    closing_message: str = ""
    error: str | None = None


def _gates_to_dict(gates: GatesReport) -> dict[str, Any]:
    return {
        "results": [asdict(r) for r in gates.results],
        "hidden_pass": gates.hidden_pass,
        "hidden_total": gates.hidden_total,
        "hidden_evaluated": gates.hidden_evaluated,
        "cap": gates.cap,
    }


def _judge_to_dict(outcome: JudgeOutcome) -> dict[str, Any]:
    return {
        "status": outcome.status,
        "attempts_used": outcome.attempts_used,
        "samples": [asdict(s) for s in outcome.samples],
        "combined": asdict(outcome.combined) if outcome.combined else None,
    }


def write_row_artifacts(
    row_dir: Path,
    *,
    events: list[dict],
    diff_raw: str,
    gates: GatesReport | None,
    metrics: AgentMetrics | None,
    judge_outcome: JudgeOutcome | None,
    seed_contents: dict[str, str] | None = None,
    closing_message: str = "",
    label: str = "",
    model_id: str = "",
    thinking: str = "",
    route: str = "",
) -> None:
    row_dir.mkdir(parents=True, exist_ok=True)
    with gzip.open(row_dir / "agent.jsonl.gz", "wt", encoding="utf-8") as f:
        for entry in events:
            f.write(json.dumps(entry) + "\n")
    (row_dir / "diff.raw.patch").write_text(diff_raw, encoding="utf-8")
    (row_dir / "diff.patch").write_text(anonymize_diff(diff_raw), encoding="utf-8")
    if gates is not None:
        (row_dir / "gates.json").write_text(json.dumps(_gates_to_dict(gates), indent=2), encoding="utf-8")
    if metrics is not None:
        (row_dir / "metrics.json").write_text(json.dumps(asdict(metrics), indent=2), encoding="utf-8")
    if judge_outcome is not None:
        (row_dir / "judge.json").write_text(json.dumps(_judge_to_dict(judge_outcome), indent=2), encoding="utf-8")
    if seed_contents is not None:
        # row.json is what makes `agent judge` possible without re-running
        # anything: the diff alone is unreadable to the judge without the
        # baseline contents of the files it touches.
        (row_dir / "row.json").write_text(
            json.dumps(
                {
                    "seed_contents": seed_contents,
                    "closing_message": closing_message,
                    "label": label,
                    "model_id": model_id,
                    "thinking": thinking,
                    "route": route,
                },
                indent=2,
            ),
            encoding="utf-8",
        )


def write_judge_json(row_dir: Path, judge_outcome: JudgeOutcome) -> None:
    """Rewrite a row's judge.json and nothing else.

    This exists so rejudge_run does not call write_row_artifacts: that helper
    gzip-writes agent.jsonl.gz from its events argument, so re-judging through
    it with events=[] would silently destroy the raw event stream that is the
    row's only primary record — and rewrite diff.raw.patch from the
    already-anonymized diff.patch, corrupting it in place."""
    row_dir.mkdir(parents=True, exist_ok=True)
    (row_dir / "judge.json").write_text(
        json.dumps(_judge_to_dict(judge_outcome), indent=2), encoding="utf-8"
    )


def _mask_keys(value: Any) -> Any:
    if isinstance(value, dict):
        return {k: (_MASK if k in _MASKED_KEY_NAMES else _mask_keys(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [_mask_keys(v) for v in value]
    return value


def write_run_toml(path: Path, suite_dict: dict[str, Any], *, git_sha: str, pi_version: str) -> None:
    payload = {
        "run": {"git_sha": git_sha, "pi_version": pi_version},
        "suite": _mask_keys(suite_dict),
    }
    with path.open("wb") as f:
        tomli_w.dump(payload, f)

def _outcome_code(report: RowReport) -> str:
    if report.error:
        return "ISOLATION_ERROR"
    if report.gates is None:
        return "UNKNOWN"
    codes = report.gates.triggered_codes
    return codes[0] if codes else "OK"


def _hidden_ratio(report: RowReport) -> str:
    if report.gates is None or not report.gates.hidden_evaluated:
        return "N/A"
    return f"{report.gates.hidden_pass}/{report.gates.hidden_total}"


def _combined(report: RowReport) -> JudgeScore | None:
    """The median score, or None when the row was not scored. Every read of
    `combined` goes through here: it is Optional even on a status="scored"
    outcome as far as the type checker is concerned, and a judge_fail row must
    render N/A rather than raise."""
    if report.judge is None or report.judge.status != "scored":
        return None
    return report.judge.combined


def _rubric_total(report: RowReport) -> int | str:
    combined = _combined(report)
    return combined.total if combined is not None else "N/A"


def _verdict(report: RowReport) -> str:
    combined = _combined(report)
    return combined.verdict if combined is not None else "N/A"


def _composite_str(report: RowReport) -> str:
    return str(report.composite) if report.composite is not None else "N/A"


def _quality_table(reports: list[RowReport]) -> str:
    lines = [
        "| label | model | thinking | route | outcome | hidden | rubric | cap | composite | verdict |",
        "|---|---|---|---|---|---|---|---|---|---|",
    ]
    for r in reports:
        cap = r.gates.cap if r.gates is not None else "N/A"
        lines.append(
            f"| {r.label} | {r.model_id} | {r.thinking} | {r.route} | {_outcome_code(r)} | "
            f"{_hidden_ratio(r)} | {_rubric_total(r)} | {cap} | {_composite_str(r)} | {_verdict(r)} |"
        )
    return "\n".join(lines)


def _throughput(m: AgentMetrics) -> tuple[float, float]:
    """(gen_tok_s, e2e_tok_s) — derived here rather than stored on the metrics,
    so a zero denominator can only ever be a 0.0 in one place.

    gen_tok_s divides by the time the model actually spent generating
    (assistant message_start to message_end); e2e_tok_s divides by the whole
    row, so tool execution and thinking overhead come out of it. A row whose
    wall time is dominated by tools shows a large gap between the two, which is
    the distinction the spec's Speed table exists to make visible."""
    gen = round(m.output_tok / m.gen_seconds, 2) if m.gen_seconds > 0 else 0.0
    e2e = round(m.output_tok / m.wall_seconds, 2) if m.wall_seconds > 0 else 0.0
    return gen, e2e


def _speed_table(reports: list[RowReport]) -> str:
    lines = [
        "| label | wall_s | gen_s | ttft_first_ms | ttft_median_ms | gen_tok_s | e2e_tok_s "
        "| in_tok | out_tok | cache_read | cache_write | reasoning_tok | tools | requests |",
        "|---|---|---|---|---|---|---|---|---|---|---|---|---|",
    ]
    for r in reports:
        m = r.metrics
        if m is None:
            lines.append(f"| {r.label} | " + "N/A | " * 12 + "N/A |")
            continue
        gen_tok_s, e2e_tok_s = _throughput(m)
        median_ttft = round(median(m.ttfts_ms), 1) if m.ttfts_ms else 0.0
        lines.append(
            f"| {r.label} | {m.wall_seconds:.2f} | {m.gen_seconds:.2f} | {m.ttft_first_ms:.1f} "
            f"| {median_ttft} | {gen_tok_s} | {e2e_tok_s} | {m.input_tok} | {m.output_tok} "
            f"| {m.cache_read_tok} | {m.cache_write_tok} | {m.reasoning_tok} "
            f"| {m.tool_call_count} | {m.requests} |"
        )
    return "\n".join(lines)


def compute_pareto_stars(reports: list[RowReport]) -> set[str]:
    """A row is starred iff no other scored row is both faster (lower
    wall_seconds) and higher-quality (higher composite) than it."""
    points: list[tuple[RowReport, float, int]] = []
    for r in reports:
        if r.composite is None or r.metrics is None:
            continue  # an N/A composite has no position on either axis
        points.append((r, r.metrics.wall_seconds, r.composite))
    starred: set[str] = set()
    for row, wall, comp in points:
        dominated = any(other_wall < wall and other_comp > comp for other, other_wall, other_comp in points if other is not row)
        if not dominated:
            starred.add(row.label)
    return starred


def _two_axis_table(reports: list[RowReport]) -> str:
    scored = sorted((r for r in reports if r.composite is not None), key=lambda r: -(r.composite or 0))
    unscored = [r for r in reports if r.composite is None]
    stars = compute_pareto_stars(reports)
    lines = ["| label | composite | wall_s | pareto |", "|---|---|---|---|"]
    for r in [*scored, *unscored]:
        wall_s = f"{r.metrics.wall_seconds:.2f}" if r.metrics is not None else "N/A"
        star = "*" if r.label in stars else ""
        lines.append(f"| {r.label} | {_composite_str(r)} | {wall_s} | {star} |")
    return "\n".join(lines)


def _anomalies_table(reports: list[RowReport]) -> str:
    lines = ["| label | anomaly |", "|---|---|"]
    for r in reports:
        if r.gates is not None:
            if r.gates.cap < 1.0:
                lines.append(f"| {r.label} | cap applied: x{r.gates.cap} |")
            if "VACUOUS_TEST" in r.gates.triggered_codes:
                lines.append(f"| {r.label} | VACUOUS_TEST |")
            combined = _combined(r)
            rubric_total = combined.total if combined is not None else None
            if rubric_total is not None and rubric_total >= 70 and r.gates.hidden_evaluated and r.gates.hidden_pass == 0 and r.gates.hidden_total > 0:
                lines.append(f"| {r.label} | rubric {rubric_total} despite all hidden tests failing |")
            if r.gates.hidden_evaluated and detect_overclaim(r.closing_message, r.gates.hidden_pass, r.gates.hidden_total):
                lines.append(f"| {r.label} | overclaim: closing message claims tests pass |")
        if r.metrics is not None:
            # thinking no-op is not a derived flag: whether --thinking does
            # anything is a per-backend config fact, so the observable below is
            # the one a run can actually prove (spec: a flag and a log line,
            # never a gate).
            if r.metrics.thinking_off_reasoning:
                lines.append(
                    f"| {r.label} | thinking=off but {r.metrics.reasoning_tok} reasoning tokens |"
                )
            if r.metrics.anomaly:
                lines.append(f"| {r.label} | {r.metrics.anomaly} |")
    if len(lines) == 2:
        lines.append("| — | none |")
    return "\n".join(lines)


def _errors_table(reports: list[RowReport]) -> str:
    """Why a row has no data. ISOLATION_ERROR alone does not tell an operator
    whether their GGUF is missing, pi is not on PATH, or the model refused the
    task, and that distinction is the whole next action."""
    lines = ["| label | model | error |", "|---|---|---|"]
    for r in reports:
        error = r.error
        if error:
            lines.append(f"| {r.label} | {r.model_id} | {error[:200]} |")
    return "" if len(lines) == 2 else "\n".join(lines)


def render_summary(run_id: str, reports: list[RowReport]) -> str:
    out = (
        f"# Agent benchmark run {run_id}\n\n"
        f"## Quality\n\n{_quality_table(reports)}\n\n"
        f"## Speed\n\n{_speed_table(reports)}\n\n"
        f"## Two-axis\n\n{_two_axis_table(reports)}\n\n"
        f"## Anomalies\n\n{_anomalies_table(reports)}\n"
    )
    errors = _errors_table(reports)
    if errors:
        out += f"\n## Errors\n\n{errors}\n"
    return out


def write_metrics_jsonl(path: Path, reports: list[RowReport]) -> None:
    with path.open("w", encoding="utf-8") as f:
        for r in reports:
            combined = _combined(r)
            f.write(
                json.dumps(
                    {
                        "label": r.label,
                        "model_id": r.model_id,
                        "thinking": r.thinking,
                        "route": r.route,
                        "outcome": _outcome_code(r),
                        "hidden_pass": r.gates.hidden_pass if r.gates else None,
                        "hidden_total": r.gates.hidden_total if r.gates else None,
                        "cap": r.gates.cap if r.gates else None,
                        "rubric_total": combined.total if combined is not None else None,
                        "composite": r.composite,
                        "metrics": asdict(r.metrics) if r.metrics else None,
                    }
                )
                + "\n"
            )
