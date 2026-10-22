from datetime import UTC, datetime
from unittest.mock import patch

import pytest
from typer.testing import CliRunner

from llmbench.benchmark.errors import BenchmarkError
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
            f"providers failed to restore (saved to {run_dir}): omlx down",
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
        assert "omlx down" in result.output
        assert mock_save.called
        assert state.extra["benchmarks"]["last_run_dir"] == str(run_dir)


def test_run_records_the_run_dir_so_show_results_latest_finds_it(tmp_path):
    """A completed run records its OWN directory in `last_run_dir`, not the
    results base dir: show-results --latest appends `/summary.md` to whatever
    is stored, and write_results puts summary.md in `<base>/<run_id>`.
    Regression: the base dir was stored, so --latest reported "results not
    found: <base>/summary.md" for every completed run."""
    run = BenchmarkRun(
        run_id="20260905-143200",
        workload_name="chat",
        started_at=datetime(2026, 8, 28, 14, 32, 0, tzinfo=UTC),
        results=[],
    )

    with (
        patch("llmbench.benchmark.cli.load_registry"),
        patch("llmbench.benchmark.cli.run_benchmark", return_value=run),
    ):
        runner = CliRunner()
        result = runner.invoke(app, ["run", "--results-dir", str(tmp_path)])
        assert result.exit_code == 0, result.output

        # Where results.write_results puts it: <base>/<run_id>/summary.md.
        run_dir = tmp_path / run.run_id
        run_dir.mkdir(parents=True, exist_ok=True)
        (run_dir / "summary.md").write_text("# summary", encoding="utf-8")

        shown = runner.invoke(app, ["show-results", "--latest"])
        assert shown.exit_code == 0, shown.output
        assert "# summary" in shown.output


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


@pytest.mark.parametrize(
    ("env", "want"),
    [
        ({}, "code"),
        ({"LLMBENCH_WORKLOAD": "long"}, "long"),
        ({"MODELMAN_BENCHMARK_WORKLOAD": "short"}, "short"),
        ({"LLMBENCH_WORKLOAD": "long", "MODELMAN_BENCHMARK_WORKLOAD": "short"}, "long"),
        ({"LLMBENCH_WORKLOAD": "", "MODELMAN_BENCHMARK_WORKLOAD": "short"}, "short"),
    ],
    ids=["flag", "llmbench", "modelman-alias", "llmbench-wins", "empty-is-unset"],
)
def test_run_takes_the_workload_from_the_environment(monkeypatch, env, want):
    """LLMBENCH_WORKLOAD overrides --workload. MODELMAN_BENCHMARK_WORKLOAD,
    the name it had under modelman, keeps working: a wrapper script that
    exports the old name must not silently fall back to the flag's default
    and benchmark a different workload."""
    for name in ("LLMBENCH_WORKLOAD", "MODELMAN_BENCHMARK_WORKLOAD"):
        monkeypatch.delenv(name, raising=False)
    for name, value in env.items():
        monkeypatch.setenv(name, value)
    seen = []

    def _get_workload(name):
        seen.append(name)
        raise BenchmarkError("stop here")

    with patch("llmbench.benchmark.cli.get_workload", _get_workload):
        result = CliRunner().invoke(app, ["run", "--workload", "code"])
    assert result.exit_code == 1
    assert seen == [want]


@pytest.mark.parametrize("run_id", ["..", "../../x"])
def test_show_results_refuses_a_run_id_outside_the_results_dir(tmp_path, run_id):
    """#277: show-results joined --run-id onto the results dir unchecked, so
    `..` printed whatever summary.md sat above it. Same usage error as
    `eval show`, and nothing is read."""
    results_dir = tmp_path / "a" / "b" / "results"
    results_dir.mkdir(parents=True)
    outside = results_dir / run_id
    outside.mkdir(parents=True, exist_ok=True)
    (outside / "summary.md").write_text("OUTSIDE-THE-RESULTS-DIR", encoding="utf-8")

    with patch("llmbench.benchmark.cli.DEFAULT_RESULTS_DIR", results_dir):
        result = CliRunner().invoke(app, ["show-results", "--run-id", run_id])

    assert result.exit_code == 1, result.output
    assert f"error: invalid --run-id {run_id!r}" in result.output
    assert "OUTSIDE-THE-RESULTS-DIR" not in result.output


def test_show_results_prints_the_summary_for_a_valid_run_id(tmp_path):
    run_dir = tmp_path / "20260905-143200"
    run_dir.mkdir()
    (run_dir / "summary.md").write_text("# summary", encoding="utf-8")

    with patch("llmbench.benchmark.cli.DEFAULT_RESULTS_DIR", tmp_path):
        result = CliRunner().invoke(app, ["show-results", "--run-id", "20260905-143200"])

    assert result.exit_code == 0, result.output
    assert "# summary" in result.output
