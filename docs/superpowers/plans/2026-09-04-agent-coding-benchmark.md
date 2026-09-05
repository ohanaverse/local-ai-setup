# Speed + Quality Agentic Coding Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `modelman benchmark agent` — a harness that runs a real coding task through the `pi` agent across a matrix of model/thinking/route configurations, grades the result on deterministic gates plus a blind LLM rubric, and reports speed and quality as separable, comparable columns.

**Architecture:** A new `modelman/src/modelman/benchmark/agent/` sub-package, mounted as a Typer sub-app on the existing `benchmark_app` (`modelman benchmark agent ...`). It reuses the existing benchmark subsystem's isolation helpers (`modelman.benchmark.isolation`), results directory convention, and state-pointer pattern, but owns its own suite/task/row model rather than extending the single-turn `workloads/` machinery, since an agent row is a multi-request agentic run, not a single completion.

**Tech Stack:** Python 3.13, `uv`, Typer, `requests`, stdlib `tomllib`/`subprocess`/`unittest`/`gzip`. No new third-party dependencies — the design's judge transport reuses the `requests` plumbing already in `benchmark/workloads/base.py`.

**Spec:** `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md` — this plan implements it section by section; executors should read both. Anything this plan doesn't spell out in full is settled by the spec.

## Global Constraints

