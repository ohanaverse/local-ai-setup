import json
from datetime import UTC, datetime

import pytest

from llmbench.benchmark.results import (
    BenchmarkRun,
    RunDirError,
    TargetResult,
    run_dir,
    write_results,
)
from llmbench.benchmark.workloads.base import BenchmarkMetrics


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


@pytest.mark.parametrize(
    "run_id",
    [
        "20260905-143200",  # throughput and agent runners: %Y%m%d-%H%M%S
        "eval-20260905-143200",  # eval runner
        "eval-20260905-143200-2",  # eval runner, second run in the same second
        "run-2026-01-01",
        "my.run_1",
        "20260905-143200/",  # trailing slash from shell tab-completion is stripped
    ],
)
def test_run_dir_joins_an_id_a_runner_can_produce(tmp_path, run_id):
    expected = tmp_path / run_id.rstrip("/")
    assert run_dir(tmp_path, run_id) == expected


@pytest.mark.parametrize(
    "run_id, expected_msg",
    [
        ("", "error: invalid --run-id ''"),
        (".", "error: invalid --run-id '.'"),
        ("..", "error: invalid --run-id '..'"),
        ("../x", "error: invalid --run-id '../x' (hidden directory names not allowed)"),
        ("../../x", "error: invalid --run-id '../../x' (hidden directory names not allowed)"),
        ("a/b", "error: invalid --run-id 'a/b'"),
        ("/etc", "error: invalid --run-id '/etc'"),
        ("a\\b", "error: invalid --run-id 'a\\\\b'"),
        ("a b", "error: invalid --run-id 'a b'"),
        ("a\n", "error: invalid --run-id 'a\\n'"),
        ("~", "error: invalid --run-id '~'"),
        ("...", "error: invalid --run-id '...' (hidden directory names not allowed)"),
        (".git", "error: invalid --run-id '.git' (hidden directory names not allowed)"),
        (".config", "error: invalid --run-id '.config' (hidden directory names not allowed)"),
    ],
)
def test_run_dir_refuses_invalid_ids(tmp_path, run_id, expected_msg):
    """The id is joined onto results_dir, so it must name a child of it."""
    with pytest.raises(RunDirError) as excinfo:
        run_dir(tmp_path, run_id)
    assert str(excinfo.value) == expected_msg


def test_run_dir_refuses_overlong_id(tmp_path):
    """Run IDs exceeding 255 bytes (filesystem limit) are refused."""
    long_id = "a" * 256  # 256 bytes > 255 limit
    with pytest.raises(RunDirError) as excinfo:
        run_dir(tmp_path, long_id)
    assert "exceeds 255 byte filename limit" in str(excinfo.value)
