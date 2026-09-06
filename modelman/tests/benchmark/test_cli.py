from datetime import UTC, datetime
from unittest.mock import patch

from typer.testing import CliRunner

from modelman.benchmark.results import BenchmarkRun
from modelman.benchmark.runner import RunSavedButRestoreFailed
from modelman.main import app


def test_benchmark_list_workloads():
    runner = CliRunner()
    result = runner.invoke(app, ["benchmark", "list-workloads"])
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
        raise RunSavedButRestoreFailed(
            f"providers failed to restore (saved to {run_dir}): llamacpp down",
            run_dir=run_dir,
            run=run,
        )

    with (
        patch("modelman.benchmark.cli.load_registry"),
        patch("modelman.benchmark.cli.load_state") as mock_state,
        patch("modelman.benchmark.cli.save_state") as mock_save,
        patch("modelman.benchmark.cli.run_benchmark", _raise),
    ):
        state = mock_state.return_value
        state.extra = {}

        runner = CliRunner()
        result = runner.invoke(
            app,
            ["benchmark", "run", "--results-dir", str(tmp_path)],
        )
        assert result.exit_code == 1
        assert "llamacpp down" in result.output
        assert mock_save.called
        assert state.extra["benchmarks"]["last_run_dir"] == str(run_dir)
