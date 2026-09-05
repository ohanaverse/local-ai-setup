"""Tests for modelman.benchmark.agent.report — artifact writes and
run.toml. Key masking is the one test that matters most here: a leaked
LiteLLM API key in a committed or shared run.toml is a credential leak,
not just a bug (spec: "run.toml and all logs mask key values").
"""

import gzip
import json

from modelman.benchmark.agent.gates import GateResult, GatesReport
from modelman.benchmark.agent.pidriver import AgentMetrics
from modelman.benchmark.agent.report import write_row_artifacts, write_run_toml


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
