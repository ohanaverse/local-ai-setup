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
