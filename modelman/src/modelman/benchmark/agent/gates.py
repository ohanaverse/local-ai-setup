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
        report.results.append(GateResult(gate_number, GATE_NAMES[gate_number], outcome, code, detail))
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
    diff_result = subprocess.run(
        ["git", "diff", "--quiet", "HEAD"], cwd=workspace.root, capture_output=True, text=True
    )
    has_diff = diff_result.returncode != 0
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

    # Gates 8-9 are added in Task 10.
    skipped_from(8)
    return finish()
