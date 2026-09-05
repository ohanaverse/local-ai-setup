"""End-to-end gate evaluation against the real day31-drift bundle, closing
out Phase 3 (spec: gates tests must reach "all nine codes reachable").
Reuses the hand-authored correct-fix and symptom patches from
test_day31_drift_bundle.py so both files stay consistent with the same two
patches rather than drifting apart.
"""

from pathlib import Path

from modelman.benchmark.agent.gates import evaluate
from modelman.benchmark.agent.pidriver import PiRunResult
from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace

from .test_day31_drift_bundle import CORRECT_CALENDARLIB, SYMPTOM_PATCH_BILLING

# parents[4] = monorepo root from modelman/tests/benchmark/agent/ (see Task 4)
TASK_ROOT = Path(__file__).resolve().parents[4] / "benchmarks" / "tasks" / "day31-drift"

REAL_REGRESSION_TEST = (
    "import unittest\nfrom datetime import date\nfrom kettlecomb import add_months\n\n"
    "class T(unittest.TestCase):\n"
    "    def test_feb_boundary(self):\n"
    "        self.assertEqual(add_months(date(2025, 1, 31), 1), date(2025, 2, 28))\n"
)


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
    return [{"type": "message_end", "message": {"role": "assistant", "usage": {}, "content": []}}]


def _evaluate(ws, task):
    return evaluate(ws, task, _ok_run(), events=_reply_events(), session_file_present=True)


def test_correct_fix_with_real_test_passes_every_gate():
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "calendarlib.py").write_text(CORRECT_CALENDARLIB, encoding="utf-8")
        (ws.root / "tests" / "test_regression.py").write_text(REAL_REGRESSION_TEST, encoding="utf-8")
        report = _evaluate(ws, task)
        codes = [r.code for r in report.results]
        assert codes == [None] * 9, f"a passing gate must carry no failure code: {report.results}"
        assert all(r.outcome == "pass" for r in report.results)
        assert (report.hidden_pass, report.hidden_total) == (6, 6)
        assert report.cap == 1.0
    finally:
        destroy_workspace(ws)


def test_symptom_patch_without_a_test_gets_partial_hidden_cap():
    """The symptom patch fixes the two hidden cases that happen to survive the
    buggy add_months by coincidence (a 30-day-month target, and a mid-month
    non-boundary case) but leaves the four February-adjacent cases failing — a
    genuine partial pass, not all-or-nothing.

    This is the spec's own worked example for the cap minimum: NO_REGRESSION_TEST
    (x0.85) and a partial hidden pass (x0.50) firing together must yield 0.50,
    not whichever gate ran last.
    """
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "billing.py").write_text(SYMPTOM_PATCH_BILLING, encoding="utf-8")
        report = _evaluate(ws, task)
        assert report.results[6].code == "NO_REGRESSION_TEST"  # gate 7: no new test file
        assert report.results[8].code == "PARTIAL_HIDDEN_PASS"  # gate 9
        assert (report.hidden_pass, report.hidden_total) == (2, 6)
        assert report.cap == 0.50
    finally:
        destroy_workspace(ws)


def test_agent_error_and_timeout_and_no_diff_reachable_on_real_bundle():
    """Confirms the three earliest short-circuit codes are reachable against the
    real bundle too, not just the mini-drift fixture."""
    task = load_task(TASK_ROOT)

    ws = create_workspace(task)
    try:
        bad_run = PiRunResult(
            exit_code=1,
            timed_out=False,
            aborted=False,
            seen_message_end=False,
            unparsed_lines=0,
            stderr_tail="traceback",
        )
        report = evaluate(ws, task, bad_run, events=[], session_file_present=True)
        assert report.results[0].code == "AGENT_ERROR"
    finally:
        destroy_workspace(ws)

    ws = create_workspace(task)
    try:
        timed_out_run = PiRunResult(
            exit_code=None,
            timed_out=True,
            aborted=False,
            seen_message_end=True,
            unparsed_lines=0,
            stderr_tail="",
        )
        report = evaluate(ws, task, timed_out_run, events=_reply_events(), session_file_present=True)
        assert report.results[1].code == "TIMEOUT"
    finally:
        destroy_workspace(ws)

    ws = create_workspace(task)
    try:
        assert _evaluate(ws, task).results[2].code == "NO_DIFF"
    finally:
        destroy_workspace(ws)


def test_broken_build_reachable_on_real_bundle():
    """Gate 4 against a bundle whose package really is un-importable: the
    day31 suite's import_check names `kettlecomb`, so this also proves the
    gates read the bundle's own gates.toml rather than a hardcoded module."""
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "ledger.py").write_text("def broken(((", encoding="utf-8")
        report = _evaluate(ws, task)
        assert report.results[3].code == "BROKEN_BUILD"
        assert report.cap == 0.25
        assert all(r.outcome == "skipped" for r in report.results[4:])
    finally:
        destroy_workspace(ws)


def test_tampered_and_vacuous_reachable_on_real_bundle():
    """The last two codes in the taxonomy, on the real bundle: editing a shipped
    test file, and adding a new one that asserts nothing."""
    task = load_task(TASK_ROOT)

    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "calendarlib.py").write_text(CORRECT_CALENDARLIB, encoding="utf-8")
        (ws.root / "tests" / "test_ledger.py").write_text(
            "import unittest\n\nclass T(unittest.TestCase):\n    def test_noop(self):\n        pass\n",
            encoding="utf-8",
        )
        report = _evaluate(ws, task)
        assert report.results[5].code == "TAMPERED_TESTS"
        assert report.cap == 0.0
    finally:
        destroy_workspace(ws)

    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "calendarlib.py").write_text(CORRECT_CALENDARLIB, encoding="utf-8")
        (ws.root / "tests" / "test_vacuous.py").write_text(
            REAL_REGRESSION_TEST.replace(
                "self.assertEqual(add_months(date(2025, 1, 31), 1), date(2025, 2, 28))",
                "self.assertTrue(True)",
            ),
            encoding="utf-8",
        )
        report = _evaluate(ws, task)
        assert report.results[7].code == "VACUOUS_TEST"
        assert report.cap == 0.70
        assert (report.hidden_pass, report.hidden_total) == (6, 6)
    finally:
        destroy_workspace(ws)