- Python `==3.13.*`, everything runs via `uv run` (`modelman/CLAUDE.md`) — never invoke `python`/`pytest` directly.
- No new runtime dependencies. Judge HTTP calls use `requests` (already a dependency); TOML parsing uses stdlib `tomllib`/`tomli_w` (already used by `registry.py`/`state.py`).
- `pyproject.toml`'s `[tool.hatch.build.targets.wheel] packages = ["src/modelman"]` already covers nested sub-packages (the existing `benchmark/workloads/` sub-package needed no separate entry) — **do not add a `benchmark.agent` entry**, it would be redundant.
- All new tests are hermetic: no GPU, no network, no live provider, no live `ollama`/LiteLLM process. Every subprocess call (`git`, `pi`, `python -m unittest`) in tests either runs against a fixture the test controls or is mocked.
- Every test file gets a module or per-test comment stating what behavior is covered and why it matters (this repo's convention — see `modelman/tests/benchmark/test_runner.py` for the exact style to match).
- Follow `modelman/tests/benchmark/` conventions: one test file per module, `tmp_path`-based fixtures, env-var path redirection (`MODELMAN_REGISTRY`, `MODELMAN_STATE`) rather than mocking the registry/state modules themselves.
- Run focused tests per task (`uv run pytest tests/benchmark/agent/test_X.py -v`) — run `make check`/`make test` in full only on the final task of each phase.
- Commit after every task, referencing this plan (e.g. `feat(agent-bench): add task bundle loader - completes plan item #1`).

---

## Phase 1: Task bundle + workspace

### Task 1: Task bundle loader + mini-drift test fixture

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/__init__.py` (empty)
- Create: `modelman/src/modelman/benchmark/agent/task.py`
- Create: `modelman/tests/benchmark/agent/__init__.py` (empty)
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/task.md`
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/gates.toml`
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/rubric.md`
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/meta.toml`
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/visible/pkg/__init__.py`
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/visible/tests/test_pkg.py`
- Create: `modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/hidden/test_hidden.py`
- Test: `modelman/tests/benchmark/agent/test_task.py`

**Interfaces:**
- Produces: `TaskBundle` dataclass (`task_id: str`, `path: Path`, `task_md: str`, `rubric_md: str`, `gates_config: dict`, `meta: dict`, `visible_dir: Path`, `hidden_dir: Path`); `load_task(path: Path) -> TaskBundle`; `list_task_bundles(root: Path) -> list[TaskBundle]`. Every later module that touches a task bundle imports `TaskBundle`/`load_task` from here — this is the shared vocabulary for the rest of the plan.

This is a tiny fixture bundle (not `day31-drift`, authored in Task 4) used only to exercise the loader and, in Task 2, the workspace — a package with one function, one passing visible test, and one hidden test, so later fixture-heavy tests don't need the full 350-line `day31-drift` bundle just to check plumbing.

- [ ] **Step 1: Write the fixture bundle**

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/task.md`:
```markdown
# Off-by-one in `pkg.add_one`

`pkg.add_one(n)` is supposed to return `n + 1`. Callers report it sometimes
returns `n` unchanged. Find and fix the bug; add a regression test.
```

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/visible/pkg/__init__.py`:
```python
def add_one(n: int) -> int:
    if n % 2 == 0:
        return n  # bug: even inputs aren't incremented
    return n + 1
```

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/visible/tests/test_pkg.py`:
```python
import unittest

from pkg import add_one


class TestAddOne(unittest.TestCase):
    def test_odd_input(self):
        self.assertEqual(add_one(3), 4)


if __name__ == "__main__":
    unittest.main()
```

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/hidden/test_hidden.py`:
```python
import unittest

from pkg import add_one


class TestAddOneHidden(unittest.TestCase):
    def test_even_input_four(self):
        self.assertEqual(add_one(4), 5)

    def test_even_input_six(self):
        self.assertEqual(add_one(6), 7)


if __name__ == "__main__":
    unittest.main()
```

Two independent test methods, not one, so Phase 3's gate tests can exercise a genuine partial hidden-test pass ratio (1 of 2), not just all-pass/all-fail.

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/gates.toml`:
```toml
[hidden]
files = ["test_hidden.py"]
```

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/rubric.md`:
```markdown
# Rubric (fixture — not judged in tests)

| dimension | pts |
|---|---|
| root_cause | 30 |
| approach | 25 |
| test_quality | 20 |
| scope | 15 |
| coherence | 10 |
```

`modelman/tests/benchmark/agent/fixtures/tasks/mini-drift/meta.toml`:
```toml
[meta]
intended_fix = "remove the `n % 2 == 0` special case"

[tiers]
frontier = "6/6"
```

- [ ] **Step 2: Write the failing test**

`modelman/tests/benchmark/agent/test_task.py`:
```python
"""Tests for modelman.benchmark.agent.task — task bundle loading.

Every row config in a suite points at a task bundle by path; if the bundle
is missing a required file the harness must fail at load time with a clear
message, not partway through a run after an agent has already been billed.
"""

from pathlib import Path

import pytest

from modelman.benchmark.agent.task import load_task
from modelman.benchmark.errors import BenchmarkError

FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "tasks"


def test_load_task_reads_all_bundle_fields():
    """A well-formed bundle loads task.md/rubric.md text and gates/meta TOML.

    This is the contract every other agent-bench module depends on: task.py
    is the only place that touches the filesystem layout of a task bundle.
    """
    task = load_task(FIXTURE_ROOT / "mini-drift")
    assert task.task_id == "mini-drift"
    assert "add_one" in task.task_md
    assert task.gates_config["hidden"]["files"] == ["test_hidden.py"]
    assert task.meta["meta"]["intended_fix"].startswith("remove")
    assert (task.visible_dir / "pkg" / "__init__.py").exists()
    assert (task.hidden_dir / "test_hidden.py").exists()


def test_load_task_missing_required_file_raises(tmp_path):
    """A bundle missing gates.toml fails loudly and names the missing file.

    Silently proceeding with a half-loaded bundle would let a suite run
    agents against a task with no way to grade the result.
    """
    bundle = tmp_path / "broken"
    bundle.mkdir()
    (bundle / "task.md").write_text("x", encoding="utf-8")
    (bundle / "rubric.md").write_text("x", encoding="utf-8")
    (bundle / "meta.toml").write_text("", encoding="utf-8")
    (bundle / "visible").mkdir()

    with pytest.raises(BenchmarkError, match="gates.toml"):
        load_task(bundle)


def test_load_task_missing_visible_dir_raises(tmp_path):
    """A bundle with no visible/ directory can't seed a workspace at all."""
    bundle = tmp_path / "broken"
    bundle.mkdir()
    for name in ("task.md", "gates.toml", "rubric.md", "meta.toml"):
        (bundle / name).write_text("", encoding="utf-8")

    with pytest.raises(BenchmarkError, match="visible"):
        load_task(bundle)
```

- [ ] **Step 3: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_task.py -v` (from `modelman/`)
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.benchmark.agent'`

- [ ] **Step 4: Write the implementation**

`modelman/src/modelman/benchmark/agent/__init__.py`: empty file.

`modelman/src/modelman/benchmark/agent/task.py`:
```python
"""Task bundle loading and validation."""

from __future__ import annotations

import tomllib
from dataclasses import dataclass
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError

REQUIRED_FILES = ("task.md", "gates.toml", "rubric.md", "meta.toml")


@dataclass
class TaskBundle:
    task_id: str
    path: Path
    task_md: str
    rubric_md: str
    gates_config: dict
    meta: dict
    visible_dir: Path
    hidden_dir: Path


def load_task(path: Path) -> TaskBundle:
    """Load and validate a task bundle directory.

    Raises BenchmarkError naming every missing required file/dir so a typo'd
    suite fails at preflight, not mid-run after an agent has already run.
    """
    path = Path(path)
    if not path.is_dir():
        raise BenchmarkError(f"task bundle not found: {path}")

    missing = [name for name in REQUIRED_FILES if not (path / name).is_file()]
    visible_dir = path / "visible"
    if not visible_dir.is_dir():
        missing.append("visible/")
    if missing:
        raise BenchmarkError(f"task bundle {path} missing required entries: {', '.join(missing)}")

    with (path / "gates.toml").open("rb") as f:
        gates_config = tomllib.load(f)
    with (path / "meta.toml").open("rb") as f:
        meta = tomllib.load(f)

    return TaskBundle(
        task_id=path.name,
        path=path,
        task_md=(path / "task.md").read_text(encoding="utf-8"),
        rubric_md=(path / "rubric.md").read_text(encoding="utf-8"),
        gates_config=gates_config,
        meta=meta,
        visible_dir=visible_dir,
        hidden_dir=path / "hidden",
    )


def list_task_bundles(root: Path) -> list[TaskBundle]:
    """Return every valid task bundle directly under root, sorted by id."""
    bundles = []
    root = Path(root)
    if not root.is_dir():
        return bundles
    for child in sorted(root.iterdir()):
        if child.is_dir() and (child / "task.md").is_file():
            bundles.append(load_task(child))
    return bundles
```

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_task.py -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/__init__.py \
        modelman/src/modelman/benchmark/agent/task.py \
        modelman/tests/benchmark/agent/__init__.py \
        modelman/tests/benchmark/agent/test_task.py \
        modelman/tests/benchmark/agent/fixtures/tasks/mini-drift
git commit -m "feat(agent-bench): add task bundle loader - completes plan item #1"
```

### Task 2: Workspace — seed, diff, hidden copy-in, baseline worktree

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/workspace.py`
- Test: `modelman/tests/benchmark/agent/test_workspace.py`

**Interfaces:**
- Consumes: `TaskBundle` from Task 1 (`task.visible_dir`, `task.hidden_dir`).
- Produces: `Workspace` dataclass (`root: Path`, `baseline_sha: str`); `create_workspace(task: TaskBundle, base_dir: Path | None = None) -> Workspace`; `destroy_workspace(workspace: Workspace) -> None`; `Workspace` methods `.seed_hidden(task: TaskBundle) -> None`, `.diff() -> str`, `.new_files_since_baseline() -> list[Path]`, `.modified_or_deleted_since_baseline() -> list[Path]`, `.file_at_baseline(relpath: str) -> str | None`, `.checkout_baseline_worktree(dest: Path) -> None`, `.remove_worktree(dest: Path) -> None`. `gates.py` (Task 12) and `pidriver.py` (Task 7) both consume this class exactly as named here.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_workspace.py`:
```python
"""Tests for modelman.benchmark.agent.workspace — the scratch git repo an
agent row runs in.

The vacuous-test gate (spec gate 8) depends on being able to check out the
pre-agent baseline commit in a second worktree and run just the agent's new
test file against it; if workspace seeding or the baseline commit is wrong,
every gate built on top of it is unreliable.
"""

from pathlib import Path

from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace

FIXTURE_ROOT = Path(__file__).parent / "fixtures" / "tasks"


def _task():
    return load_task(FIXTURE_ROOT / "mini-drift")


def test_create_workspace_seeds_visible_and_commits_baseline():
    """visible/ becomes the workspace root, committed as a git baseline.

    Everything downstream (diff, gates, the judge's baseline file lookup)
    assumes commit 0 is exactly the seeded visible/ tree, nothing else.
    """
    ws = create_workspace(_task())
    try:
        assert (ws.root / "pkg" / "__init__.py").exists()
        assert (ws.root / "tests" / "test_pkg.py").exists()
        assert ws.baseline_sha
        assert ws.diff() == ""  # nothing changed yet
    finally:
        destroy_workspace(ws)


def test_diff_reports_new_and_modified_files():
    """A new file and an edited file both show up in diff()/new_files().

    An agent's fix is a new test file plus an edit to the buggy module; both
    kinds of change must be visible or gate 3 (NO_DIFF) and gate 7
    (NO_REGRESSION_TEST) can't tell a real fix from a no-op.
    """
    ws = create_workspace(_task())
    try:
        (ws.root / "pkg" / "__init__.py").write_text(
            "def add_one(n: int) -> int:\n    return n + 1\n", encoding="utf-8"
        )
        (ws.root / "tests" / "test_regression.py").write_text(
            "import unittest\n", encoding="utf-8"
        )
        diff_text = ws.diff()
        assert "add_one" in diff_text
        assert [p.name for p in ws.new_files_since_baseline()] == ["test_regression.py"]
        assert [p.name for p in ws.modified_or_deleted_since_baseline()] == ["__init__.py"]
    finally:
        destroy_workspace(ws)


def test_seed_hidden_copies_hidden_files_into_tests_package():
    """hidden/ files land inside tests/ (joining the visible test package,
    so unittest's dotted module names resolve) only after seed_hidden() is
    called — hidden tests must not exist in the workspace during the run.
    """
    task = _task()
    ws = create_workspace(task)
    try:
        assert not (ws.root / "tests" / "test_hidden.py").exists()
        ws.seed_hidden(task)
        assert (ws.root / "tests" / "test_hidden.py").exists()
    finally:
        destroy_workspace(ws)


def test_checkout_baseline_worktree_has_no_uncommitted_changes(tmp_path):
    """The baseline worktree is a clean checkout of commit 0, isolated from
    the main workspace tree — the vacuous-test gate copies a new test file
    into it without touching the row's own workspace.
    """
    ws = create_workspace(_task())
    dest = tmp_path / "baseline-copy"
    try:
        ws.checkout_baseline_worktree(dest)
        assert (dest / "pkg" / "__init__.py").exists()
        original = (ws.root / "pkg" / "__init__.py").read_text(encoding="utf-8")
        assert (dest / "pkg" / "__init__.py").read_text(encoding="utf-8") == original
    finally:
        ws.remove_worktree(dest)
        destroy_workspace(ws)


def test_file_at_baseline_returns_none_for_new_file():
    """A file that didn't exist at baseline has no baseline content — the
    judge's "seed contents of touched files" input must skip it, not error.
    """
    ws = create_workspace(_task())
    try:
        assert ws.file_at_baseline("pkg/__init__.py") is not None
        assert ws.file_at_baseline("does/not/exist.py") is None
    finally:
        destroy_workspace(ws)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_workspace.py -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'modelman.benchmark.agent.workspace'`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/workspace.py`:
```python
"""Scratch git workspace for one agent-benchmark row."""

from __future__ import annotations

import shutil
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path

from modelman.benchmark.agent.task import TaskBundle
from modelman.benchmark.errors import BenchmarkError

BASELINE_COMMIT_MESSAGE = "baseline"


def _git(args: list[str], cwd: Path, check: bool = True) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["git", *args], cwd=cwd, capture_output=True, text=True, check=False
    )
    if check and result.returncode != 0:
        raise BenchmarkError(f"git {' '.join(args)} failed in {cwd}: {result.stderr.strip()}")
    return result


@dataclass
class Workspace:
    root: Path
    baseline_sha: str

    def seed_hidden(self, task: TaskBundle) -> None:
        """Copy hidden/'s contents into tests/, joining the visible test
        package so unittest's dotted module names (tests.test_x) resolve.
        Called only after the agent run has already finished — hidden
        tests must never be visible during the run itself."""
        if not task.hidden_dir.is_dir():
            return
        shutil.copytree(task.hidden_dir, self.root / "tests", dirs_exist_ok=True)

    def diff(self) -> str:
        """Diff of everything since the baseline commit, tracked mods and
        new files alike. Stages first so untracked new files are included —
        `git diff <sha>` alone ignores untracked paths."""
        _git(["add", "-A"], cwd=self.root)
        return _git(["diff", self.baseline_sha, "--cached", "--"], cwd=self.root).stdout

    def _status_since_baseline(self) -> list[tuple[str, str]]:
        _git(["add", "-A"], cwd=self.root)
        result = _git(
            ["diff", self.baseline_sha, "--cached", "--name-status", "--"], cwd=self.root
        )
        entries = []
        for line in result.stdout.splitlines():
            status, _, name = line.partition("\t")
            entries.append((status, name))
        return entries

    def new_files_since_baseline(self) -> list[Path]:
        return [self.root / name for status, name in self._status_since_baseline() if status == "A"]

    def modified_or_deleted_since_baseline(self) -> list[Path]:
        return [
            self.root / name
            for status, name in self._status_since_baseline()
            if status in ("M", "D")
        ]

    def file_at_baseline(self, relpath: str) -> str | None:
        result = _git(["show", f"{self.baseline_sha}:{relpath}"], cwd=self.root, check=False)
        if result.returncode != 0:
            return None
        return result.stdout

    def checkout_baseline_worktree(self, dest: Path) -> None:
        """Add a detached worktree at the baseline commit for the
        vacuous-test check (gate 8) — a clean copy the harness can drop the
        agent's new test file into without touching the row's own tree."""
        _git(["worktree", "add", "--detach", "--force", str(dest), self.baseline_sha], cwd=self.root)

    def remove_worktree(self, dest: Path) -> None:
        _git(["worktree", "remove", "--force", str(dest)], cwd=self.root, check=False)
        shutil.rmtree(dest, ignore_errors=True)


def create_workspace(task: TaskBundle, base_dir: Path | None = None) -> Workspace:
    """Copy visible/ into a fresh temp dir, git init, commit as baseline."""
    root = Path(tempfile.mkdtemp(prefix="agent-bench-", dir=str(base_dir) if base_dir else None))
    shutil.copytree(task.visible_dir, root, dirs_exist_ok=True)
    _git(["init", "-q"], cwd=root)
    _git(["config", "user.email", "agent-bench@local"], cwd=root)
    _git(["config", "user.name", "agent-bench"], cwd=root)
    _git(["add", "-A"], cwd=root)
    _git(["commit", "-q", "-m", BASELINE_COMMIT_MESSAGE], cwd=root)
    sha = _git(["rev-parse", "HEAD"], cwd=root).stdout.strip()
    return Workspace(root=root, baseline_sha=sha)


def destroy_workspace(workspace: Workspace) -> None:
    shutil.rmtree(workspace.root, ignore_errors=True)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_workspace.py -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/workspace.py \
        modelman/tests/benchmark/agent/test_workspace.py
git commit -m "feat(agent-bench): add git-backed workspace - completes plan item #2"
```

### Task 3: The `day31-drift` task bundle

**Files:**
- Create: `benchmarks/tasks/day31-drift/task.md`
- Create: `benchmarks/tasks/day31-drift/README.md` (copied into `visible/README.md`, see below)
- Create: `benchmarks/tasks/day31-drift/visible/README.md`
- Create: `benchmarks/tasks/day31-drift/visible/kettlecomb/__init__.py`
- Create: `benchmarks/tasks/day31-drift/visible/kettlecomb/calendarlib.py`
- Create: `benchmarks/tasks/day31-drift/visible/kettlecomb/billing.py`
- Create: `benchmarks/tasks/day31-drift/visible/kettlecomb/ledger.py`
- Create: `benchmarks/tasks/day31-drift/visible/tests/test_ledger.py`
- Create: `benchmarks/tasks/day31-drift/hidden/test_day31.py`
- Create: `benchmarks/tasks/day31-drift/gates.toml`
- Create: `benchmarks/tasks/day31-drift/rubric.md`
- Create: `benchmarks/tasks/day31-drift/meta.toml`

**Interfaces:** None — this is content, loaded by `task.load_task` (Task 1) exactly like the `mini-drift` fixture. `gates.toml`'s `[build]`/`[hidden]` keys are the contract `gates.py` (Task 12) reads.

This is the task bundle the spec designs in detail (`## The first task: day31-drift`). There is no code step here — the deliverable is the content itself, verified by Task 4's integration test.

- [ ] **Step 1: Write the bug report**

`benchmarks/tasks/day31-drift/task.md`:
```markdown
# Bug: prorated credits drift for day-30/day-31 billing anchors

kettlecomb bills metered seats on a monthly cycle. Each cycle,
`Ledger.accrue_cycles` computes a prorated credit amount via
`billing.prorate` and advances the account's billing anchor to the next
month.

Customers whose billing anchor falls on day 30 or 31 are reporting that
their prorated credits are wrong, and it gets worse the longer the account
has been active. Several day-31 accounts have also reported their invoice
date silently creeping earlier in the month over time, especially for
accounts that have been through February.

Find the root cause and fix it. Add a regression test that would have
caught the bug.
```

- [ ] **Step 2: Write the visible package (with the bug)**

`benchmarks/tasks/day31-drift/visible/README.md`:
```markdown
# kettlecomb

An invented metered-seat credit ledger, used only as a benchmark fixture.
Bills a seat on a monthly cycle from a per-account billing anchor date.

- `calendarlib.py` — month-length and month-arithmetic helpers.
- `billing.py` — prorates a cycle's credits from the anchor day and month length.
- `ledger.py` — accrues N billing cycles, advancing the anchor by one month each time.
```

`benchmarks/tasks/day31-drift/visible/kettlecomb/calendarlib.py`:
```python
"""Calendar arithmetic for kettlecomb billing cycles."""

from __future__ import annotations

from datetime import date


def month_length(year: int, month: int) -> int:
    """Number of days in year-month, accounting for leap years."""
    if month == 2:
        is_leap = year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
        return 29 if is_leap else 28
    if month in (4, 6, 9, 11):
        return 30
    return 31


def add_months(d: date, n: int) -> date:
    """Advance d by n months, keeping the same day-of-month where valid."""
    total = d.month - 1 + n
    year = d.year + total // 12
    month = total % 12 + 1
    try:
        return date(year, month, d.day)
    except ValueError:
        return date(year, month, 30)
```

`benchmarks/tasks/day31-drift/visible/kettlecomb/billing.py`:
```python
"""Billing calculations for kettlecomb seat credits."""

from __future__ import annotations

from .calendarlib import month_length


def prorate(anchor_day: int, year: int, month: int, credits_per_cycle: float) -> float:
    """Fraction of credits_per_cycle earned for a cycle starting at
    anchor_day in year-month, prorated by the month's actual length."""
    length = month_length(year, month)
    effective_day = min(anchor_day, length)
    days_active = length - effective_day + 1
    return round(credits_per_cycle * days_active / length, 4)
```

`benchmarks/tasks/day31-drift/visible/kettlecomb/ledger.py`:
```python
"""Metered-seat credit ledger for kettlecomb."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date

from .billing import prorate
from .calendarlib import add_months


@dataclass
class Ledger:
    anchor: date
    credits_per_cycle: float
    history: list[float] = field(default_factory=list)

    def accrue_cycles(self, n: int) -> float:
        """Accrue n billing cycles, advancing the anchor by one month each
        time. Returns total credits earned across all n cycles."""
        total = 0.0
        current = self.anchor
        for _ in range(n):
            credits = prorate(current.day, current.year, current.month, self.credits_per_cycle)
            self.history.append(credits)
            total += credits
            current = add_months(current, 1)
        self.anchor = current
        return total
```

`benchmarks/tasks/day31-drift/visible/kettlecomb/__init__.py`:
```python
"""kettlecomb — an invented metered-seat credit ledger (benchmark fixture domain)."""

from .billing import prorate
from .calendarlib import add_months, month_length
from .ledger import Ledger

__all__ = ["Ledger", "add_months", "month_length", "prorate"]
```

- [ ] **Step 3: Write the shipped visible test (passes as-is)**

`benchmarks/tasks/day31-drift/visible/tests/test_ledger.py`:
```python
"""Baseline regression tests for kettlecomb, shipped with the task.

Every fixture here uses a day-15 billing anchor, which never touches the
add_months day-clamping edge case — these tests pass on both the buggy and
a fixed calendarlib, and exist only to prove an agent's fix didn't break
anything unrelated (spec gate 5, VISIBLE_REGRESSION).
"""

import unittest
from datetime import date

from kettlecomb import Ledger, add_months, month_length, prorate


class TestMonthLength(unittest.TestCase):
    def test_month_length_31_day_month(self):
        self.assertEqual(month_length(2025, 1), 31)

    def test_month_length_30_day_month(self):
        self.assertEqual(month_length(2025, 4), 30)


class TestAddMonthsMidMonth(unittest.TestCase):
    def test_add_one_month_mid_month(self):
        self.assertEqual(add_months(date(2025, 3, 15), 1), date(2025, 4, 15))

    def test_add_months_rolls_year(self):
        self.assertEqual(add_months(date(2025, 11, 15), 3), date(2026, 2, 15))


class TestProrate(unittest.TestCase):
    def test_full_month_from_day_one(self):
        self.assertAlmostEqual(prorate(1, 2025, 3, 100.0), 100.0, places=4)

    def test_mid_month_anchor_prorates_partial(self):
        result = prorate(15, 2025, 3, 100.0)
        self.assertAlmostEqual(result, 100.0 * (31 - 15 + 1) / 31, places=4)


class TestLedgerAccrual(unittest.TestCase):
    def test_accrue_three_cycles_from_day_15_anchor(self):
        ledger = Ledger(anchor=date(2025, 3, 15), credits_per_cycle=100.0)
        total = ledger.accrue_cycles(3)
        self.assertEqual(ledger.anchor, date(2025, 6, 15))
        self.assertGreater(total, 0)
        self.assertEqual(len(ledger.history), 3)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 4: Write the hidden acceptance tests**

`benchmarks/tasks/day31-drift/hidden/test_day31.py`:
```python
"""Hidden acceptance tests for day31-drift. Copied into the workspace only
after the agent's run has finished — never visible during the run."""

import calendar
import unittest
from datetime import date

from kettlecomb import Ledger, add_months, prorate


def _reference_add_months(d: date, n: int) -> date:
    """Independent oracle for the intended fix, deliberately not reusing
    kettlecomb.calendarlib so a still-buggy add_months can't pass by
    coincidence."""
    total = d.month - 1 + n
    year = d.year + total // 12
    month = total % 12 + 1
    day = min(d.day, calendar.monthrange(year, month)[1])
    return date(year, month, day)


class TestAddMonthsBoundaries(unittest.TestCase):
    def test_day31_anchor_into_february_non_leap(self):
        self.assertEqual(add_months(date(2025, 1, 31), 1), date(2025, 2, 28))

    def test_day31_anchor_into_february_leap(self):
        self.assertEqual(add_months(date(2024, 1, 31), 1), date(2024, 2, 29))

    def test_day31_anchor_into_30_day_month(self):
        self.assertEqual(add_months(date(2025, 1, 31), 3), date(2025, 4, 30))

    def test_december_anchor_rolls_year(self):
        self.assertEqual(add_months(date(2025, 12, 15), 1), date(2026, 1, 15))

    def test_february_29_anchor_twelve_months_later(self):
        self.assertEqual(add_months(date(2024, 2, 29), 12), date(2025, 2, 28))

    def test_eight_cycle_accumulation_from_day31_anchor_matches_independent_calc(self):
        ledger = Ledger(anchor=date(2025, 1, 31), credits_per_cycle=100.0)
        total = ledger.accrue_cycles(8)

        expected_dates = []
        current = date(2025, 1, 31)
        for _ in range(8):
            expected_dates.append(current)
            current = _reference_add_months(current, 1)
        expected_total = sum(prorate(d.day, d.year, d.month, 100.0) for d in expected_dates)

        self.assertEqual(ledger.anchor, current)
        self.assertAlmostEqual(total, expected_total, places=6)
        self.assertAlmostEqual(sum(ledger.history), expected_total, places=6)


if __name__ == "__main__":
    unittest.main()
```

- [ ] **Step 5: Write gates.toml, rubric.md, meta.toml**

`benchmarks/tasks/day31-drift/gates.toml`:
```toml
[build]
import_check = "kettlecomb"
tests_dir = "tests"

[hidden]
files = ["test_day31.py"]
```

`benchmarks/tasks/day31-drift/rubric.md`:
```markdown
# Judge rubric — day31-drift

You are scoring a diff produced by a coding agent asked to fix a reported
bug in an invented codebase called `kettlecomb`. You do not know whether
this code passes tests; do not speculate about test results. Score only
what the diff and the agent's closing message show you.

| dimension | pts | question |
|---|---|---|
| root_cause | 30 | did the change land in month arithmetic (`calendarlib.add_months`) or one layer removed (`billing.prorate`); a diff that only special-cases a day number or month number is a symptom patch |
| approach | 25 | general last-day clamping + leap-aware month lengths + year rollover, rather than a fix that happens to work by coincidence |
| test_quality | 20 | does the added test name a real calendar boundary and assert the specific behavior, rather than asserting something trivially true |
| scope | 15 | penalize drive-by rewrites, public API changes, new dependencies, or a truncated diff (see the `[TRUNCATED]` marker) |
| coherence | 10 | does the closing message match what the diff actually does |

Respond with strict JSON, no prose outside the object:

```json
{"scores": {"root_cause": 0, "approach": 0, "test_quality": 0, "scope": 0, "coherence": 0},
 "total": 0,
 "verdict": "symptom_patch | partial | principled_fix | no_useful_change",
 "flags": ["..."],
 "rationale": "..."}
```
```

`benchmarks/tasks/day31-drift/meta.toml`:
```toml
# Never shown to the agent or the judge — for the human reading the report.
[meta]
intended_root_cause = "calendarlib.add_months clamps an invalid day to a hardcoded 30 instead of the target month's actual last day, and the clamp target isn't leap-aware"
intended_fix = "day = min(d.day, month_length(year, month)); return date(year, month, day) — no try/except needed"

[tiers]
frontier_cloud = "6/6 with a real regression test"
mid_tier_cloud_and_healthy_local_27b = "3-5/6, typically failing the leap-year or accumulation case"
quantized_or_smaller_local = "plausible symptom patch in prorate; non-empty diff, self-consistent explanation, 0 composite after cap"
```

- [ ] **Step 6: Commit**

```bash
git add benchmarks/tasks/day31-drift
git commit -m "feat(agent-bench): add day31-drift task bundle - completes plan item #3"
```

### Task 4: Hand-verify the bundle — correct fix, symptom patch, vacuous check

**Files:**
- Test: `modelman/tests/benchmark/agent/test_day31_drift_bundle.py`

**Interfaces:**
- Consumes: `load_task` (Task 1), `create_workspace`/`destroy_workspace`/`Workspace.seed_hidden`/`Workspace.checkout_baseline_worktree`/`Workspace.remove_worktree` (Task 2).
- Produces: nothing new — this is the Phase 1 deliverable the spec calls "verified by hand," expressed as an automated integration test so it stays a regression check rather than a one-off manual run.

This is the phase gate: it proves the `day31-drift` bundle (Task 3) and the workspace primitives (Task 2) actually do what the spec claims — a correct fix clears the hidden suite, a plausible symptom patch does not, and the git-worktree vacuous-test mechanism correctly separates a real regression test from a trivial one.

- [ ] **Step 1: Write the test**

`modelman/tests/benchmark/agent/test_day31_drift_bundle.py`:
```python
"""Integration test for the day31-drift task bundle itself (not the harness
code) — hand-verifies the spec's Phase 1 claims: a correct fix clears every
hidden test, a symptom patch does not, and the vacuous-test mechanism (a
git worktree at the baseline commit) correctly tells a real regression test
apart from one that trivially passes on unfixed code.
"""

import json
import subprocess
import sys
from pathlib import Path

from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace

TASK_ROOT = Path(__file__).resolve().parents[3] / "benchmarks" / "tasks" / "day31-drift"

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_day31_drift_bundle.py -v`
Expected: FAIL at collection (`modelman.benchmark.agent.workspace` doesn't exist yet) if run before Task 2, or PASS immediately after Tasks 1–3 are done — this test only exercises bundle content and already-built workspace primitives, so there's no red step of its own once those exist. Run it once now to confirm all 4 cases pass.

- [ ] **Step 3: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_day31_drift_bundle.py -v`
Expected: PASS (4 tests). If `test_symptom_patch_fails_hidden_tests` unexpectedly passes 6/6, or `test_correct_fix_clears_all_hidden_tests` fails, the bundle content in Task 3 has a bug — fix the bundle, not the test.

- [ ] **Step 4: Commit**

```bash
git add modelman/tests/benchmark/agent/test_day31_drift_bundle.py
git commit -m "test(agent-bench): hand-verify day31-drift bundle - completes plan item #4"
```

---

## Phase 2: pi driver + metrics

**Ordering note:** the spec's module table lists `pidriver.py` as depending on `suite.py`. This plan builds `pidriver.py` first (Phase 2) and `suite.py` fourth (Phase 4) so Phase 2 is testable standalone, per the spec's own phasing ("Phase 1–2 are the only phases with genuine unknown risk"). `RowConfig` and `DirectRouteConfig` are therefore defined in `pidriver.py`, and `suite.py` imports them from there in Phase 4 — the reverse of the spec's table, but the same two types either way.

### Task 5: Route resolution + `models.json` generation

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Test: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Produces: `RowConfig` (`label, model_id, thinking, route, provider_id`), `DirectRouteConfig` (`base_url, api`), `PiTarget` (`pi_provider, launch_id, base_url, api, api_key, context_window, reasoning`, property `.model_arg`), `resolve_pi_target(row, model_name, routes_direct, live_models_path=LIVE_PI_MODELS_PATH) -> PiTarget`, `build_models_json(target) -> dict`, `write_pi_config(target, config_dir) -> Path`, `build_pi_command(target, thinking, session_dir, prompt) -> list[str]`. `suite.py` (Task 14) imports `RowConfig`/`DirectRouteConfig`; `runner.py` (Task 16) imports everything else from this module.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_pidriver.py`:
```python
"""Tests for modelman.benchmark.agent.pidriver — route resolution and pi
config generation.

Route resolution is the part of the design most likely to silently
misaddress a model (spec: "a registry id sent under the wrong provider
could never be addressed and its bare form would be sent upstream, a 400
at the gateway") — these tests pin the litellm-vs-direct addressing rules
exactly as the spec's Route resolution section states them.
"""

import json
from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import (
    DirectRouteConfig,
    RowConfig,
    build_models_json,
    build_pi_command,
    resolve_pi_target,
    write_pi_config,
)
from modelman.benchmark.errors import BenchmarkError


def test_litellm_route_keys_by_full_registry_id(tmp_path):
    """route=litellm addresses pi's litellm provider by the full registry
    model id — the LiteLLM model_list is keyed on it, not the bare name."""
    live = tmp_path / "models.json"
    live.write_text(
        json.dumps(
            {
                "providers": {
                    "litellm": {
                        "baseUrl": "http://localhost:4000/v1",
                        "apiKey": "sk-live-key",
                        "models": [{"id": "ollama/qwen3.8:27b-mlx", "contextWindow": 131072, "reasoning": False}],
                    }
                }
            }
        ),
        encoding="utf-8",
    )
    row = RowConfig(label="r1", model_id="ollama/qwen3.8:27b-mlx", thinking="off", route="litellm", provider_id="ollama")

    target = resolve_pi_target(row, "qwen3.8:27b-mlx", routes_direct={}, live_models_path=live)

    assert target.pi_provider == "litellm"
    assert target.launch_id == "ollama/qwen3.8:27b-mlx"
    assert target.model_arg == "litellm/ollama/qwen3.8:27b-mlx"
    assert target.api_key == "sk-live-key"
    assert target.context_window == 131072
    assert target.reasoning is False


def test_direct_route_keys_by_bare_model_name(tmp_path):
    """route=direct addresses the backend's own endpoint by its bare
    model_name, per [routes.direct.<provider>] in the suite."""
    row = RowConfig(label="r1", model_id="omlx/Ornith-1.5-35B-A3B-MLX-6bit", thinking="high", route="direct", provider_id="omlx")
    routes_direct = {"omlx": DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")}

    target = resolve_pi_target(row, "Ornith-1.5-35B-A3B-MLX-6bit", routes_direct, live_models_path=tmp_path / "missing.json")

    assert target.pi_provider == "omlx"
    assert target.launch_id == "Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.model_arg == "omlx/Ornith-1.5-35B-A3B-MLX-6bit"
    assert target.base_url == "http://localhost:8000/v1"
    assert target.api_key == "ollama"  # placeholder — pi rejects an empty apiKey


def test_direct_route_missing_config_block_raises(tmp_path):
    """A direct row with no matching [routes.direct.<provider>] block fails
    at resolution with the provider name, not a KeyError deep in dict access."""
    row = RowConfig(label="r1", model_id="omlx/x", thinking="off", route="direct", provider_id="omlx")
    with pytest.raises(BenchmarkError, match="omlx"):
        resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")


def test_litellm_route_missing_key_raises(tmp_path):
    """No working LiteLLM apiKey anywhere means the row can't be judged or
    run through the gateway — fail clearly instead of sending an empty key."""
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    with pytest.raises(BenchmarkError, match="apiKey"):
        resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")


def test_build_models_json_matches_wt_pi_models_shape(tmp_path):
    """Provider/entry shape mirrors wt/internal/agents/pi_models.go exactly
    (id/contextWindow/input/reasoning/_launch) so an agent row behaves like
    a wt-launched pi session."""
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    target = resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")
    doc = build_models_json(target)
    entry = doc["providers"]["litellm"]["models"][0]
    assert entry["_launch"] is True
    assert entry["id"] == "ollama/x"
    assert entry["input"] == ["text", "image"]


def test_write_pi_config_writes_models_json(tmp_path):
    row = RowConfig(label="r1", model_id="ollama/x", thinking="off", route="litellm", provider_id="ollama")
    target = resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")
    config_dir = tmp_path / "run-config"
    path = write_pi_config(target, config_dir)
    assert path == config_dir / "models.json"
    assert json.loads(path.read_text(encoding="utf-8"))["providers"]["litellm"]["models"][0]["id"] == "ollama/x"


def test_build_pi_command_shape(tmp_path):
    row = RowConfig(label="r1", model_id="ollama/x", thinking="high", route="litellm", provider_id="ollama")
    target = resolve_pi_target(row, "x", routes_direct={}, live_models_path=tmp_path / "missing.json")
    cmd = build_pi_command(target, "high", tmp_path / "session", "do the task")
    assert cmd[0] == "pi"
    assert "--mode" in cmd and "json" in cmd
    assert cmd[cmd.index("--model") + 1] == "litellm/ollama/x"
    assert cmd[cmd.index("--thinking") + 1] == "high"
    assert cmd[-1] == "do the task"
    assert "--no-approve" in cmd
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/pidriver.py`:
```python
"""pi agent driver: route resolution, models.json generation, process
execution, and metrics extraction from pi's `--mode json` event stream."""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from modelman.benchmark.errors import BenchmarkError

DEFAULT_CONTEXT_WINDOW = 262144
LIVE_PI_MODELS_PATH = Path.home() / ".pi" / "agent" / "models.json"

PI_BASE_ARGS = [
    "--mode", "json",
    "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files",
    "--no-approve",
]


@dataclass
class DirectRouteConfig:
    base_url: str
    api: str


@dataclass
class RowConfig:
    label: str
    model_id: str
    thinking: str
    route: str  # "direct" | "litellm"
    provider_id: str


@dataclass
class PiTarget:
    pi_provider: str
    launch_id: str
    base_url: str
    api: str
    api_key: str
    context_window: int
    reasoning: bool

    @property
    def model_arg(self) -> str:
        return f"{self.pi_provider}/{self.launch_id}"


def _load_live_models(path: Path) -> dict:
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {}


def _lookup_live_entry(live: dict, pi_provider: str, launch_id: str) -> dict | None:
    provider = live.get("providers", {}).get(pi_provider, {})
    for entry in provider.get("models", []):
        if entry.get("id") == launch_id:
            return entry
    return None


def resolve_pi_target(
    row: RowConfig,
    model_name: str,
    routes_direct: dict[str, DirectRouteConfig],
    live_models_path: Path = LIVE_PI_MODELS_PATH,
) -> PiTarget:
    """Map (model, route) to a pi provider/launch id.

    litellm route keys by the full registry model id (the LiteLLM
    model_list is keyed on it); direct route keys by the backend's own bare
    model_name — mirrors wt's addressing convention exactly, since pi
    splits --model on the first slash and a registry id under the wrong
    provider "could never be addressed" (spec, Verified against the live
    setup).
    """
    live = _load_live_models(live_models_path)

    if row.route == "litellm":
        pi_provider = "litellm"
        launch_id = row.model_id
        litellm_entry = live.get("providers", {}).get("litellm", {})
        base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
        api = "openai-completions"
        api_key = litellm_entry.get("apiKey")
        if not api_key:
            raise BenchmarkError(
                "no LiteLLM apiKey found in ~/.pi/agent/models.json; launch a wt "
                "pi session in litellm mode at least once to seed it"
            )
    elif row.route == "direct":
        direct_cfg = routes_direct.get(row.provider_id)
        if direct_cfg is None:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )
        pi_provider = row.provider_id
        launch_id = model_name
        base_url = direct_cfg.base_url
        api = direct_cfg.api
        api_key = "ollama"  # pi rejects an empty apiKey even for keyless local backends
    else:
        raise BenchmarkError(f"row {row.label!r} has unknown route: {row.route!r}")

    live_entry = _lookup_live_entry(live, pi_provider, launch_id) or {}
    context_window = live_entry.get("contextWindow", DEFAULT_CONTEXT_WINDOW)
    reasoning = live_entry.get("reasoning", True)

    return PiTarget(
        pi_provider=pi_provider,
        launch_id=launch_id,
        base_url=base_url,
        api=api,
        api_key=api_key,
        context_window=context_window,
        reasoning=reasoning,
    )


def build_models_json(target: PiTarget) -> dict:
    """Provider ids and entry shape mirror wt/internal/agents/pi_models.go
    exactly (id/contextWindow/input/reasoning/_launch) so an agent row
    behaves like a wt-launched session."""
    return {
        "providers": {
            target.pi_provider: {
                "api": target.api,
                "apiKey": target.api_key,
                "baseUrl": target.base_url,
                "models": [
                    {
                        "_launch": True,
                        "contextWindow": target.context_window,
                        "id": target.launch_id,
                        "input": ["text", "image"],
                        "reasoning": target.reasoning,
                    }
                ],
            }
        }
    }


def write_pi_config(target: PiTarget, config_dir: Path) -> Path:
    """Write models.json into a per-run PI_CODING_AGENT_DIR. The caller
    removes config_dir in a finally block — it contains the LiteLLM key."""
    config_dir.mkdir(parents=True, exist_ok=True)
    path = config_dir / "models.json"
    path.write_text(json.dumps(build_models_json(target), indent=2), encoding="utf-8")
    return path


def build_pi_command(target: PiTarget, thinking: str, session_dir: Path, prompt: str) -> list[str]:
    return [
        "pi",
        *PI_BASE_ARGS,
        "--session-dir", str(session_dir),
        "--model", target.model_arg,
        "--thinking", thinking,
        "-p", prompt,
    ]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py
git commit -m "feat(agent-bench): add pi route resolution + config gen - completes plan item #5"
```

### Task 6: Fake agent fixture + process streaming with hard timeout

**Files:**
- Create: `modelman/tests/benchmark/agent/fixtures/fake_agent.py`
- Modify: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Modify: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Produces: `PiRunResult` (`completed: bool, timed_out: bool, wall_ms: int, events: list[dict], unparsed_lines: int, message_end_seen: bool`); `run_pi_process(cmd: list[str], cwd: Path, env: dict[str, str], timeout_s: float) -> PiRunResult`. `gates.py` (Task 10) and `runner.py` (Task 16) consume `PiRunResult` exactly as named here; Task 7's `compute_metrics` consumes it too.

The fake agent pins the exact JSONL event shape from the spec's "Verified against the live setup" section (`session, agent_start, turn_start, message_start, message_update` with `thinking_delta`/`text_delta`, `message_end` with `usage.input/output/reasoning`, `tool_execution_start`/`tool_execution_end`, `turn_end, agent_end, agent_settled`) so every later test builds on one fixed, known-good fixture.

- [ ] **Step 1: Write the fake agent fixture**

`modelman/tests/benchmark/agent/fixtures/fake_agent.py`:
```python
#!/usr/bin/env python3
"""Deterministic stand-in for `pi --mode json`, used by pidriver tests.

Emits exactly the event shape pidriver.py parses (see the spec's "Verified
against the live setup" section): session, agent_start, turn_start,
message_start, message_update (thinking_delta/text_delta), message_end
with usage.input/output/reasoning, tool_execution_start/end, turn_end,
agent_end, agent_settled. --hang/--delay/--malformed-line exercise the
timeout and unparsed-line paths without a real multi-minute agent run.
"""

import argparse
import json
import sys
import time


def _emit(event: dict) -> None:
    print(json.dumps(event), flush=True)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--hang", action="store_true")
    parser.add_argument("--delay", type=float, default=0.0)
    parser.add_argument("--malformed-line", action="store_true")
    args, _ = parser.parse_known_args()

    _emit({"type": "session", "id": "fake-session"})
    _emit({"type": "agent_start"})
    _emit({"type": "turn_start"})
    _emit({"type": "message_start", "role": "assistant"})
    time.sleep(args.delay)
    _emit({"type": "message_update", "delta": {"type": "thinking_delta", "text": "..."}})
    _emit({"type": "message_update", "delta": {"type": "text_delta", "text": "Looking into it"}})
    if args.malformed_line:
        print("{not json", flush=True)
    _emit({"type": "tool_execution_start", "name": "read_file"})
    time.sleep(0.05)
    _emit({"type": "tool_execution_end", "name": "read_file"})
    _emit(
        {
            "type": "message_end",
            "usage": {"input": 382, "output": 38, "reasoning": 33, "cache_read": 0, "cost": {"total": 0.0}},
        }
    )
    _emit({"type": "turn_end"})
    _emit({"type": "agent_end"})
    _emit({"type": "agent_settled"})

    if args.hang:
        time.sleep(3600)


if __name__ == "__main__":
    main()
```

- [ ] **Step 2: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_pidriver.py`:
```python
import os
import sys

from modelman.benchmark.agent.pidriver import run_pi_process

FAKE_AGENT = Path(__file__).parent / "fixtures" / "fake_agent.py"


def test_run_pi_process_completes_normally():
    """A normal run parses every event and reports completed/not-timed-out."""
    result = run_pi_process([sys.executable, str(FAKE_AGENT)], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=10)
    assert result.completed is True
    assert result.timed_out is False
    assert result.message_end_seen is True
    assert result.unparsed_lines == 0
    assert len(result.events) >= 8


def test_run_pi_process_counts_unparsed_lines_without_failing():
    """A malformed JSONL line is skipped and counted, never fatal (spec:
    'A line that fails JSON parsing is counted in unparsed_lines and
    skipped')."""
    result = run_pi_process(
        [sys.executable, str(FAKE_AGENT), "--malformed-line"], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=10
    )
    assert result.completed is True
    assert result.unparsed_lines == 1


def test_run_pi_process_kills_process_group_on_timeout():
    """A hung agent is killed (process group, not just the parent) well
    before its own 3600s sleep — proves killpg actually fires rather than
    the harness just waiting the child out."""
    result = run_pi_process(
        [sys.executable, str(FAKE_AGENT), "--hang"], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=0.5
    )
    assert result.timed_out is True
    assert result.completed is False
    assert result.wall_ms < 3000
```

- [ ] **Step 3: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL — `run_pi_process` doesn't exist yet

- [ ] **Step 4: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/pidriver.py` (add these imports to the existing `import` block at the top: `os`, `queue`, `signal`, `subprocess`, `threading`, `time`):
```python
@dataclass
class PiRunResult:
    completed: bool
    timed_out: bool
    wall_ms: int
    events: list[dict]
    unparsed_lines: int
    message_end_seen: bool


def _reader_thread(pipe, q: "queue.Queue[str | None]") -> None:
    for line in iter(pipe.readline, ""):
        q.put(line)
    q.put(None)


def run_pi_process(cmd: list[str], cwd: Path, env: dict[str, str], timeout_s: float) -> PiRunResult:
    """Spawn the agent process, timestamp + parse each JSONL line, and
    enforce a hard timeout by killing the whole process group. pi spawns
    child shell processes for tool execution; killing only the parent
    would orphan a child into the next row's timing and GPU usage."""
    start = time.monotonic()
    proc = subprocess.Popen(
        cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        text=True, start_new_session=True,
    )
    q: queue.Queue[str | None] = queue.Queue()
    reader = threading.Thread(target=_reader_thread, args=(proc.stdout, q), daemon=True)
    reader.start()

    events: list[dict] = []
    unparsed_lines = 0
    message_end_seen = False
    deadline = start + timeout_s
    timed_out = False

    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            timed_out = True
            break
        try:
            line = q.get(timeout=remaining)
        except queue.Empty:
            timed_out = True
            break
        if line is None:
            break
        raw = line.rstrip("\n")
        if not raw:
            continue
        try:
            event = json.loads(raw)
        except json.JSONDecodeError:
            unparsed_lines += 1
            continue
        if event.get("type") == "message_end":
            message_end_seen = True
        events.append({"ts": time.time(), "event": event})

    if timed_out:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        completed = False
    else:
        proc.wait()
        completed = proc.returncode == 0

    wall_ms = int((time.monotonic() - start) * 1000)
    return PiRunResult(
        completed=completed,
        timed_out=timed_out,
        wall_ms=wall_ms,
        events=events,
        unparsed_lines=unparsed_lines,
        message_end_seen=message_end_seen,
    )
```

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (10 tests)

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py \
        modelman/tests/benchmark/agent/fixtures/fake_agent.py
git commit -m "feat(agent-bench): stream pi process with hard timeout kill - completes plan item #6"
```

### Task 7: Metrics — TTFT, gen/e2e throughput, tool time, anomaly flags

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/pidriver.py`
- Modify: `modelman/tests/benchmark/agent/test_pidriver.py`

**Interfaces:**
- Consumes: `PiRunResult` (Task 6).
- Produces: `RowMetrics` dataclass (`wall_ms, requests, turns, ttft_first_ms, ttft_mean_ms, ttft_max_ms, gen_tok_s, e2e_tok_s, in_tok, out_tok, cache_read_tok, reasoning_tok, tool_ms, cost_usd, unparsed_lines, thinking_noop, cold_first_token`); `compute_metrics(run_result: PiRunResult, *, thinking_level: str) -> RowMetrics`. `gates.py` doesn't touch this; `report.py` (Task 22) and `runner.py` (Task 16) both consume `RowMetrics` by these exact field names — the summary table's Speed columns (Task 23) read them directly.

This closes out Phase 2 — the spec calls this "where `gen_tok_s`/`e2e_tok_s` semantics get frozen," so the test below pins the arithmetic with a synthetic, hand-computed event sequence rather than trusting the fake agent's real timing (which is not deterministic enough to assert exact numbers on).

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_pidriver.py`:
```python
from modelman.benchmark.agent.pidriver import PiRunResult, compute_metrics


def _synthetic_run(*, reasoning_tok: int = 33) -> PiRunResult:
    """A hand-timed two-request run: request 1 takes 1.0s wall (0.2s to
    first token), request 2 takes 0.5s wall (0.1s to first token); one
    0.3s tool call sits between them. Total wall clock is fixed at 2000ms
    so e2e_tok_s has a known denominator."""
    events = [
        {"ts": 0.0, "event": {"type": "message_start"}},
        {"ts": 0.2, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "a"}}},
        {"ts": 1.0, "event": {"type": "message_end", "usage": {"input": 100, "output": 20, "reasoning": reasoning_tok}}},
        {"ts": 1.0, "event": {"type": "tool_execution_start"}},
        {"ts": 1.3, "event": {"type": "tool_execution_end"}},
        {"ts": 1.3, "event": {"type": "message_start"}},
        {"ts": 1.4, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "b"}}},
        {"ts": 1.8, "event": {"type": "message_end", "usage": {"input": 50, "output": 10, "reasoning": 0}}},
        {"ts": 1.8, "event": {"type": "turn_start"}},
    ]
    return PiRunResult(
        completed=True, timed_out=False, wall_ms=2000, events=events, unparsed_lines=0, message_end_seen=True
    )


def test_compute_metrics_ttft_and_throughput():
    """gen_tok_s excludes tool time (busy throughput); e2e_tok_s divides by
    wall clock (spec: the gap between the two is diagnostic on its own)."""
    metrics = compute_metrics(_synthetic_run(), thinking_level="high")

    assert metrics.requests == 2
    assert metrics.ttft_first_ms == 200
    assert metrics.ttft_mean_ms == pytest.approx(150.0)
    assert metrics.ttft_max_ms == 200
    assert metrics.in_tok == 150
    assert metrics.out_tok == 30
    assert metrics.reasoning_tok == 33
    assert metrics.tool_ms == 300
    # gen_seconds = (1.0-0.0) + (1.8-1.3) = 1.5s busy; 30 output tokens
    assert metrics.gen_tok_s == pytest.approx(30 / 1.5, rel=1e-3)
    # e2e uses the full 2000ms wall clock, not just busy time
    assert metrics.e2e_tok_s == pytest.approx(30 / 2.0, rel=1e-3)


def test_compute_metrics_flags_thinking_noop():
    """thinking != 'off' but reasoning_tok == 0 means the backend silently
    ignored the level — the row is not comparable to its partner row."""
    metrics = compute_metrics(_synthetic_run(reasoning_tok=0), thinking_level="high")
    assert metrics.thinking_noop is True


def test_compute_metrics_thinking_off_is_never_flagged_as_noop():
    metrics = compute_metrics(_synthetic_run(reasoning_tok=0), thinking_level="off")
    assert metrics.thinking_noop is False


def test_compute_metrics_flags_cold_first_token():
    """ttft_first_ms >= 3x ttft_mean_ms means the model was still loading
    despite warmup — flagged, not silently averaged into the mean."""
    events = [
        {"ts": 0.0, "event": {"type": "message_start"}},
        {"ts": 3.0, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "a"}}},
        {"ts": 3.1, "event": {"type": "message_end", "usage": {"input": 10, "output": 5, "reasoning": 0}}},
        {"ts": 3.1, "event": {"type": "message_start"}},
        {"ts": 3.15, "event": {"type": "message_update", "delta": {"type": "text_delta", "text": "b"}}},
        {"ts": 3.2, "event": {"type": "message_end", "usage": {"input": 10, "output": 5, "reasoning": 0}}},
    ]
    run_result = PiRunResult(completed=True, timed_out=False, wall_ms=3200, events=events, unparsed_lines=0, message_end_seen=True)
    metrics = compute_metrics(run_result, thinking_level="off")
    assert metrics.cold_first_token is True


def test_compute_metrics_end_to_end_with_fake_agent():
    """Sanity check against the real subprocess path (Task 6's fixture),
    not just synthetic event lists — proves compute_metrics accepts exactly
    what run_pi_process actually produces."""
    result = run_pi_process([sys.executable, str(FAKE_AGENT)], cwd=Path.cwd(), env=os.environ.copy(), timeout_s=10)
    metrics = compute_metrics(result, thinking_level="off")
    assert metrics.in_tok == 382
    assert metrics.out_tok == 38
    assert metrics.reasoning_tok == 33
    assert metrics.tool_ms >= 40  # fixture sleeps 0.05s between tool start/end
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: FAIL — `compute_metrics`/`RowMetrics` don't exist yet

- [ ] **Step 3: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/pidriver.py`:
```python
@dataclass
class RowMetrics:
    wall_ms: int
    requests: int
    turns: int
    ttft_first_ms: int | None
    ttft_mean_ms: float | None
    ttft_max_ms: int | None
    gen_tok_s: float | None
    e2e_tok_s: float | None
    in_tok: int
    out_tok: int
    cache_read_tok: int
    reasoning_tok: int
    tool_ms: int
    cost_usd: float
    unparsed_lines: int
    thinking_noop: bool
    cold_first_token: bool


def compute_metrics(run_result: PiRunResult, *, thinking_level: str) -> RowMetrics:
    requests = turns = 0
    in_tok = out_tok = cache_read_tok = reasoning_tok = 0
    cost_usd = 0.0
    tool_ms = 0
    gen_seconds = 0.0
    gen_tokens = 0
    ttfts: list[float] = []

    msg_start_ts: float | None = None
    first_text_seen = False
    tool_start_ts: float | None = None

    for entry in run_result.events:
        ts = entry["ts"]
        ev = entry["event"]
        etype = ev.get("type")

        if etype == "turn_start":
            turns += 1
        elif etype == "message_start":
            requests += 1
            msg_start_ts = ts
            first_text_seen = False
        elif etype == "message_update":
            delta = ev.get("delta", {})
            if delta.get("type") == "text_delta" and not first_text_seen and msg_start_ts is not None:
                first_text_seen = True
                ttfts.append(ts - msg_start_ts)
        elif etype == "message_end":
            if msg_start_ts is not None:
                gen_seconds += max(ts - msg_start_ts, 0.0)
            usage = ev.get("usage", {})
            in_tok += usage.get("input", 0)
            out_tok += usage.get("output", 0)
            reasoning_tok += usage.get("reasoning", 0)
            cache_read_tok += usage.get("cache_read", 0)
            cost_usd += usage.get("cost", {}).get("total", 0.0)
            gen_tokens += usage.get("output", 0)
            msg_start_ts = None
        elif etype == "tool_execution_start":
            tool_start_ts = ts
        elif etype == "tool_execution_end" and tool_start_ts is not None:
            tool_ms += int((ts - tool_start_ts) * 1000)
            tool_start_ts = None

    ttft_first_ms = int(ttfts[0] * 1000) if ttfts else None
    ttft_mean_ms = round(sum(ttfts) / len(ttfts) * 1000, 2) if ttfts else None
    ttft_max_ms = int(max(ttfts) * 1000) if ttfts else None

    gen_tok_s = round(gen_tokens / gen_seconds, 2) if gen_seconds > 0 else None
    e2e_tok_s = round(out_tok / (run_result.wall_ms / 1000), 2) if run_result.wall_ms > 0 else None

    thinking_noop = thinking_level != "off" and reasoning_tok == 0
    cold_first_token = bool(
        ttft_first_ms is not None and ttft_mean_ms and ttft_first_ms >= 3 * ttft_mean_ms
    )

    return RowMetrics(
        wall_ms=run_result.wall_ms,
        requests=requests,
        turns=turns,
        ttft_first_ms=ttft_first_ms,
        ttft_mean_ms=ttft_mean_ms,
        ttft_max_ms=ttft_max_ms,
        gen_tok_s=gen_tok_s,
        e2e_tok_s=e2e_tok_s,
        in_tok=in_tok,
        out_tok=out_tok,
        cache_read_tok=cache_read_tok,
        reasoning_tok=reasoning_tok,
        tool_ms=tool_ms,
        cost_usd=round(cost_usd, 6),
        unparsed_lines=run_result.unparsed_lines,
        thinking_noop=thinking_noop,
        cold_first_token=cold_first_token,
    )
```

Also add `import pytest` at the top of `test_pidriver.py` (used by `pytest.approx` in this task's tests).

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_pidriver.py -v`
Expected: PASS (15 tests)

- [ ] **Step 5: Run the full pidriver+workspace+task test slice and commit**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (all tests from Tasks 1–7)

```bash
git add modelman/src/modelman/benchmark/agent/pidriver.py \
        modelman/tests/benchmark/agent/test_pidriver.py
git commit -m "feat(agent-bench): compute speed metrics from pi event stream - completes plan item #7"
```

---

## Phase 3: Gates

### Task 8: Gate skeleton + gates 1–4 (AGENT_ERROR, TIMEOUT, NO_DIFF, BROKEN_BUILD)

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/gates.py`
- Test: `modelman/tests/benchmark/agent/test_gates.py`

**Interfaces:**
- Consumes: `TaskBundle` (Task 1), `Workspace` (Task 2), `PiRunResult` (Task 6).
- Produces: `GateResult` (`gate_number, name, outcome, code, detail`), `GatesReport` (`results: list[GateResult], hidden_pass: int, hidden_total: int, hidden_evaluated: bool, cap: float`, property `.triggered_codes`), `evaluate(workspace, task, run_result, *, session_file_present: bool) -> GatesReport`. `runner.py` (Task 16) and `report.py` (Task 22) both consume `GatesReport` exactly as named here.

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
    (workspace.root / "tests" / "test_regression.py").write_text(
        "import unittest\n\nclass T(unittest.TestCase):\n    def test_trivial(self):\n        self.assertTrue(True)\n",
        encoding="utf-8",
    )
    # Deliberately don't fix pkg/__init__.py — the "fix" here is a no-op,
    # only the vacuous test is added, so the diff is non-empty (satisfies
    # gate 3) but the bug is untouched.
    (workspace.root / "pkg" / "__init__.py").write_text(
        (workspace.root / "pkg" / "__init__.py").read_text(encoding="utf-8") + "\n# comment\n",
        encoding="utf-8",
    )
    report = evaluate(workspace, _task(), _ok_run(), session_file_present=True)
    assert report.results[7].code == "VACUOUS_TEST"
    assert report.cap == 0.70


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
Expected: PASS (13 tests)

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

TASK_ROOT = Path(__file__).resolve().parents[3] / "benchmarks" / "tasks" / "day31-drift"

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

## Phase 4: Suite + runner + isolation loop

### Task 12: Suite parsing + cartesian row expansion

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/suite.py`
- Test: `modelman/tests/benchmark/agent/test_suite.py`

**Interfaces:**
- Consumes: `RowConfig`, `DirectRouteConfig` from `pidriver.py` (Task 5).
- Produces: `JudgeConfig` (`model, thinking, temperature, samples, max_attempts, route`), `Suite` (`name, task_path, passes, cooldown_s, agent_timeout_s, judge, routes_direct, rows, repair_rounds`), `load_suite(path: Path, registry: Registry) -> Suite`. `runner.py` (Task 14) and `cli.py` (Task 15) both consume `Suite`/`load_suite` exactly as named here.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_suite.py`:
```python
"""Tests for modelman.benchmark.agent.suite — TOML parsing and cartesian
row expansion.

Hand-writing every model x thinking x route combination is how suites stop
getting maintained (spec); these tests pin the expansion arithmetic and the
two error cases most likely to silently misconfigure a run: an unknown
model reference and a missing [routes.direct.<provider>] block.
"""

from pathlib import Path

import pytest

from modelman.benchmark.agent.pidriver import DirectRouteConfig, RowConfig
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import ModelEntry, ProviderEntry, Registry

FIXTURE_ROOT = Path(__file__).parent / "fixtures"


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local"), ProviderEntry(id="omlx", name="oMLX", location="local")],
        models=[
            ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a"),
            ModelEntry(id="omlx/b", family="f", provider_id="omlx", model_name="b-real-name"),
        ],
    )


def _write_suite(tmp_path: Path, body: str) -> Path:
    path = tmp_path / "suite.toml"
    path.write_text(body, encoding="utf-8")
    return path


def test_cartesian_expansion_produces_every_combination(tmp_path):
    body = """
name = "test suite"
task = "some/task"
passes = 1
cooldown_s = 5
agent_timeout_s = 60

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a", "omlx/b"]
thinking = ["off", "high"]
routes = ["direct", "litellm"]
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert len(suite.rows) == 8
    assert {(r.model_id, r.thinking, r.route) for r in suite.rows} == {
        (m, t, rt) for m in ("ollama/a", "omlx/b") for t in ("off", "high") for rt in ("direct", "litellm")
    }
    assert all(r.provider_id in ("ollama", "omlx") for r in suite.rows)
    assert len({r.label for r in suite.rows}) == 8  # every label unique


def test_pinned_row_with_explicit_label(tmp_path):
    body = """
name = "test suite"
task = "some/task"
passes = 1
cooldown_s = 5
agent_timeout_s = 60

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
label = "baseline"
model = "ollama/a"
thinking = "off"
route = "direct"
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert len(suite.rows) == 1
    assert suite.rows[0].label == "baseline"
    assert suite.rows[0].provider_id == "ollama"


def test_unknown_model_reference_raises(tmp_path):
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/does-not-exist"]
thinking = ["off"]
routes = ["direct"]
"""
    with pytest.raises(BenchmarkError, match="does-not-exist"):
        load_suite(_write_suite(tmp_path, body), _registry())


def test_provider_override_distinguishes_shared_backend_variants(tmp_path):
    """A row can override provider= so e.g. omlx 4-bit and 6-bit — served by
    one provider that must be isolated with the exact variant name — stay
    distinguishable, per the spec's route-resolution section."""
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
label = "sixbit"
model = "omlx/b"
thinking = "off"
route = "direct"
provider = "omlx-6bit"
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert suite.rows[0].provider_id == "omlx-6bit"


def test_judge_route_direct_is_rejected(tmp_path):
    """[judge] supports route=litellm only in v1 — direct has no place
    judging cloud-sequenced rows (spec review resolution)."""
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "direct"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
"""
    with pytest.raises(BenchmarkError, match="route"):
        load_suite(_write_suite(tmp_path, body), _registry())


def test_routes_direct_blocks_parsed(tmp_path):
    body = """
name = "test suite"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"
api = "openai-completions"

[[rows]]
models = ["omlx/b"]
thinking = ["off"]
routes = ["direct"]
"""
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    assert suite.routes_direct["omlx"] == DirectRouteConfig(base_url="http://localhost:8000/v1", api="openai-completions")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/suite.py`:
```python
"""Suite TOML parsing and cartesian row expansion."""

from __future__ import annotations

import itertools
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.agent.pidriver import DirectRouteConfig, RowConfig
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry


@dataclass
class JudgeConfig:
    model: str
    thinking: str
    temperature: float
    samples: int
    max_attempts: int
    route: str


@dataclass
class Suite:
    name: str
    task_path: Path
    passes: int
    cooldown_s: float
    agent_timeout_s: float
    judge: JudgeConfig
    routes_direct: dict[str, DirectRouteConfig] = field(default_factory=dict)
    rows: list[RowConfig] = field(default_factory=list)
    repair_rounds: int = 0


def _short_model(model_id: str) -> str:
    return model_id.split("/")[-1]


def _provider_for(model_id: str, registry: Registry) -> str:
    try:
        return registry.model(model_id).provider_id
    except KeyError as exc:
        raise BenchmarkError(f"suite row references unknown model: {model_id}") from exc


def _expand_rows(raw_rows: list[dict], registry: Registry) -> list[RowConfig]:
    rows: list[RowConfig] = []
    index = 0
    for raw in raw_rows:
        if "model" in raw:
            index += 1
            model_id = raw["model"]
            _provider_for(model_id, registry)  # validates the model exists
            provider_id = raw.get("provider") or _provider_for(model_id, registry)
            label = raw.get("label") or f"{index:02d}--{_short_model(model_id)}--{raw['thinking']}--{raw['route']}"
            rows.append(
                RowConfig(label=label, model_id=model_id, thinking=raw["thinking"], route=raw["route"], provider_id=provider_id)
            )
            continue

        models = raw.get("models", [])
        thinkings = raw.get("thinking", [])
        routes = raw.get("routes", [])
        for model_id, thinking, route in itertools.product(models, thinkings, routes):
            index += 1
            provider_id = raw.get("provider") or _provider_for(model_id, registry)
            label = f"{index:02d}--{_short_model(model_id)}--{thinking}--{route}"
            rows.append(RowConfig(label=label, model_id=model_id, thinking=thinking, route=route, provider_id=provider_id))
    return rows


def load_suite(path: Path, registry: Registry) -> Suite:
    path = Path(path)
    with path.open("rb") as f:
        raw = tomllib.load(f)

    judge_raw = raw["judge"]
    if judge_raw["route"] != "litellm":
        raise BenchmarkError(
            f"[judge] route must be 'litellm' in v1, got {judge_raw['route']!r} — "
            "the judge is a robust cloud model reached through the existing proxy"
        )
    judge = JudgeConfig(
        model=judge_raw["model"],
        thinking=judge_raw["thinking"],
        temperature=judge_raw["temperature"],
        samples=judge_raw.get("samples", 1),
        max_attempts=judge_raw.get("max_attempts", 2),
        route=judge_raw["route"],
    )

    routes_direct = {
        provider_id: DirectRouteConfig(base_url=cfg["base_url"], api=cfg["api"])
        for provider_id, cfg in raw.get("routes", {}).get("direct", {}).items()
    }

    repair_rounds = raw.get("repair_rounds", 0)
    if repair_rounds != 0:
        raise BenchmarkError("repair_rounds is not yet supported (v1 ships repair_rounds = 0 only)")

    return Suite(
        name=raw["name"],
        task_path=Path(raw["task"]),
        passes=raw.get("passes", 1),
        cooldown_s=raw.get("cooldown_s", 20),
        agent_timeout_s=raw.get("agent_timeout_s", 420),
        judge=judge,
        routes_direct=routes_direct,
        rows=_expand_rows(raw.get("rows", []), registry),
        repair_rounds=repair_rounds,
    )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/suite.py \
        modelman/tests/benchmark/agent/test_suite.py
git commit -m "feat(agent-bench): add suite parsing + cartesian row expansion - completes plan item #12"
```

### Task 13: Preflight validation

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/suite.py`
- Modify: `modelman/tests/benchmark/agent/test_suite.py`

**Interfaces:**
- Produces: `preflight(suite: Suite, registry: Registry, task: TaskBundle, *, plist_path: Path = LITELLM_PLIST) -> None`. `runner.py` (Task 14) calls this as phase 0, before touching any provider.

Per the spec: preflight must fail fast on isolation helpers missing from `PATH`, a `route=direct` row with no matching `[routes.direct.<provider>]` block, `hidden/` leaking into `visible/`, and a missing judge API key — "rather than dying mid-run after paying for the agent rows."

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_suite.py`:
```python
from modelman.benchmark.agent.task import load_task
from modelman.benchmark.agent.suite import preflight

MINI_DRIFT = Path(__file__).parent / "fixtures" / "tasks" / "mini-drift"


def _passing_suite_toml() -> str:
    return """
name = "test suite"
task = "some/task"

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
"""


def test_preflight_passes_when_everything_is_configured(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(MINI_DRIFT)
    preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_missing_isolation_helper_raises(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: None)
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(MINI_DRIFT)
    with pytest.raises(BenchmarkError, match="llm-isolate-provider"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_missing_direct_route_block_raises(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    body = _passing_suite_toml().replace("[routes.direct.ollama]", "[routes.direct.something-else]")
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    task = load_task(MINI_DRIFT)
    with pytest.raises(BenchmarkError, match="ollama"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_hidden_leaked_into_visible_raises(tmp_path, monkeypatch):
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-test")
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    leaky_task_dir = tmp_path / "leaky-task"
    import shutil as _shutil

    _shutil.copytree(MINI_DRIFT, leaky_task_dir)
    _shutil.copy(leaky_task_dir / "hidden" / "test_hidden.py", leaky_task_dir / "visible" / "tests" / "test_hidden.py")
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(leaky_task_dir)
    with pytest.raises(BenchmarkError, match="test_hidden.py"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")


def test_preflight_missing_openrouter_key_raises(tmp_path, monkeypatch):
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")
    suite = load_suite(_write_suite(tmp_path, _passing_suite_toml()), _registry())
    task = load_task(MINI_DRIFT)
    with pytest.raises(BenchmarkError, match="OPENROUTER_API_KEY"):
        preflight(suite, _registry(), task, plist_path=tmp_path / "missing.plist")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: FAIL — `preflight` doesn't exist yet

- [ ] **Step 3: Write the implementation**

Add to the top of `modelman/src/modelman/benchmark/agent/suite.py` (new imports: `os`, `plistlib`, `shutil`; new import `from modelman.benchmark.agent.task import TaskBundle`):
```python
LITELLM_PLIST = Path.home() / "Library" / "LaunchAgents" / "local.litellm.proxy.plist"
```

Append to `modelman/src/modelman/benchmark/agent/suite.py`:
```python
def _openrouter_key_available(plist_path: Path) -> bool:
    if os.environ.get("OPENROUTER_API_KEY"):
        return True
    if not plist_path.exists():
        return False
    try:
        with plist_path.open("rb") as f:
            data = plistlib.load(f)
    except Exception:
        return False
    return bool(data.get("EnvironmentVariables", {}).get("OPENROUTER_API_KEY"))


def preflight(suite: Suite, registry: Registry, task: TaskBundle, *, plist_path: Path = LITELLM_PLIST) -> None:
    """Fail fast on everything that would otherwise die mid-run, after
    already paying for the agent rows."""
    missing_helpers = [
        name for name in ("llm-isolate-provider", "llm-restore-providers") if shutil.which(name) is None
    ]
    if missing_helpers:
        raise BenchmarkError(
            f"isolation helper(s) not found on PATH: {', '.join(missing_helpers)}. "
            "Ensure local-ai-setup/bin is on PATH."
        )

    for row in suite.rows:
        if row.route == "direct" and row.provider_id not in suite.routes_direct:
            raise BenchmarkError(
                f"row {row.label!r} uses route=direct for provider {row.provider_id!r} "
                f"but no [routes.direct.{row.provider_id}] block is configured"
            )

    if task.hidden_dir.is_dir():
        hidden_names = {p.name for p in task.hidden_dir.rglob("*") if p.is_file()}
        visible_names = {p.name for p in task.visible_dir.rglob("*") if p.is_file()}
        leaked = hidden_names & visible_names
        if leaked:
            raise BenchmarkError(f"hidden/ file name(s) also present under visible/: {', '.join(sorted(leaked))}")

    if suite.judge.model.split("/")[0] == "openrouter" and not _openrouter_key_available(plist_path):
        raise BenchmarkError(
            f"OPENROUTER_API_KEY not found for judge model {suite.judge.model!r}; "
            "check ~/Library/LaunchAgents/local.litellm.proxy.plist"
        )
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_suite.py -v`
Expected: PASS (11 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/suite.py \
        modelman/tests/benchmark/agent/test_suite.py
git commit -m "feat(agent-bench): add suite preflight validation - completes plan item #13"
```

### Task 14: Runner — phase 0 (preflight) + phase 1 (execute) + isolation loop

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/runner.py`
- Test: `modelman/tests/benchmark/agent/test_runner.py`

**Interfaces:**
- Consumes: `Suite`/`preflight` (Task 13), `RowConfig` (Task 5), `TaskBundle`/`load_task` (Task 1), `Workspace`/`create_workspace`/`destroy_workspace` (Task 2), `GatesReport`/`evaluate` (Task 10), everything in `pidriver.py` (Tasks 5–7), `modelman.benchmark.isolation.isolate_provider`/`restore_providers` (existing module).
- Produces: `RowRunResult` (`row, pass_number, row_dir, gates, metrics, diff_raw, error`), `run_suite(suite, registry, *, row_filter=None, results_dir=None, live_models_path=pidriver.LIVE_PI_MODELS_PATH) -> tuple[Path, list[RowRunResult]]`. `cli.py` (Task 15) calls `run_suite`; `report.py` (Task 22) and `judge.py`-wiring (Task 21) both consume `RowRunResult` by these exact field names.

**Important:** call every `pidriver` function as `pidriver.some_function(...)` (module-qualified — `from modelman.benchmark.agent import pidriver`), not `from ... import run_pi_process`. A direct-name import binds a private copy in `runner.py`'s namespace that a test's `monkeypatch.setattr(pidriver_module, "run_pi_process", fake)` cannot reach; the existing `isolation` import already follows the module-qualified pattern for the same reason.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_runner.py`:
```python
"""Tests for modelman.benchmark.agent.runner — phase 0/1 orchestration.

The isolation loop's grouping-by-provider and its failure-containment (one
provider's isolation failure must not abort the rest of the suite,
matching modelman.benchmark.runner's existing per-target error pattern)
are the two behaviors most likely to silently waste a multi-hour local
sweep if they regress. run_pi_process itself is mocked here — Tasks 5-7
already cover its own correctness against a real subprocess.
"""

from pathlib import Path

import pytest

import modelman.benchmark.isolation as isolation_module
from modelman.benchmark.agent import pidriver as pidriver_module
from modelman.benchmark.agent.pidriver import PiRunResult
from modelman.benchmark.agent.runner import run_suite
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import ModelEntry, ProviderEntry, Registry

MINI_DRIFT = Path(__file__).parent / "fixtures" / "tasks" / "mini-drift"


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def _suite_toml(task_path: Path, models: str = '["ollama/a"]') -> str:
    return f"""
name = "test suite"
task = "{task_path}"
passes = 1
cooldown_s = 0
agent_timeout_s = 5

[judge]
model = "some-cloud/model"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[[rows]]
models = {models}
thinking = ["off"]
routes = ["direct"]
"""


def _write_suite(tmp_path: Path, body: str, name: str = "suite.toml") -> Path:
    path = tmp_path / name
    path.write_text(body, encoding="utf-8")
    return path


def _no_diff_run(*args, **kwargs) -> PiRunResult:
    return PiRunResult(completed=True, timed_out=False, wall_ms=50, events=[], unparsed_lines=0, message_end_seen=True)


@pytest.fixture(autouse=True)
def _hermetic_preflight(monkeypatch):
    monkeypatch.setattr("shutil.which", lambda name: f"/usr/local/bin/{name}")


def test_run_suite_isolates_once_per_provider_group(tmp_path, monkeypatch):
    """Two rows on the same provider share one isolate_provider() call."""
    calls = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: calls.append(pid))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: calls.append("restore"))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["ollama/a", "ollama/a"]')
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json"
    )

    assert calls == ["ollama", "restore"]
    assert len(results) == 2
    assert all(r.error is None for r in results)
    assert all(r.gates is not None and r.gates.results[2].code == "NO_DIFF" for r in results)


def test_run_suite_contains_isolation_failure_to_its_group(tmp_path, monkeypatch):
    """An isolation failure marks that provider's rows with the error and
    the suite continues — matching the existing single-turn runner's
    per-target error containment."""

    def _fail_isolate(pid):
        raise BenchmarkError(f"isolation failed for {pid}")

    monkeypatch.setattr(isolation_module, "isolate_provider", _fail_isolate)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json"
    )

    assert len(results) == 1
    assert results[0].error == "isolation failed for ollama"
    assert results[0].gates is None


def test_run_suite_row_filter_selects_by_label(tmp_path, monkeypatch):
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    body = _suite_toml(MINI_DRIFT, models='["ollama/a", "ollama/a"]')
    suite = load_suite(_write_suite(tmp_path, body), _registry())
    wanted_label = suite.rows[0].label
    run_dir, results = run_suite(
        suite,
        _registry(),
        row_filter=[wanted_label],
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
    )
    assert len(results) == 1
    assert results[0].row.label == wanted_label
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/runner.py`:
```python
"""Suite orchestration: phase 0 (preflight) and phase 1 (execute)."""

from __future__ import annotations

import itertools
import os
import shutil
import tempfile
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path

from modelman.benchmark import isolation
from modelman.benchmark.agent import pidriver
from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.gates import evaluate as evaluate_gates
from modelman.benchmark.agent.suite import RowConfig, Suite, preflight
from modelman.benchmark.agent.task import TaskBundle, load_task
from modelman.benchmark.agent.workspace import create_workspace, destroy_workspace
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import Registry

DEFAULT_RESULTS_DIR = Path.home() / ".config" / "local-ai" / "benchmarks"


@dataclass
class RowRunResult:
    row: RowConfig
    pass_number: int
    row_dir: Path
    gates: GatesReport | None
    metrics: pidriver.RowMetrics | None
    diff_raw: str
    error: str | None = None


def _row_dir(run_dir: Path, index: int, row: RowConfig, pass_number: int) -> Path:
    return run_dir / f"{index:02d}--{row.label}--p{pass_number}"


def _prompt_for(task: TaskBundle) -> str:
    return (
        f"{task.task_md}\n\n"
        "Work only inside this repository. When you believe the bug is fixed, "
        "add a regression test under tests/ and stop."
    )


def _run_single_row(
    row: RowConfig,
    pass_number: int,
    task: TaskBundle,
    suite: Suite,
    registry: Registry,
    row_dir: Path,
    live_models_path: Path,
) -> RowRunResult:
    row_dir.mkdir(parents=True, exist_ok=True)
    model = registry.model(row.model_id)
    target = pidriver.resolve_pi_target(row, model.model_name, suite.routes_direct, live_models_path=live_models_path)

    workspace = create_workspace(task)
    config_dir = Path(tempfile.mkdtemp(prefix="agent-bench-config-"))
    try:
        pidriver.write_pi_config(target, config_dir)
        cmd = pidriver.build_pi_command(target, row.thinking, row_dir, _prompt_for(task))
        env = {**os.environ, "PI_CODING_AGENT_DIR": str(config_dir)}
        run_result = pidriver.run_pi_process(cmd, cwd=workspace.root, env=env, timeout_s=suite.agent_timeout_s)
        metrics = pidriver.compute_metrics(run_result, thinking_level=row.thinking)
        session_present = any(row_dir.iterdir())
        gates = evaluate_gates(workspace, task, run_result, session_file_present=session_present)
        diff_raw = workspace.diff()
        return RowRunResult(
            row=row, pass_number=pass_number, row_dir=row_dir, gates=gates, metrics=metrics, diff_raw=diff_raw
        )
    finally:
        shutil.rmtree(config_dir, ignore_errors=True)
        destroy_workspace(workspace)


def _select_rows(rows: list[RowConfig], row_filter: list[str] | None) -> list[RowConfig]:
    if not row_filter:
        return list(rows)
    wanted = set(row_filter)
    return [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]


def run_suite(
    suite: Suite,
    registry: Registry,
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    live_models_path: Path = pidriver.LIVE_PI_MODELS_PATH,
) -> tuple[Path, list[RowRunResult]]:
    """Phase 0 (preflight) + phase 1 (execute): group rows by provider,
    isolate once per group, run each row's agent + gates, restore at the
    end. Judging (phase 2) and the rendered report (phase 3) are added by
    Tasks 19-23."""
    task = load_task(suite.task_path)
    preflight(suite, registry, task)

    rows = _select_rows(suite.rows, row_filter)
    results_dir = results_dir or DEFAULT_RESULTS_DIR
    run_id = datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    run_dir = results_dir / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    results: list[RowRunResult] = []
    index = 0
    ordered = sorted(rows, key=lambda r: r.provider_id)
    for provider_id, group in itertools.groupby(ordered, key=lambda r: r.provider_id):
        group_rows = list(group)
        try:
            isolation.isolate_provider(provider_id)
        except BenchmarkError as exc:
            for row in group_rows:
                index += 1
                for pass_number in range(1, suite.passes + 1):
                    results.append(
                        RowRunResult(
                            row=row,
                            pass_number=pass_number,
                            row_dir=_row_dir(run_dir, index, row, pass_number),
                            gates=None,
                            metrics=None,
                            diff_raw="",
                            error=str(exc),
                        )
                    )
            continue

        for row in group_rows:
            index += 1
            for pass_number in range(1, suite.passes + 1):
                row_dir = _row_dir(run_dir, index, row, pass_number)
                results.append(_run_single_row(row, pass_number, task, suite, registry, row_dir, live_models_path))
                if pass_number < suite.passes:
                    time.sleep(suite.cooldown_s)

    isolation.restore_providers()
    return run_dir, results
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/runner.py \
        modelman/tests/benchmark/agent/test_runner.py
git commit -m "feat(agent-bench): add runner phase 0/1 orchestration - completes plan item #14"
```

### Task 15: CLI (`run`/`list-tasks`/`list-suites`/`--dry-run`) + smoke suite

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/cli.py`
- Modify: `modelman/src/modelman/benchmark/cli.py`
- Create: `benchmarks/suites/smoke.toml`
- Test: `modelman/tests/benchmark/agent/test_cli.py`

**Interfaces:**
- Consumes: `run_suite` (Task 14), `load_suite` (Task 12/13), `list_task_bundles` (Task 1).
- Produces: `agent_app` (Typer sub-app), mounted as `modelman benchmark agent`. This closes Phase 4 — the spec's "first end-to-end multi-row local run happens here."

- [ ] **Step 1: Write the smoke suite**

`benchmarks/suites/smoke.toml`:
```toml
name = "agent bench smoke test"
task = "benchmarks/tasks/day31-drift"
passes = 1
cooldown_s = 0
agent_timeout_s = 180

[judge]
model = "ollama/glm-5.3-flash:cloud"
thinking = "off"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
label = "smoke"
model = "ollama/glm-5.3-flash:cloud"
thinking = "off"
route = "litellm"
```

One pinned row, a fast `:cloud` model reached through LiteLLM — no local model is loaded, so no isolation is needed, matching the spec's live-verification note.

- [ ] **Step 2: Write the failing test**

`modelman/tests/benchmark/agent/test_cli.py`:
```python
"""Tests for modelman.benchmark.agent.cli — the modelman benchmark agent
subcommand surface.

--dry-run resolving the matrix without executing anything is the harness's
own safeguard against loading a 27B model six times because of a typo'd
suite (spec) — the test for it asserts run_suite is never called, not just
that the command exits 0.
"""

from pathlib import Path

from typer.testing import CliRunner

from modelman.benchmark.agent.cli import agent_app
import modelman.benchmark.agent.cli as cli_module
from modelman.registry import ModelEntry, ProviderEntry, Registry
from modelman.benchmark.agent.runner import RowRunResult
from modelman.benchmark.agent.pidriver import RowConfig

FIXTURE_TASKS = Path(__file__).parent / "fixtures" / "tasks"

runner = CliRunner()


def _registry() -> Registry:
    return Registry(
        providers=[ProviderEntry(id="ollama", name="Ollama", location="local")],
        models=[ModelEntry(id="ollama/a", family="f", provider_id="ollama", model_name="a")],
    )


def test_list_tasks_lists_bundles_under_root():
    result = runner.invoke(agent_app, ["list-tasks", "--root", str(FIXTURE_TASKS)])
    assert result.exit_code == 0
    assert "mini-drift" in result.output


def test_dry_run_never_calls_run_suite(tmp_path, monkeypatch):
    def _boom(*args, **kwargs):
        raise AssertionError("run_suite must not be called during --dry-run")

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _boom)

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path), "--dry-run"])
    assert result.exit_code == 0
    assert "01" in result.output
    assert "dry run" in result.output


