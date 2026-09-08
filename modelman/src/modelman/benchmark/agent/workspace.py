"""Scratch git workspace for one agent-benchmark row."""

from __future__ import annotations

import shutil
import subprocess
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

from modelman.benchmark.agent.task import TaskBundle
from modelman.benchmark.errors import BenchmarkError

BASELINE_COMMIT_MESSAGE = "baseline"


def _git(args: list[str], cwd: Path, check: bool = True) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, check=False)
    if check and result.returncode != 0:
        raise BenchmarkError(f"git {' '.join(args)} failed in {cwd}: {result.stderr.strip()}")
    return result


@dataclass
class Workspace:
    root: Path
    baseline_sha: str
    # Memoized _status_since_baseline() result for this instance. Each row
    # gets its own fresh Workspace (create_workspace()/destroy_workspace()
    # bracket a single row — see runner.py's _run_single_row), so caching
    # here can never leak status across rows or passes. Within one row,
    # gates.py and runner.py call the three status-reading methods below
    # up to 5-6 times against an unchanged tree (the only mutation is
    # seed_hidden(), which explicitly invalidates this cache — see there).
    # `None` means "not yet computed"; recomputed lazily on next access.
    _status_cache: list[tuple[str, str]] | None = field(default=None, init=False, repr=False)

    def seed_hidden(self, task: TaskBundle) -> None:
        """Copy hidden/'s contents into the bundle's configured tests_dir,
        joining the visible test package so unittest's dotted module names
        resolve. Called only after the agent run has already finished —
        hidden tests must never be visible during the run itself.

        This is the one call in a row's lifecycle that mutates the tree
        after _status_since_baseline() may have already been cached (gate
        9 runs last, after gates 3/6/7 have all read status) — invalidate
        the cache so a hypothetical later status read (there isn't one
        today, but a future gate could add one) never sees stale data."""
        self._status_cache = None
        if not task.hidden_dir.is_dir():
            return
        tests_dir = task.gates_config["build"]["tests_dir"]
        shutil.copytree(task.hidden_dir, self.root / tests_dir, dirs_exist_ok=True)

    def diff(self) -> str:
        """Diff of everything since the baseline commit, tracked mods and
        new files alike. Stages first so untracked new files are included —
        `git diff <sha>` alone ignores untracked paths."""
        _git(["add", "-A"], cwd=self.root)
        return _git(["diff", self.baseline_sha, "--cached", "--"], cwd=self.root).stdout

    def _status_since_baseline(self) -> list[tuple[str, str]]:
        if self._status_cache is not None:
            return self._status_cache
        _git(["add", "-A"], cwd=self.root)
        result = _git(["diff", self.baseline_sha, "--cached", "--name-status", "--"], cwd=self.root)
        entries = []
        for line in result.stdout.splitlines():
            parts = line.split("\t")
            status = parts[0]
            if status[0] in ("R", "C"):
                # A rename/copy line is "R100\told\tnew" (three fields, and
                # the letter carries a similarity percentage) rather than the
                # two-field "A"/"M"/"D" lines the callers below match on.
                # Treat it as the old path disappearing and the new path
                # appearing, which is what gates 3/7 and the judge's
                # seed_contents actually need to see.
                old_name, new_name = parts[1], parts[2]
                if not _is_build_artifact(old_name):
                    entries.append(("D", old_name))
                if not _is_build_artifact(new_name):
                    entries.append(("A", new_name))
                continue
            name = parts[1]
            if _is_build_artifact(name):
                continue
            entries.append((status, name))
        self._status_cache = entries
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
        _git(
            ["worktree", "add", "--detach", "--force", str(dest), self.baseline_sha], cwd=self.root
        )

    def remove_worktree(self, dest: Path) -> None:
        _git(["worktree", "remove", "--force", str(dest)], cwd=self.root, check=False)
        shutil.rmtree(dest, ignore_errors=True)


# Bytecode a run leaves behind: `python -m unittest` imports the tests, and the
# agent runs `python3` freely in there. `_status_since_baseline` stages with
# `git add -A`, so without this a `test_day31.cpython-313.pyc` becomes an added
# file — which gate 7's `test_` prefix then reads as a new regression test and
# gate 8 tries to decode as UTF-8 source. Written to .git/info/exclude rather
# than a tracked .gitignore so the agent sees exactly the tree the bundle
# describes, and so a bundle cannot reintroduce the problem with its own
# ignore file.
EXCLUDE_PATTERNS = ("__pycache__/", "*.pyc", "*.pyo")


def _write_info_exclude(root: Path) -> None:
    exclude = root / ".git" / "info" / "exclude"
    exclude.parent.mkdir(parents=True, exist_ok=True)
    existing = exclude.read_text(encoding="utf-8") if exclude.exists() else ""
    missing = [pat for pat in EXCLUDE_PATTERNS if pat not in existing]
    if missing:
        exclude.write_text(existing + "\n".join(missing) + "\n", encoding="utf-8")


def _is_build_artifact(name: str) -> bool:
    """Belt to info/exclude's braces: an already-staged artifact (or one a
    bundle's own ignore rules let through) must never reach the diff the judge
    reads or the new-test-file list gate 7 uses."""
    parts = name.split("/")
    return name.endswith((".pyc", ".pyo")) or "__pycache__" in parts


def create_workspace(task: TaskBundle, base_dir: Path | None = None) -> Workspace:
    """Copy visible/ into a fresh temp dir, git init, commit as baseline."""
    root = Path(tempfile.mkdtemp(prefix="agent-bench-", dir=str(base_dir) if base_dir else None))
    shutil.copytree(task.visible_dir, root, dirs_exist_ok=True)
    _git(["init", "-q"], cwd=root)
    _git(["config", "user.email", "agent-bench@local"], cwd=root)
    _git(["config", "user.name", "agent-bench"], cwd=root)
    _write_info_exclude(root)
    _git(["add", "-A"], cwd=root)
    _git(["commit", "-q", "-m", BASELINE_COMMIT_MESSAGE], cwd=root)
    sha = _git(["rev-parse", "HEAD"], cwd=root).stdout.strip()
    return Workspace(root=root, baseline_sha=sha)


def destroy_workspace(workspace: Workspace) -> None:
    shutil.rmtree(workspace.root, ignore_errors=True)
