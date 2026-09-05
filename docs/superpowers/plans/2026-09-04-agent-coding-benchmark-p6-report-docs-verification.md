# Agent coding benchmark — Phase 6: Report + artifacts + docs (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 6: Report + artifacts + docs

### Task 19: Per-row artifact writes + `run.toml` with key masking

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/report.py`
- Test: `modelman/tests/benchmark/agent/test_report.py`

**Interfaces:**
- Consumes: `GatesReport` (Task 10), `JudgeOutcome` (Task 16), `RowMetrics` (Task 7). Deliberately does **not** import from `runner.py` (`RowRunResult`) — that would create a circular import (`runner.py` will call into `report.py`). Instead this module defines its own `RowReport` adapter dataclass; `runner.py`/`cli.py` (Task 21) construct one per row from a `RowRunResult`.
- Produces: `RowReport` (`label, model_id, thinking, route, gates, metrics, judge, composite, closing_message, error`); `write_row_artifacts(row_dir, *, events, diff_raw, gates, metrics, judge_outcome) -> None`; `write_run_toml(path, suite_dict, *, git_sha, pi_version) -> None`.

`write_row_artifacts` never writes a session file — pi already writes its own directly into `row_dir` via `--session-dir` (Task 5's `build_pi_command`). It's safe to call more than once per row (once after gates, again after judging adds `judge.json`) since every file is fully overwritten, matching the spec's "written incrementally" requirement without needing a stateful writer.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_report.py`:
```python
"""Tests for modelman.benchmark.agent.report — artifact writes and
run.toml. Key masking is the one test that matters most here: a leaked
LiteLLM API key in a committed or shared run.toml is a credential leak,
not just a bug (spec: "run.toml and all logs mask key values").
"""

import gzip
import json
from pathlib import Path

from modelman.benchmark.agent.gates import GateResult, GatesReport
from modelman.benchmark.agent.pidriver import RowMetrics
from modelman.benchmark.agent.report import write_row_artifacts, write_run_toml


def _gates() -> GatesReport:
    return GatesReport(
        results=[GateResult(gate_number=i, name=f"G{i}", outcome="pass") for i in range(1, 10)],
        hidden_pass=6,
        hidden_total=6,
        hidden_evaluated=True,
        cap=1.0,
    )


def _metrics() -> RowMetrics:
    return RowMetrics(
        wall_ms=1000, requests=2, turns=1, ttft_first_ms=100, ttft_mean_ms=100.0, ttft_max_ms=100,
        gen_tok_s=10.0, e2e_tok_s=8.0, in_tok=100, out_tok=50, cache_read_tok=0, reasoning_tok=0,
        tool_ms=200, cost_usd=0.0, unparsed_lines=0, thinking_noop=False, reasoning_while_off=False,
        cold_first_token=False,
    )


def test_write_row_artifacts_writes_expected_files(tmp_path):
    row_dir = tmp_path / "row1"
    write_row_artifacts(
        row_dir,
        events=[{"ts": 1.0, "event": {"type": "session"}}],
        diff_raw="diff --git a/tmp/xyz/f.py b/tmp/xyz/f.py\n",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
    )
    assert (row_dir / "agent.jsonl.gz").exists()
    with gzip.open(row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["event"]["type"] == "session"
    assert (row_dir / "diff.raw.patch").read_text(encoding="utf-8").startswith("diff --git a/tmp/xyz")
    assert "xyz" not in (row_dir / "diff.patch").read_text(encoding="utf-8")
    assert json.loads((row_dir / "gates.json").read_text(encoding="utf-8"))["cap"] == 1.0
    assert json.loads((row_dir / "metrics.json").read_text(encoding="utf-8"))["wall_ms"] == 1000
    assert not (row_dir / "judge.json").exists()  # no judge_outcome -> no file


def test_write_run_toml_masks_api_keys(tmp_path):
    suite_dict = {
        "judge": {"model": "x"},
        "routes": {"direct": {"omlx": {"base_url": "http://localhost:8000/v1", "api": "openai-completions"}}},
        "resolved_api_key": "sk-super-secret-value",
    }
    path = tmp_path / "run.toml"
    write_run_toml(path, suite_dict, git_sha="abc123", pi_version="1.2.3")
    text = path.read_text(encoding="utf-8")
    assert "sk-super-secret-value" not in text
    assert "abc123" in text
    assert "1.2.3" in text
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/report.py`:
```python
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
from modelman.benchmark.agent.pidriver import RowMetrics

_MASKED_KEY_NAMES = {"apiKey", "api_key", "resolved_api_key"}
_MASK = "***"


@dataclass
class RowReport:
    label: str
    model_id: str
    thinking: str
    route: str
    gates: GatesReport | None
    metrics: RowMetrics | None
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
    metrics: RowMetrics | None,
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/report.py \
        modelman/tests/benchmark/agent/test_report.py
git commit -m "feat(agent-bench): add per-row artifact writes + masked run.toml - completes plan item #19"
```

