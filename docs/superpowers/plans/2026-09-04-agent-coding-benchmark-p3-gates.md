# Agent coding benchmark — Phase 3: Gates (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


## Phase 3: Gates

## Phase 3 corrections (found while executing; listings above are the original plan)

Nine defects, all of which would have produced a green-or-misleading suite and
wrong scores on real runs. **The authoritative code is the "Shipped state"
section at the end of this file**, which supersedes every listing above.

17. **Tasks 8–11 were written against the pre-Phase-2 `PiRunResult`** (`completed`, `wall_ms`, `events`, `message_end_seen`). Those fields no longer exist: events come back as a separate list, so `evaluate()` takes them as an `events=` keyword, and every call site in Tasks 8–11 needed the new constructor fields.
18. **Gate 1's condition also failed on a timed-out run**, so gate 2's `TIMEOUT` was unreachable — and the task's own test asserted a code the implementation could not emit. A timeout explains a non-zero exit, so gate 1 defers to gate 2 there.
19. **`mini-drift/gates.toml` had no `[build]` section**, so gates 4–6 `KeyError`ed on a bundle `load_task` accepted. Fixed in Phase 1's file.
20. **`_run_discover` called `discover(tests_dir, top_level_dir=".")`**, which aborts with "Start directory is not importable" for a `tests/` dir without `__init__.py` — the layout *both* bundles use. Every healthy run would have read `BROKEN_BUILD` and capped at 0.25.
21. **Gate 3 used `git diff --quiet HEAD`**, which ignores untracked files, so an agent whose only change was a new test file scored `NO_DIFF` and a composite of zero.
22. **`ALL_HIDDEN_FAILING` was missing from `CAP_TABLE`** despite being the spec's own row ("all hidden tests fail, or `BROKEN_BUILD` → ×0.25"), so a row that passed no hidden test kept a ×1.00 cap.
23. **`PARTIAL_HIDDEN_PASS` (×0.50) was missing too** — the spec's worked example (NO_REGRESSION_TEST ×0.85 with a partial hidden pass, "expects the minimum, 0.50") is exactly what Task 11 asserts, and it only passes once that row exists.
24. **Task 10's min-across-conditions test asserted `hidden 1/2`, which `mini-drift` cannot produce** (both hidden tests use even inputs, so unfixed code fails 2/2). Rewritten to fire `VACUOUS_TEST` and `ALL_HIDDEN_FAILING` together, which is what the test is for.
25. **`add()` recorded a failure code on gates that passed** (`code` was a required positional, and the code-only-when-failed rule lived nowhere). It is optional now and only attached on a failure — a `Pass` row carrying `AGENT_ERROR` contradicts itself in the report.

Two smaller notes: `add()`'s signature is `(gate_number, passed, code=None, detail="")`, so a pass is `add(5, True)` rather than the plan's `add(5, "pass")`; and `run_test_file` returns **one outcome per test**, not one per file (`-> list[TestOutcome]`), because gate 9's ratio counts tests.

