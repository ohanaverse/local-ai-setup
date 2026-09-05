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