### Task 20: Summary report — four tables + Pareto stars + `metrics.jsonl`

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/report.py`
- Modify: `modelman/tests/benchmark/agent/test_report.py`

**Interfaces:**
- Produces: `compute_pareto_stars(reports: list[RowReport]) -> set[str]`; `render_summary(run_id: str, reports: list[RowReport]) -> str`; `write_metrics_jsonl(path: Path, reports: list[RowReport]) -> None`. `runner.py`/`cli.py` (Task 21) call `render_summary` and `write_metrics_jsonl` once per completed run.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_report.py`:
```python
from modelman.benchmark.agent.report import RowReport, compute_pareto_stars, render_summary, write_metrics_jsonl
from modelman.benchmark.agent.judge import JudgeOutcome, JudgeScore


def _judge_outcome(total: int, verdict: str = "principled_fix") -> JudgeOutcome:
    score = JudgeScore(
        scores={"root_cause": total, "approach": 0, "test_quality": 0, "scope": 0, "coherence": 0},
        total=total, verdict=verdict, flags=[], rationale="", raw_text="{}",
    )
    return JudgeOutcome(status="scored", samples=[score], combined=score, attempts_used=1)


def _row(label, *, wall_ms, composite, gates=None, judge=None) -> RowReport:
    metrics = RowMetrics(
        wall_ms=wall_ms, requests=1, turns=1, ttft_first_ms=10, ttft_mean_ms=10.0, ttft_max_ms=10,
        gen_tok_s=5.0, e2e_tok_s=4.0, in_tok=10, out_tok=5, cache_read_tok=0, reasoning_tok=0,
        tool_ms=0, cost_usd=0.0, unparsed_lines=0, thinking_noop=False, reasoning_while_off=False,
        cold_first_token=False,
    )
    return RowReport(
        label=label, model_id="ollama/a", thinking="off", route="direct",
        gates=gates or _gates(), metrics=metrics, judge=judge, composite=composite,
    )


def test_compute_pareto_stars_only_stars_nondominated_rows():
    """No other row is both faster and higher-quality — row 'slow-good' and
    'fast-bad' are each nondominated even though neither is best on both
    axes; 'dominated' loses to 'fast-bad' on both."""
    rows = [
        _row("fast-bad", wall_ms=1000, composite=40),
        _row("slow-good", wall_ms=5000, composite=90),
        _row("dominated", wall_ms=2000, composite=30),  # slower AND worse than fast-bad
    ]
    stars = compute_pareto_stars(rows)
    assert stars == {"fast-bad", "slow-good"}


def test_render_summary_includes_all_four_tables():
    rows = [_row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80))]
    summary = render_summary("run-1", rows)
    assert "## Quality" in summary
    assert "## Speed" in summary
    assert "## Two-axis" in summary
    assert "## Anomalies" in summary
    assert "r1" in summary


def test_render_summary_na_composite_rows_are_never_starred():
    """A JUDGE_FAIL row (composite None) sorts to the bottom of the
    two-axis table and never earns a Pareto star (spec review resolution)."""
    fail_gates = _gates()
    rows = [
        _row("scored", wall_ms=1000, composite=80, judge=_judge_outcome(80)),
        _row("judge-failed", wall_ms=500, composite=None, gates=fail_gates, judge=None),
    ]
    summary = render_summary("run-1", rows)
    two_axis_section = summary.split("## Two-axis")[1].split("## Anomalies")[0]
    lines = [line for line in two_axis_section.splitlines() if line.startswith("|")]
    assert lines[-1].split("|")[1].strip() == "judge-failed"  # N/A row sorts last
    assert "*" not in lines[-1]


def test_render_summary_lists_all_three_thinking_latency_anomalies():
    """The Anomalies table is the only place a reader learns a row is not
    comparable to its partner row, so each flag from RowMetrics must surface
    there — including reasoning_while_off, the mismatch observed live when
    --thinking off still returned reasoning tokens."""
    row = _row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80))
    row.metrics.thinking_noop = True
    row.metrics.reasoning_while_off = True
    row.metrics.cold_first_token = True
    anomalies = render_summary("run-1", [row]).split("## Anomalies")[1]
    assert "thinking no-op" in anomalies
    assert "reasoning emitted with thinking=off" in anomalies
    assert "cold first token" in anomalies


def test_write_metrics_jsonl_one_line_per_row(tmp_path):
    rows = [_row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80)), _row("r2", wall_ms=2000, composite=50)]
    path = tmp_path / "metrics.jsonl"
    write_metrics_jsonl(path, rows)
    lines = path.read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    assert json.loads(lines[0])["label"] == "r1"
    assert json.loads(lines[0])["composite"] == 80
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: FAIL — `render_summary`/`compute_pareto_stars`/`write_metrics_jsonl` don't exist yet

- [ ] **Step 3: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/report.py` (add `from modelman.benchmark.agent.judge import detect_overclaim` to the existing judge import line):
```python
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


def _rubric_total(report: RowReport) -> int | str:
    if report.judge is None or report.judge.status != "scored":
        return "N/A"
    return report.judge.combined.total


def _verdict(report: RowReport) -> str:
    if report.judge is None or report.judge.status != "scored":
        return "N/A"
    return report.judge.combined.verdict


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


def _speed_table(reports: list[RowReport]) -> str:
    lines = [
        "| label | ttft_first | ttft_mean | gen_tok_s | e2e_tok_s | in_tok | out_tok | reasoning_tok | tool_ms | requests | cost |",
        "|---|---|---|---|---|---|---|---|---|---|---|",
    ]
    for r in reports:
        m = r.metrics
        if m is None:
            lines.append(f"| {r.label} | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |")
            continue
        lines.append(
            f"| {r.label} | {m.ttft_first_ms} | {m.ttft_mean_ms} | {m.gen_tok_s} | {m.e2e_tok_s} | "
            f"{m.in_tok} | {m.out_tok} | {m.reasoning_tok} | {m.tool_ms} | {m.requests} | {m.cost_usd} |"
        )
    return "\n".join(lines)


def compute_pareto_stars(reports: list[RowReport]) -> set[str]:
    """A row is starred iff no other scored row is both faster (lower
    wall_ms) and higher-quality (higher composite) than it."""
    scored = [r for r in reports if r.composite is not None and r.metrics is not None]
    starred: set[str] = set()
    for r in scored:
        dominated = any(
            other.metrics.wall_ms < r.metrics.wall_ms and other.composite > r.composite for other in scored
        )
        if not dominated:
            starred.add(r.label)
    return starred


def _two_axis_table(reports: list[RowReport]) -> str:
    scored = sorted((r for r in reports if r.composite is not None), key=lambda r: -r.composite)
    unscored = [r for r in reports if r.composite is None]
    stars = compute_pareto_stars(reports)
    lines = ["| label | composite | wall_ms | pareto |", "|---|---|---|---|"]
    for r in [*scored, *unscored]:
        wall_ms = r.metrics.wall_ms if r.metrics is not None else "N/A"
        star = "*" if r.label in stars else ""
        lines.append(f"| {r.label} | {_composite_str(r)} | {wall_ms} | {star} |")
    return "\n".join(lines)


def _anomalies_table(reports: list[RowReport]) -> str:
    lines = ["| label | anomaly |", "|---|---|"]
    for r in reports:
        if r.gates is not None:
            if r.gates.cap < 1.0:
                lines.append(f"| {r.label} | cap applied: x{r.gates.cap} |")
            if "VACUOUS_TEST" in r.gates.triggered_codes:
                lines.append(f"| {r.label} | VACUOUS_TEST |")
            rubric_total = r.judge.combined.total if r.judge and r.judge.status == "scored" else None
            if rubric_total is not None and rubric_total >= 70 and r.gates.hidden_evaluated and r.gates.hidden_pass == 0 and r.gates.hidden_total > 0:
                lines.append(f"| {r.label} | rubric {rubric_total} despite all hidden tests failing |")
            if r.gates.hidden_evaluated and detect_overclaim(r.closing_message, r.gates.hidden_pass, r.gates.hidden_total):
                lines.append(f"| {r.label} | overclaim: closing message claims tests pass |")
        if r.metrics is not None:
            if r.metrics.thinking_noop:
                lines.append(f"| {r.label} | thinking no-op |")
            if r.metrics.reasoning_while_off:
                lines.append(f"| {r.label} | reasoning emitted with thinking=off |")
            if r.metrics.cold_first_token:
                lines.append(f"| {r.label} | cold first token |")
    if len(lines) == 2:
        lines.append("| — | none |")
    return "\n".join(lines)


def render_summary(run_id: str, reports: list[RowReport]) -> str:
    return (
        f"# Agent benchmark run {run_id}\n\n"
        f"## Quality\n\n{_quality_table(reports)}\n\n"
        f"## Speed\n\n{_speed_table(reports)}\n\n"
        f"## Two-axis\n\n{_two_axis_table(reports)}\n\n"
        f"## Anomalies\n\n{_anomalies_table(reports)}\n"
    )


def write_metrics_jsonl(path: Path, reports: list[RowReport]) -> None:
    with path.open("w", encoding="utf-8") as f:
        for r in reports:
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
                        "rubric_total": r.judge.combined.total if r.judge and r.judge.status == "scored" else None,
                        "composite": r.composite,
                        "metrics": asdict(r.metrics) if r.metrics else None,
                    }
                )
                + "\n"
            )
```