def test_run_records_agent_last_run_pointer(tmp_path, monkeypatch):
    row = RowConfig(label="r1", model_id="ollama/a", thinking="off", route="direct", provider_id="ollama")
    fake_run_dir = tmp_path / "results" / "20260101-000000"

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(
        cli_module,
        "run_suite",
        lambda *a, **k: (fake_run_dir, [RowRunResult(row=row, pass_number=1, row_dir=fake_run_dir / "01", gates=None, metrics=None, diff_raw="", error=None)]),
    )
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path)])
    assert result.exit_code == 0

    from modelman.state import load_state

    state = load_state()
    assert state.extra["benchmarks"]["agent_last_run"] == str(fake_run_dir)
```

- [ ] **Step 3: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_cli.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 4: Write the implementation**

`modelman/src/modelman/benchmark/agent/cli.py`:
```python
"""CLI for `modelman benchmark agent`."""

from __future__ import annotations

from pathlib import Path

import typer

from modelman.benchmark.agent.runner import run_suite
from modelman.benchmark.agent.suite import load_suite
from modelman.benchmark.agent.task import list_task_bundles
from modelman.benchmark.errors import BenchmarkError
from modelman.registry import load_registry
from modelman.state import load_state, save_state

agent_app = typer.Typer(help="Agentic coding benchmark harness (real coding task, gates + judge).")

