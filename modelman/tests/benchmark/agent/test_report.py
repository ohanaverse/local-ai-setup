"""Tests for modelman.benchmark.agent.report — artifact writes and
run.toml. Key masking is the one test that matters most here: a leaked
LiteLLM API key in a committed or shared run.toml is a credential leak,
not just a bug (spec: "run.toml and all logs mask key values").
"""

import gzip
import json

from modelman.benchmark.agent.gates import GateResult, GatesReport
from modelman.benchmark.agent.judge import JudgeOutcome, JudgeScore
from modelman.benchmark.agent.pidriver import AgentMetrics
from modelman.benchmark.agent.report import (
    RowReport,
    compute_pareto_stars,
    render_summary,
    write_judge_json,
    write_metrics_jsonl,
    write_row_artifacts,
    write_run_toml,
)


def _gates() -> GatesReport:
    return GatesReport(
        results=[GateResult(gate_number=i, name=f"G{i}", outcome="pass") for i in range(1, 10)],
        hidden_pass=6,
        hidden_total=6,
        hidden_evaluated=True,
        cap=1.0,
    )


def _metrics() -> AgentMetrics:
    return AgentMetrics(
        requests=2, turns=1, gen_seconds=1.0, input_tok=100, output_tok=50, cache_read_tok=0,
        cache_write_tok=0, reasoning_tok=0, tool_call_count=3, ttfts_ms=[100.0],
        ttft_first_ms=100.0, ttft_subseq_median_ms=100.0, wall_seconds=1.0,
        final_text="done", anomaly="", cold_first_token=False, thinking_off_reasoning=False,
    )


def test_write_row_artifacts_writes_expected_files(tmp_path):
    row_dir = tmp_path / "row1"
    write_row_artifacts(
        row_dir,
        events=[{"type": "session", "version": 3, "id": "s", "_ts": 0.0}],
        diff_raw="diff --git a/tmp/xyz/f.py b/tmp/xyz/f.py\n",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
    )
    assert (row_dir / "agent.jsonl.gz").exists()
    with gzip.open(row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["type"] == "session"
    assert (row_dir / "diff.raw.patch").read_text(encoding="utf-8").startswith("diff --git a/tmp/xyz")
    assert "xyz" not in (row_dir / "diff.patch").read_text(encoding="utf-8")
    assert json.loads((row_dir / "gates.json").read_text(encoding="utf-8"))["cap"] == 1.0
    assert json.loads((row_dir / "metrics.json").read_text(encoding="utf-8"))["wall_seconds"] == 1.0
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



def _judge_outcome(total: int, verdict: str = "principled_fix") -> JudgeOutcome:
    score = JudgeScore(
        scores={"root_cause": total, "approach": 0, "test_quality": 0, "scope": 0, "coherence": 0},
        total=total, verdict=verdict, flags=[], rationale="", raw_text="{}",
    )
    return JudgeOutcome(status="scored", samples=[score], combined=score, attempts_used=1)


def _row(label, *, wall_ms, composite, gates=None, judge=None) -> RowReport:
    # wall_ms keeps the call sites readable; AgentMetrics stores seconds
    metrics = AgentMetrics(
        requests=1, turns=1, gen_seconds=wall_ms / 1000.0, input_tok=10, output_tok=5,
        cache_read_tok=0, cache_write_tok=0, reasoning_tok=0, tool_call_count=0,
        ttfts_ms=[10.0], ttft_first_ms=10.0, ttft_subseq_median_ms=10.0,
        wall_seconds=wall_ms / 1000.0, final_text="", anomaly="",
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


def test_render_summary_lists_every_derived_anomaly():
    """The Anomalies table is the only place a reader learns a row is not
    comparable to its partner row, so each flag compute_metrics can derive
    must surface there — including the thinking=off reasoning leak observed
    live, and the CACHE_ANOMALY/COLD_FIRST_TOKEN pair that rides in the single
    `anomaly` string.

    "thinking no-op" is deliberately absent: whether --thinking has any effect
    is a comparison across the off/high rows of a suite, not a property of one
    row's event stream, so it belongs in the guide's analysis rather than a
    per-row flag that would silently assert something unobservable."""
    row = _row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80))
    row.metrics.thinking_off_reasoning = True
    row.metrics.reasoning_tok = 11
    row.metrics.cold_first_token = True
    row.metrics.anomaly = "CACHE_ANOMALY+COLD_FIRST_TOKEN"
    anomalies = render_summary("run-1", [row]).split("## Anomalies")[1]
    assert "thinking=off but 11 reasoning tokens" in anomalies
    assert "CACHE_ANOMALY+COLD_FIRST_TOKEN" in anomalies


def test_write_metrics_jsonl_one_line_per_row(tmp_path):
    rows = [_row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80)), _row("r2", wall_ms=2000, composite=50)]
    path = tmp_path / "metrics.jsonl"
    write_metrics_jsonl(path, rows)
    lines = path.read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    assert json.loads(lines[0])["label"] == "r1"
    assert json.loads(lines[0])["composite"] == 80


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


def test_write_judge_json_leaves_other_artifacts_alone(tmp_path):
    """The re-judge path must not disturb agent.jsonl.gz — the bug this helper
    exists to prevent was write_row_artifacts(events=[]) truncating it."""
    row_dir = tmp_path / "row3"
    write_row_artifacts(
        row_dir,
        events=[{"type": "agent_settled", "_ts": 1.0}],
        diff_raw="",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
    )
    write_judge_json(row_dir, _judge_outcome(80))
    with gzip.open(row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["type"] == "agent_settled"
    assert json.loads((row_dir / "judge.json").read_text(encoding="utf-8"))["combined"]["total"] == 80


def test_summary_lists_error_reasons():
    """A row with no data must say why: an outcome code alone leaves the
    operator guessing between a missing model file and a refused task."""
    reports = [
        RowReport(label="bad", model_id="ollama/y", thinking="off", route="litellm",
                  gates=None, metrics=None, judge=None, composite=None, closing_message="",
                  error="failed to isolate ollama: llamacpp did not come back up"),
    ]
    md = render_summary("run1", reports)
    assert "## Errors" in md
    assert "llamacpp did not come back up" in md


def test_summary_omits_errors_section_when_clean():
    md = render_summary("run1", [_row("good", wall_ms=1000.0, composite=0.8)])
    assert "## Errors" not in md


def test_judge_json_includes_the_failure_reason(tmp_path):
    from modelman.benchmark.agent.judge import JudgeOutcome

    row_dir = tmp_path / "rowj"
    write_judge_json(
        row_dir,
        JudgeOutcome(status="judge_fail", samples=[], combined=None, attempts_used=2,
                     error='HTTP 404: no deployment for model "opus-4"'),
    )
    data = json.loads((row_dir / "judge.json").read_text(encoding="utf-8"))
    assert "no deployment" in data["error"]
