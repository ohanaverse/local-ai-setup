import json
from datetime import UTC, datetime

import pytest
import typer

from llmbench.benchmark.results import BenchmarkRun, TargetResult, run_dir, write_results
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
        "...",  # odd, but a plain child name: it cannot leave results_dir
    ],
)
def test_run_dir_joins_an_id_a_runner_can_produce(tmp_path, run_id):
    assert run_dir(tmp_path, run_id) == tmp_path / run_id


@pytest.mark.parametrize(
    "run_id",
    [
        "",
        ".",
        "..",
        "../x",
        "../../x",
        "a/b",
        "/etc",
        "a\\b",
        "a b",
        "a\n",
        "~",
        # What shell tab-completion of a run directory produces. Refused on
        # purpose, not stripped: eval show/judge always refused it, and the
        # other three commands now match them.
        "20260905-143200/",
    ],
)
def test_run_dir_refuses_an_id_that_is_not_one_plain_directory_name(tmp_path, capsys, run_id):
    """#277: the id is joined onto results_dir, so it must name a child of it.
    The refusal is the usage error `eval show` has always printed."""
    with pytest.raises(typer.Exit) as excinfo:
        run_dir(tmp_path, run_id)
    assert excinfo.value.exit_code == 1
    assert capsys.readouterr().err == f"error: invalid --run-id {run_id!r}\n"