DEFAULT_TASKS_DIR = Path("benchmarks/tasks")
DEFAULT_SUITES_DIR = Path("benchmarks/suites")


@agent_app.command("list-tasks")
def list_tasks_cmd(
    root: Path = typer.Option(DEFAULT_TASKS_DIR, "--root", help="Directory containing task bundles"),  # noqa: B008
) -> None:
    """List task bundles under root."""
    for task in list_task_bundles(root):
        hidden_count = len(task.gates_config.get("hidden", {}).get("files", []))
        typer.echo(f"{task.task_id}  (hidden files: {hidden_count})")


@agent_app.command("list-suites")
def list_suites_cmd(
    root: Path = typer.Option(DEFAULT_SUITES_DIR, "--root", help="Directory containing suite TOML files"),  # noqa: B008
) -> None:
    """List suite files under root, with their expanded row count."""
    if not root.is_dir():
        return
    registry = load_registry()
    for path in sorted(root.glob("*.toml")):
        try:
            suite = load_suite(path, registry)
        except BenchmarkError as exc:
            typer.echo(f"{path.name}: error: {exc}", err=True)
            continue
        typer.echo(f"{path.name}  ({suite.name}, {len(suite.rows)} rows)")


@agent_app.command("run")
def run_cmd(
    suite: Path = typer.Option(..., "--suite", help="Path to a suite TOML file"),  # noqa: B008
    row: list[str] = typer.Option([], "--row", help="Row label or index to run (repeatable)"),  # noqa: B008
    results_dir: Path | None = typer.Option(  # noqa: B008
        None, "--results-dir", help="Directory for run artifacts"
    ),
    dry_run: bool = typer.Option(False, "--dry-run", help="Resolve and print the row matrix; run nothing"),
) -> None:
    """Run a suite against a real coding task."""
    registry = load_registry()
    try:
        loaded_suite = load_suite(suite, registry)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    rows = loaded_suite.rows
    if row:
        wanted = set(row)
        rows = [r for i, r in enumerate(rows, start=1) if r.label in wanted or str(i) in wanted]

    if dry_run:
        for i, r in enumerate(rows, start=1):
            typer.echo(
                f"{i:02d}  {r.label}  model={r.model_id}  thinking={r.thinking}  "
                f"route={r.route}  provider={r.provider_id}"
            )
        typer.echo(f"{len(rows)} row(s) resolved, dry run — nothing executed")
        return

    try:
        run_dir, results = run_suite(loaded_suite, registry, row_filter=row or None, results_dir=results_dir)
    except BenchmarkError as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    state = load_state()
    benchmarks = state.extra.setdefault("benchmarks", {})
    benchmarks["agent_last_run"] = str(run_dir)
    save_state(state)

    ok = sum(1 for r in results if r.error is None)
    typer.echo(f"Agent benchmark complete: {len(results)} row(s), {ok} ran without an isolation error")
    typer.echo(f"Results: {run_dir}")


