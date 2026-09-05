# Agent coding benchmark — Phase 3: Gates (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 3: Gates

### Task 8: Gate skeleton + gates 1–4 (AGENT_ERROR, TIMEOUT, NO_DIFF, BROKEN_BUILD)

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/gates.py`
- Test: `modelman/tests/benchmark/agent/test_gates.py`

**Interfaces:**
- Consumes: `TaskBundle` (Task 1), `Workspace` (Task 2), `PiRunResult` (Task 6).
- Produces: `GateResult` (`gate_number, name, outcome, code, detail`), `GatesReport` (`results: list[GateResult], hidden_pass: int, hidden_total: int, hidden_evaluated: bool, cap: float`, property `.triggered_codes`), `evaluate(workspace, task, run_result, *, session_file_present: bool) -> GatesReport`. `runner.py` (Task 14) and `report.py` (Task 19) both consume `GatesReport` exactly as named here.

This task builds the short-circuit skeleton (`SHORT_CIRCUIT_CODES`, the `add()`/`finish()` closures) and the first four gates; Tasks 9–10 extend the same `evaluate()` function.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_gates.py`:
```python
"""Tests for modelman.benchmark.agent.gates — deterministic gate evaluation.

Gate 8 (vacuous-test detection) is, per the spec, "the highest-value check
in the harness" — a model that adds a test which trivially passes on
unfixed code must not be credited with a real regression test. These tests
build up the full nine-gate taxonomy against the mini-drift fixture (fast,
controlled) before Task 11 re-runs the same taxonomy against the real
day31-drift bundle with hand-authored patches.
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
    return PiRunResult(completed=True, timed_out=False, wall_ms=100, events=[], unparsed_lines=0, message_end_seen=True)


@pytest.fixture
def workspace():
    ws = create_workspace(_task())
    yield ws
    destroy_workspace(ws)


def test_agent_error_short_circuits_when_agent_did_not_exit_cleanly(workspace):
    """A crashed/incomplete agent process fails gate 1 and every later gate
    is recorded as skipped, not passed — an unevaluated gate must never
    silently count as a pass."""
    bad_run = PiRunResult(completed=False, timed_out=False, wall_ms=10, events=[], unparsed_lines=0, message_end_seen=False)
    report = evaluate(workspace, _task(), bad_run, session_file_present=True)
    assert report.results[0].outcome == "fail"
    assert report.results[0].code == "AGENT_ERROR"
    assert all(r.outcome == "skipped" for r in report.results[1:])
    assert report.cap == 0.0


def test_timeout_short_circuits(workspace):
    timed_out_run = PiRunResult(completed=False, timed_out=True, wall_ms=999999, events=[], unparsed_lines=0, message_end_seen=True)
    report = evaluate(workspace, _task(), timed_out_run, session_file_present=True)
    assert report.results[1].code == "TIMEOUT"
    assert all(r.outcome == "skipped" for r in report.results[2:])
    assert report.cap == 0.0


def test_no_diff_short_circuits(workspace):
    """An agent that touched nothing at all fails gate 3 before any test
    infrastructure gate runs."""
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[2].code == "NO_DIFF"
    assert all(r.outcome == "skipped" for r in report.results[3:])
    assert report.cap == 0.0


def test_broken_build_short_circuits(workspace):
    """Breaking the import (not just failing a test) is caught by gate 4
    before gate 5 even tries to run the visible suite."""
    (workspace.root / "pkg" / "__init__.py").write_text("this is not valid python(((", encoding="utf-8")
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[3].code == "BROKEN_BUILD"
    assert all(r.outcome == "skipped" for r in report.results[4:])
    assert report.cap == 0.25
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_gates.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/gates.py`:
```python
"""Deterministic gate evaluation and failure-taxonomy scoring."""

from __future__ import annotations

import json
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.agent.pidriver import PiRunResult
from modelman.benchmark.agent.task import TaskBundle
from modelman.benchmark.agent.workspace import Workspace

CAP_TABLE = {
    "NO_REGRESSION_TEST": 0.85,
    "VACUOUS_TEST": 0.70,
    "BROKEN_BUILD": 0.25,
    "TAMPERED_TESTS": 0.0,
    "TIMEOUT": 0.0,
    "NO_DIFF": 0.0,
    "AGENT_ERROR": 0.0,
}

GATE_NAMES = {
    1: "AGENT_EXITED",
    2: "WITHIN_TIMEOUT",
    3: "NON_EMPTY_DIFF",
    4: "BUILD_INTACT",
    5: "VISIBLE_TESTS_PASS",
    6: "TESTS_NOT_TAMPERED",
    7: "HAS_REGRESSION_TEST",
    8: "REGRESSION_TEST_NOT_VACUOUS",
    9: "HIDDEN_TESTS",
}


@dataclass
class GateResult:
    gate_number: int
    name: str
    outcome: str  # "pass" | "fail" | "skipped"
    code: str | None = None
    detail: str = ""


@dataclass
class GatesReport:
    results: list[GateResult] = field(default_factory=list)
    hidden_pass: int = 0
    hidden_total: int = 0
    hidden_evaluated: bool = False
    cap: float = 1.0

    @property
    def triggered_codes(self) -> list[str]:
        return [r.code for r in self.results if r.outcome == "fail" and r.code]


def _import_check(root: Path, module_name: str) -> bool:
    result = subprocess.run(
        [sys.executable, "-c", f"import {module_name}"], cwd=root, capture_output=True, text=True
    )
    return result.returncode == 0


def _run_discover(root: Path, tests_dir: str) -> tuple[int, int, int] | None:
    """Run the full visible suite via unittest discovery. Returns
    (total, failures, errors), or None if discovery crashed the subprocess
    outright (e.g. a SyntaxError in a test file) rather than surfacing as a
    failing test — unittest's own discover() already converts an import
    error in a test module into a failing pseudo-test, so only a
    subprocess-level crash needs this None case."""
    script = (
        "import json, sys, unittest\n"
        "suite = unittest.defaultTestLoader.discover(sys.argv[1], top_level_dir='.')\n"
        "runner = unittest.TextTestRunner(stream=sys.stderr, verbosity=0)\n"
        "result = runner.run(suite)\n"
        "json.dump({'total': result.testsRun, 'failures': len(result.failures), "
        "'errors': len(result.errors)}, sys.stdout)\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", script, tests_dir], cwd=root, capture_output=True, text=True
    )
    if not result.stdout.strip():
        return None
    data = json.loads(result.stdout)
    return data["total"], data["failures"], data["errors"]


def _run_module(root: Path, module_name: str) -> tuple[int, int, int] | None:
    script = (
        "import json, sys, unittest\n"
        "suite = unittest.defaultTestLoader.loadTestsFromName(sys.argv[1])\n"
        "runner = unittest.TextTestRunner(stream=sys.stderr, verbosity=0)\n"
        "result = runner.run(suite)\n"
        "json.dump({'total': result.testsRun, 'failures': len(result.failures), "
        "'errors': len(result.errors)}, sys.stdout)\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", script, module_name], cwd=root, capture_output=True, text=True
    )
    if not result.stdout.strip():
        return None
    data = json.loads(result.stdout)
    return data["total"], data["failures"], data["errors"]


def evaluate(
    workspace: Workspace, task: TaskBundle, run_result: PiRunResult, *, session_file_present: bool
) -> GatesReport:
    results: list[GateResult] = []
    report = GatesReport(results=results)
    build_cfg = task.gates_config.get("build", {})
    import_check = build_cfg.get("import_check", task.task_id)
    tests_dir = build_cfg.get("tests_dir", "tests")

    def add(n: int, outcome: str, code: str | None = None, detail: str = "") -> None:
        results.append(GateResult(gate_number=n, name=GATE_NAMES[n], outcome=outcome, code=code, detail=detail))

    def finish() -> GatesReport:
        for n in range(len(results) + 1, 10):
            add(n, "skipped")
        report.cap = _compute_cap(report)
        return report

    if run_result.completed and run_result.message_end_seen and session_file_present:
        add(1, "pass")
    else:
        add(1, "fail", "AGENT_ERROR", "agent did not exit cleanly, no message_end seen, or no session file")
        return finish()

    if not run_result.timed_out:
        add(2, "pass")
    else:
        add(2, "fail", "TIMEOUT")
        return finish()

    diff_text = workspace.diff()
    if diff_text.strip():
        add(3, "pass")
    else:
        add(3, "fail", "NO_DIFF")
        return finish()

    build_ok = _import_check(workspace.root, import_check)
    discover_result = _run_discover(workspace.root, tests_dir) if build_ok else None
    if build_ok and discover_result is not None:
        add(4, "pass")
    else:
        add(4, "fail", "BROKEN_BUILD")
        return finish()

    # Gates 5-9 don't exist yet at this point in the plan (Tasks 9-10 add
    # them) — a build that gets this far is fully evaluated by what's
    # implemented so far, and finish() marks the rest skipped.
    return finish()


def _compute_cap(report: GatesReport) -> float:
    caps = [1.0]
    for code in report.triggered_codes:
        if code in CAP_TABLE:
            caps.append(CAP_TABLE[code])
    if report.hidden_evaluated and report.hidden_total > 0:
        if report.hidden_pass == report.hidden_total:
            pass
        elif report.hidden_pass == 0:
            caps.append(0.25)
        else:
            caps.append(0.50)
    return min(caps)
```