Also add, near the top of the test file (alongside the existing imports, not mid-file — `make check` runs `ruff check tests/` with `E402` enabled): `from modelman.benchmark.agent.pidriver import RowMetrics` and `import json` — both are already imported by Task 19's version of the file if following this plan in order; add only what's missing.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/report.py \
        modelman/tests/benchmark/agent/test_report.py
git commit -m "feat(agent-bench): add summary report tables + metrics.jsonl - completes plan item #20"
```

### Task 21: Wire reporting into the runner + `agent show` + `agent judge`

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/report.py`
- Modify: `modelman/src/modelman/benchmark/agent/runner.py`
- Modify: `modelman/src/modelman/benchmark/agent/cli.py`
- Modify: `modelman/tests/benchmark/agent/test_report.py`
- Modify: `modelman/tests/benchmark/agent/test_runner.py`
- Modify: `modelman/tests/benchmark/agent/test_cli.py`

**Interfaces:**
- `write_row_artifacts` gains several optional keyword params (`seed_contents`, `closing_message`, `label`, `model_id`, `thinking`, `route`) and writes a small `row.json` sidecar when given — this is what makes `agent judge` able to rebuild a judge prompt from disk without re-running the agent.
- `write_judge_json(row_dir, judge_outcome) -> None` is new: the re-judge path's only write. `rejudge_run` must **not** go through `write_row_artifacts`, because that helper gzip-writes `agent.jsonl.gz` from its `events` argument, so passing `events=[]` (the obvious shortcut) truncates the raw event stream — the only record of what the agent actually did.
- `runner.py` gains `rejudge_run(run_dir, *, row_filter=None, samples_override=None, judge_transport_factory=None) -> list[dict]` (returns one `{"label", "rubric_total", "composite", "verdict"}` dict per re-judged row).
- `run_suite` now writes `run.toml`, `summary.md`, and `metrics.jsonl` into `run_dir` before returning — the CLI's `run`/`show` commands read these files, not the in-memory `RowRunResult` list.

**Scope note:** `rejudge_run` rewrites each row's `judge.json` and prints the new scores; it does **not** regenerate the run's `summary.md`/`metrics.jsonl` (that would require re-deriving every row's identity from `row.json` files, which is a reasonable follow-up but not required by the spec's stated purpose for this command — "how a rubric edit gets evaluated cheaply"). Document this limitation in guide 09 (Task 25).

- [ ] **Step 1: Extend `report.write_row_artifacts` with the `row.json` sidecar**

Write the failing test first — append to `modelman/tests/benchmark/agent/test_report.py`:
```python
def test_write_row_artifacts_writes_row_json_sidecar_when_given(tmp_path):
    row_dir = tmp_path / "row2"
    write_row_artifacts(
        row_dir,
        events=[],
        diff_raw="",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
        seed_contents={"a.py": "original"},
        closing_message="Fixed it.",
        label="r1",
        model_id="ollama/a",
        thinking="off",
        route="direct",
    )
    row_info = json.loads((row_dir / "row.json").read_text(encoding="utf-8"))
    assert row_info["seed_contents"] == {"a.py": "original"}
    assert row_info["closing_message"] == "Fixed it."
    assert row_info["label"] == "r1"
```