__all__ = ["agent_app"]
```

In `modelman/src/modelman/benchmark/cli.py`, add the import and mount:
```python
from modelman.benchmark.agent.cli import agent_app
```
and, immediately after `benchmark_app = typer.Typer(help="Benchmark local LLM models.")`:
```python
benchmark_app.add_typer(agent_app, name="agent")
```

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_cli.py -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Run the full Phase 4 slice, verify `--dry-run` against the real bundle, and commit**

Run: `uv run pytest tests/benchmark/agent/ -v` — all tests from Tasks 1–15 pass.

Run (manual, from the monorepo root, not `modelman/`): `cd .. && uv run --project modelman modelman benchmark agent run --suite benchmarks/suites/smoke.toml --dry-run` (or `cd modelman && uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run` if running from `modelman/`) — should print exactly one resolved row and "dry run — nothing executed", proving `list_task_bundles`/`load_suite`/the CLI wiring all agree on real paths, not just fixtures.

```bash
git add modelman/src/modelman/benchmark/agent/cli.py \
        modelman/src/modelman/benchmark/cli.py \
        modelman/tests/benchmark/agent/test_cli.py \
        benchmarks/suites/smoke.toml
git commit -m "feat(agent-bench): add agent CLI + smoke suite - completes plan item #15"
```

---

## Phase 5: Judge

### Task 16: Judge core — anonymization, prompt, contract validation, retries

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/judge.py`
- Test: `modelman/tests/benchmark/agent/test_judge.py`

**Interfaces:**
- Produces: `DIMENSIONS`, `MAX_POINTS`, `VERDICTS`; `JudgeContractError`, `JudgeTransportError` (exceptions); `JudgeTransport` (Protocol: `complete(self, prompt: str, *, temperature: float) -> str`); `JudgeScore` (`scores, total, verdict, flags, rationale, raw_text`); `JudgeOutcome` (`status: "scored"|"judge_fail", samples, combined, attempts_used`); `anonymize_diff(diff_text, max_chars=20000) -> str`; `anonymize_message(message) -> str`; `build_prompt(task_md, seed_contents, diff_text, closing_message, rubric_md) -> str`; `parse_response(raw_text) -> JudgeScore`; `judge_row(transport, prompt, *, temperature, samples, max_attempts) -> JudgeOutcome`; `apply_cap(rubric_total, cap) -> int`; `detect_overclaim(closing_message, hidden_pass, hidden_total) -> bool`. Task 17 adds `LiteLLMJudgeTransport` (implements the `JudgeTransport` protocol) to this same module; Task 18 wires `judge_row`/`apply_cap`/`detect_overclaim` into `runner.py`.

A transport implementation signals a network/API failure by raising `JudgeTransportError`, never a bare `requests` exception — `judge_row`'s retry loop only knows about `JudgeContractError` (malformed JSON) and `JudgeTransportError` (transport failed after its own internal retry); this keeps the pure logic in this task decoupled from `requests` entirely, and Task 17's fake-transport tests can simulate a persistent network failure without mocking HTTP.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_judge.py`:
```python
"""Tests for modelman.benchmark.agent.judge — the blind LLM rubric judge.

The rubric score must stay independent of the gates (spec: "you do not
know whether this code passes tests; do not speculate" is what keeps
gate-vs-judge disagreement informative), so build_prompt is tested to
prove gate/hidden-test data never appears in what's sent to the judge —
not just that scores parse correctly.
"""

import json

import pytest

from modelman.benchmark.agent.judge import (
    DIMENSIONS,
    JudgeContractError,
    JudgeOutcome,
    JudgeScore,
    JudgeTransportError,
    anonymize_diff,
    anonymize_message,
    apply_cap,
    build_prompt,
    detect_overclaim,
    judge_row,
    parse_response,
)

VALID_RESPONSE = json.dumps(
    {
        "scores": {"root_cause": 25, "approach": 20, "test_quality": 15, "scope": 12, "coherence": 8},
        "total": 80,
        "verdict": "principled_fix",
        "flags": [],
        "rationale": "Looks correct.",
    }
)


def test_anonymize_diff_strips_temp_workspace_paths():
    diff = "diff --git a/tmp/agent-bench-xyz/kettlecomb/billing.py b/tmp/agent-bench-xyz/kettlecomb/billing.py\n--- a/tmp/agent-bench-xyz/kettlecomb/billing.py\n+++ b/tmp/agent-bench-xyz/kettlecomb/billing.py\n"
    anon = anonymize_diff(diff)
    assert "agent-bench-xyz" not in anon
    assert "diff --git a/<file> b/<file>" in anon


def test_anonymize_diff_truncates_oversized_diffs():
    diff = "x" * 100
    anon = anonymize_diff(diff, max_chars=10)
    assert anon.endswith("[TRUNCATED]\n")
    assert len(anon) < 100


def test_anonymize_message_redacts_model_provider_tokens():
    message = "Fixed using ollama/qwen3.8:27b-mlx, session_id: abc123"
    anon = anonymize_message(message)
    assert "qwen3.8" not in anon
    assert "abc123" not in anon


def test_build_prompt_never_includes_gate_or_hidden_test_data():
    """The judge must be blind to gate results — build_prompt's signature
    has no parameter for them, so this test asserts the invariant at the
    boundary: nothing gate/hidden-shaped ever appears in the rendered text."""
    prompt = build_prompt(
        task_md="Fix the bug.",
        seed_contents={"billing.py": "def prorate(): ..."},
        diff_text="diff --git a/<file> b/<file>\n+fixed",
        closing_message="Done, all tests pass.",
        rubric_md="score this",
    )
    assert "HIDDEN_TESTS_FAILED" not in prompt
    assert "gate" not in prompt.lower()
    assert "do not speculate" in prompt.lower()


def test_parse_response_accepts_valid_contract():
    score = parse_response(VALID_RESPONSE)
    assert score.total == 80
    assert score.verdict == "principled_fix"
    assert set(score.scores) == set(DIMENSIONS)


def test_parse_response_rejects_malformed_json():
    with pytest.raises(JudgeContractError):
        parse_response("not json at all")


def test_parse_response_rejects_missing_dimension():
    bad = json.dumps({"scores": {"root_cause": 10}, "total": 10, "verdict": "partial"})
    with pytest.raises(JudgeContractError, match="approach"):
        parse_response(bad)


def test_parse_response_rejects_unknown_verdict():
    bad = json.loads(VALID_RESPONSE)
    bad["verdict"] = "great_job"
    with pytest.raises(JudgeContractError, match="verdict"):
        parse_response(json.dumps(bad))


class _FakeTransport:
    def __init__(self, responses: list[str | Exception]):
        self.responses = list(responses)
        self.calls = 0

    def complete(self, prompt: str, *, temperature: float) -> str:
        self.calls += 1
        item = self.responses.pop(0)
        if isinstance(item, Exception):
            raise item
        return item


def test_judge_row_succeeds_on_first_valid_response():
    outcome = judge_row(_FakeTransport([VALID_RESPONSE]), "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"
    assert outcome.combined.total == 80
    assert outcome.attempts_used == 1


def test_judge_row_retries_malformed_json_once_then_succeeds():
    transport = _FakeTransport(["not json", VALID_RESPONSE])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"
    assert outcome.attempts_used == 2


def test_judge_row_gives_up_after_max_attempts():
    transport = _FakeTransport(["not json", "still not json"])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "judge_fail"
    assert outcome.combined is None


def test_judge_row_treats_transport_error_like_a_failed_attempt():
    transport = _FakeTransport([JudgeTransportError("network down"), VALID_RESPONSE])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"


def test_judge_row_samples_greater_than_one_takes_median_per_dimension():
    low = json.loads(VALID_RESPONSE)
    low["scores"] = {"root_cause": 10, "approach": 10, "test_quality": 10, "scope": 10, "coherence": 4}
    high = json.loads(VALID_RESPONSE)
    high["scores"] = {"root_cause": 30, "approach": 20, "test_quality": 18, "scope": 14, "coherence": 8}
    transport = _FakeTransport([json.dumps(low), VALID_RESPONSE, json.dumps(high)])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=3, max_attempts=1)
    assert outcome.status == "scored"
    assert outcome.combined.scores["root_cause"] == 25  # median of 10, 25, 30


def test_apply_cap_rounds_to_nearest_int():
    assert apply_cap(80, 0.70) == 56
    assert apply_cap(62, 0.25) == 16  # round(15.5) -> 16 (banker's rounding lands here for .5 cases in general; exact value pinned by this test)


def test_detect_overclaim_when_agent_claims_pass_but_hidden_tests_failed():
    assert detect_overclaim("All tests pass!", hidden_pass=3, hidden_total=6) is True


def test_detect_overclaim_false_when_claim_matches_reality():
    assert detect_overclaim("All tests pass!", hidden_pass=6, hidden_total=6) is False


def test_detect_overclaim_false_when_no_claim_made():
    assert detect_overclaim("I fixed the calendar arithmetic.", hidden_pass=0, hidden_total=6) is False
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/judge.py`:
```python
"""Blind LLM judge for agent-produced diffs."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from statistics import median
from typing import Protocol

DIMENSIONS = ("root_cause", "approach", "test_quality", "scope", "coherence")
MAX_POINTS = {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10}
VERDICTS = {"symptom_patch", "partial", "principled_fix", "no_useful_change"}


class JudgeContractError(Exception):
    """The judge's response didn't satisfy the strict JSON score contract."""


class JudgeTransportError(Exception):
    """A transport failed to get a response at all (network/API failure).

    Transports raise this after exhausting their own internal retry, so
    judge_row's retry loop treats a network failure exactly like a
    malformed response: one more attempt, then JUDGE_FAIL.
    """


class JudgeTransport(Protocol):
    def complete(self, prompt: str, *, temperature: float) -> str: ...


@dataclass
class JudgeScore:
    scores: dict[str, int]
    total: int
    verdict: str
    flags: list[str]
    rationale: str
    raw_text: str


@dataclass
class JudgeOutcome:
    status: str  # "scored" | "judge_fail"
    samples: list[JudgeScore]
    combined: JudgeScore | None
    attempts_used: int


_DIFF_HEADER_RE = re.compile(r"^diff --git a/\S+ b/\S+$", re.MULTILINE)
_MINUS_HEADER_RE = re.compile(r"^--- a/\S+$", re.MULTILINE)
_PLUS_HEADER_RE = re.compile(r"^\+\+\+ b/\S+$", re.MULTILINE)


def anonymize_diff(diff_text: str, max_chars: int = 20000) -> str:
    """Normalize diff headers (they embed temp workspace paths carrying the
    run id) and truncate oversized diffs with an explicit marker."""
    text = _DIFF_HEADER_RE.sub("diff --git a/<file> b/<file>", diff_text)
    text = _MINUS_HEADER_RE.sub("--- a/<file>", text)
    text = _PLUS_HEADER_RE.sub("+++ b/<file>", text)
    if len(text) > max_chars:
        text = text[:max_chars] + "\n[TRUNCATED]\n"
    return text


_MODEL_TOKEN_RE = re.compile(r"\b(?:ollama|omlx|llamacpp|openrouter|litellm)/[\w.\-:]+\b")
_TIMESTAMP_RE = re.compile(r"\b\d{4}-\d{2}-\d{2}T[\d:.]+Z?\b")
_SESSION_TOKEN_RE = re.compile(r"\b(session|token)[-_ ]?id\s*[:=]\s*\S+", re.IGNORECASE)


def anonymize_message(message: str) -> str:
    """Strip model/provider/session/token strings and timestamps."""
    text = _MODEL_TOKEN_RE.sub("<model>", message)
    text = _TIMESTAMP_RE.sub("<timestamp>", text)
    text = _SESSION_TOKEN_RE.sub(lambda m: f"{m.group(1)}: <redacted>", text)
    return text


def build_prompt(
    task_md: str, seed_contents: dict[str, str], diff_text: str, closing_message: str, rubric_md: str
) -> str:
    """Everything the judge sees. No gate results, no hidden tests, no
    meta.toml, no config label, no timing/token stats — the rubric itself
    states the judge must not speculate about test results."""
    seed_section = (
        "\n\n".join(f"--- {path} (baseline) ---\n{content}" for path, content in seed_contents.items())
        or "(no baseline files touched)"
    )
    return (
        f"{rubric_md}\n\n"
        f"## Task\n{task_md}\n\n"
        f"## Baseline contents of touched files\n{seed_section}\n\n"
        f"## Diff\n```diff\n{diff_text}\n```\n\n"
        f"## Agent's closing message\n{anonymize_message(closing_message)}\n\n"
        "Respond with strict JSON matching the schema above. No prose outside the JSON object."
    )


def parse_response(raw_text: str) -> JudgeScore:
    try:
        data = json.loads(raw_text)
    except json.JSONDecodeError as exc:
        raise JudgeContractError(f"response is not valid JSON: {exc}") from exc

    if not isinstance(data, dict):
        raise JudgeContractError("response is not a JSON object")

    missing = [k for k in ("scores", "total", "verdict") if k not in data]
    if missing:
        raise JudgeContractError(f"response missing keys: {missing}")

    scores = data["scores"]
    missing_dims = [d for d in DIMENSIONS if d not in scores]
    if missing_dims:
        raise JudgeContractError(f"scores missing dimensions: {missing_dims}")
    for dim in DIMENSIONS:
        value = scores[dim]
        if not isinstance(value, int) or not (0 <= value <= MAX_POINTS[dim]):
            raise JudgeContractError(f"invalid score for {dim}: {value!r}")

    if data["verdict"] not in VERDICTS:
        raise JudgeContractError(f"unknown verdict: {data['verdict']!r}")

    return JudgeScore(
        scores={d: scores[d] for d in DIMENSIONS},
        total=int(data["total"]),
        verdict=data["verdict"],
        flags=list(data.get("flags", [])),
        rationale=str(data.get("rationale", "")),
        raw_text=raw_text,
    )


def judge_row(
    transport: JudgeTransport, prompt: str, *, temperature: float, samples: int, max_attempts: int
) -> JudgeOutcome:
    collected: list[JudgeScore] = []
    attempts_used = 0
    for _ in range(samples):
        score: JudgeScore | None = None
        for _attempt in range(max_attempts):
            attempts_used += 1
            try:
                raw = transport.complete(prompt, temperature=temperature)
                score = parse_response(raw)
                break
            except (JudgeContractError, JudgeTransportError):
                continue
        if score is None:
            return JudgeOutcome(status="judge_fail", samples=collected, combined=None, attempts_used=attempts_used)
        collected.append(score)

    if len(collected) == 1:
        combined = collected[0]
    else:
        combined_scores = {dim: round(median(s.scores[dim] for s in collected)) for dim in DIMENSIONS}
        combined = JudgeScore(
            scores=combined_scores,
            total=sum(combined_scores.values()),
            verdict=collected[-1].verdict,
            flags=sorted({f for s in collected for f in s.flags}),
            rationale=collected[-1].rationale,
            raw_text="\n---\n".join(s.raw_text for s in collected),
        )
    return JudgeOutcome(status="scored", samples=collected, combined=combined, attempts_used=attempts_used)


def apply_cap(rubric_total: int, cap: float) -> int:
    return round(rubric_total * cap)


_TEST_PASS_CLAIM_RE = re.compile(r"\b(all tests? pass|tests? (?:are )?passing|tests? succeeded)\b", re.IGNORECASE)


def detect_overclaim(closing_message: str, hidden_pass: int, hidden_total: int) -> bool:
    """Free signal: grep the closing message for a test-passing claim and
    compare against the actual hidden-test ratio — a computed column, not
    a judge dimension."""
    return bool(_TEST_PASS_CLAIM_RE.search(closing_message)) and hidden_pass < hidden_total
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: PASS (17 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/judge.py \
        modelman/tests/benchmark/agent/test_judge.py
git commit -m "feat(agent-bench): add judge contract, anonymization, retries - completes plan item #16"
```

### Task 17: LiteLLM HTTP transport with one retry on transport failure

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/judge.py`
- Modify: `modelman/tests/benchmark/agent/test_judge.py`

**Interfaces:**
- Produces: `LiteLLMJudgeTransport(base_url, api_key, model, *, retry_backoff_s=2.0)` implementing `JudgeTransport.complete`. `runner.py` (Task 18) constructs this for the judge phase.

Reuses the `requests` plumbing already in `benchmark/workloads/base.py`'s pattern (direct `requests` calls, no session pooling needed for one request per judge call) rather than a pi subprocess — pi exposes no temperature control, and a fresh HTTP request gives the same context isolation a fresh process would, more cheaply.

- [ ] **Step 1: Write the failing test**

Append to `modelman/tests/benchmark/agent/test_judge.py`:
```python
import requests

import modelman.benchmark.agent.judge as judge_module
from modelman.benchmark.agent.judge import LiteLLMJudgeTransport


class _FakeResponse:
    def __init__(self, status=200, payload=None):
        self.status_code = status
        self._payload = payload or {"choices": [{"message": {"content": "hi"}}]}

    def raise_for_status(self):
        if self.status_code >= 400:
            raise requests.HTTPError(f"status {self.status_code}")

    def json(self):
        return self._payload


def test_litellm_transport_posts_expected_payload(monkeypatch):
    captured = {}

    def fake_post(url, headers=None, json=None, timeout=None):
        captured["url"] = url
        captured["headers"] = headers
        captured["json"] = json
        return _FakeResponse(payload={"choices": [{"message": {"content": '{"total": 1}'}}]})

    monkeypatch.setattr(judge_module.requests, "post", fake_post)
    transport = LiteLLMJudgeTransport(
        base_url="http://localhost:4000/v1", api_key="sk-test", model="openrouter/anthropic/claude-opus-4"
    )
    result = transport.complete("prompt text", temperature=0.0)

    assert result == '{"total": 1}'
    assert captured["url"] == "http://localhost:4000/v1/chat/completions"
    assert captured["headers"]["Authorization"] == "Bearer sk-test"
    assert captured["json"]["model"] == "openrouter/anthropic/claude-opus-4"
    assert captured["json"]["temperature"] == 0.0


def test_litellm_transport_retries_once_then_succeeds(monkeypatch):
    calls = {"n": 0}

    def flaky_post(url, headers=None, json=None, timeout=None):
        calls["n"] += 1
        if calls["n"] == 1:
            raise requests.ConnectionError("boom")
        return _FakeResponse(payload={"choices": [{"message": {"content": "ok"}}]})

    monkeypatch.setattr(judge_module.requests, "post", flaky_post)
    transport = LiteLLMJudgeTransport(base_url="http://localhost:4000/v1", api_key="k", model="m", retry_backoff_s=0.0)
    assert transport.complete("p", temperature=0.0) == "ok"
    assert calls["n"] == 2


def test_litellm_transport_raises_judge_transport_error_after_retry_exhausted(monkeypatch):
    def always_fails(url, headers=None, json=None, timeout=None):
        raise requests.ConnectionError("still down")

    monkeypatch.setattr(judge_module.requests, "post", always_fails)
    transport = LiteLLMJudgeTransport(base_url="http://localhost:4000/v1", api_key="k", model="m", retry_backoff_s=0.0)
    with pytest.raises(JudgeTransportError):
        transport.complete("p", temperature=0.0)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: FAIL — `LiteLLMJudgeTransport` doesn't exist yet

- [ ] **Step 3: Write the implementation**

Add `import time` and `import requests` to the top of `modelman/src/modelman/benchmark/agent/judge.py`, then append:
```python
class LiteLLMJudgeTransport:
    """Direct chat-completions call through LiteLLM at a fixed temperature.
    Not a pi subprocess: pi has no temperature flag, and a fresh HTTP
    request gives the same context isolation a fresh process would, more
    cheaply."""

    def __init__(self, base_url: str, api_key: str, model: str, *, retry_backoff_s: float = 2.0):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.model = model
        self.retry_backoff_s = retry_backoff_s

    def complete(self, prompt: str, *, temperature: float) -> str:
        try:
            return self._post(prompt, temperature)
        except requests.RequestException:
            time.sleep(self.retry_backoff_s)
            try:
                return self._post(prompt, temperature)
            except requests.RequestException as exc:
                raise JudgeTransportError(f"judge transport failed after retry: {exc}") from exc

    def _post(self, prompt: str, temperature: float) -> str:
        response = requests.post(
            f"{self.base_url}/chat/completions",
            headers={"Authorization": f"Bearer {self.api_key}"},
            json={
                "model": self.model,
                "messages": [{"role": "user", "content": prompt}],
                "temperature": temperature,
                "stream": False,
            },
            timeout=120,
        )
        response.raise_for_status()
        data = response.json()
        return data["choices"][0]["message"]["content"]
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_judge.py -v`
Expected: PASS (20 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/judge.py \
        modelman/tests/benchmark/agent/test_judge.py
git commit -m "feat(agent-bench): add LiteLLM judge transport with retry - completes plan item #17"
```

### Task 18: Wire judging into the runner (phase 2) + `--skip-judge`

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/runner.py`
- Modify: `modelman/tests/benchmark/agent/test_runner.py`
- Modify: `modelman/src/modelman/benchmark/agent/cli.py`
- Modify: `modelman/tests/benchmark/agent/test_cli.py`

**Interfaces:**
- Consumes: everything in `judge.py` (Tasks 16–17).
- Produces: `RowRunResult` gains `seed_contents: dict[str, str]`, `closing_message: str`, `judge: JudgeOutcome | None`, `composite: int | None`; `run_suite` gains `skip_judge: bool = False` and `judge_transport_factory: Callable[[JudgeConfig, Path], JudgeTransport] | None = None` (defaults to a real `LiteLLMJudgeTransport` builder — the injection point is what makes this task's tests hermetic). `report.py` (Task 22) reads all four new `RowRunResult` fields by these exact names.

Judging runs **after** `isolation.restore_providers()` on purpose (spec): the judge is a cloud model and must not contend with a loaded local model, and its latency must stay off the measured clock. The `agent judge --run-id/--latest` CLI subcommand that re-judges **persisted** artifacts without re-running any agent is deferred to Phase 6 (Task 24) — it needs `report.py`'s on-disk format to read back from, which doesn't exist yet.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_runner.py` (add `import json` to the existing import block):
```python
def test_run_suite_judges_rows_after_restore_and_sets_composite(tmp_path, monkeypatch):
    """Judging happens after restore_providers, on every row with gates
    evaluated, and the composite is rubric_total x cap (spec Scoring)."""
    order = []
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: order.append(("isolate", pid)))
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: order.append(("restore",)))
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            order.append(("judge",))
            return json.dumps(
                {
                    "scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
                    "total": 100,
                    "verdict": "principled_fix",
                    "flags": [],
                    "rationale": "ok",
                }
            )

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        judge_transport_factory=lambda judge_cfg, path: _FakeJudgeTransport(),
    )

    assert order == [("isolate", "ollama"), ("restore",), ("judge",)]
    assert results[0].judge is not None
    assert results[0].judge.status == "scored"
    # This row is NO_DIFF (cap x0.00), so the composite is 0 regardless of
    # the (fake, maximal) rubric score — proving the cap is actually applied.
    assert results[0].composite == 0


def test_run_suite_skip_judge_leaves_composite_none(tmp_path, monkeypatch):
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite,
        _registry(),
        results_dir=tmp_path / "results",
        live_models_path=tmp_path / "missing.json",
        skip_judge=True,
    )
    assert results[0].judge is None
    assert results[0].composite is None
```

Append to `modelman/tests/benchmark/agent/test_cli.py`:
```python
def test_run_passes_skip_judge_through_to_run_suite(tmp_path, monkeypatch):
    captured = {}

    def _fake_run_suite(suite_obj, registry_obj, **kwargs):
        captured.update(kwargs)
        return tmp_path / "results" / "run1", []

    monkeypatch.setattr(cli_module, "load_registry", lambda: _registry())
    monkeypatch.setattr(cli_module, "run_suite", _fake_run_suite)
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    suite_path = tmp_path / "suite.toml"
    suite_path.write_text(
        """
