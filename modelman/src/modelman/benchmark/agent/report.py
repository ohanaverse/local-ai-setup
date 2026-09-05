"""Per-row artifact writes and the run summary report."""

from __future__ import annotations

import gzip
import json
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

import tomli_w

from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.judge import JudgeOutcome, anonymize_diff
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
