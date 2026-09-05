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
        if hidden_total > 0 and hidden_pass == hidden_total:
            add(9, True)
        elif hidden_total > 0 and hidden_pass == 0:
            # the spec's cap table: "all hidden tests fail, or BROKEN_BUILD" is
            # a x0.25 outcome, distinct from a partial pass
            add(9, False, "ALL_HIDDEN_FAILING", f"0/{hidden_total}")
        else:
            add(9, False, "HIDDEN_TESTS_FAILED", f"{hidden_pass}/{hidden_total}")
    else:
        report.results.append(GateResult(9, GATE_NAMES[9], "skipped"))

    return finish()
