# tests/benchmark/eval/test_rejudge.py
"""Tests for eval.runner.rejudge_run — re-scoring persisted responses
without regenerating them, using the judge config persisted in run.toml."""

import json
from pathlib import Path

from modelman.benchmark.eval.category import Category, Item
from modelman.benchmark.eval.runner import rejudge_run
from modelman.benchmark.judge_core import Rubric


def _seed_run(tmp_path: Path) -> Path:
    row_dir = tmp_path / "01--row1"
    item_dir = row_dir / "mini_review" / "i1"
    item_dir.mkdir(parents=True)
    (item_dir / "response.txt").write_text("off by one in the loop", encoding="utf-8")
    (tmp_path / "run.toml").write_text(
        """
[run]
git_sha = "abc123"

[suite.judge]
model = "j"
temperature = 0.0
samples = 1
max_attempts = 2
route = "litellm"

[suite.coding]
""",
        encoding="utf-8",
    )
    return tmp_path


class _FakeJudgeTransport:
    def complete(self, prompt: str, *, temperature: float) -> str:
        return json.dumps({"scores": {"a": 90}, "total": 90})


def test_rejudge_run_rescores_from_persisted_response(tmp_path):
    # rejudge_run must read the response.txt written by the original run,
    # re-score it through the judge, and persist a fresh judge.json —
    # without ever regenerating the model's response. This is the core
    # contract: cheap re-scoring after a rubric/judge-model change.
    run_dir = _seed_run(tmp_path)
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    outcomes = rejudge_run(
        run_dir,
        [category],
        judge_transport_factory=lambda judge_cfg: _FakeJudgeTransport(),
    )
    assert outcomes == [{"row": "01--row1", "category": "mini_review", "item": "i1", "total": 90}]
    judge_data = json.loads(
        (run_dir / "01--row1" / "mini_review" / "i1" / "judge.json").read_text()
    )
    assert judge_data["combined"]["total"] == 90


def test_rejudge_run_samples_override_takes_precedence_over_run_toml(tmp_path):
    # samples_override lets a rejudge call ask for more samples than the
    # original run used (run.toml says samples=1); this verifies the
    # override wins over the persisted config rather than being ignored.
    run_dir = _seed_run(tmp_path)
    category = Category(
        name="mini_review",
        path=Path("."),
        items=[Item(id="i1", prompt="review this", meta={})],
        rubric=Rubric(dimensions={"a": 100}),
        rubric_md="score a (100)",
    )
    calls = []

    class _CountingTransport:
        def complete(self, prompt: str, *, temperature: float) -> str:
            calls.append(1)
            return json.dumps({"scores": {"a": 90}, "total": 90})

    rejudge_run(
        run_dir,
        [category],
        samples_override=3,
        judge_transport_factory=lambda judge_cfg: _CountingTransport(),
    )
    assert len(calls) == 3  # one sample per configured sample count, not run.toml's samples=1
