"""Tests for modelman.benchmark.agent.workspace — the scratch git repo an
agent row runs in.

The vacuous-test gate (spec gate 8) depends on being able to check out the
pre-agent baseline commit in a second worktree and run just the agent's new
test file against it; if the workspace seeding or the baseline commit is
wrong, every gate built on top of it is unreliable.
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


def test_bytecode_written_after_baseline_is_not_reported_as_a_change(tmp_path):
    """The visible-test run and the agent itself both produce __pycache__ in the
    workspace; `git add -A` would stage it, gate 7's `test_` prefix would read
    `test_x.cpython-313.pyc` as a new regression test, and gate 8 would try to
    decode bytecode as source — which is exactly how this passed on a machine
    with a global gitignore and failed on CI."""
    ws = create_workspace(_task(), base_dir=tmp_path)
    try:
        (ws.root / "tests" / "__pycache__").mkdir(parents=True, exist_ok=True)
        (ws.root / "tests" / "__pycache__" / "test_pkg.cpython-313.pyc").write_bytes(b"\xf3\r\n\x00junk")
        (ws.root / "tests" / "stale.pyc").write_bytes(b"\xf3\r\n\x00junk")
        assert ws.new_files_since_baseline() == []
        assert ws.modified_or_deleted_since_baseline() == []
    finally:
        destroy_workspace(ws)


def test_real_new_test_file_is_still_reported(tmp_path):
    """The exclusion must not blind gate 7 to the thing it looks for."""
    ws = create_workspace(_task(), base_dir=tmp_path)
    try:
        (ws.root / "tests" / "test_genuine.py").write_text("def test_x():\n    assert True\n", encoding="utf-8")
        names = [p.name for p in ws.new_files_since_baseline()]
        assert names == ["test_genuine.py"]
    finally:
        destroy_workspace(ws)
