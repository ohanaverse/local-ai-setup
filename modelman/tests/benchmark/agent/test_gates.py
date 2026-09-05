"""Tests for modelman.benchmark.agent.gates — deterministic gate evaluation.

Gate 8 (vacuous-test detection) is, per the spec, "the highest-value check in
the harness" — a model that adds a test which trivially passes on unfixed code
must not be credited with a real regression test. These tests build up the full
nine-gate taxonomy against the mini-drift fixture (fast, controlled) before
Task 11 re-runs the same taxonomy against the real day31-drift bundle with
hand-authored patches.
"""

from pathlib import Path

import pytest

from modelman.benchmark.agent.gates import evaluate
from modelman.benchmark.agent.pidriver import PiRunResult
from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace

FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "tasks"


def _task():
    return load_task(FIXTURE_ROOT / "mini-drift")


def _ok_run() -> PiRunResult:
    return PiRunResult(
        exit_code=0,
        timed_out=False,
        aborted=False,
        seen_message_end=True,
        unparsed_lines=0,
        stderr_tail="",
    )


def _reply_events() -> list[dict]:
    """The minimum a run must show to count as having replied: one assistant
    message_end. Gate 1 looks for it in the event list, which run_pi_process
    returns separately from PiRunResult."""
    return [{"type": "message_end", "message": {"role": "assistant", "usage": {}, "content": []}}]


@pytest.fixture
def workspace():
    ws = create_workspace(_task())
    yield ws
    destroy_workspace(ws)


def test_agent_error_short_circuits_when_agent_did_not_exit_cleanly(workspace):
    """A crashed/incomplete agent process fails gate 1 and every later gate is
    recorded as skipped, not passed — an unevaluated gate must never silently
    count as a pass."""
    bad_run = PiRunResult(
        exit_code=1,
        timed_out=False,
        aborted=False,
        seen_message_end=False,
        unparsed_lines=0,
        stderr_tail="boom",
    )
    report = evaluate(workspace, _task(), bad_run, events=[], session_file_present=True)
    assert report.results[0].outcome == "fail"
    assert report.results[0].code == "AGENT_ERROR"
    assert all(r.outcome == "skipped" for r in report.results[1:])
    assert report.cap == 0.0


def test_timeout_short_circuits(workspace):
    timed_out_run = PiRunResult(
        exit_code=None,
        timed_out=True,
        aborted=False,
        seen_message_end=True,
        unparsed_lines=0,
        stderr_tail="",
    )
    report = evaluate(workspace, _task(), timed_out_run, events=_reply_events(), session_file_present=True)
    assert report.results[1].code == "TIMEOUT"
    assert all(r.outcome == "skipped" for r in report.results[2:])
    assert report.cap == 0.0


def test_no_diff_short_circuits(workspace):
    """An agent that touched nothing at all fails gate 3 before any test
    infrastructure gate runs."""
    report = evaluate(
        workspace, _task(), _ok_run(), events=_reply_events(), session_file_present=True
    )
    assert report.results[2].code == "NO_DIFF"
    assert all(r.outcome == "skipped" for r in report.results[3:])
    assert report.cap == 0.0


def test_broken_build_short_circuits(workspace):
    """Breaking the import (not just failing a test) is caught by gate 4 before
    gate 5 even tries to run the visible suite."""
    (workspace.root / "pkg" / "__init__.py").write_text("this is not valid python(((", encoding="utf-8")
    report = evaluate(
        workspace, _task(), _ok_run(), events=_reply_events(), session_file_present=True
    )
    assert report.results[3].code == "BROKEN_BUILD"
    assert all(r.outcome == "skipped" for r in report.results[4:])
    assert report.cap == 0.25


def _fix_and_add_regression_test(workspace, extra: str = "") -> None:
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
    )
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\nfrom pkg import add_one\n\n"
        "class T(unittest.TestCase):\n    def test_even(self):\n        self.assertEqual(add_one(4), 5)\n"
        + extra,
        encoding="utf-8",
    )


def _evaluate(workspace):
    return evaluate(
        workspace, _task(), _ok_run(), events=_reply_events(), session_file_present=True
    )


