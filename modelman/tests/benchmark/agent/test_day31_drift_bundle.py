"""Integration test for the day31-drift task bundle itself (not the harness
code) — hand-verifies the spec's Phase 1 claims: a correct fix clears every
hidden test, a symptom patch does not, and the vacuous-test mechanism (a
git worktree at the baseline commit) correctly tells a real regression test
apart from one that trivially passes on unfixed code.

This is the phase gate for the bundle content and the workspace primitives,
so a broken task is caught here rather than after a multi-hour model sweep.
"""

import json
import subprocess
import sys
from pathlib import Path

from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace

# parents[4] is the monorepo root from modelman/tests/benchmark/agent/; the CI
# job runs with working-directory=modelman, so the root must come from this
# file's location, never from Path.cwd().
TASK_ROOT = Path(__file__).resolve().parents[4] / "benchmarks" / "tasks" / "day31-drift"

CORRECT_CALENDARLIB = '''"""Calendar arithmetic for kettlecomb billing cycles."""

from __future__ import annotations

from datetime import date


def month_length(year: int, month: int) -> int:
    if month == 2:
        is_leap = year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
        return 29 if is_leap else 28
    if month in (4, 6, 9, 11):
        return 30
    return 31


def add_months(d: date, n: int) -> date:
    total = d.month - 1 + n
    year = d.year + total // 12
    month = total % 12 + 1
    day = min(d.day, month_length(year, month))
    return date(year, month, day)
'''

SYMPTOM_PATCH_BILLING = '''"""Billing calculations for kettlecomb seat credits."""

from __future__ import annotations

from .calendarlib import month_length


def prorate(anchor_day: int, year: int, month: int, credits_per_cycle: float) -> float:
    length = month_length(year, month)
    effective_day = min(anchor_day, 28)  # symptom patch: masks the crash, add_months still buggy
    days_active = length - effective_day + 1
    return round(credits_per_cycle * days_active / length, 4)
'''

_RUNNER_SCRIPT = (
    "import json, sys, unittest\n"
    "loader = unittest.defaultTestLoader\n"
    "suite = loader.loadTestsFromName(sys.argv[1])\n"
    "runner = unittest.TextTestRunner(stream=sys.stderr, verbosity=0)\n"
    "result = runner.run(suite)\n"
    "json.dump({'total': result.testsRun, 'failures': len(result.failures), "
    "'errors': len(result.errors)}, sys.stdout)\n"
)


def _run_test_module(root: Path, module_name: str) -> tuple[int, int, int]:
    result = subprocess.run(
        [sys.executable, "-c", _RUNNER_SCRIPT, module_name],
        cwd=root,
        capture_output=True,
        text=True,
    )
    counts = json.loads(result.stdout)
    return counts["total"], counts["failures"], counts["errors"]


def test_correct_fix_clears_all_hidden_tests():
    """A hand-authored correct patch (proper last-day clamping, no
    try/except) must pass all 6 hidden tests — the harness's own proof that
    the intended fix actually satisfies the hidden suite."""
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "calendarlib.py").write_text(CORRECT_CALENDARLIB, encoding="utf-8")
        ws.seed_hidden(task)
        assert _run_test_module(ws.root, "tests.test_day31") == (6, 0, 0)
    finally:
        destroy_workspace(ws)


def test_symptom_patch_fails_hidden_tests():
    """A hand-authored symptom patch (clamps in billing.prorate, leaves the
    real bug in calendarlib.add_months untouched) must fail at least one
    hidden test — proves the hidden suite discriminates a plausible-looking
    wrong answer from the real fix."""
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "billing.py").write_text(SYMPTOM_PATCH_BILLING, encoding="utf-8")
        ws.seed_hidden(task)
        total, failures, errors = _run_test_module(ws.root, "tests.test_day31")
        assert total == 6
        assert failures + errors >= 1
    finally:
        destroy_workspace(ws)


def test_vacuous_regression_test_passes_on_unfixed_baseline():
    """A trivial 'test' that asserts nothing about the bug still passes on
    the unfixed baseline — exactly what spec gate 8 exists to catch."""
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    dest = ws.root.parent / "baseline-check-vacuous"
    trivial_test = (
        "import unittest\n\nclass T(unittest.TestCase):\n"
        "    def test_true(self):\n        self.assertTrue(True)\n"
    )
    try:
        (ws.root / "tests" / "test_vacuous.py").write_text(trivial_test, encoding="utf-8")
        ws.checkout_baseline_worktree(dest)
        (dest / "tests" / "test_vacuous.py").write_text(trivial_test, encoding="utf-8")
        assert _run_test_module(dest, "tests.test_vacuous") == (1, 0, 0)
    finally:
        ws.remove_worktree(dest)
        destroy_workspace(ws)


def test_real_regression_test_fails_on_unfixed_baseline():
    """A real regression test (asserts the Feb-boundary fix) fails against
    the unfixed baseline — the not-vacuous case gate 8 must let through."""
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    dest = ws.root.parent / "baseline-check-real"
    real_test = (
        "import unittest\nfrom datetime import date\nfrom kettlecomb import add_months\n\n"
        "class T(unittest.TestCase):\n"
        "    def test_feb_boundary(self):\n"
        "        self.assertEqual(add_months(date(2025, 1, 31), 1), date(2025, 2, 28))\n"
    )
    try:
        (ws.root / "tests" / "test_regression.py").write_text(real_test, encoding="utf-8")
        ws.checkout_baseline_worktree(dest)
        (dest / "tests" / "test_regression.py").write_text(real_test, encoding="utf-8")
        total, failures, errors = _run_test_module(dest, "tests.test_regression")
        assert total == 1
        assert failures + errors == 1
    finally:
        ws.remove_worktree(dest)
        destroy_workspace(ws)
