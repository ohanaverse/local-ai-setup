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