Run it (`uv run pytest tests/benchmark/agent/test_report.py -v`) — FAILS (unexpected keyword arguments). Then change `write_row_artifacts`'s signature and body in `modelman/src/modelman/benchmark/agent/report.py` to:
```python
def write_row_artifacts(
    row_dir: Path,
    *,
    events: list[dict],
    diff_raw: str,
    gates: GatesReport | None,
    metrics: RowMetrics | None,
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
        row_info = {
            "seed_contents": seed_contents,
            "closing_message": closing_message,
            "label": label,
            "model_id": model_id,
            "thinking": thinking,
            "route": route,
        }
        (row_dir / "row.json").write_text(json.dumps(row_info, indent=2), encoding="utf-8")
```

Run again — PASSES (8 tests total in `test_report.py`).

Also append the new write helper to `modelman/src/modelman/benchmark/agent/report.py`, and its test to `test_report.py`:

```python
def write_judge_json(row_dir: Path, judge_outcome: JudgeOutcome) -> None:
    """Rewrite a row's judge.json and nothing else.

    This exists so rejudge_run does not call write_row_artifacts: that helper
    gzip-writes agent.jsonl.gz from its events argument, so re-judging through
    it with events=[] would silently destroy the raw event stream that is the
    row's only primary record."""
    row_dir.mkdir(parents=True, exist_ok=True)
    (row_dir / "judge.json").write_text(
        json.dumps(_judge_to_dict(judge_outcome), indent=2), encoding="utf-8"
    )
```

```python
def test_write_judge_json_leaves_other_artifacts_alone(tmp_path):
    """The re-judge path must not disturb agent.jsonl.gz — the bug this helper
    exists to prevent was write_row_artifacts(events=[]) truncating it."""
    row_dir = tmp_path / "row3"
    write_row_artifacts(
        row_dir,
        events=[{"ts": 1.0, "event": {"type": "agent_settled"}}],
        diff_raw="",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
    )
    write_judge_json(row_dir, _judge_outcome(80))
    with gzip.open(row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["event"]["type"] == "agent_settled"
    assert json.loads((row_dir / "judge.json").read_text(encoding="utf-8"))["combined"]["total"] == 80
```

