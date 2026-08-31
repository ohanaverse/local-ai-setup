from datetime import UTC, datetime
from unittest.mock import patch

from typer.testing import CliRunner

from modelman.benchmark.results import BenchmarkRun
from modelman.main import app


def test_run_saves_latest_state_pointer(tmp_path):
    with (
        patch("modelman.benchmark.cli.load_registry"),
        patch("modelman.benchmark.cli.load_state") as mock_state,
        patch("modelman.benchmark.cli.save_state") as mock_save,
        patch("modelman.benchmark.cli.run_benchmark") as mock_run,
    ):
        mock_run.return_value = BenchmarkRun(
            run_id="20260905-143200",
            workload_name="chat",
            started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=UTC),
            results=[],
        )
        state = mock_state.return_value
        state.extra = {}

        runner = CliRunner()
        result = runner.invoke(
            app,
            ["benchmark", "run", "--results-dir", str(tmp_path)],
        )
        assert result.exit_code == 0, result.output
        assert mock_save.called
        assert state.extra["benchmarks"]["last_run"] == "2026-08-28T14:32:00+00:00"
