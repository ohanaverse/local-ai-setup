from datetime import UTC, datetime
from unittest.mock import patch

from typer.testing import CliRunner

from llmbench.benchmark.results import BenchmarkRun
from llmbench.benchmark.runner import WorkloadRunSavedButRestoreFailed
from llmbench.main import app


def test_benchmark_list_workloads():
    runner = CliRunner()
    result = runner.invoke(app, ["list-workloads"])
    assert result.exit_code == 0
    assert "chat" in result.output


def test_run_records_latest_even_when_restore_failed(tmp_path):
    """A restore-failed run still records the --latest pointer and exits
    non-zero, so show-results --latest can find the surviving run."""
    run_dir = tmp_path / "20260905-143200"
    run = BenchmarkRun(
        run_id="20260905-143200",
        workload_name="chat",
        started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=UTC),
        results=[],
    )

    def _raise(*args, **kwargs):
        raise WorkloadRunSavedButRestoreFailed(
            f"providers failed to restore (saved to {run_dir}): llamacpp down",
            run_dir=run_dir,
            run=run,
        )

    with (
        patch("llmbench.benchmark.cli.load_registry"),
        patch("llmbench.benchmark.cli.load_state") as mock_state,
        patch("llmbench.benchmark.cli.save_state") as mock_save,
        patch("llmbench.benchmark.cli.run_benchmark", _raise),
    ):
        state = mock_state.return_value
        state.extra = {}

        runner = CliRunner()
        result = runner.invoke(
            app,
            ["run", "--results-dir", str(tmp_path)],
        )
        assert result.exit_code == 1
        assert "llamacpp down" in result.output
        assert mock_save.called
        assert state.extra["benchmarks"]["last_run_dir"] == str(run_dir)


def test_run_without_model_or_family_exits_2():
    """#179: no exposed-models default — `benchmark run` with neither --model
    nor --family is a usage error (exit 2, the runner's message), and nothing
    is isolated."""
    from llmbench.registry import Registry
    from llmbench.state import StateStore

    def _no_isolate(*_args, **_kwargs):
        raise AssertionError("nothing may be isolated without a selection")

    with (
        patch("llmbench.benchmark.cli.load_registry", return_value=Registry()),
        patch("llmbench.benchmark.cli.load_state", return_value=StateStore()),
        patch("llmbench.benchmark.runner.isolate_provider", _no_isolate),
    ):
        result = CliRunner().invoke(app, ["run"])
    assert result.exit_code == 2, result.output
    assert "error: name models (--model) or pass --family" in result.output
