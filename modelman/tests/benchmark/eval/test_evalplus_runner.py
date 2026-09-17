"""Tests for modelman.benchmark.eval.evalplus_runner — the subprocess
wrapper around EvalPlus for the `coding` category. No real EvalPlus
invocation here (see plan Task 11 for the live-verification step); `run_cmd`
is injected so these tests exercise only the command construction and
pass@1 parsing.
"""

from modelman.benchmark.eval.evalplus_runner import run_coding_category


class _FakeCompletedProcess:
    def __init__(self, returncode: int, stdout: str, stderr: str = ""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def test_run_coding_category_parses_pass_at_1_from_stdout():
    # Tests that the runner correctly extracts pass@1 ratio from EvalPlus stdout.
    # Ensures the regex parsing and float conversion work correctly on valid output.
    def fake_run(cmd, **kwargs):
        assert "--base-url" in cmd
        assert "http://localhost:8000/v1" in cmd
        assert "--model" in cmd
        assert "server-name" in cmd
        return _FakeCompletedProcess(0, "humaneval (base tests)\npass@1: 0.732\n")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="server-name",
        api_key="ollama",
        dataset="humaneval",
        limit=5,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 == 0.732
    assert result.error is None


def test_run_coding_category_reports_nonzero_exit_as_error():
    # Tests that nonzero exit codes are captured as errors with stderr included.
    # Important for detecting subprocess failures or EvalPlus internal errors.
    def fake_run(cmd, **kwargs):
        return _FakeCompletedProcess(1, "", "connection refused")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="m",
        api_key="k",
        dataset="humaneval",
        limit=None,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 is None
    assert "exited 1" in result.error


def test_run_coding_category_reports_unparseable_output_as_error():
    # Tests that successful exit (code 0) but unparseable output is treated as error.
    # Ensures partial/malformed EvalPlus output doesn't lead to silent failures.
    def fake_run(cmd, **kwargs):
        return _FakeCompletedProcess(0, "no pass rate printed here")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="m",
        api_key="k",
        dataset="humaneval",
        limit=None,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 is None
    assert "could not parse" in result.error


def test_run_coding_category_regex_boundary_avoids_pass_at_n_false_positive():
    # Tests that regex boundary correctly rejects pass@10 and extracts pass@1.
    # Ensures multiple pass@X metrics don't cause false positives from higher-order metrics.
    def fake_run(cmd, **kwargs):
        return _FakeCompletedProcess(0, "humaneval (base tests)\npass@10: 0.532\npass@1: 0.732\n")

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="m",
        api_key="k",
        dataset="humaneval",
        limit=None,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 == 0.732
    assert result.error is None