def test_visible_regression_does_not_short_circuit(workspace):
    """Breaking a shipped test (without touching its file) fails gate 5 but
    still lets gates 6-7 run — VISIBLE_REGRESSION is diagnostic, not fatal to
    later gates, per the spec."""
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n\n", encoding="utf-8"
    )  # still importable, but now every call is wrong -> shipped test fails
    report = _evaluate(workspace)
    assert report.results[4].code == "VISIBLE_REGRESSION"
    assert report.results[5].outcome != "skipped"  # gate 6 still ran
    assert report.results[6].outcome != "skipped"  # gate 7 still ran


def test_tampered_tests_short_circuits(workspace):
    """Editing the pre-existing shipped test file is TAMPERED_TESTS and does
    short-circuit — gates 7-9 cannot tell a real new test from a doctored old
    one once this has happened."""
    _fix_and_add_regression_test(workspace)
    (workspace.root / "tests" / "test_pkg.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n    def test_noop(self):\n        pass\n",
        encoding="utf-8",
    )
    report = _evaluate(workspace)
    assert report.results[5].code == "TAMPERED_TESTS"
    assert all(r.outcome == "skipped" for r in report.results[6:])
    assert report.cap == 0.0


def test_no_regression_test_when_no_new_test_file_added(workspace):
    """A fix with no accompanying new test file fails gate 7 but is not a
    short-circuit — gate 9 (hidden tests) still has to run on it."""
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
    )
    report = _evaluate(workspace)
    assert report.results[6].code == "NO_REGRESSION_TEST"
    assert report.cap <= 0.85


def test_gate_seven_passes_when_new_test_file_added(workspace):
    _fix_and_add_regression_test(workspace)
    report = _evaluate(workspace)
    assert report.results[6].outcome == "pass"


def test_vacuous_test_detected_when_new_test_passes_on_unfixed_baseline(workspace):
    """A new test that would pass even without the fix is VACUOUS_TEST — the
    spec's highest-value check."""
    # pkg IS fixed here, deliberately. With the real bug still present the
    # all-hidden-failing cap (x0.25) also fires and wins the min(), so the
    # row's cap would no longer isolate the x0.70 vacuous cap this test pins
    # (see the next test for that combination). assertTrue(True) still passes
    # on the unfixed baseline, which is what makes the test vacuous.
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
    )
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n"
        "    def test_trivial(self):\n        self.assertTrue(True)\n",
        encoding="utf-8",
    )
    report = _evaluate(workspace)
    assert report.results[7].code == "VACUOUS_TEST"
    assert "VACUOUS_TEST" in report.triggered_codes, "the cap must not fire on a code report.py never sees"
    assert (report.hidden_pass, report.hidden_total) == (2, 2)  # real fix, hollow test
    assert report.cap == 0.70


def test_cap_is_the_minimum_across_every_triggered_condition(workspace):
    """VACUOUS_TEST (x0.70) and ALL_HIDDEN_FAILING (x0.25) firing on one row
    must yield 0.25 — the minimum across every triggered condition, not
    whichever gate happened to run last (spec: "Worst is explicit")."""
    # pkg left unfixed, and the new test asserts nothing about the bug: both
    # conditions fire together on this row
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n"
        "    def test_trivial(self):\n        self.assertTrue(True)\n",
        encoding="utf-8",
    )
    report = _evaluate(workspace)
    assert "VACUOUS_TEST" in report.triggered_codes
    assert "ALL_HIDDEN_FAILING" in report.triggered_codes
    assert (report.hidden_pass, report.hidden_total) == (0, 2)
    assert report.cap == 0.25, "the stricter of the two caps wins"


def test_hidden_tests_run_after_agent_process_exits_not_during(workspace):
    """A hidden test file is copied in only after the run; a file no model can
    have seen during the run must still be discoverable by the same mechanism
    the visible tests use."""
    _fix_and_add_regression_test(workspace)
    report = _evaluate(workspace)
    assert report.hidden_evaluated is True
    assert report.hidden_total == 2


def test_no_hidden_tests_skips_gate_9(workspace):
    """A bundle with no hidden tests must skip rather than pass, so the
    composite formula's 0.60 weight is not silently earned by nothing."""
    report = _evaluate(workspace)  # NO_DIFF short-circuits before gate 9
    assert report.results[8].outcome == "skipped"
    assert report.hidden_evaluated is False
