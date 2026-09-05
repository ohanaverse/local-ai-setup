# Agent coding benchmark — Phase 1: Task bundle + workspace (plan extract)

> Part of [the agent-coding-benchmark plan index](2026-09-04-agent-coding-benchmark.md). Shared Goal / Architecture / Tech Stack / Global Constraints and the phase order live there.


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

> **Collection warning:** the `test_pkg.py` / `test_hidden.py` files in this fixture are collected by pytest as real tests and fail at import (`from pkg import add_one` — `pkg` only exists inside a seeded workspace). That is unavoidable, since those names are what unittest discovery requires inside a workspace. Task 2 adds `tests/benchmark/agent/conftest.py` with `collect_ignore = ["fixtures"]` to fix it, so between this task and that one, run only `uv run pytest tests/benchmark/agent/test_task.py` — a directory-wide run fails at collection.

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
[build]
import_check = "pkg"
tests_dir = "tests"

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
frontier = "2/2"
```

(`2/2`, matching this fixture's two hidden tests — not the `6/6` of the real `day31-drift` bundle.)

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
    bundles: list[TaskBundle] = []
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
- Create: `modelman/tests/benchmark/agent/conftest.py`
- Test: `modelman/tests/benchmark/agent/test_workspace.py`

**Step 0 (do this first): keep pytest out of the fixtures tree.** Task 1's task-bundle fixture ships `visible/tests/test_pkg.py` and `hidden/test_hidden.py`, which pytest collects as ordinary tests and then fails to import (`pkg` exists only inside a seeded workspace) — so `uv run pytest tests/benchmark/agent/`, `make test`, and CI all die at collection, not at an assertion. Write `modelman/tests/benchmark/agent/conftest.py`:

```python
"""Collection config for the agent-benchmark tests.

fixtures/tasks/ holds *task bundles* — miniature repositories that are data
for these tests, not tests. Their files are named test_pkg.py /
test_hidden.py on purpose (that is the layout unittest discovery expects
inside a seeded workspace, and the same layout as the real
benchmarks/tasks/day31-drift bundle), which is exactly the name pytest
collects. Collected in place they import `pkg`, a module that only exists
inside a seeded temp workspace, so the entire suite fails at collection
rather than just these fixtures.
"""

collect_ignore = ["fixtures"]
```

(The real bundle lives under `benchmarks/tasks/`, outside `testpaths = ["tests"]`, so it never needed the guard — this is purely about fixtures under `tests/`.)

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
        modelman/tests/benchmark/agent/conftest.py \
        modelman/tests/benchmark/agent/test_workspace.py
git commit -m "feat(agent-bench): add git-backed workspace - completes plan item #2"
```

### Task 3: The `day31-drift` task bundle

**Files:**
- Create: `benchmarks/tasks/day31-drift/task.md`
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

TASK_ROOT = Path(__file__).resolve().parents[4] / "benchmarks" / "tasks" / "day31-drift"
# parents[4] is the monorepo root from modelman/tests/benchmark/agent/. parents[3]
# is modelman/, and the task bundles live one level up — CI runs this file with
# working-directory=modelman, so the repo root must be derived from the file, not
# from Path.cwd().

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