name = "s"
task = "some/task"

[judge]
model = "x"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[[rows]]
models = ["ollama/a"]
thinking = ["off"]
routes = ["direct"]
""",
        encoding="utf-8",
    )
    result = runner.invoke(agent_app, ["run", "--suite", str(suite_path), "--skip-judge"])
    assert result.exit_code == 0
    assert captured["skip_judge"] is True
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_runner.py tests/benchmark/agent/test_cli.py -v`
Expected: FAIL — `run_suite` doesn't accept `judge_transport_factory`/`skip_judge` yet, and `run_cmd` has no `--skip-judge` flag

- [ ] **Step 3: Extend `runner.py`**

Add these imports to `modelman/src/modelman/benchmark/agent/runner.py` (alongside the existing ones): `import json`, `from collections.abc import Callable`, `from modelman.benchmark.agent import judge`, `from modelman.benchmark.agent.suite import JudgeConfig` (add to the existing `from modelman.benchmark.agent.suite import ...` line).

Change the `RowRunResult` dataclass to:
```python
@dataclass
class RowRunResult:
    row: RowConfig
    pass_number: int
    row_dir: Path
    gates: GatesReport | None
    metrics: pidriver.RowMetrics | None
    diff_raw: str
    seed_contents: dict[str, str] = field(default_factory=dict)
    closing_message: str = ""
    judge: judge.JudgeOutcome | None = None
    composite: int | None = None
    error: str | None = None
```
(add `field` to the existing `from dataclasses import dataclass` line: `from dataclasses import dataclass, field`)

In `_run_single_row`, replace:
```python
        diff_raw = workspace.diff()
        return RowRunResult(
            row=row, pass_number=pass_number, row_dir=row_dir, gates=gates, metrics=metrics, diff_raw=diff_raw
        )
```
with:
```python
        diff_raw = workspace.diff()
        touched = workspace.modified_or_deleted_since_baseline() + workspace.new_files_since_baseline()
        seed_contents = {}
        for path in touched:
            rel = str(path.relative_to(workspace.root))
            content = workspace.file_at_baseline(rel)
            if content is not None:
                seed_contents[rel] = content
        closing_message = _closing_message(run_result)
        return RowRunResult(
            row=row,
            pass_number=pass_number,
            row_dir=row_dir,
            gates=gates,
            metrics=metrics,
            diff_raw=diff_raw,
            seed_contents=seed_contents,
            closing_message=closing_message,
        )
```

Add this helper above `_run_single_row`:
```python
def _closing_message(run_result: pidriver.PiRunResult) -> str:
    """Concatenate text_delta content from the agent's final message only —
    resets on every message_start, so only the last assistant message's
    text survives."""
    parts: list[str] = []
    capturing = False
    for entry in run_result.events:
        ev = entry["event"]
        etype = ev.get("type")
        if etype == "message_start":
            parts = []
            capturing = True
        elif etype == "message_update" and capturing:
            delta = ev.get("delta", {})
            if delta.get("type") == "text_delta":
                parts.append(delta.get("text", ""))
        elif etype == "message_end":
            capturing = False
    return "".join(parts)


def _build_judge_transport(judge_cfg: JudgeConfig, live_models_path: Path) -> judge.JudgeTransport:
    try:
        live = json.loads(live_models_path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        live = {}
    litellm_entry = live.get("providers", {}).get("litellm", {})
    api_key = litellm_entry.get("apiKey")
    if not api_key:
        raise BenchmarkError("no LiteLLM apiKey found in ~/.pi/agent/models.json for the judge transport")
    base_url = litellm_entry.get("baseUrl", "http://localhost:4000/v1")
    return judge.LiteLLMJudgeTransport(base_url=base_url, api_key=api_key, model=judge_cfg.model)


def _judge_all(
    suite: Suite, task: TaskBundle, results: list[RowRunResult], live_models_path: Path, factory
) -> None:
    transport = factory(suite.judge, live_models_path)
    for result in results:
        if result.error is not None or result.gates is None:
            continue
        prompt = judge.build_prompt(
            task_md=task.task_md,
            seed_contents=result.seed_contents,
            diff_text=judge.anonymize_diff(result.diff_raw),
            closing_message=result.closing_message,
            rubric_md=task.rubric_md,
        )
        outcome = judge.judge_row(
            transport,
            prompt,
            temperature=suite.judge.temperature,
            samples=suite.judge.samples,
            max_attempts=suite.judge.max_attempts,
        )
        result.judge = outcome
        result.composite = judge.apply_cap(outcome.combined.total, result.gates.cap) if outcome.status == "scored" else None
```

Change the `run_suite` signature and its tail to:
```python
def run_suite(
    suite: Suite,
    registry: Registry,
    *,
    row_filter: list[str] | None = None,
    results_dir: Path | None = None,
    live_models_path: Path = pidriver.LIVE_PI_MODELS_PATH,
    skip_judge: bool = False,
    judge_transport_factory: Callable[[JudgeConfig, Path], judge.JudgeTransport] | None = None,
) -> tuple[Path, list[RowRunResult]]:
```
and, replacing the final two lines (`isolation.restore_providers()` / `return run_dir, results`):
```python
    isolation.restore_providers()

    if not skip_judge:
        _judge_all(suite, task, results, live_models_path, judge_transport_factory or _build_judge_transport)

    return run_dir, results
```

- [ ] **Step 4: Extend `cli.py`**

In `modelman/src/modelman/benchmark/agent/cli.py`, add a `--skip-judge` option to `run_cmd` and pass it through:
```python
    skip_judge: bool = typer.Option(False, "--skip-judge", help="Skip the judge phase"),
```
(add as a parameter, and change the `run_suite(...)` call to `run_suite(loaded_suite, registry, row_filter=row or None, results_dir=results_dir, skip_judge=skip_judge)`).

- [ ] **Step 5: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py tests/benchmark/agent/test_cli.py -v`
Expected: PASS (all tests, including the two new ones per file)

- [ ] **Step 6: Run the full Phase 5 slice and commit**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (all tests from Tasks 1–18)

```bash
git add modelman/src/modelman/benchmark/agent/runner.py \
        modelman/src/modelman/benchmark/agent/cli.py \
        modelman/tests/benchmark/agent/test_runner.py \
        modelman/tests/benchmark/agent/test_cli.py
git commit -m "feat(agent-bench): wire judging into the runner + --skip-judge - completes plan item #18"
```

---

## Phase 6: Report + artifacts + docs

### Task 19: Per-row artifact writes + `run.toml` with key masking

**Files:**
- Create: `modelman/src/modelman/benchmark/agent/report.py`
- Test: `modelman/tests/benchmark/agent/test_report.py`

**Interfaces:**
- Consumes: `GatesReport` (Task 10), `JudgeOutcome` (Task 16), `RowMetrics` (Task 7). Deliberately does **not** import from `runner.py` (`RowRunResult`) — that would create a circular import (`runner.py` will call into `report.py`). Instead this module defines its own `RowReport` adapter dataclass; `runner.py`/`cli.py` (Task 21) construct one per row from a `RowRunResult`.
- Produces: `RowReport` (`label, model_id, thinking, route, gates, metrics, judge, composite, closing_message, error`); `write_row_artifacts(row_dir, *, events, diff_raw, gates, metrics, judge_outcome) -> None`; `write_run_toml(path, suite_dict, *, git_sha, pi_version) -> None`.

`write_row_artifacts` never writes a session file — pi already writes its own directly into `row_dir` via `--session-dir` (Task 5's `build_pi_command`). It's safe to call more than once per row (once after gates, again after judging adds `judge.json`) since every file is fully overwritten, matching the spec's "written incrementally" requirement without needing a stateful writer.

- [ ] **Step 1: Write the failing test**

`modelman/tests/benchmark/agent/test_report.py`:
```python
"""Tests for modelman.benchmark.agent.report — artifact writes and
run.toml. Key masking is the one test that matters most here: a leaked
LiteLLM API key in a committed or shared run.toml is a credential leak,
not just a bug (spec: "run.toml and all logs mask key values").
"""

import gzip
import json
from pathlib import Path

from modelman.benchmark.agent.gates import GateResult, GatesReport
from modelman.benchmark.agent.pidriver import RowMetrics
from modelman.benchmark.agent.report import write_row_artifacts, write_run_toml


def _gates() -> GatesReport:
    return GatesReport(
        results=[GateResult(gate_number=i, name=f"G{i}", outcome="pass") for i in range(1, 10)],
        hidden_pass=6,
        hidden_total=6,
        hidden_evaluated=True,
        cap=1.0,
    )


def _metrics() -> RowMetrics:
    return RowMetrics(
        wall_ms=1000, requests=2, turns=1, ttft_first_ms=100, ttft_mean_ms=100.0, ttft_max_ms=100,
        gen_tok_s=10.0, e2e_tok_s=8.0, in_tok=100, out_tok=50, cache_read_tok=0, reasoning_tok=0,
        tool_ms=200, cost_usd=0.0, unparsed_lines=0, thinking_noop=False, cold_first_token=False,
    )


def test_write_row_artifacts_writes_expected_files(tmp_path):
    row_dir = tmp_path / "row1"
    write_row_artifacts(
        row_dir,
        events=[{"ts": 1.0, "event": {"type": "session"}}],
        diff_raw="diff --git a/tmp/xyz/f.py b/tmp/xyz/f.py\n",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
    )
    assert (row_dir / "agent.jsonl.gz").exists()
    with gzip.open(row_dir / "agent.jsonl.gz", "rt", encoding="utf-8") as f:
        assert json.loads(f.readline())["event"]["type"] == "session"
    assert (row_dir / "diff.raw.patch").read_text(encoding="utf-8").startswith("diff --git a/tmp/xyz")
    assert "xyz" not in (row_dir / "diff.patch").read_text(encoding="utf-8")
    assert json.loads((row_dir / "gates.json").read_text(encoding="utf-8"))["cap"] == 1.0
    assert json.loads((row_dir / "metrics.json").read_text(encoding="utf-8"))["wall_ms"] == 1000
    assert not (row_dir / "judge.json").exists()  # no judge_outcome -> no file


def test_write_run_toml_masks_api_keys(tmp_path):
    suite_dict = {
        "judge": {"model": "x"},
        "routes": {"direct": {"omlx": {"base_url": "http://localhost:8000/v1", "api": "openai-completions"}}},
        "resolved_api_key": "sk-super-secret-value",
    }
    path = tmp_path / "run.toml"
    write_run_toml(path, suite_dict, git_sha="abc123", pi_version="1.2.3")
    text = path.read_text(encoding="utf-8")
    assert "sk-super-secret-value" not in text
    assert "abc123" in text
    assert "1.2.3" in text
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: FAIL with `ModuleNotFoundError`

- [ ] **Step 3: Write the implementation**

`modelman/src/modelman/benchmark/agent/report.py`:
```python
"""Per-row artifact writes and the run summary report."""

from __future__ import annotations

import gzip
import json
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

import tomli_w

from modelman.benchmark.agent.gates import GatesReport
from modelman.benchmark.agent.judge import JudgeOutcome, anonymize_diff
from modelman.benchmark.agent.pidriver import RowMetrics

_MASKED_KEY_NAMES = {"apiKey", "api_key", "resolved_api_key"}
_MASK = "***"


@dataclass
class RowReport:
    label: str
    model_id: str
    thinking: str
    route: str
    gates: GatesReport | None
    metrics: RowMetrics | None
    judge: JudgeOutcome | None
    composite: int | None
    closing_message: str = ""
    error: str | None = None


def _gates_to_dict(gates: GatesReport) -> dict[str, Any]:
    return {
        "results": [asdict(r) for r in gates.results],
        "hidden_pass": gates.hidden_pass,
        "hidden_total": gates.hidden_total,
        "hidden_evaluated": gates.hidden_evaluated,
        "cap": gates.cap,
    }


def _judge_to_dict(outcome: JudgeOutcome) -> dict[str, Any]:
    return {
        "status": outcome.status,
        "attempts_used": outcome.attempts_used,
        "samples": [asdict(s) for s in outcome.samples],
        "combined": asdict(outcome.combined) if outcome.combined else None,
    }


def write_row_artifacts(
    row_dir: Path,
    *,
    events: list[dict],
    diff_raw: str,
    gates: GatesReport | None,
    metrics: RowMetrics | None,
    judge_outcome: JudgeOutcome | None,
) -> None:
    row_dir.mkdir(parents=True, exist_ok=True)
    with gzip.open(row_dir / "agent.jsonl.gz", "wt", encoding="utf-8") as f:
        for entry in events:
            f.write(json.dumps(entry) + "\n")
    (row_dir / "diff.raw.patch").write_text(diff_raw, encoding="utf-8")
    (row_dir / "diff.patch").write_text(anonymize_diff(diff_raw), encoding="utf-8")
    if gates is not None:
        (row_dir / "gates.json").write_text(json.dumps(_gates_to_dict(gates), indent=2), encoding="utf-8")
    if metrics is not None:
        (row_dir / "metrics.json").write_text(json.dumps(asdict(metrics), indent=2), encoding="utf-8")
    if judge_outcome is not None:
        (row_dir / "judge.json").write_text(json.dumps(_judge_to_dict(judge_outcome), indent=2), encoding="utf-8")


def _mask_keys(value: Any) -> Any:
    if isinstance(value, dict):
        return {k: (_MASK if k in _MASKED_KEY_NAMES else _mask_keys(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [_mask_keys(v) for v in value]
    return value


def write_run_toml(path: Path, suite_dict: dict[str, Any], *, git_sha: str, pi_version: str) -> None:
    payload = {
        "run": {"git_sha": git_sha, "pi_version": pi_version},
        "suite": _mask_keys(suite_dict),
    }
    with path.open("wb") as f:
        tomli_w.dump(payload, f)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/report.py \
        modelman/tests/benchmark/agent/test_report.py
git commit -m "feat(agent-bench): add per-row artifact writes + masked run.toml - completes plan item #19"
```

### Task 20: Summary report — four tables + Pareto stars + `metrics.jsonl`

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/report.py`
- Modify: `modelman/tests/benchmark/agent/test_report.py`

**Interfaces:**
- Produces: `compute_pareto_stars(reports: list[RowReport]) -> set[str]`; `render_summary(run_id: str, reports: list[RowReport]) -> str`; `write_metrics_jsonl(path: Path, reports: list[RowReport]) -> None`. `runner.py`/`cli.py` (Task 21) call `render_summary` and `write_metrics_jsonl` once per completed run.

- [ ] **Step 1: Write the failing tests**

Append to `modelman/tests/benchmark/agent/test_report.py`:
```python
from modelman.benchmark.agent.report import RowReport, compute_pareto_stars, render_summary, write_metrics_jsonl
from modelman.benchmark.agent.judge import JudgeOutcome, JudgeScore


def _judge_outcome(total: int, verdict: str = "principled_fix") -> JudgeOutcome:
    score = JudgeScore(
        scores={"root_cause": total, "approach": 0, "test_quality": 0, "scope": 0, "coherence": 0},
        total=total, verdict=verdict, flags=[], rationale="", raw_text="{}",
    )
    return JudgeOutcome(status="scored", samples=[score], combined=score, attempts_used=1)


def _row(label, *, wall_ms, composite, gates=None, judge=None) -> RowReport:
    metrics = RowMetrics(
        wall_ms=wall_ms, requests=1, turns=1, ttft_first_ms=10, ttft_mean_ms=10.0, ttft_max_ms=10,
        gen_tok_s=5.0, e2e_tok_s=4.0, in_tok=10, out_tok=5, cache_read_tok=0, reasoning_tok=0,
        tool_ms=0, cost_usd=0.0, unparsed_lines=0, thinking_noop=False, cold_first_token=False,
    )
    return RowReport(
        label=label, model_id="ollama/a", thinking="off", route="direct",
        gates=gates or _gates(), metrics=metrics, judge=judge, composite=composite,
    )


def test_compute_pareto_stars_only_stars_nondominated_rows():
    """No other row is both faster and higher-quality — row 'slow-good' and
    'fast-bad' are each nondominated even though neither is best on both
    axes; 'dominated' loses to 'fast-bad' on both."""
    rows = [
        _row("fast-bad", wall_ms=1000, composite=40),
        _row("slow-good", wall_ms=5000, composite=90),
        _row("dominated", wall_ms=2000, composite=30),  # slower AND worse than fast-bad
    ]
    stars = compute_pareto_stars(rows)
    assert stars == {"fast-bad", "slow-good"}


def test_render_summary_includes_all_four_tables():
    rows = [_row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80))]
    summary = render_summary("run-1", rows)
    assert "## Quality" in summary
    assert "## Speed" in summary
    assert "## Two-axis" in summary
    assert "## Anomalies" in summary
    assert "r1" in summary


def test_render_summary_na_composite_rows_are_never_starred():
    """A JUDGE_FAIL row (composite None) sorts to the bottom of the
    two-axis table and never earns a Pareto star (spec review resolution)."""
    fail_gates = _gates()
    rows = [
        _row("scored", wall_ms=1000, composite=80, judge=_judge_outcome(80)),
        _row("judge-failed", wall_ms=500, composite=None, gates=fail_gates, judge=None),
    ]
    summary = render_summary("run-1", rows)
    two_axis_section = summary.split("## Two-axis")[1].split("## Anomalies")[0]
    lines = [line for line in two_axis_section.splitlines() if line.startswith("|")]
    assert lines[-1].split("|")[1].strip() == "judge-failed"  # N/A row sorts last
    assert "*" not in lines[-1]


def test_write_metrics_jsonl_one_line_per_row(tmp_path):
    rows = [_row("r1", wall_ms=1000, composite=80, judge=_judge_outcome(80)), _row("r2", wall_ms=2000, composite=50)]
    path = tmp_path / "metrics.jsonl"
    write_metrics_jsonl(path, rows)
    lines = path.read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    assert json.loads(lines[0])["label"] == "r1"
    assert json.loads(lines[0])["composite"] == 80
```

- [ ] **Step 2: Run test to verify it fails**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: FAIL — `render_summary`/`compute_pareto_stars`/`write_metrics_jsonl` don't exist yet

- [ ] **Step 3: Write the implementation**

Append to `modelman/src/modelman/benchmark/agent/report.py` (add `from modelman.benchmark.agent.judge import detect_overclaim` to the existing judge import line):
```python
def _outcome_code(report: RowReport) -> str:
    if report.error:
        return "ISOLATION_ERROR"
    if report.gates is None:
        return "UNKNOWN"
    codes = report.gates.triggered_codes
    return codes[0] if codes else "OK"


def _hidden_ratio(report: RowReport) -> str:
    if report.gates is None or not report.gates.hidden_evaluated:
        return "N/A"
    return f"{report.gates.hidden_pass}/{report.gates.hidden_total}"


def _rubric_total(report: RowReport) -> int | str:
    if report.judge is None or report.judge.status != "scored":
        return "N/A"
    return report.judge.combined.total


def _verdict(report: RowReport) -> str:
    if report.judge is None or report.judge.status != "scored":
        return "N/A"
    return report.judge.combined.verdict


def _composite_str(report: RowReport) -> str:
    return str(report.composite) if report.composite is not None else "N/A"


def _quality_table(reports: list[RowReport]) -> str:
    lines = [
        "| label | model | thinking | route | outcome | hidden | rubric | cap | composite | verdict |",
        "|---|---|---|---|---|---|---|---|---|---|",
    ]
    for r in reports:
        cap = r.gates.cap if r.gates is not None else "N/A"
        lines.append(
            f"| {r.label} | {r.model_id} | {r.thinking} | {r.route} | {_outcome_code(r)} | "
            f"{_hidden_ratio(r)} | {_rubric_total(r)} | {cap} | {_composite_str(r)} | {_verdict(r)} |"
        )
    return "\n".join(lines)


def _speed_table(reports: list[RowReport]) -> str:
    lines = [
        "| label | ttft_first | ttft_mean | gen_tok_s | e2e_tok_s | in_tok | out_tok | reasoning_tok | tool_ms | requests | cost |",
        "|---|---|---|---|---|---|---|---|---|---|---|",
    ]
    for r in reports:
        m = r.metrics
        if m is None:
            lines.append(f"| {r.label} | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |")
            continue
        lines.append(
            f"| {r.label} | {m.ttft_first_ms} | {m.ttft_mean_ms} | {m.gen_tok_s} | {m.e2e_tok_s} | "
            f"{m.in_tok} | {m.out_tok} | {m.reasoning_tok} | {m.tool_ms} | {m.requests} | {m.cost_usd} |"
        )
    return "\n".join(lines)


def compute_pareto_stars(reports: list[RowReport]) -> set[str]:
    """A row is starred iff no other scored row is both faster (lower
    wall_ms) and higher-quality (higher composite) than it."""
    scored = [r for r in reports if r.composite is not None and r.metrics is not None]
    starred: set[str] = set()
    for r in scored:
        dominated = any(
            other.metrics.wall_ms < r.metrics.wall_ms and other.composite > r.composite for other in scored
        )
        if not dominated:
            starred.add(r.label)
    return starred


def _two_axis_table(reports: list[RowReport]) -> str:
    scored = sorted((r for r in reports if r.composite is not None), key=lambda r: -r.composite)
    unscored = [r for r in reports if r.composite is None]
    stars = compute_pareto_stars(reports)
    lines = ["| label | composite | wall_ms | pareto |", "|---|---|---|---|"]
    for r in [*scored, *unscored]:
        wall_ms = r.metrics.wall_ms if r.metrics is not None else "N/A"
        star = "*" if r.label in stars else ""
        lines.append(f"| {r.label} | {_composite_str(r)} | {wall_ms} | {star} |")
    return "\n".join(lines)


def _anomalies_table(reports: list[RowReport]) -> str:
    lines = ["| label | anomaly |", "|---|---|"]
    for r in reports:
        if r.gates is not None:
            if r.gates.cap < 1.0:
                lines.append(f"| {r.label} | cap applied: x{r.gates.cap} |")
            if "VACUOUS_TEST" in r.gates.triggered_codes:
                lines.append(f"| {r.label} | VACUOUS_TEST |")
            rubric_total = r.judge.combined.total if r.judge and r.judge.status == "scored" else None
            if rubric_total is not None and rubric_total >= 70 and r.gates.hidden_evaluated and r.gates.hidden_pass == 0 and r.gates.hidden_total > 0:
                lines.append(f"| {r.label} | rubric {rubric_total} despite all hidden tests failing |")
            if r.gates.hidden_evaluated and detect_overclaim(r.closing_message, r.gates.hidden_pass, r.gates.hidden_total):
                lines.append(f"| {r.label} | overclaim: closing message claims tests pass |")
        if r.metrics is not None:
            if r.metrics.thinking_noop:
                lines.append(f"| {r.label} | thinking no-op |")
            if r.metrics.cold_first_token:
                lines.append(f"| {r.label} | cold first token |")
    if len(lines) == 2:
        lines.append("| — | none |")
    return "\n".join(lines)


def render_summary(run_id: str, reports: list[RowReport]) -> str:
    return (
        f"# Agent benchmark run {run_id}\n\n"
        f"## Quality\n\n{_quality_table(reports)}\n\n"
        f"## Speed\n\n{_speed_table(reports)}\n\n"
        f"## Two-axis\n\n{_two_axis_table(reports)}\n\n"
        f"## Anomalies\n\n{_anomalies_table(reports)}\n"
    )


def write_metrics_jsonl(path: Path, reports: list[RowReport]) -> None:
    with path.open("w", encoding="utf-8") as f:
        for r in reports:
            f.write(
                json.dumps(
                    {
                        "label": r.label,
                        "model_id": r.model_id,
                        "thinking": r.thinking,
                        "route": r.route,
                        "outcome": _outcome_code(r),
                        "hidden_pass": r.gates.hidden_pass if r.gates else None,
                        "hidden_total": r.gates.hidden_total if r.gates else None,
                        "cap": r.gates.cap if r.gates else None,
                        "rubric_total": r.judge.combined.total if r.judge and r.judge.status == "scored" else None,
                        "composite": r.composite,
                        "metrics": asdict(r.metrics) if r.metrics else None,
                    }
                )
                + "\n"
            )
```

Also add, near the top of the test file (alongside the existing imports): `from modelman.benchmark.agent.pidriver import RowMetrics` and `import json` — both are already imported by Task 19's version of the file if following this plan in order; add only what's missing.

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_report.py -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/report.py \
        modelman/tests/benchmark/agent/test_report.py
git commit -m "feat(agent-bench): add summary report tables + metrics.jsonl - completes plan item #20"
```

### Task 21: Wire reporting into the runner + `agent show` + `agent judge`

**Files:**
- Modify: `modelman/src/modelman/benchmark/agent/report.py`
- Modify: `modelman/src/modelman/benchmark/agent/runner.py`
- Modify: `modelman/src/modelman/benchmark/agent/cli.py`
- Modify: `modelman/tests/benchmark/agent/test_report.py`
- Modify: `modelman/tests/benchmark/agent/test_runner.py`
- Modify: `modelman/tests/benchmark/agent/test_cli.py`

**Interfaces:**
- `write_row_artifacts` gains two optional keyword params (`seed_contents`, `closing_message`) and writes a small `row.json` sidecar when given — this is what makes `agent judge` able to rebuild a judge prompt from disk without re-running the agent.
- `runner.py` gains `rejudge_run(run_dir, *, row_filter=None, samples_override=None, judge_transport_factory=None) -> list[dict]` (returns one `{"label", "rubric_total", "composite", "verdict"}` dict per re-judged row).
- `run_suite` now writes `run.toml`, `summary.md`, and `metrics.jsonl` into `run_dir` before returning — the CLI's `run`/`show` commands read these files, not the in-memory `RowRunResult` list.

**Scope note:** `rejudge_run` rewrites each row's `judge.json` and prints the new scores; it does **not** regenerate the run's `summary.md`/`metrics.jsonl` (that would require re-deriving every row's identity from `row.json` files, which is a reasonable follow-up but not required by the spec's stated purpose for this command — "how a rubric edit gets evaluated cheaply"). Document this limitation in guide 09 (Task 25).

- [ ] **Step 1: Extend `report.write_row_artifacts` with the `row.json` sidecar**

Write the failing test first — append to `modelman/tests/benchmark/agent/test_report.py`:
```python
def test_write_row_artifacts_writes_row_json_sidecar_when_given(tmp_path):
    row_dir = tmp_path / "row2"
    write_row_artifacts(
        row_dir,
        events=[],
        diff_raw="",
        gates=_gates(),
        metrics=_metrics(),
        judge_outcome=None,
        seed_contents={"a.py": "original"},
        closing_message="Fixed it.",
        label="r1",
        model_id="ollama/a",
        thinking="off",
        route="direct",
    )
    row_info = json.loads((row_dir / "row.json").read_text(encoding="utf-8"))
    assert row_info["seed_contents"] == {"a.py": "original"}
    assert row_info["closing_message"] == "Fixed it."
    assert row_info["label"] == "r1"
```

Run it (`uv run pytest tests/benchmark/agent/test_report.py -v`) — FAILS (unexpected keyword arguments). Then change `write_row_artifacts`'s signature and body in `modelman/src/modelman/benchmark/agent/report.py` to:
```python
def write_row_artifacts(
    row_dir: Path,
    *,
    events: list[dict],
    diff_raw: str,
    gates: GatesReport | None,
    metrics: RowMetrics | None,
    judge_outcome: JudgeOutcome | None,
    seed_contents: dict[str, str] | None = None,
    closing_message: str = "",
    label: str = "",
    model_id: str = "",
    thinking: str = "",
    route: str = "",
) -> None:
    row_dir.mkdir(parents=True, exist_ok=True)
    with gzip.open(row_dir / "agent.jsonl.gz", "wt", encoding="utf-8") as f:
        for entry in events:
            f.write(json.dumps(entry) + "\n")
    (row_dir / "diff.raw.patch").write_text(diff_raw, encoding="utf-8")
    (row_dir / "diff.patch").write_text(anonymize_diff(diff_raw), encoding="utf-8")
    if gates is not None:
        (row_dir / "gates.json").write_text(json.dumps(_gates_to_dict(gates), indent=2), encoding="utf-8")
    if metrics is not None:
        (row_dir / "metrics.json").write_text(json.dumps(asdict(metrics), indent=2), encoding="utf-8")
    if judge_outcome is not None:
        (row_dir / "judge.json").write_text(json.dumps(_judge_to_dict(judge_outcome), indent=2), encoding="utf-8")
    if seed_contents is not None:
        row_info = {
            "seed_contents": seed_contents,
            "closing_message": closing_message,
            "label": label,
            "model_id": model_id,
            "thinking": thinking,
            "route": route,
        }
        (row_dir / "row.json").write_text(json.dumps(row_info, indent=2), encoding="utf-8")
```

Run again — PASSES (7 tests total in `test_report.py`).

- [ ] **Step 2: Write the failing runner test**

Append to `modelman/tests/benchmark/agent/test_runner.py`:
```python
def test_run_suite_writes_run_artifacts(tmp_path, monkeypatch):
    """run_suite persists run.toml, summary.md, and metrics.jsonl — the
    CLI's show command reads these files, not the in-memory result list."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json", skip_judge=True
    )

    assert (run_dir / "run.toml").exists()
    assert (run_dir / "summary.md").exists()
    assert (run_dir / "metrics.jsonl").exists()
    assert (results[0].row_dir / "gates.json").exists()
    assert (results[0].row_dir / "row.json").exists()