(Again: `write_judge_json` into `test_report.py`'s existing top-of-file import line, and `_judge_outcome` is the helper Task 20 already added.)

- [ ] **Step 2: Write the failing runner test**

Append to `modelman/tests/benchmark/agent/test_runner.py`:
```python
def test_run_suite_writes_run_artifacts(tmp_path, monkeypatch):
    """run_suite persists run.toml, summary.md, and metrics.jsonl — the
    CLI's show command reads these files, not the in-memory result list."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json", skip_judge=True
    )

    assert (run_dir / "run.toml").exists()
    assert (run_dir / "summary.md").exists()
    assert (run_dir / "metrics.jsonl").exists()
    assert (results[0].row_dir / "gates.json").exists()
    assert (results[0].row_dir / "row.json").exists()
```

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v` — FAILS (files don't exist yet).

- [ ] **Step 3: Wire `report.py` into `runner.py`**

Add imports to `modelman/src/modelman/benchmark/agent/runner.py`: `import subprocess`, `import tomllib`, `from modelman.benchmark.agent import report`. Add `events: list[dict] = field(default_factory=list)` to `RowRunResult` (alongside `seed_contents`).

In `_run_single_row`, add `events=run_result.events` to the `RowRunResult(...)` call.

Add these helpers above `run_suite`:
```python
def _git_sha() -> str:
    try:
        result = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=False)
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
        "routes_direct": {pid: {"base_url": c.base_url, "api": c.api} for pid, c in suite.routes_direct.items()},
        "rows": [
            {
                "label": r.label,
                "model_id": r.model_id,
                "thinking": r.thinking,
                "route": r.route,
                "provider_id": r.provider_id,
            }
            for r in suite.rows
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
    if result.error is not None or result.gates is None:
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
```

Replace the tail of `run_suite` (from `isolation.restore_providers()` onward) with:
```python
    isolation.restore_providers()

    for result in results:
        _persist_row_artifacts(result)

    if not skip_judge:
        _judge_all(suite, task, results, live_models_path, judge_transport_factory or _build_judge_transport)
        for result in results:
            _persist_row_artifacts(result)

    row_reports = [_to_row_report(r) for r in results]
    (run_dir / "summary.md").write_text(report.render_summary(run_id, row_reports), encoding="utf-8")
    report.write_metrics_jsonl(run_dir / "metrics.jsonl", row_reports)
    report.write_run_toml(run_dir / "run.toml", _suite_to_dict(suite), git_sha=_git_sha(), pi_version=_pi_version())

    return run_dir, results
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: PASS (all tests, including the new one)

- [ ] **Step 5: Add `rejudge_run` + `agent show`/`agent judge` CLI commands**

Write the failing test first — append to `modelman/tests/benchmark/agent/test_cli.py`:
```python
def test_show_prints_persisted_summary(tmp_path, monkeypatch):
    run_dir = tmp_path / "results" / "run1"
    run_dir.mkdir(parents=True)
    (run_dir / "summary.md").write_text("# hello from disk\n", encoding="utf-8")
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    from modelman.state import StateStore, save_state

    state = StateStore()
    state.extra["benchmarks"] = {"agent_last_run": str(run_dir)}
    save_state(state)

    result = runner.invoke(agent_app, ["show", "--latest"])
    assert result.exit_code == 0
    assert "hello from disk" in result.output
```

Append to `modelman/tests/benchmark/agent/test_runner.py`:
```python
def test_rejudge_run_rewrites_judge_json_from_persisted_artifacts(tmp_path, monkeypatch):
    """agent judge re-scores from row.json/diff.patch/gates.json alone —
    no agent process, no workspace, no isolation — and leaves every other
    artifact in the row directory byte-identical."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json", skip_judge=True
    )

    from modelman.benchmark.agent.runner import rejudge_run

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            return json.dumps(
                {"scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
                 "total": 100, "verdict": "principled_fix", "flags": [], "rationale": "ok"}
            )

    # Seed a raw event stream so "the re-judge left it alone" is assertable.
    with gzip.open(results[0].row_dir / "agent.jsonl.gz", "wt", encoding="utf-8") as f:
        f.write(json.dumps({"ts": 1.0, "event": {"type": "agent_settled"}}) + "\n")

    outcomes = rejudge_run(run_dir, judge_transport_factory=lambda cfg, path: _FakeJudgeTransport())
    assert len(outcomes) == 1
    assert outcomes[0]["rubric_total"] == 100
    assert json.loads((results[0].row_dir / "judge.json").read_text(encoding="utf-8"))["combined"]["total"] == 100
    with gzip.open(results[0].row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["event"]["type"] == "agent_settled"  # not truncated
```

Run both (`uv run pytest tests/benchmark/agent/test_runner.py tests/benchmark/agent/test_cli.py -v`) — FAIL (`rejudge_run`, `show`, `judge` don't exist yet). Add `import gzip` to `test_runner.py`'s existing top-of-file import block while you are there.

Add to `modelman/src/modelman/benchmark/agent/runner.py`:
```python
def rejudge_run(
    run_dir: Path,
    *,
    row_filter: list[str] | None = None,
    samples_override: int | None = None,
    judge_transport_factory=None,
) -> list[dict]:
    """Re-score every row's persisted diff/row.json against the current
    rubric, without re-running any agent. Rewrites each row's judge.json;
    does not regenerate summary.md/metrics.jsonl (see Task 21 scope note)."""
    with (run_dir / "run.toml").open("rb") as f:
        run_data = tomllib.load(f)
    suite_data = run_data["suite"]
    task = load_task(Path(suite_data["task"]))
    judge_cfg = JudgeConfig(**suite_data["judge"])
    if samples_override is not None:
        judge_cfg.samples = samples_override

    transport = (judge_transport_factory or _build_judge_transport)(judge_cfg, pidriver.LIVE_PI_MODELS_PATH)

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
        gates_data = json.loads((row_dir / "gates.json").read_text(encoding="utf-8")) if (row_dir / "gates.json").exists() else {"cap": 0.0}
        composite = judge.apply_cap(outcome.combined.total, gates_data["cap"]) if outcome.status == "scored" else None
        # write_judge_json, NOT write_row_artifacts: the latter rewrites
        # agent.jsonl.gz from its events argument, so re-judging would truncate
        # the row's raw event stream (and rewrite diff.raw.patch from the
        # already-anonymized diff.patch, corrupting it in place).
        report.write_judge_json(row_dir, outcome)
        outcomes.append(
            {
                "label": row_info["label"],
                "rubric_total": outcome.combined.total if outcome.status == "scored" else None,
                "composite": composite,
                "verdict": outcome.combined.verdict if outcome.status == "scored" else "JUDGE_FAIL",
            }
        )
    return outcomes
```

Add to `modelman/src/modelman/benchmark/agent/cli.py` (new imports: `from modelman.benchmark.agent.runner import DEFAULT_RESULTS_DIR, rejudge_run, run_suite`, replacing the existing narrower `run_suite`-only import):
```python
@agent_app.command("show")
def show_cmd(
    latest: bool = typer.Option(False, "--latest", help="Show the latest agent run"),
    run_id: str | None = typer.Option(None, "--run-id", help="Run id to show"),
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    """Print the persisted summary.md for an agent benchmark run."""
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("agent_last_run")
        if not run_dir_str:
            typer.echo("error: no latest agent run recorded", err=True)
            raise typer.Exit(1)
        md_path = Path(run_dir_str) / "summary.md"
    else:
        md_path = results_dir / str(run_id) / "summary.md"
    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)
    typer.echo(md_path.read_text(encoding="utf-8"))


@agent_app.command("judge")
def judge_cmd(
    latest: bool = typer.Option(False, "--latest", help="Re-judge the latest agent run"),
    run_id: str | None = typer.Option(None, "--run-id", help="Run id to re-judge"),
    row: list[str] = typer.Option([], "--row", help="Row directory name to re-judge (repeatable)"),  # noqa: B008
    samples: int | None = typer.Option(None, "--samples", help="Override [judge].samples for this re-judge"),
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    """Re-score an existing run's persisted artifacts without re-running any agent."""
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("agent_last_run")
        if not run_dir_str:
            typer.echo("error: no latest agent run recorded", err=True)
            raise typer.Exit(1)
        target_dir = Path(run_dir_str)
    else:
        target_dir = results_dir / str(run_id)

    try:
        outcomes = rejudge_run(target_dir, row_filter=row or None, samples_override=samples)
    except (BenchmarkError, FileNotFoundError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    for outcome in outcomes:
        typer.echo(f"{outcome['label']}: rubric={outcome['rubric_total']} composite={outcome['composite']} verdict={outcome['verdict']}")
```

Update the `run_cmd`'s existing `from modelman.benchmark.agent.runner import run_suite` line to `from modelman.benchmark.agent.runner import DEFAULT_RESULTS_DIR, rejudge_run, run_suite` (one import line covering all three).

- [ ] **Step 6: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (every test from Tasks 1–21)

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/report.py \
        modelman/src/modelman/benchmark/agent/runner.py \
        modelman/src/modelman/benchmark/agent/cli.py \
        modelman/tests/benchmark/agent/test_report.py \
        modelman/tests/benchmark/agent/test_runner.py \
        modelman/tests/benchmark/agent/test_cli.py
git commit -m "feat(agent-bench): persist run artifacts + add show/judge commands - completes plan item #21"
```

### Task 22: `q4-agent-sweep.toml` + `.gitignore`

**Files:**
- Create: `benchmarks/suites/q4-agent-sweep.toml`
- Modify: `.gitignore`

**Interfaces:** None — content only.

- [ ] **Step 1: Write the full sweep suite**

`benchmarks/suites/q4-agent-sweep.toml`:
```toml
name = "q4 agent sweep"
task = "benchmarks/tasks/day31-drift"
passes = 1
cooldown_s = 20
agent_timeout_s = 420

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"
api = "openai-completions"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[routes.direct.llamacpp]
base_url = "http://localhost:8080/v1"
api = "openai-completions"

[[rows]]
models   = ["ollama/qwen3.8:27b-mlx"]
thinking = ["off", "high"]
routes   = ["direct", "litellm"]

[[rows]]
models   = ["omlx/mlx-community--Qwen3.8-27B-4bit"]
thinking = ["off", "high"]
routes   = ["litellm"]

# omlx over the direct route needs the name the server actually serves,
# because the registry model_name is org-prefixed (`mlx-community/…`) while
# omlx keys on the repo basename. Confirm with
#   curl -s localhost:8000/v1/models
# and then uncomment this row, correcting direct_model to whatever it prints.
# [[rows]]
# label        = "qwen3.8-27b-omlx-direct"
# model        = "omlx/mlx-community--Qwen3.8-27B-4bit"
# thinking     = "off"
# route        = "direct"
# direct_model = "Qwen3.8-27B-4bit"
```

Every id here is checked against `~/.config/local-ai/registry.toml` by `load_suite`, which refuses the file otherwise — so this list is deliberately restricted to ids that exist today. `ollama/qwen3.8:27b-mlx` and `omlx/mlx-community--Qwen3.8-27B-4bit` do. The `Ornith-1.5-35B-A3B-MLX-6bit` variant this task originally listed does **not** exist as a registry model (it is a LiteLLM deployment name and an `llm-isolate-provider omlx-6bit` target only), so it belongs in a commented row rather than an active one: a sweep file that cannot load is a sweep that never runs.

When an `omlx` 6-bit registry entry does get added, that row is also where the `provider =` override earns its keep — isolate the exact variant with `provider = "omlx-6bit"` while `model =` stays the registry id, exactly as `modelman benchmark`'s own isolation does.

- [ ] **Step 2: Add the bench workspace temp-dir pattern to `.gitignore`**

Check the current end of `.gitignore` first (`tail -5 .gitignore`), then append:
```
# Agent-benchmark scratch workspaces (modelman/src/modelman/benchmark/agent/workspace.py)
/tmp/agent-bench-*
```
This line documents intent but is defensive only — `workspace.py`'s `tempfile.mkdtemp` always writes under the OS temp dir, never inside the repo, so nothing under this pattern should ever actually be tracked; it exists in case `--keep-workspace` (a future flag, not built by this plan) is ever pointed at a path inside the repo by mistake.

- [ ] **Step 3: Commit**

```bash
git add benchmarks/suites/q4-agent-sweep.toml .gitignore
git commit -m "feat(agent-bench): add full q4 sweep suite - completes plan item #22"
```

### Task 23: Guide 09 + README/CLAUDE.md cross-links

**Files:**
- Create: `docs/guides/09-agent-benchmarks.md`
- Modify: `benchmarks/README.md`
- Modify: `docs/guides/05-benchmarks.md`
- Modify: `CLAUDE.md` (root)
- Modify: `modelman/CLAUDE.md`

**Interfaces:** None — documentation only. This task's `<!-- UNVERIFIED -->` blocks (matching guide 05's existing convention) get replaced with real captured output in Task 24, once a live smoke run has actually happened.

- [ ] **Step 1: Write guide 09**

`docs/guides/09-agent-benchmarks.md`:
```markdown
# Agent coding benchmarks — `modelman benchmark agent`

> Use this to: run a real coding task through the `pi` agent across a matrix of model/thinking/route configurations, and read a report that separates speed from quality instead of ranking on tokens/sec alone.

Design rationale, gate taxonomy, and scoring rules: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`. This guide is the day-to-day usage doc; the spec is the source of truth for *why* each rule exists.

This guide, unlike 00/02/04/05/08, embeds no `litellm_exposed` snapshots — nothing here goes stale when a model is exposed/unexposed.

## Prerequisites

- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, isolation helpers on `PATH`).
- `pi` installed and on `PATH` — this harness drives `pi --mode json`, not a direct HTTP request, for the agent rows.
- A working LiteLLM apiKey already seeded into `~/.pi/agent/models.json` — launch any `wt` agent in litellm gateway mode once if you've never done so; the harness reads that key rather than storing its own.
- `OPENROUTER_API_KEY` available (via `~/Library/LaunchAgents/local.litellm.proxy.plist` or the env) if your suite's `[judge]` model is an OpenRouter model — preflight checks this before running any agent row.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark agent list-tasks --root ../benchmarks/tasks
uv run modelman benchmark agent list-suites --root ../benchmarks/suites
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run
```

<!-- UNVERIFIED — a live run spawns a real pi agent process against a real (cloud) model and costs real judge tokens; capture this output the first time the smoke suite is actually run end-to-end. -->
```bash
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml
# → Agent benchmark complete: 1 row(s), 1 ran without an isolation error
#   Results: /Users/keith/.config/local-ai/benchmarks/<run-id>

uv run modelman benchmark agent show --latest
```

## Steps

### 1. Pick or author a task

A task bundle lives under `benchmarks/tasks/<task-id>/` and needs `task.md`, `visible/`, `hidden/`, `gates.toml`, `rubric.md`, `meta.toml`. `day31-drift` (a calendar-arithmetic bug in an invented billing domain, `kettlecomb`) ships as the first task — see its `task.md` for the exact bug report an agent receives, and `meta.toml` for the tier expectations a run's results should be read against. Task-authoring rules are in the spec's "The first task" section: bespoke domain (no public repo an LLM might have memorized), a deterministic gate, and a plausible wrong answer that isn't just "said no."

### 2. Write or reuse a suite

A suite (`benchmarks/suites/*.toml`) picks a task and a `[[rows]]` matrix (model × thinking × route, cartesian-expanded). `smoke.toml` is a one-row, fast-cloud-model suite for plumbing checks; `q4-agent-sweep.toml` is the fuller local-model matrix. `--dry-run` prints the resolved row list without running anything — always dry-run a new or edited suite before a real run, since a mistyped model id loads a 27B model for nothing.

### 3. Run it

```bash
uv run modelman benchmark agent run --suite <path> [--row <label-or-index>]... [--passes N] [--skip-judge] [--dry-run]
```

Local rows are grouped by provider and isolated once per group (stop-others, start, warmup) via the same `bin/llm-isolate-provider`/`bin/llm-restore-providers` helpers `modelman benchmark` uses — see [05-benchmarks](05-benchmarks.md) Step 1 for exactly what isolation does per backend. Judging always runs *after* `restore_providers()`, so a cloud judge call never contends with a loaded local model.

### 4. Read the report

```bash
uv run modelman benchmark agent show --latest
uv run modelman benchmark agent show --run-id <run-id>
```

`summary.md` (copy it into `benchmarks/results/` yourself if you want it version-controlled, same as the bash benchmarks' results) has four tables: **Quality** (outcome code, hidden n/m, rubric, cap, composite, verdict), **Speed** (TTFT, `gen_tok_s`/`e2e_tok_s`, tool time, tokens, cost), **Two-axis** (sorted by composite, Pareto-nondominated rows starred — "fastest config at ≥ this quality"), and **Anomalies** (every cap applied, vacuous test, thinking no-op, reasoning emitted with `thinking=off`, cold-first-token, overclaim, and any row scoring ≥70 rubric points despite failing every hidden test). A `JUDGE_FAIL` row keeps its gates/speed data and shows `N/A` for quality — it never voids the row, and it never earns a Pareto star.

Per-row artifacts live under `~/.config/local-ai/benchmarks/<run-id>/<row-dir>/`: `agent.jsonl.gz` (raw event stream), `agent.session.jsonl` (pi's own session file), `diff.raw.patch`/`diff.patch` (as-produced / anonymized), `gates.json`, `metrics.json`, `judge.json`, `row.json` (what `agent judge` needs to re-score later).

### 5. Re-judge cheaply after a rubric edit

```bash
uv run modelman benchmark agent judge --latest [--row <row-dir>]... [--samples N]
```

Re-scores from each row's persisted `row.json`/`diff.patch`/`gates.json` — no agent re-run, no isolation. It rewrites each row's `judge.json` and prints the new rubric total/composite/verdict per row; it does **not** regenerate `summary.md` — re-read the individual `judge.json` files (or `agent show --latest`, which still reflects the *original* judge pass) until a future enhancement adds full summary regeneration.

## Verification

<!-- UNVERIFIED — requires a completed agent run. -->
```bash
ls ~/.config/local-ai/benchmarks/<run-id>/
# → run.toml  summary.md  metrics.jsonl  <row-dir>/...
```

## Gotchas

- **`pi` needs no permission-bypass flag** for this harness — tool execution works under `--no-approve` in `--mode json` because non-interactive modes never prompt; see the spec's "Verified against the live setup" for why `--no-approve` is passed anyway (pinning project-trust behavior, not enabling tools).
- **`route = "direct"` requires a matching `[routes.direct.<provider>]` block** in the suite — preflight fails immediately, naming the missing provider, rather than after an agent has already run.
- **`omlx` 4-bit and 6-bit share one provider.** Use a row's `provider =` override (not just the model id) to isolate the exact variant — same rule as `modelman benchmark`'s own isolation, see [05-benchmarks](05-benchmarks.md) Gotchas.
- **`route = "direct"` sends the registry `model_name`, which is not always what the backend serves.** True for `ollama` (bare names), not for `omlx`, whose registry entries are org-prefixed (`mlx-community/X`) while the server knows `X`. Set the row's `direct_model = "X"` (check `curl -s localhost:8000/v1/models`) or the row's pi requests will 400. A misaddressed row does not look like a configuration error here — it looks like a model that failed the task, which is the one misreading this harness cannot allow.
- **`thinking = "off"` is a request, not a guarantee.** Observed live: `--thinking off` against `ollama/glm-5.3-flash:cloud` still returned `usage.reasoning = 11` and a thinking block. The Anomalies table flags those rows (`reasoning emitted with thinking=off`); treat an off/high pair as uncomparable when either is flagged.
- **`repair_rounds` is accepted but rejected if non-zero.** The seam exists (per-turn retry after a failed run) but is disabled in v1 — see the spec's "Deferred: the repair round."
- **Local-model timeouts are a config input, not a bug.** A 27B model in a six-round agentic task can exceed `agent_timeout_s`; `TIMEOUT` caps the composite at 0 but is reported as its own outcome class — raise `agent_timeout_s` per-backend rather than reading a timeout as "this model can't do it."
- **Judging costs cloud API spend on every row.** `--skip-judge` exists for plumbing checks; `agent judge --row` re-scores a subset of an existing run without re-running agents.

## Going deeper

- Full design: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`
- Implementation plan: `docs/superpowers/plans/2026-09-04-agent-coding-benchmark.md`
- Module map: `modelman/CLAUDE.md` (Benchmark subsystem)
- Single-turn speed benchmarks (the fast triage tool this harness doesn't replace): [05-benchmarks](05-benchmarks.md)
```

- [ ] **Step 2: Cross-link from `benchmarks/README.md`**

Add a new section (after the existing "Quick start" section):
```markdown
## Agentic coding benchmarks

`modelman benchmark agent` runs a real coding task through the `pi` agent
across a model/thinking/route matrix and grades the result on deterministic
gates plus a blind LLM rubric — see [docs/guides/09-agent-benchmarks.md](../docs/guides/09-agent-benchmarks.md).

- **`tasks/`** — task bundles (`day31-drift` ships first)
- **`suites/`** — suite TOML files (`smoke.toml`, `q4-agent-sweep.toml`)
```

- [ ] **Step 3: Cross-link from `docs/guides/05-benchmarks.md`**

In its "## Going deeper" section, add one line:
```markdown
- Agentic (not single-turn) coding benchmarks — real task, gates + judge: [09-agent-benchmarks](09-agent-benchmarks.md)
```

- [ ] **Step 4: Update the root `CLAUDE.md`**

Under `## Commands`, add:
```markdown
- `modelman benchmark agent run --suite <path>` — agentic coding benchmark (real task, gates + judge); see `docs/guides/09-agent-benchmarks.md`
```

Under `## Architecture`, in the `modelman/` bullet, append a clause: `; the agentic coding benchmark (\`benchmark/agent/\`) is a separate module tree under the same package`.

In the guide-staleness bullet under `## Key Gotchas` (the one starting "Guide docs embed live `litellm_exposed` snapshots"), append a sentence: `Guide 09 is exempt — it carries no exposure snapshots.`

- [ ] **Step 5: Update `modelman/CLAUDE.md`**

Under `### Benchmark subsystem`, add a new bullet after the existing `workloads/` bullet:
```markdown
- `src/modelman/benchmark/agent/` — the agentic coding benchmark (`modelman benchmark agent`): `suite.py` (TOML parsing + row expansion + preflight), `task.py` (task bundle loading), `workspace.py` (scratch git repo per row), `pidriver.py` (route resolution, pi process driver, speed metrics), `gates.py` (nine-gate deterministic taxonomy + composite cap), `judge.py` (blind LLM rubric judge), `report.py` (artifact writes + summary tables), `runner.py` (phase orchestration + isolation loop), `cli.py` (`run`/`list-tasks`/`list-suites`/`show`/`judge`). Design: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`.
- Tests: `tests/benchmark/agent/` (one file per module above, plus `fixtures/fake_agent.py` and `fixtures/tasks/mini-drift/`).
```

- [ ] **Step 6: Check links and commit**

Run: `./bin/check-links` (from the monorepo root) — verifies every new cross-link resolves.

```bash
git add docs/guides/09-agent-benchmarks.md benchmarks/README.md docs/guides/05-benchmarks.md CLAUDE.md modelman/CLAUDE.md
git commit -m "docs(agent-bench): add guide 09 + cross-links - completes plan item #23"
```

### Task 24: Full verification — `make test-all`, lint, and a live smoke run

**Files:** None new — this task runs checks and, if a live smoke run is performed, updates the two `<!-- UNVERIFIED -->` blocks in `docs/guides/09-agent-benchmarks.md` with real captured output (matching guide 05's existing convention for exactly this situation).

- [ ] **Step 1: Run the full modelman suite**

```bash
cd modelman
uv run pytest -q
uv run ruff check src/ tests/
uv run mypy src/
```
Expected: all pass. Fix any lint/type issues introduced by this plan's modules before proceeding (in particular, `dict`/`list` mutable defaults, unused imports pulled in during the incremental edits across Tasks 8–21, and the `noqa: B008` markers on Typer `Option(...)` defaults matching the existing `benchmark/cli.py` convention).

- [ ] **Step 2: Run the monorepo-wide checks**

```bash
cd ..
make lint-shell
make check-links
```
Expected: both pass — `check-links` in particular validates every link Task 23 added.

- [ ] **Step 3: Confirm the CLI is mounted and discoverable end-to-end**

```bash
cd modelman
uv run modelman benchmark agent list-tasks --root ../benchmarks/tasks
uv run modelman benchmark agent list-suites --root ../benchmarks/suites
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run
```
Expected: `day31-drift` listed with its hidden-file count; both suites listed with their row counts; the dry-run prints exactly one resolved row for `smoke.toml`.

- [ ] **Step 4: Live smoke run (required — capture the real output)**

This is the plan's only end-to-end proof that route resolution, `models.json` generation, the pi process driver, gates, judging, and artifact writes agree on a real run. Costs one fast cloud agent row plus one judge call.

```bash
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml
uv run modelman benchmark agent show --latest
```

Paste the real output into `docs/guides/09-agent-benchmarks.md`'s two `<!-- UNVERIFIED -->` blocks (TL;DR and Verification), replacing the comment with the actual command output, exactly as guide 05 does for its own not-yet-run-live sections. Do not hand-edit or beautify the captured text — its value is that it is what the harness really printed. If the run cannot complete (no LiteLLM key, proxy down), record the *failure* honestly in the guide's place and leave the markers, and say so in the final report — do not fabricate output.

- [ ] **Step 5: Run `make test-all` from the monorepo root**

```bash
cd ..
make test-all
```
Expected: PASS — this is the umbrella target (lint + modelman `make check`/`make test` + wt `go build`/`vet`/`test`) and is CLAUDE.md's own definition of "done" for a change touching `modelman/`.

One thing to watch here, specific to this plan: the new agent-bench tests exercise `git init`/`git worktree` and `git -c`-free `subprocess` calls inside the OS temp dir. If the sandbox that runs `make test-all` denies `git` writes anywhere but the repo, these are the tests that fail first — report that as an environment limitation rather than "fixing" it by skipping.

- [ ] **Step 6: Final commit**

```bash
git add -A
git status   # confirm only expected files are staged before committing
git commit -m "docs(agent-bench): capture live smoke run output - completes plan item #24"
```
(Skip this commit entirely if Step 4 was skipped and nothing changed.)

---