Gates 5–9 aren't implemented yet at this point in the plan — Task 9 edits this same function to add them. A row that passes gates 1–4 is, for now, fully evaluated by what exists: `finish()` marks gates 5–9 `skipped`. No test in this task exercises "gate 4 passes" (`test_broken_build_short_circuits` is the only gate-4 test, and it fails gate 4), so this is a genuine, working stopping point, not a stub.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_gates.py -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/gates.py \
        modelman/tests/benchmark/agent/test_gates.py
git commit -m "feat(agent-bench): add gates 1-4 with short-circuit skeleton - completes plan item #8"
```

### Task 9: Gates 5–7 (VISIBLE_REGRESSION, TAMPERED_TESTS, NO_REGRESSION_TEST)

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/gates.py`
- Modify: `modelman/tests/benchmark/agent/test_gates.py`

**Interfaces:** Same as Task 8 — this task extends the body of `evaluate()`, no new public names.

Per the spec: gate 5 is deliberately **not** a short-circuit gate (a broken visible test doesn't make gates 6–9 un-evaluable), while gates 6 forbids editing pre-existing test files and gate 7 requires a genuinely new one — complementary, not contradictory.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_gates.py`:
```python
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


def test_visible_regression_does_not_short_circuit(workspace):
    """Breaking a shipped test (without touching its file) fails gate 5 but
    still lets gates 6-7 run — VISIBLE_REGRESSION is diagnostic, not fatal
    to later gates, per the spec."""
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n\n", encoding="utf-8"
    )  # still importable, but now every call is wrong -> shipped test fails
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[4].code == "VISIBLE_REGRESSION"
    assert report.results[5].outcome != "skipped"  # gate 6 still ran
    assert report.results[6].outcome != "skipped"  # gate 7 still ran


def test_tampered_tests_short_circuits(workspace):
    """Editing the pre-existing shipped test file is TAMPERED_TESTS and
    does short-circuit — gates 7-9 can't tell a real new test from a
    doctored old one once this has happened."""
    _fix_and_add_regression_test(workspace)
    (workspace.root / "tests" / "test_pkg.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n    def test_noop(self):\n        pass\n",
        encoding="utf-8",
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[5].code == "TAMPERED_TESTS"
    assert all(r.outcome == "skipped" for r in report.results[6:])
    assert report.cap == 0.0


def test_no_regression_test_when_no_new_test_file_added(workspace):
    """A fix with no accompanying new test file fails gate 7 but is not a
    short-circuit — gate 9 (hidden tests) still needs to run on it."""
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[6].code == "NO_REGRESSION_TEST"
    assert report.cap <= 0.85


def test_gate_seven_passes_when_new_test_file_added(workspace):
    _fix_and_add_regression_test(workspace)
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[6].outcome == "pass"
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_gates.py -v`
Expected: FAIL — `test_visible_regression_does_not_short_circuit` and the others index into gate slots that `finish()` currently marks `skipped` unconditionally once gate 4 passes.

- [ ] **Step 3: Extend the implementation**

In `modelman/src/modelman/benchmark/agent/gates.py`, replace:
```python
    # Gates 5-9 don't exist yet at this point in the plan (Tasks 9-10 add
    # them) — a build that gets this far is fully evaluated by what's
    # implemented so far, and finish() marks the rest skipped.
    return finish()
```
with:
```python
    total, failures, errors = discover_result
    if total > 0 and failures == 0 and errors == 0:
        add(5, "pass")
    else:
        add(5, "fail", "VISIBLE_REGRESSION", f"{failures} failures, {errors} errors of {total}")

    tampered = [
        p for p in workspace.modified_or_deleted_since_baseline()
        if p.relative_to(workspace.root).parts[0] == tests_dir
    ]
    if not tampered:
        add(6, "pass")
    else:
        add(6, "fail", "TAMPERED_TESTS", ", ".join(str(p.relative_to(workspace.root)) for p in tampered))
        return finish()

    new_tests = [
        p for p in workspace.new_files_since_baseline()
        if p.relative_to(workspace.root).parts[0] == tests_dir and p.name.startswith("test_")
    ]
    if new_tests:
        add(7, "pass")
    else:
        add(7, "fail", "NO_REGRESSION_TEST")

    # Gates 8-9 are added in Task 10.
    return finish()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_gates.py -v`
Expected: PASS (8 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/gates.py \
        modelman/tests/benchmark/agent/test_gates.py
git commit -m "feat(agent-bench): add gates 5-7 - completes plan item #9"
```

### Task 10: Gate 8 (VACUOUS_TEST) + gate 9 (hidden pass ratio) + cap wiring

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/gates.py`
- Modify: `modelman/tests/benchmark/agent/test_gates.py`

**Interfaces:** Same as Task 8 — completes `evaluate()`. `_compute_cap` (already written in Task 8) now receives real `hidden_pass`/`hidden_total`/`hidden_evaluated` values instead of the Task 8/9 defaults.

Gate 8 is, per the spec, "the highest-value check in the harness": it runs the agent's new test file(s) against a `git worktree add` checkout of the baseline commit and fires only when they **pass** there — proof the test asserts nothing about the bug.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_gates.py`:
```python
def test_vacuous_test_detected_when_new_test_passes_on_unfixed_baseline(workspace):
    """A new test that would pass even without the fix is VACUOUS_TEST —
    the spec's highest-value check."""
    # pkg IS fixed here, deliberately. With the real bug still present the
    # all-hidden-failing cap (x0.25) also fires and wins the min(), so the
    # row's cap would no longer isolate the x0.70 vacuous cap this test pins
    # (see the next test for that combination). assertTrue(True) still passes
    # on the unfixed baseline, which is what makes the test vacuous.
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
    )
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n    def test_trivial(self):\n        self.assertTrue(True)\n",
        encoding="utf-8",
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[7].code == "VACUOUS_TEST"
    assert (report.hidden_pass, report.hidden_total) == (2, 2)  # real fix, hollow test
    assert report.cap == 0.70


def test_cap_is_the_minimum_across_every_triggered_condition(workspace):
    """VACUOUS_TEST (x0.70) and all-hidden-failing (x0.25) firing on one row
    must yield 0.25 — the min across triggered conditions, not whichever gate
    happened to run last."""
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n    def test_trivial(self):\n        self.assertTrue(True)\n",
        encoding="utf-8",
    )
    # pkg left buggy — only a comment is appended, so the diff is non-empty
    # (gate 3) while both hidden tests still fail.
    (workspace.root / "pkg" / "__init__.py").write_text(
        (workspace.root / "pkg" / "__init__.py").read_text(encoding="utf-8") + "\n# comment\n",
        encoding="utf-8",
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.triggered_codes == ["VACUOUS_TEST", "HIDDEN_TESTS_FAILED 0/2"]
    assert report.cap == 0.25


def test_real_regression_test_is_not_vacuous(workspace):
    _fix_and_add_regression_test(workspace)
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[7].outcome == "pass"


def test_hidden_tests_pass_ratio_recorded_and_capped(workspace):
    """A correct fix with a real regression test clears both hidden tests
    and gets the full ×1.00 cap."""
    _fix_and_add_regression_test(workspace)
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[8].outcome == "pass"
    assert (report.hidden_pass, report.hidden_total) == (2, 2)
    assert report.cap == 1.0


def test_all_hidden_tests_failing_caps_at_quarter(workspace):
    """A fix with a real regression test that still fails every hidden
    test gets the all-fail cap, not the partial one."""
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n"
        "    def test_placeholder(self):\n        self.assertTrue(False)\n",  # fails on purpose, not vacuous
        encoding="utf-8",
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert (report.hidden_pass, report.hidden_total) == (0, 2)  # pkg untouched, still buggy
    assert report.cap == 0.25


def test_partial_hidden_pass_caps_at_half(workspace):
    """A fix that only handles one of the two hidden boundary cases gets
    the partial-pass cap (>=1 of m, not all) — hidden tests run
    independently of gates 6-8, so this pairs with a real, non-vacuous
    regression test."""
    (workspace.root / "pkg" / "__init__.py").write_text(
        "def add_one(n: int) -> int:\n"
        "    if n == 4:\n"
        "        return 5  # deliberately narrow fix, exercised only for this test\n"
        "    if n % 2 == 0:\n"
        "        return n\n"
        "    return n + 1\n",
        encoding="utf-8",
    )
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\nfrom pkg import add_one\n\n"
        "class T(unittest.TestCase):\n    def test_four(self):\n        self.assertEqual(add_one(4), 5)\n",
        encoding="utf-8",
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert (report.hidden_pass, report.hidden_total) == (1, 2)
    assert report.cap == 0.50
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_gates.py -v`
Expected: FAIL — gate 8/9 indices don't exist yet (`finish()` still marks them skipped)

- [ ] **Step 3: Extend the implementation**

In `modelman/src/modelman/benchmark/agent/gates.py`, replace:
```python
    # Gates 8-9 are added in Task 10.
    return finish()
```
with:
```python
    if new_tests:
        if _check_vacuous(workspace, new_tests, tests_dir):
            add(8, "fail", "VACUOUS_TEST")
        else:
            add(8, "pass")
    else:
        add(8, "skipped")

    hidden_files = task.gates_config.get("hidden", {}).get("files", [])
    if hidden_files:
        workspace.seed_hidden(task)
        hidden_pass = hidden_total = 0
        for filename in hidden_files:
            module = f"{tests_dir}.{Path(filename).stem}"
            counts = _run_module(workspace.root, module)
            if counts is not None:
                total_m, failures_m, errors_m = counts
                hidden_total += total_m
                hidden_pass += total_m - failures_m - errors_m
        report.hidden_pass = hidden_pass
        report.hidden_total = hidden_total
        report.hidden_evaluated = True
        if hidden_total > 0 and hidden_pass == hidden_total:
            add(9, "pass")
        else:
            add(9, "fail", f"HIDDEN_TESTS_FAILED {hidden_pass}/{hidden_total}")
    else:
        add(9, "skipped")

    return finish()
```

Then add this helper function above `evaluate`:
```python
def _check_vacuous(workspace: Workspace, new_tests: list[Path], tests_dir: str) -> bool:
    """A new test is vacuous if it PASSES against the unfixed baseline — it
    asserts nothing about the bug. A collection error or assertion failure
    both count as 'not vacuous'."""
    dest = workspace.root.parent / f"{workspace.root.name}-baseline-check"
    workspace.checkout_baseline_worktree(dest)
    try:
        for test_file in new_tests:
            rel = test_file.relative_to(workspace.root)
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(test_file.read_text(encoding="utf-8"), encoding="utf-8")
            module = f"{tests_dir}.{test_file.stem}"
            counts = _run_module(dest, module)
            if counts is not None:
                total, failures, errors = counts
                if total > 0 and failures == 0 and errors == 0:
                    return True
        return False
    finally:
        workspace.remove_worktree(dest)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_gates.py -v`
Expected: PASS (14 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/gates.py \
        modelman/tests/benchmark/agent/test_gates.py
git commit -m "feat(agent-bench): add vacuous-test check + hidden pass ratio + caps - completes plan item #10"
```

### Task 11: Full taxonomy against the real `day31-drift` bundle

**Files:**
- Create: `modelman/tests/benchmark/agent/test_gates_day31_drift.py`

**Interfaces:** None new — this is the spec's own testing requirement for `gates.py` ("miniature fixture task bundle + pre-authored patches... asserts exact taxonomy code") applied to the real bundle, closing out Phase 3.

`mini-drift` (Tasks 8–10) is fast and isolates gate logic from task-content bugs. This task proves the same `evaluate()` produces the right numbers against the actual `day31-drift` content (Task 3), reusing the hand-authored patches from Task 4's bundle verification.

- [ ] **Step 1: Write the test**

`modelman/tests/benchmark/agent/test_gates_day31_drift.py`:
```python
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

TASK_ROOT = Path(__file__).resolve().parents[4] / "benchmarks" / "tasks" / "day31-drift"
# parents[4] = monorepo root from modelman/tests/benchmark/agent/ (see Task 4).

REAL_REGRESSION_TEST = (
    "import unittest\nfrom datetime import date\nfrom kettlecomb import add_months\n\n"
    "class T(unittest.TestCase):\n"
    "    def test_feb_boundary(self):\n"
    "        self.assertEqual(add_months(date(2025, 1, 31), 1), date(2025, 2, 28))\n"
)


def _ok_run() -> PiRunResult:
    return PiRunResult(completed=True, timed_out=False, wall_ms=100, events=[], unparsed_lines=0, message_end_seen=True)


def test_correct_fix_with_real_test_passes_every_gate():
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "calendarlib.py").write_text(CORRECT_CALENDARLIB, encoding="utf-8")
        (ws.root / "tests" / "test_regression.py").write_text(REAL_REGRESSION_TEST, encoding="utf-8")
        report = evaluate(ws, task, _ok_run(), session_file_present=True)
        codes = [r.code for r in report.results]
        assert codes == [None] * 9  # every gate passed, no failure code anywhere
        assert (report.hidden_pass, report.hidden_total) == (6, 6)
        assert report.cap == 1.0
    finally:
        destroy_workspace(ws)


def test_symptom_patch_without_a_test_gets_partial_hidden_cap():
    """The symptom patch fixes the two hidden cases that happen to survive
    the buggy add_months by coincidence (a 30-day-month target, and a
    mid-month non-boundary case) but leaves the four February-adjacent
    cases failing — a genuine partial pass, not all-or-nothing."""
    task = load_task(TASK_ROOT)
    ws = create_workspace(task)
    try:
        (ws.root / "kettlecomb" / "billing.py").write_text(SYMPTOM_PATCH_BILLING, encoding="utf-8")
        report = evaluate(ws, task, _ok_run(), session_file_present=True)
        assert report.results[6].code == "NO_REGRESSION_TEST"  # gate 7: no new test file
        assert (report.hidden_pass, report.hidden_total) == (2, 6)
        assert report.cap == 0.50  # min(0.85 from gate 7, 0.50 from partial hidden pass)
    finally:
        destroy_workspace(ws)


def test_agent_error_and_timeout_and_no_diff_reachable_on_real_bundle():
    """Confirms the three earliest short-circuit codes are reachable
    against the real bundle too, not just the mini-drift fixture."""
    task = load_task(TASK_ROOT)

    ws = create_workspace(task)
    try:
        bad_run = PiRunResult(completed=False, timed_out=False, wall_ms=1, events=[], unparsed_lines=0, message_end_seen=False)
        assert evaluate(ws, task, bad_run, session_file_present=True).results[0].code == "AGENT_ERROR"
    finally:
        destroy_workspace(ws)

    ws = create_workspace(task)
    try:
        timed_out_run = PiRunResult(completed=False, timed_out=True, wall_ms=1, events=[], unparsed_lines=0, message_end_seen=True)
        assert evaluate(ws, task, timed_out_run, session_file_present=True).results[1].code == "TIMEOUT"
    finally:
        destroy_workspace(ws)

    ws = create_workspace(task)
    try:
        assert evaluate(ws, task, _ok_run(), session_file_present=True).results[2].code == "NO_DIFF"
    finally:
        destroy_workspace(ws)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_gates_day31_drift.py -v`
Expected: This should already PASS if Tasks 3–10 are all correctly implemented — there is no new production code in this task, only a stronger integration check. If any assertion fails, the bug is in `gates.py`, the bundle content, or the hidden-pass arithmetic above — fix the code, not the numbers in this test (they were derived by hand-tracing the actual buggy `add_months` against each hidden test case; see the task description above).

- [ ] **Step 3: Run the full Phase 3 slice and commit**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (all tests from Tasks 1–11)

```bash
git add modelman/tests/benchmark/agent/test_gates_day31_drift.py
git commit -m "test(agent-bench): verify full gate taxonomy against day31-drift - completes plan item #11"
```

---
