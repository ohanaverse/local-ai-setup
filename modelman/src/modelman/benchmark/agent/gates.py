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