```

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v` — FAILS (files don't exist yet).

- [ ] **Step 3: Wire `report.py` into `runner.py`**

Add imports to `modelman/src/modelman/benchmark/agent/runner.py`: `import subprocess`, `import tomllib`, `from modelman.benchmark.agent import report`. Add `events: list[dict] = field(default_factory=list)` to `RowRunResult` (alongside `seed_contents`).

In `_run_single_row`, add `events=run_result.events` to the `RowRunResult(...)` call.

Add these helpers above `run_suite`:
```python
def _git_sha() -> str:
    try:
        result = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=False)
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


def _pi_version() -> str:
    try:
        result = subprocess.run(["pi", "--version"], capture_output=True, text=True, check=False)
        return result.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


def _suite_to_dict(suite: Suite) -> dict:
    return {
        "name": suite.name,
        "task": str(suite.task_path),
        "passes": suite.passes,
        "cooldown_s": suite.cooldown_s,
        "agent_timeout_s": suite.agent_timeout_s,
        "judge": {
            "model": suite.judge.model,
            "thinking": suite.judge.thinking,
            "temperature": suite.judge.temperature,
            "samples": suite.judge.samples,
            "max_attempts": suite.judge.max_attempts,
            "route": suite.judge.route,
        },
        "routes_direct": {pid: {"base_url": c.base_url, "api": c.api} for pid, c in suite.routes_direct.items()},
        "rows": [
            {
                "label": r.label,
                "model_id": r.model_id,
                "thinking": r.thinking,
                "route": r.route,
                "provider_id": r.provider_id,
            }
            for r in suite.rows
        ],
    }


def _to_row_report(result: RowRunResult) -> report.RowReport:
    return report.RowReport(
        label=result.row.label,
        model_id=result.row.model_id,
        thinking=result.row.thinking,
        route=result.row.route,
        gates=result.gates,
        metrics=result.metrics,
        judge=result.judge,
        composite=result.composite,
        closing_message=result.closing_message,
        error=result.error,
    )


def _persist_row_artifacts(result: RowRunResult) -> None:
    if result.error is not None or result.gates is None:
        return
    report.write_row_artifacts(
        result.row_dir,
        events=result.events,
        diff_raw=result.diff_raw,
        gates=result.gates,
        metrics=result.metrics,
        judge_outcome=result.judge,
        seed_contents=result.seed_contents,
        closing_message=result.closing_message,
        label=result.row.label,
        model_id=result.row.model_id,
        thinking=result.row.thinking,
        route=result.row.route,
    )
```

Replace the tail of `run_suite` (from `isolation.restore_providers()` onward) with:
```python
    isolation.restore_providers()

    for result in results:
        _persist_row_artifacts(result)

    if not skip_judge:
        _judge_all(suite, task, results, live_models_path, judge_transport_factory or _build_judge_transport)
        for result in results:
            _persist_row_artifacts(result)

    row_reports = [_to_row_report(r) for r in results]
    (run_dir / "summary.md").write_text(report.render_summary(run_id, row_reports), encoding="utf-8")
    report.write_metrics_jsonl(run_dir / "metrics.jsonl", row_reports)
    report.write_run_toml(run_dir / "run.toml", _suite_to_dict(suite), git_sha=_git_sha(), pi_version=_pi_version())

    return run_dir, results
```

- [ ] **Step 4: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/test_runner.py -v`
Expected: PASS (all tests, including the new one)

- [ ] **Step 5: Add `rejudge_run` + `agent show`/`agent judge` CLI commands**

Write the failing test first — append to `modelman/tests/benchmark/agent/test_cli.py`:
```python
def test_show_prints_persisted_summary(tmp_path, monkeypatch):
    run_dir = tmp_path / "results" / "run1"
    run_dir.mkdir(parents=True)
    (run_dir / "summary.md").write_text("# hello from disk\n", encoding="utf-8")
    monkeypatch.setenv("MODELMAN_STATE", str(tmp_path / "modelman.toml"))

    from modelman.state import ModelState, StateStore, save_state

    state = StateStore()
    state.extra["benchmarks"] = {"agent_last_run": str(run_dir)}
    save_state(state)

    result = runner.invoke(agent_app, ["show", "--latest"])
    assert result.exit_code == 0
    assert "hello from disk" in result.output
```

Append to `modelman/tests/benchmark/agent/test_runner.py`:
```python
def test_rejudge_run_rewrites_judge_json_from_persisted_artifacts(tmp_path, monkeypatch):
    """agent judge re-scores from row.json/diff.patch/gates.json alone —
    no agent process, no workspace, no isolation."""
    monkeypatch.setattr(isolation_module, "isolate_provider", lambda pid: None)
    monkeypatch.setattr(isolation_module, "restore_providers", lambda: None)
    monkeypatch.setattr(pidriver_module, "run_pi_process", _no_diff_run)

    suite = load_suite(_write_suite(tmp_path, _suite_toml(MINI_DRIFT)), _registry())
    run_dir, results = run_suite(
        suite, _registry(), results_dir=tmp_path / "results", live_models_path=tmp_path / "missing.json", skip_judge=True
    )

    from modelman.benchmark.agent.runner import rejudge_run

    class _FakeJudgeTransport:
        def complete(self, prompt, *, temperature):
            return json.dumps(
                {"scores": {"root_cause": 30, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
                 "total": 100, "verdict": "principled_fix", "flags": [], "rationale": "ok"}
            )

    outcomes = rejudge_run(run_dir, judge_transport_factory=lambda cfg, path: _FakeJudgeTransport())
    assert len(outcomes) == 1
    assert outcomes[0]["rubric_total"] == 100
    assert json.loads((results[0].row_dir / "judge.json").read_text(encoding="utf-8"))["combined"]["total"] == 100
```

Run both (`uv run pytest tests/benchmark/agent/test_runner.py tests/benchmark/agent/test_cli.py -v`) — FAIL (`rejudge_run`, `show`, `judge` don't exist yet).

Add to `modelman/src/modelman/benchmark/agent/runner.py`:
```python
def rejudge_run(
    run_dir: Path,
    *,
    row_filter: list[str] | None = None,
    samples_override: int | None = None,
    judge_transport_factory=None,
) -> list[dict]:
    """Re-score every row's persisted diff/row.json against the current
    rubric, without re-running any agent. Rewrites each row's judge.json;
    does not regenerate summary.md/metrics.jsonl (see Task 21 scope note)."""
    with (run_dir / "run.toml").open("rb") as f:
        run_data = tomllib.load(f)
    suite_data = run_data["suite"]
    task = load_task(Path(suite_data["task"]))
    judge_cfg = JudgeConfig(**suite_data["judge"])
    if samples_override is not None:
        judge_cfg.samples = samples_override

    transport = (judge_transport_factory or _build_judge_transport)(judge_cfg, pidriver.LIVE_PI_MODELS_PATH)

    outcomes = []
    for row_dir in sorted(p for p in run_dir.iterdir() if p.is_dir()):
        if row_filter and row_dir.name not in row_filter:
            continue
        row_json_path = row_dir / "row.json"
        diff_path = row_dir / "diff.patch"
        if not (row_json_path.exists() and diff_path.exists()):
            continue
        row_info = json.loads(row_json_path.read_text(encoding="utf-8"))
        prompt = judge.build_prompt(
            task_md=task.task_md,
            seed_contents=row_info["seed_contents"],
            diff_text=diff_path.read_text(encoding="utf-8"),
            closing_message=row_info["closing_message"],
            rubric_md=task.rubric_md,
        )
        outcome = judge.judge_row(
            transport,
            prompt,
            temperature=judge_cfg.temperature,
            samples=judge_cfg.samples,
            max_attempts=judge_cfg.max_attempts,
        )
        gates_data = json.loads((row_dir / "gates.json").read_text(encoding="utf-8")) if (row_dir / "gates.json").exists() else {"cap": 0.0}
        composite = judge.apply_cap(outcome.combined.total, gates_data["cap"]) if outcome.status == "scored" else None
        report.write_row_artifacts(
            row_dir,
            events=[],
            diff_raw=diff_path.read_text(encoding="utf-8"),
            gates=None,
            metrics=None,
            judge_outcome=outcome,
        )
        outcomes.append(
            {
                "label": row_info["label"],
                "rubric_total": outcome.combined.total if outcome.status == "scored" else None,
                "composite": composite,
                "verdict": outcome.combined.verdict if outcome.status == "scored" else "JUDGE_FAIL",
            }
        )
    return outcomes
```

Add to `modelman/src/modelman/benchmark/agent/cli.py` (new imports: `from modelman.benchmark.agent.runner import DEFAULT_RESULTS_DIR, rejudge_run, run_suite`, replacing the existing narrower `run_suite`-only import):
```python
@agent_app.command("show")
def show_cmd(
    latest: bool = typer.Option(False, "--latest", help="Show the latest agent run"),
    run_id: str | None = typer.Option(None, "--run-id", help="Run id to show"),
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    """Print the persisted summary.md for an agent benchmark run."""
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("agent_last_run")
        if not run_dir_str:
            typer.echo("error: no latest agent run recorded", err=True)
            raise typer.Exit(1)
        md_path = Path(run_dir_str) / "summary.md"
    else:
        md_path = results_dir / str(run_id) / "summary.md"
    if not md_path.exists():
        typer.echo(f"error: results not found: {md_path}", err=True)
        raise typer.Exit(1)
    typer.echo(md_path.read_text(encoding="utf-8"))


@agent_app.command("judge")
def judge_cmd(
    latest: bool = typer.Option(False, "--latest", help="Re-judge the latest agent run"),
    run_id: str | None = typer.Option(None, "--run-id", help="Run id to re-judge"),
    row: list[str] = typer.Option([], "--row", help="Row directory name to re-judge (repeatable)"),  # noqa: B008
    samples: int | None = typer.Option(None, "--samples", help="Override [judge].samples for this re-judge"),
    results_dir: Path = typer.Option(DEFAULT_RESULTS_DIR, "--results-dir"),  # noqa: B008
) -> None:
    """Re-score an existing run's persisted artifacts without re-running any agent."""
    if not latest and not run_id:
        typer.echo("error: specify --latest or --run-id", err=True)
        raise typer.Exit(1)
    if latest:
        state = load_state()
        run_dir_str = state.extra.get("benchmarks", {}).get("agent_last_run")
        if not run_dir_str:
            typer.echo("error: no latest agent run recorded", err=True)
            raise typer.Exit(1)
        target_dir = Path(run_dir_str)
    else:
        target_dir = results_dir / str(run_id)

    try:
        outcomes = rejudge_run(target_dir, row_filter=row or None, samples_override=samples)
    except (BenchmarkError, FileNotFoundError) as exc:
        typer.echo(f"error: {exc}", err=True)
        raise typer.Exit(1) from exc

    for outcome in outcomes:
        typer.echo(f"{outcome['label']}: rubric={outcome['rubric_total']} composite={outcome['composite']} verdict={outcome['verdict']}")
```

Update the `run_cmd`'s existing `from modelman.benchmark.agent.runner import run_suite` line to `from modelman.benchmark.agent.runner import DEFAULT_RESULTS_DIR, rejudge_run, run_suite` (one import line covering all three).

- [ ] **Step 6: Run test to verify it passes**

Run: `uv run pytest tests/benchmark/agent/ -v`
Expected: PASS (every test from Tasks 1–21)

- [ ] **Step 7: Commit**

```bash
git add modelman/src/modelman/benchmark/agent/report.py \
        modelman/src/modelman/benchmark/agent/runner.py \
        modelman/src/modelman/benchmark/agent/cli.py \
        modelman/tests/benchmark/agent/test_report.py \
        modelman/tests/benchmark/agent/test_runner.py \
        modelman/tests/benchmark/agent/test_cli.py
git commit -m "feat(agent-bench): persist run artifacts + add show/judge commands - completes plan item #21"
```

### Task 22: `q4-agent-sweep.toml` + `.gitignore`

**Files:**
- Create: `benchmarks/suites/q4-agent-sweep.toml`
- Modify: `.gitignore`

**Interfaces:** None — content only.

- [ ] **Step 1: Write the full sweep suite**

`benchmarks/suites/q4-agent-sweep.toml`:
```toml
name = "q4 agent sweep"
task = "benchmarks/tasks/day31-drift"
passes = 1
cooldown_s = 20
agent_timeout_s = 420

[judge]
model = "openrouter/anthropic/claude-opus-4"
thinking = "low"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[routes.direct.omlx]
base_url = "http://localhost:8000/v1"
api = "openai-completions"

[routes.direct.ollama]
base_url = "http://localhost:11434/v1"
api = "openai-completions"

[routes.direct.llamacpp]
base_url = "http://localhost:8080/v1"
api = "openai-completions"

[[rows]]
models   = ["ollama/qwen3.8:27b-mlx", "omlx/Ornith-1.5-35B-A3B-MLX-6bit"]
thinking = ["off", "high"]
routes   = ["direct", "litellm"]
```

- [ ] **Step 2: Add the bench workspace temp-dir pattern to `.gitignore`**

Check the current end of `.gitignore` first (`tail -5 .gitignore`), then append:
```
# Agent-benchmark scratch workspaces (modelman/src/modelman/benchmark/agent/workspace.py)
/tmp/agent-bench-*
```
This line documents intent but is defensive only — `workspace.py`'s `tempfile.mkdtemp` always writes under the OS temp dir, never inside the repo, so nothing under this pattern should ever actually be tracked; it exists in case `--keep-workspace` (a future flag, not built by this plan) is ever pointed at a path inside the repo by mistake.

- [ ] **Step 3: Commit**

```bash
git add benchmarks/suites/q4-agent-sweep.toml .gitignore
git commit -m "feat(agent-bench): add full q4 sweep suite - completes plan item #22"
```

### Task 23: Guide 09 + README/CLAUDE.md cross-links

**Files:**
- Create: `docs/guides/09-agent-benchmarks.md`
- Modify: `benchmarks/README.md`
- Modify: `docs/guides/05-benchmarks.md`
- Modify: `CLAUDE.md` (root)
- Modify: `modelman/CLAUDE.md`

**Interfaces:** None — documentation only. This task's `<!-- UNVERIFIED -->` blocks (matching guide 05's existing convention) get replaced with real captured output in Task 24, once a live smoke run has actually happened.

- [ ] **Step 1: Write guide 09**

`docs/guides/09-agent-benchmarks.md`:
```markdown
# Agent coding benchmarks — `modelman benchmark agent`

> Use this to: run a real coding task through the `pi` agent across a matrix of model/thinking/route configurations, and read a report that separates speed from quality instead of ranking on tokens/sec alone.

Design rationale, gate taxonomy, and scoring rules: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`. This guide is the day-to-day usage doc; the spec is the source of truth for *why* each rule exists.

This guide, unlike 00/02/04/05/08, embeds no `litellm_exposed` snapshots — nothing here goes stale when a model is exposed/unexposed.

## Prerequisites

- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, isolation helpers on `PATH`).
- `pi` installed and on `PATH` — this harness drives `pi --mode json`, not a direct HTTP request, for the agent rows.
- A working LiteLLM apiKey already seeded into `~/.pi/agent/models.json` — launch any `wt` agent in litellm gateway mode once if you've never done so; the harness reads that key rather than storing its own.
- `OPENROUTER_API_KEY` available (via `~/Library/LaunchAgents/local.litellm.proxy.plist` or the env) if your suite's `[judge]` model is an OpenRouter model — preflight checks this before running any agent row.

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup/modelman
uv run modelman benchmark agent list-tasks --root ../benchmarks/tasks
uv run modelman benchmark agent list-suites --root ../benchmarks/suites
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run
```

<!-- UNVERIFIED — a live run spawns a real pi agent process against a real (cloud) model and costs real judge tokens; capture this output the first time the smoke suite is actually run end-to-end. -->
```bash
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml
# → Agent benchmark complete: 1 row(s), 1 ran without an isolation error
#   Results: /Users/keith/.config/local-ai/benchmarks/<run-id>

uv run modelman benchmark agent show --latest
```

## Steps

### 1. Pick or author a task

A task bundle lives under `benchmarks/tasks/<task-id>/` and needs `task.md`, `visible/`, `hidden/`, `gates.toml`, `rubric.md`, `meta.toml`. `day31-drift` (a calendar-arithmetic bug in an invented billing domain, `kettlecomb`) ships as the first task — see its `task.md` for the exact bug report an agent receives, and `meta.toml` for the tier expectations a run's results should be read against. Task-authoring rules are in the spec's "The first task" section: bespoke domain (no public repo an LLM might have memorized), a deterministic gate, and a plausible wrong answer that isn't just "said no."

### 2. Write or reuse a suite

A suite (`benchmarks/suites/*.toml`) picks a task and a `[[rows]]` matrix (model × thinking × route, cartesian-expanded). `smoke.toml` is a one-row, fast-cloud-model suite for plumbing checks; `q4-agent-sweep.toml` is the fuller local-model matrix. `--dry-run` prints the resolved row list without running anything — always dry-run a new or edited suite before a real run, since a mistyped model id loads a 27B model for nothing.

### 3. Run it

```bash
uv run modelman benchmark agent run --suite <path> [--row <label-or-index>]... [--passes N] [--skip-judge] [--dry-run]
```

Local rows are grouped by provider and isolated once per group (stop-others, start, warmup) via the same `bin/llm-isolate-provider`/`bin/llm-restore-providers` helpers `modelman benchmark` uses — see [05-benchmarks](05-benchmarks.md) Step 1 for exactly what isolation does per backend. Judging always runs *after* `restore_providers()`, so a cloud judge call never contends with a loaded local model.

### 4. Read the report

```bash
uv run modelman benchmark agent show --latest
uv run modelman benchmark agent show --run-id <run-id>
```

`summary.md` (also archived under `benchmarks/results/` per the existing dated convention) has four tables: **Quality** (outcome code, hidden n/m, rubric, cap, composite, verdict), **Speed** (TTFT, `gen_tok_s`/`e2e_tok_s`, tool time, tokens, cost), **Two-axis** (sorted by composite, Pareto-nondominated rows starred — "fastest config at ≥ this quality"), and **Anomalies** (every cap applied, vacuous test, thinking no-op, cold-first-token, overclaim, and any row scoring ≥70 rubric points despite failing every hidden test). A `JUDGE_FAIL` row keeps its gates/speed data and shows `N/A` for quality — it never voids the row, and it never earns a Pareto star.

Per-row artifacts live under `~/.config/local-ai/benchmarks/<run-id>/<row-dir>/`: `agent.jsonl.gz` (raw event stream), `agent.session.jsonl` (pi's own session file), `diff.raw.patch`/`diff.patch` (as-produced / anonymized), `gates.json`, `metrics.json`, `judge.json`, `row.json` (what `agent judge` needs to re-score later).

### 5. Re-judge cheaply after a rubric edit

```bash
uv run modelman benchmark agent judge --latest [--row <row-dir>]... [--samples N]
```

Re-scores from each row's persisted `row.json`/`diff.patch`/`gates.json` — no agent re-run, no isolation. It rewrites each row's `judge.json` and prints the new rubric total/composite/verdict per row; it does **not** regenerate `summary.md` — re-read the individual `judge.json` files (or `agent show --latest`, which still reflects the *original* judge pass) until a future enhancement adds full summary regeneration.

## Verification

<!-- UNVERIFIED — requires a completed agent run. -->
```bash
ls ~/.config/local-ai/benchmarks/<run-id>/
# → run.toml  summary.md  metrics.jsonl  <row-dir>/...
```

## Gotchas

- **`pi` needs no permission-bypass flag** for this harness — tool execution works under `--no-approve` in `--mode json` because non-interactive modes never prompt; see the spec's "Verified against the live setup" for why `--no-approve` is passed anyway (pinning project-trust behavior, not enabling tools).
- **`route = "direct"` requires a matching `[routes.direct.<provider>]` block** in the suite — preflight fails immediately, naming the missing provider, rather than after an agent has already run.
- **`omlx` 4-bit and 6-bit share one provider.** Use a row's `provider =` override (not just the model id) to isolate the exact variant — same rule as `modelman benchmark`'s own isolation, see [05-benchmarks](05-benchmarks.md) Gotchas.
- **`repair_rounds` is accepted but rejected if non-zero.** The seam exists (per-turn retry after a failed run) but is disabled in v1 — see the spec's "Deferred: the repair round."
- **Local-model timeouts are a config input, not a bug.** A 27B model in a six-round agentic task can exceed `agent_timeout_s`; `TIMEOUT` caps the composite at 0 but is reported as its own outcome class — raise `agent_timeout_s` per-backend rather than reading a timeout as "this model can't do it."
- **Judging costs cloud API spend on every row.** `--skip-judge` exists for plumbing checks; `agent judge --row` re-scores a subset of an existing run without re-running agents.

## Going deeper

- Full design: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`
- Implementation plan: `docs/superpowers/plans/2026-09-04-agent-coding-benchmark.md`
- Module map: `modelman/CLAUDE.md` (Benchmark subsystem)
- Single-turn speed benchmarks (the fast triage tool this harness doesn't replace): [05-benchmarks](05-benchmarks.md)
```

- [ ] **Step 2: Cross-link from `benchmarks/README.md`**

Add a new section (after the existing "Quick start" section):
```markdown
## Agentic coding benchmarks

`modelman benchmark agent` runs a real coding task through the `pi` agent
across a model/thinking/route matrix and grades the result on deterministic
gates plus a blind LLM rubric — see [docs/guides/09-agent-benchmarks.md](../docs/guides/09-agent-benchmarks.md).

- **`tasks/`** — task bundles (`day31-drift` ships first)
- **`suites/`** — suite TOML files (`smoke.toml`, `q4-agent-sweep.toml`)
```

- [ ] **Step 3: Cross-link from `docs/guides/05-benchmarks.md`**

In its "## Going deeper" section, add one line:
```markdown
- Agentic (not single-turn) coding benchmarks — real task, gates + judge: [09-agent-benchmarks](09-agent-benchmarks.md)
```

- [ ] **Step 4: Update the root `CLAUDE.md`**

Under `## Commands`, add:
```markdown
- `modelman benchmark agent run --suite <path>` — agentic coding benchmark (real task, gates + judge); see `docs/guides/09-agent-benchmarks.md`
```

Under `## Architecture`, in the `modelman/` bullet, append a clause: `; the agentic coding benchmark (\`benchmark/agent/\`) is a separate module tree under the same package`.

In the guide-staleness bullet under `## Key Gotchas` (the one starting "Guide docs embed live `litellm_exposed` snapshots"), append a sentence: `Guide 09 is exempt — it carries no exposure snapshots.`

- [ ] **Step 5: Update `modelman/CLAUDE.md`**

Under `### Benchmark subsystem`, add a new bullet after the existing `workloads/` bullet:
```markdown
- `src/modelman/benchmark/agent/` — the agentic coding benchmark (`modelman benchmark agent`): `suite.py` (TOML parsing + row expansion + preflight), `task.py` (task bundle loading), `workspace.py` (scratch git repo per row), `pidriver.py` (route resolution, pi process driver, speed metrics), `gates.py` (nine-gate deterministic taxonomy + composite cap), `judge.py` (blind LLM rubric judge), `report.py` (artifact writes + summary tables), `runner.py` (phase orchestration + isolation loop), `cli.py` (`run`/`list-tasks`/`list-suites`/`show`/`judge`). Design: `docs/superpowers/specs/2026-09-04-agent-coding-benchmark-design.md`.
- Tests: `tests/benchmark/agent/` (one file per module above, plus `fixtures/fake_agent.py` and `fixtures/tasks/mini-drift/`).
```

- [ ] **Step 6: Check links and commit**

Run: `./bin/check-links` (from the monorepo root) — verifies every new cross-link resolves.

```bash
git add docs/guides/09-agent-benchmarks.md benchmarks/README.md docs/guides/05-benchmarks.md CLAUDE.md modelman/CLAUDE.md
git commit -m "docs(agent-bench): add guide 09 + cross-links - completes plan item #23"
```

### Task 24: Full verification — `make test-all`, lint, and a live smoke run

**Files:** None new — this task runs checks and, if a live smoke run is performed, updates the two `<!-- UNVERIFIED -->` blocks in `docs/guides/09-agent-benchmarks.md` with real captured output (matching guide 05's existing convention for exactly this situation).

- [ ] **Step 1: Run the full modelman suite**

```bash
cd modelman
uv run pytest -q
uv run ruff check src/ tests/
uv run mypy src/
```
Expected: all pass. Fix any lint/type issues introduced by this plan's modules before proceeding (in particular, `dict`/`list` mutable defaults, unused imports pulled in during the incremental edits across Tasks 8–21, and the `noqa: B008` markers on Typer `Option(...)` defaults matching the existing `benchmark/cli.py` convention).

- [ ] **Step 2: Run the monorepo-wide checks**

```bash
cd ..
make lint-shell
make check-links
```
Expected: both pass — `check-links` in particular validates every link Task 23 added.

- [ ] **Step 3: Confirm the CLI is mounted and discoverable end-to-end**

```bash
cd modelman
uv run modelman benchmark agent list-tasks --root ../benchmarks/tasks
uv run modelman benchmark agent list-suites --root ../benchmarks/suites
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml --dry-run
```
Expected: `day31-drift` listed with its hidden-file count; both suites listed with their row counts; the dry-run prints exactly one resolved row for `smoke.toml`.

- [ ] **Step 4 (optional, costs real judge tokens): live smoke run**

If you have a working `pi` install, a seeded LiteLLM apiKey, and `OPENROUTER_API_KEY` (or whichever key your judge model needs) available:

```bash
uv run modelman benchmark agent run --suite ../benchmarks/suites/smoke.toml
uv run modelman benchmark agent show --latest
```

Paste the real output into `docs/guides/09-agent-benchmarks.md`'s two `<!-- UNVERIFIED -->` blocks (TL;DR and Verification), replacing the comment with the actual command output, exactly as guide 05 does for its own not-yet-run-live sections. If this step is skipped, leave the `<!-- UNVERIFIED -->` markers in place — do not fabricate output.

- [ ] **Step 5: Run `make test-all` from the monorepo root**

```bash
cd ..
make test-all
```
Expected: PASS — this is the umbrella target (lint + modelman `make check`/`make test` + wt `go build`/`vet`/`test`) and is CLAUDE.md's own definition of "done" for a change touching `modelman/`.

- [ ] **Step 6: Final commit**

```bash
git add -A
git status   # confirm only expected files are staged before committing
git commit -m "$(cat <<'EOF'
docs(agent-bench): capture live smoke run output - completes plan item #24
EOF
)"
```
(Skip this commit entirely if Step 4 was skipped and nothing changed.)

---

## Plan self-review

**Spec coverage:** every spec section maps to a task — Problem/Goals/Non-goals informed scope throughout; Resolved decisions and Verified-against-live-setup facts are embedded as code comments and design choices (Tasks 5–7); Architecture's module table is Tasks 1–21 (with the noted `pidriver`↔`suite` dependency-direction flip for phase-testability); the CLI surface is Task 15/18/21; the run pipeline's three phases are Tasks 14 (0–1), 18 (2), and the combine step is folded into Task 21's `run_suite` tail rather than a separate "phase 3" function, since it's a few lines of glue, not independent logic; the `day31-drift` task is Tasks 3–4 and 11; the suite format is Tasks 12–13; metrics are Task 7; gates and failure taxonomy are Tasks 8–11; scoring is Task 10's cap logic plus the review-added minimum-across-triggered-conditions rule; the judge contract is Tasks 16–17; artifacts and report are Tasks 19–21; testing requirements are met per-module as each was built; error handling (timeouts/killpg, isolation-failure containment, `finally`-based cleanup, unknown-event tolerance, missing-key preflight, judge retry, incremental artifact writes) is threaded through Tasks 6, 14, 17, 19–21; the deferred repair round is rejected-at-load in Task 12; the file map is fully covered across Tasks 1–23; phasing matches this plan's own six phases.

**Known, deliberate scope reduction:** `agent judge` does not regenerate `summary.md`/`metrics.jsonl` (Task 21's scope note) — flagged in guide 09 as a follow-up rather than silently omitted.

**Placeholder scan:** none — the one draft that briefly introduced sloppy copy-paste code (Task 6) and a stub-attribute placeholder (Task 8) were both caught and rewritten during drafting; the final task bodies contain complete, real code throughout, with `<!-- UNVERIFIED -->` markers used only for genuinely-not-yet-run live command output, matching this repo's own guide-writing convention (guide 05).

**Type/name consistency:** verified `RowConfig`, `DirectRouteConfig`, `PiTarget`, `PiRunResult`, `RowMetrics`, `TaskBundle`, `Workspace`, `GateResult`, `GatesReport`, `JudgeScore`, `JudgeOutcome`, `RowRunResult`, and `RowReport` are constructed with the same field names everywhere they cross a task boundary; `Workspace.seed_hidden` copying into `tests/` (not the workspace root) was corrected early (Task 2) once Task 3's real hidden-test module-name resolution (`tests.test_day31`) made the mismatch visible, and Task 1's `mini-drift` hidden fixture was given two independent test methods specifically so Task 10 could exercise a genuine partial hidden-pass ratio.

