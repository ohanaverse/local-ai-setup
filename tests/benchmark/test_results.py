import json
from datetime import UTC, datetime

from modelman.benchmark.results import BenchmarkRun, TargetResult, write_results
from modelman.benchmark.workloads.base import BenchmarkMetrics


def test_write_results_creates_json_and_markdown(tmp_path):
    run = BenchmarkRun(
        run_id="20260905-143200",
        workload_name="chat",
        started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=UTC),
        results=[
            TargetResult(
                model_id="ollama/ornith-1.5:35b",
                provider_id="ollama",
                route="direct",
                pass_number=1,
                metrics=BenchmarkMetrics(
                    ttft_ms=100, total_ms=500, completion_tokens=100, prompt_tokens=10
                ),
            )
        ],
    )
    write_results(run, tmp_path)

    json_path = tmp_path / "20260905-143200" / "results.json"
    md_path = tmp_path / "20260905-143200" / "summary.md"
    payload_path = tmp_path / "20260905-143200" / "payload.json"
    assert json_path.exists()
    assert md_path.exists()
    assert payload_path.exists()

    data = json.loads(json_path.read_text())
    assert data["run_id"] == "20260905-143200"
    assert data["results"][0]["route"] == "direct"

    md = md_path.read_text()
    assert "ollama/ornith-1.5:35b" in md
    assert "100" in md
