"""Tests for modelman.benchmark.eval.evalplus_runner — the subprocess
wrapper around EvalPlus for the `coding` category. No real EvalPlus
invocation here (see plan Task 12 for the live-verification step); `run_cmd`
is injected so these tests exercise only the command construction and
pass@1 parsing. Fake stdout strings use a literal tab between "pass@1:" and
the value, matching the real `evalplus==0.3.1` output confirmed live in
Task 12 (`cprint(f"{k}:\\t{v:.3f}", ...)` in evalplus/evaluate.py).
"""

from modelman.benchmark.eval.evalplus_runner import (
    _build_command,
    _parse_pass_at_1,
    run_coding_category,
)

# Real EvalPlus output prints the base-tests-only block first, then (when
# the "+" tests also pass) a second "+" block with a different, usually
# lower, number.
_TWO_BLOCK_STDOUT = (
    "humaneval (base tests)\npass@1:\t0.732\nhumaneval+ (base + extra tests)\npass@1:\t0.658\n"
)


class _FakeCompletedProcess:
    def __init__(self, returncode: int, stdout: str, stderr: str = ""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


def test_run_coding_category_prefers_plus_block_pass_at_1_over_base():
    # Regression test for reporting the base-only pass@1 (0.732) instead of
    # the "+" (base + extra tests) pass@1 (0.658): EvalPlus prints BOTH
    # blocks in a real run, and the whole point of using EvalPlus over
    # vanilla HumanEval is the hardened "+" grading — reporting the base
    # score under this project's "coding" label would be misleadingly
    # optimistic. Asserts the "+" value (0.658) wins, not the first-matched
    # base value (0.732).
    def fake_run(cmd, **kwargs):
        assert "--base-url" in cmd
        assert "http://localhost:8000/v1" in cmd
        assert "--model" in cmd
        assert "server-name" in cmd
        return _FakeCompletedProcess(0, _TWO_BLOCK_STDOUT)

    result = run_coding_category(
        base_url="http://localhost:8000/v1",
        model="server-name",
        api_key="ollama",
        dataset="humaneval",
        limit=5,
        run_cmd=fake_run,
    )
    assert result.pass_at_1 == 0.658
    assert result.error is None


def test_parse_pass_at_1_falls_back_to_base_when_no_plus_block():
    # When EvalPlus prints only the base-tests block (e.g. a --base-only
    # run, or a dataset with no "+" extra-tests variant), there is no "+"
    # score to prefer — the parser must fall back to the base block's
    # pass@1 rather than returning None.
    stdout = "humaneval (base tests)\npass@1:\t0.732\n"
    assert _parse_pass_at_1(stdout) == 0.732


def test_build_command_uses_installed_console_script_not_uvx():
    # Regression test: _build_command must invoke the `evalplus.evaluate`
    # console script installed via modelman's own `[eval]` extra (resolved
    # on PATH from within `uv run`'s venv), not `uvx --from evalplus` — uvx
    # runs EvalPlus in a separate ephemeral environment, bypassing the
    # `[eval]` extra that's the deliberate opt-in gate for local code
    # execution (EvalPlus executes model-generated code).
    cmd = _build_command(
        dataset="humaneval",
        base_url="http://localhost:8000/v1",
        model="server-name",
        limit=None,
        workdir="/tmp/x",
    )
    assert cmd[0] == "evalplus.evaluate"
    assert "uvx" not in cmd


def test_build_command_includes_greedy_flag():
    # Tests that _build_command always passes --greedy. Live verification (plan
    # Task 12) showed that without it, evalplus's default temperature=0.0 combined
    # with do_sample=True (the non-greedy default) trips OpenAIChatDecoder's own
    # "Temperature must be positive for sampling" assertion — every real invocation
    # failed immediately before this flag was added, so its presence is load-bearing.
    cmd = _build_command(
        dataset="humaneval",
        base_url="http://localhost:8000/v1",
        model="server-name",
        limit=None,
        workdir="/tmp/x",
    )
    assert "--greedy" in cmd


def test_build_command_maps_limit_to_id_range_not_n_samples():
    # Tests that a configured `limit` becomes --id-range 0,<limit>, not --n-samples.
    # Live verification (plan Task 12) showed --n-samples is EvalPlus's
    # samples-PER-PROBLEM knob (for pass@10/pass@100), not a dataset-subset size —
    # mapping `limit` there would multiply generation calls across the full
    # dataset instead of shrinking it, and risks an uncaught subprocess timeout.
    cmd = _build_command(
        dataset="humaneval",
        base_url="http://localhost:8000/v1",
        model="server-name",
        limit=5,
        workdir="/tmp/x",
    )
    assert "--n-samples" not in cmd
    assert "--id-range" in cmd
    assert cmd[cmd.index("--id-range") + 1] == "0,5"


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
        return _FakeCompletedProcess(0, "humaneval (base tests)\npass@10:\t0.532\npass@1:\t0.732\n")

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
