"""Benchmark result writers."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from statistics import median
from typing import Any

from modelman.benchmark.workloads.base import BenchmarkMetrics


@dataclass
class TargetResult:
    model_id: str
    provider_id: str
    route: str  # "direct" or "litellm"
    pass_number: int
    metrics: BenchmarkMetrics
    error: str | None = None


@dataclass
class BenchmarkRun:
    run_id: str
    workload_name: str
    started_at: datetime
    results: list[TargetResult]
    metadata: dict[str, Any] = field(default_factory=dict)


def _metrics_to_dict(m: BenchmarkMetrics) -> dict[str, Any]:
    return {
        "ttft_ms": m.ttft_ms,
        "total_ms": m.total_ms,
        "completion_tokens": m.completion_tokens,
        "prompt_tokens": m.prompt_tokens,
        "throughput_tok_s": m.throughput_tok_s,
    }


def _target_to_dict(t: TargetResult) -> dict[str, Any]:
    return {
        "model_id": t.model_id,
        "provider_id": t.provider_id,
        "route": t.route,
        "pass_number": t.pass_number,
        "metrics": _metrics_to_dict(t.metrics),
        "error": t.error,
    }


def _median(values: list[float | int]) -> float | None:
    if not values:
        return None
    return float(median(values))


def _aggregate(results: list[TargetResult]) -> dict[str, dict[str, Any]]:
    groups: dict[tuple[str, str], list[TargetResult]] = {}
    for r in results:
        key = (r.model_id, r.route)
        groups.setdefault(key, []).append(r)

    summary: dict[str, dict[str, Any]] = {}
    for (model_id, route), records in sorted(groups.items()):
        valid = [r for r in records if r.error is None]
        summary[f"{model_id} ({route})"] = {
            "passes": len(records),
            "successful": len(valid),
            "ttft_ms": _median([r.metrics.ttft_ms for r in valid if r.metrics.ttft_ms is not None]),
            "total_ms": _median([r.metrics.total_ms for r in valid]),
            "throughput_tok_s": _median(
                [
                    r.metrics.throughput_tok_s
                    for r in valid
                    if r.metrics.throughput_tok_s is not None
                ]
            ),
        }
    return summary


def _render_markdown(run: BenchmarkRun) -> str:
    lines: list[str] = [
        f"# Benchmark Results — {run.run_id}",
        "",
        f"- **Workload:** {run.workload_name}",
        f"- **Started:** {run.started_at.isoformat()}",
        f"- **Records:** {len(run.results)}",
        "",
        "## Summary (median per model / route)",
        "",
        "| Model (route) | Passes | TTFT (ms) | Total (ms) | Throughput (tok/s) |",
        "|---------------|-------:|----------:|-----------:|-------------------:|",
    ]
    for key, vals in _aggregate(run.results).items():
        lines.append(
            f"| {key} | {vals['passes']} | "
            f"{vals['ttft_ms'] if vals['ttft_ms'] is not None else 'N/A'} | "
            f"{vals['total_ms'] if vals['total_ms'] is not None else 'N/A'} | "
            f"{vals['throughput_tok_s'] if vals['throughput_tok_s'] is not None else 'N/A'} |"
        )
    lines.append("")
    lines.append("## Raw per-pass records")
    lines.append("")
    lines.append("| Model | Route | Pass | TTFT (ms) | Total (ms) | Tokens | Throughput |")
    lines.append("|-------|-------|-----:|----------:|-----------:|-------:|-----------:|")
    for r in sorted(run.results, key=lambda x: (x.model_id, x.route, x.pass_number)):
        m = r.metrics
        lines.append(
            f"| {r.model_id} | {r.route} | {r.pass_number} | "
            f"{m.ttft_ms if m.ttft_ms is not None else 'N/A'} | "
            f"{m.total_ms} | "
            f"{m.completion_tokens if m.completion_tokens is not None else 'N/A'} | "
            f"{m.throughput_tok_s if m.throughput_tok_s is not None else 'N/A'} |"
        )
    lines.append("")
    return "\n".join(lines)


def write_results(run: BenchmarkRun, base_dir: Path) -> Path:
    """Write JSON, Markdown, and payload artifacts for a run."""
    run_dir = base_dir / run.run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    payload = {
        "run_id": run.run_id,
        "workload_name": run.workload_name,
        "started_at": run.started_at.isoformat(),
        "metadata": run.metadata,
    }
    (run_dir / "payload.json").write_text(json.dumps(payload, indent=2), encoding="utf-8")

    results = {
        "run_id": run.run_id,
        "workload_name": run.workload_name,
        "started_at": run.started_at.isoformat(),
        "results": [_target_to_dict(r) for r in run.results],
        "summary": _aggregate(run.results),
        "metadata": run.metadata,
    }
    (run_dir / "results.json").write_text(json.dumps(results, indent=2), encoding="utf-8")

    (run_dir / "summary.md").write_text(_render_markdown(run), encoding="utf-8")

    return run_dir