### Task 8: Gate skeleton + gates 1–4 (AGENT_ERROR, TIMEOUT, NO_DIFF, BROKEN_BUILD)

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/gates.py`
- Test: `modelman/tests/benchmark/agent/test_gates.py`

**Interfaces:**
- Consumes: `TaskBundle` (Task 1), `Workspace` (Task 2), `PiRunResult` (Task 6).
- Produces: `GateResult` (`gate_number, name, outcome, code, detail`), `GatesReport` (`results: list[GateResult], hidden_pass: int, hidden_total: int, hidden_evaluated: bool, cap: float`, property `.triggered_codes`), `evaluate(workspace: Workspace, task: TaskBundle, run_result: PiRunResult, *, events: list[dict], session_file_present: bool) -> GatesReport` (pi's event list arrives as a keyword because `run_pi_process` returns it separately from `PiRunResult`). `runner.py` (Task 14) and `report.py` (Task 19) both consume `GatesReport` exactly as named here.

This task builds the short-circuit skeleton (`SHORT_CIRCUIT_CODES`, the `add()`/`finish()` closures) and the first four gates; Tasks 9–10 extend the same `evaluate()` function.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_gates.py`:
```python
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
    failing test — unittest's own discover() already converts an import error
    in a test module into a failing pseudo-test, so only a subprocess-level
    crash needs this None case."""
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
    try:
        counts = json.loads(result.stdout)
    except json.JSONDecodeError:
        return None
    return counts["total"], counts["failures"], counts["errors"]


SHORT_CIRCUIT_CODES = {"AGENT_ERROR", "TIMEOUT", "NO_DIFF", "BROKEN_BUILD", "TAMPERED_TESTS"}


def evaluate(
    workspace: Workspace,
    task: TaskBundle,
    run_result: PiRunResult,
    *,
    events: list[dict],
    session_file_present: bool,
) -> GatesReport:
    """Run gates 1–9 in order, short-circuiting the rest on a structural
    failure. Returns a GatesReport with cap already applied.

    `events` is pi's own event list (run_pi_process returns it separately from
    the result); gate 1 reads the assistant reply out of it."""
    report = GatesReport()

    def add(gate_number: int, passed: bool, code: str | None, detail: str = "") -> bool:
        outcome = "pass" if passed else "fail"
        report.results.append(GateResult(gate_number, GATE_NAMES[gate_number], outcome, code, detail))
        return passed

    def skipped_from(gate_number: int) -> None:
        for gn in range(gate_number, 10):
            report.results.append(GateResult(gn, GATE_NAMES[gn], "skipped"))

    def finish(short_circuit_code: str | None, last_gate_number: int) -> GatesReport:
        if short_circuit_code:
            report.cap = min(report.cap, CAP_TABLE[short_circuit_code])
            skipped_from(last_gate_number + 1)
        return report

    # Gate 1: agent exited cleanly
    assistant_ends = [
        e
        for e in events
        if e.get("type") == "message_end" and (e.get("message") or {}).get("role") == "assistant"
    ]
    # A timeout explains a non-zero exit code, so it must not be reported here:
    # gate 1 passing while gate 2 fails is what keeps TIMEOUT reachable at all
    # (the plan's own gate-1 condition, exit_code == 0 AND not timed out, makes
    # every timed-out run fail gate 1 first, and the taxonomy then never emits
    # TIMEOUT). A run that produced no reply is still an agent error.
    replied = bool(assistant_ends) and session_file_present
    run_ok = True if run_result.timed_out else (replied and run_result.exit_code == 0)
    if not add(1, run_ok, "AGENT_ERROR", f"exit_code={run_result.exit_code} replied={replied}"):
        return finish("AGENT_ERROR", 1)

    # Gate 2: within timeout
    if not add(2, not run_result.timed_out, "TIMEOUT"):
        return finish("TIMEOUT", 2)

    # Gate 3: non-empty diff
    diff_result = subprocess.run(
        ["git", "diff", "--quiet", "HEAD"], cwd=workspace.root, capture_output=True, text=True
    )
    has_diff = diff_result.returncode != 0
    if not add(3, has_diff, "NO_DIFF"):
        return finish("NO_DIFF", 3)

    # Gate 4: build intact (visible import + visible tests still run)
    module = task.gates_config["build"]["import_check"]
    build_ok = _import_check(workspace.root, module)
    discover = _run_discover(workspace.root, task.gates_config["build"]["tests_dir"])
    build_ok = build_ok and discover is not None
    if not add(4, build_ok, "BROKEN_BUILD"):
        return finish("BROKEN_BUILD", 4)

    # Gates 5–9 continue in Tasks 9–10.
    skipped_from(5)
    return report
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

---

## Shipped state

`make check` and `make test` are green against these files (59 agent-bench
tests, 682 total). This section supersedes every listing above: Tasks 9 and 10
extend `evaluate()` in place, so their intermediate snapshots are less useful
here than the final file.

### `modelman/src/modelman/benchmark/agent/gates.py`

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
    "PARTIAL_HIDDEN_PASS": 0.50,
    "ALL_HIDDEN_FAILING": 0.25,
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
    failing test — unittest's own discover() already converts an import error
    in a test module into a failing pseudo-test, so only a subprocess-level
    crash needs this None case."""
    # No top_level_dir: neither bundle puts an __init__.py in its tests dir, and
    # discover(..., top_level_dir=".") then aborts with "Start directory is not
    # importable", which would read as BROKEN_BUILD on every healthy run.
    script = (
        "import json, sys, unittest\n"
        "suite = unittest.defaultTestLoader.discover(sys.argv[1])\n"
        "runner = unittest.TextTestRunner(stream=sys.stderr, verbosity=0)\n"
        "result = runner.run(suite)\n"
        "json.dump({'total': result.testsRun, 'failures': len(result.failures), "
        "'errors': len(result.errors)}, sys.stdout)\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", script, tests_dir], cwd=root, capture_output=True, text=True
    )
    try:
        counts = json.loads(result.stdout)
    except json.JSONDecodeError:
        return None
    return counts["total"], counts["failures"], counts["errors"]


SHORT_CIRCUIT_CODES = {"AGENT_ERROR", "TIMEOUT", "NO_DIFF", "BROKEN_BUILD", "TAMPERED_TESTS"}


@dataclass
class TestOutcome:
    name: str
    passed: bool
    skipped: bool = False
    duration_ms: int = 0
    message: str | None = None


def run_test_file(root: Path, module_name: str) -> list[TestOutcome]:
    """Run one `unittest` module by dotted name (e.g. `tests.test_day31`).

    Returns one outcome per test, not one per file: a module holds N tests and
    gate 9's pass ratio is over tests. Uses a JSON summary protocol on stdout,
    mirroring _run_discover(). A crashed subprocess (non-zero returncode with no
    valid JSON) is reported as a failing outcome, not an exception — a hidden
    test file that throws on import is a real result the row should see, not a
    harness bug."""
    import subprocess

    script = (
        "import io, json, sys, time, unittest\n"
        "class RecordedResult(unittest.TextTestResult):\n"
        "    def __init__(self, *args, **kwargs):\n"
        "        super().__init__(*args, **kwargs)\n"
        "        self.outcomes = []\n"
        "    def addSuccess(self, test):\n"
        "        super().addSuccess(test)\n"
        "        self.outcomes.append(('pass', test, None))\n"
        "    def addFailure(self, test, err):\n"
        "        super().addFailure(test, err)\n"
        "        self.outcomes.append(('fail', test, err))\n"
        "    def addError(self, test, err):\n"
        "        super().addError(test, err)\n"
        "        self.outcomes.append(('error', test, err))\n"
        "    def addSkip(self, test, reason):\n"
        "        super().addSkip(test, reason)\n"
        "        self.outcomes.append(('skip', test, reason))\n"
        "class RecordingRunner(unittest.TextTestRunner):\n"
        "    resultclass = RecordedResult\n"
        "suite = unittest.defaultTestLoader.loadTestsFromName(sys.argv[1])\n"
        "start = time.monotonic()\n"
        "result = RecordingRunner(stream=io.StringIO(), verbosity=0).run(suite)\n"
        "duration_ms = int((time.monotonic() - start) * 1000)\n"
        "n = max(len(result.outcomes), 1)\n"
        "outcomes = []\n"
        "for status, test, payload in result.outcomes:\n"
        "    outcomes.append({\n"
        "        'name': f'{test.__class__.__name__}.{test._testMethodName}',\n"
        "        'passed': status == 'pass',\n"
        "        'skipped': status == 'skip',\n"
        "        'duration_ms': duration_ms if n == 1 else max(duration_ms // n, 1),\n"
        "        'message': (str(payload[1]) if isinstance(payload, tuple) else str(payload)) if payload else None,\n"
        "    })\n"
        "print(json.dumps({'outcomes': outcomes, 'total': result.testsRun,\n"
        "                  'failures': len(result.failures), 'errors': len(result.errors)}))\n"
    )
    result = subprocess.run(
        [sys.executable, "-c", script, module_name], cwd=root, capture_output=True, text=True
    )
    try:
        summary_line = result.stdout.strip().splitlines()[-1]
        data = json.loads(summary_line)
    except (IndexError, json.JSONDecodeError):
        return [
            TestOutcome(
                name=f"{module_name}:crash",
                passed=False,
                message=(result.stderr or result.stdout or "test runner produced no summary")[-500:],
            )
        ]
    return [
        TestOutcome(
            name=o["name"],
            passed=o["passed"],
            skipped=o["skipped"],
            duration_ms=o["duration_ms"],
            message=o.get("message"),
        )
        for o in data["outcomes"]
    ]


def _run_hidden_tests(workspace: Workspace, task: TaskBundle) -> tuple[int, int]:
    """Seed and run the bundle's hidden test files. Returns (passed, total).

    The bundle's tests_dir is the workspace-relative directory the visible
    suite also lives in; seeding copies hidden files there, so discovery uses
    the same dotted-name convention as run_test_file's own tests."""
    tests_dir = task.gates_config["build"]["tests_dir"]
    hidden_files = task.gates_config.get("hidden", {}).get("files", [])
    workspace.seed_hidden(task)
    outcomes: list[TestOutcome] = []
    for filename in hidden_files:
        outcomes.extend(run_test_file(workspace.root, f"{tests_dir}.{Path(filename).stem}"))
    passed = sum(1 for o in outcomes if o.passed)
    return passed, len(outcomes)


def _detect_and_evaluate_gate8(
    workspace: Workspace, task: TaskBundle, new_tests: list[Path]
) -> list[TestOutcome]:
    """For each new test file, copy it into a baseline worktree and run it
    there. If it PASSES on the unfixed baseline, it is vacuous — proof the
    agent's "regression test" asserts nothing about the bug.

    Returns per-file outcomes (one row per file, named after it) so a report
    reader can tell which file was the hollow one. A file that fails to import
    in the baseline worktree is recorded as a failing outcome rather than
    raised: `loadTestsFromName` raises for that case, and an agent's test
    erroring on the baseline is not vacuous — which is the correct answer."""
    tests_dir = task.gates_config["build"]["tests_dir"]
    dest = workspace.root.parent / f"{workspace.root.name}-gate8-baseline"
    workspace.checkout_baseline_worktree(dest)
    outcomes: list[TestOutcome] = []
    try:
        for test_file in new_tests:
            target = dest / test_file.relative_to(workspace.root)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(test_file.read_text(encoding="utf-8"), encoding="utf-8")
            outcomes.extend(
                TestOutcome(
                    name=f"{test_file.name}:{o.name}",
                    passed=o.passed,
                    skipped=o.skipped,
                    duration_ms=o.duration_ms,
                    message=o.message,
                )
                for o in run_test_file(dest, f"{tests_dir}.{test_file.stem}")
            )
    finally:
        workspace.remove_worktree(dest)
    return outcomes


def compute_composite(gates: GatesReport, judge: object | None = None) -> float:
    """hidden_ratio * 0.60 + judge_score/100 * 0.40 * cap, both weights
    carried from the spec. No composite is computed without hidden tests —
    spec: "No composite score is computed when hidden_tests_available ==
    false"."""
    if not gates.hidden_evaluated:
        return 0.0
    hidden_ratio = gates.hidden_pass / gates.hidden_total if gates.hidden_total else 0.0
    judge_score = getattr(judge, "total", 0.0) if judge is not None else 0.0
    return hidden_ratio * 0.60 + (judge_score / 100.0) * 0.40 * gates.cap


def score_row(gates: GatesReport, judge: object | None = None) -> float:
    """Convenience alias callers use to name the two inputs explicitly."""
    return compute_composite(gates, judge)


def evaluate(
    workspace: Workspace,
    task: TaskBundle,
    run_result: PiRunResult,
    *,
    events: list[dict],
    session_file_present: bool,
) -> GatesReport:
    """Run gates 1–9 in order, short-circuiting the rest on a structural
    failure. Returns a GatesReport with cap already applied.

    `events` is pi's own event list (run_pi_process returns it separately from
    the result); gate 1 reads the assistant reply out of it."""
    report = GatesReport()

    def add(gate_number: int, passed: bool, code: str | None = None, detail: str = "") -> bool:
        outcome = "pass" if passed else "fail"
        # a code is a failure label: recording AGENT_ERROR on a gate that passed
        # would make the report's Pass/Fail column contradict itself
        report.results.append(
            GateResult(gate_number, GATE_NAMES[gate_number], outcome, code if not passed else None, detail)
        )
        return passed

    def skipped_from(gate_number: int) -> None:
        for gn in range(gate_number, 10):
            report.results.append(GateResult(gn, GATE_NAMES[gn], "skipped"))

    def finish(short_circuit_code: str | None = None, last_gate_number: int = 9) -> GatesReport:
        """Mark everything after `last_gate_number` unevaluated and apply the
        cap table to whatever fired.

        The cap is recomputed from triggered_codes rather than taken from the
        short-circuit argument alone: NO_REGRESSION_TEST and VACUOUS_TEST cap
        without short-circuiting, so a code-only cap would miss them."""
        if short_circuit_code:
            skipped_from(last_gate_number + 1)
        report.cap = min([1.0, *[CAP_TABLE[c] for c in report.triggered_codes if c in CAP_TABLE]])
        return report

    # Gate 1: agent exited cleanly
    assistant_ends = [
        e
        for e in events
        if e.get("type") == "message_end" and (e.get("message") or {}).get("role") == "assistant"
    ]
    # A timeout explains a non-zero exit code, so it must not be reported here:
    # gate 1 passing while gate 2 fails is what keeps TIMEOUT reachable at all
    # (the plan's own gate-1 condition, exit_code == 0 AND not timed out, makes
    # every timed-out run fail gate 1 first, and the taxonomy then never emits
    # TIMEOUT). A run that produced no reply is still an agent error.
    replied = bool(assistant_ends) and session_file_present
    run_ok = True if run_result.timed_out else (replied and run_result.exit_code == 0)
    if not add(1, run_ok, "AGENT_ERROR", f"exit_code={run_result.exit_code} replied={replied}"):
        return finish("AGENT_ERROR", 1)

    # Gate 2: within timeout
    if not add(2, not run_result.timed_out, "TIMEOUT"):
        return finish("TIMEOUT", 2)

    # Gate 3: non-empty diff
    # Untracked files count: `git diff --quiet HEAD` ignores them, so an agent
    # whose work is "add a test file, change nothing else" — a legitimate, if
    # incomplete, answer — was reported as having produced no diff at all and
    # capped at 0.
    changed = workspace.modified_or_deleted_since_baseline() or workspace.new_files_since_baseline()
    has_diff = bool(changed)
    if not add(3, has_diff, "NO_DIFF"):
        return finish("NO_DIFF", 3)

    tests_dir = task.gates_config["build"]["tests_dir"]

    # Gate 4: build intact (visible import + visible tests still run)
    module = task.gates_config["build"]["import_check"]
    build_ok = _import_check(workspace.root, module)
    discover = _run_discover(workspace.root, tests_dir)
    build_ok = build_ok and discover is not None
    if not add(4, build_ok, "BROKEN_BUILD"):
        return finish("BROKEN_BUILD", 4)

    # Gate 5: visible regression. Deliberately not a short-circuit: a broken
    # visible test does not make gates 6-9 un-evaluable (spec, gate 5).
    assert discover is not None  # gate 4 above returns finish() when it is
    total, failures, errors = discover
    if total > 0 and failures == 0 and errors == 0:
        add(5, True)
    else:
        add(5, False, "VISIBLE_REGRESSION", f"{failures} failures, {errors} errors of {total}")

    # Gate 6: tests not tampered. Editing a pre-existing test file is fatal,
    # and complementary to gate 7 rather than contradictory: adding a new file
    # is required, changing an old one is not allowed.
    tampered = [
        p for p in workspace.modified_or_deleted_since_baseline()
        if p.relative_to(workspace.root).parts[0] == tests_dir
    ]
    if not tampered:
        add(6, True)
    else:
        add(
            6,
            False,
            "TAMPERED_TESTS",
            ", ".join(str(p.relative_to(workspace.root)) for p in tampered),
        )
        return finish("TAMPERED_TESTS", 6)

    # Gate 7: has a regression test (a new file in the tests dir).
    new_tests = [
        p
        for p in workspace.new_files_since_baseline()
        if p.relative_to(workspace.root).parts[0] == tests_dir and p.name.startswith("test_")
    ]
    if new_tests:
        add(7, True)
    else:
        add(7, False, "NO_REGRESSION_TEST")

    # Gate 8: the regression test must not be vacuous. Only meaningful when a
    # new test file exists at all — otherwise there is nothing to check and the
    # gate is skipped, not passed.
    if new_tests:
        gate8_results = _detect_and_evaluate_gate8(workspace, task, new_tests)
        vacuous = any(o.passed for o in gate8_results)
        if vacuous:
            add(8, False, "VACUOUS_TEST")
        else:
            add(8, True)
        report.results[-1].detail = f"{len(new_tests)} new test file(s) checked against the baseline"
    else:
        report.results.append(GateResult(8, GATE_NAMES[8], "skipped"))

    # Gate 9: hidden acceptance tests. Seeded only now, after the agent's
    # process has exited, so no model can have seen them during the run.
    hidden_files = task.gates_config.get("hidden", {}).get("files", [])
    if hidden_files:
        hidden_pass, hidden_total = _run_hidden_tests(workspace, task)
        report.hidden_pass = hidden_pass
        report.hidden_total = hidden_total
        report.hidden_evaluated = True
        if hidden_pass == hidden_total and hidden_total > 0:
            add(9, True)
        elif hidden_pass == 0:
            # "all hidden tests fail, or BROKEN_BUILD" is a x0.25 outcome. A
            # hidden file that would not even load lands here too, which is the
            # right verdict: nothing about that row's correctness was measured.
            add(9, False, "ALL_HIDDEN_FAILING", f"0/{hidden_total}")
        else:
            # the spec's cap table row "partial hidden-test pass (>=1 of m, not
            # all) x0.50"; its worked example pairs this with
            # NO_REGRESSION_TEST (x0.85) and expects the minimum, 0.50
            add(9, False, "PARTIAL_HIDDEN_PASS", f"{hidden_pass}/{hidden_total}")

    else:
        report.results.append(GateResult(9, GATE_NAMES[9], "skipped"))

    return finish()
```

### `modelman/tests/benchmark/agent/test_gates.py`

```python
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
```

### `modelman/tests/benchmark/agent/test_gates_day31_drift.py`

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
```
