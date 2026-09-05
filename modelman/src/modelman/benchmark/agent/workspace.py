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
        return [
            self.root / name for status, name in self._status_since_baseline() if status == "A"
        ]

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
