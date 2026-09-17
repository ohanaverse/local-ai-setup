"""Tests for modelman.benchmark.judge_core — the rubric-parameterized judge
mechanics shared by the agent and eval benchmarks.

A second, differently-shaped Rubric (3 dimensions, no verdict enum) is
tested alongside a verdict-bearing one to prove the module is genuinely
generalized, not just the agent benchmark's old constants renamed.
"""

import json

import pytest

from modelman.benchmark.judge_core import (
    JudgeContractError,
    Rubric,
    judge_row,
    parse_response,
)

VERDICT_RUBRIC = Rubric(dimensions={"a": 60, "b": 40}, verdicts=frozenset({"good", "bad"}))
NO_VERDICT_RUBRIC = Rubric(dimensions={"x": 50, "y": 30, "z": 20}, verdicts=None)


def test_parse_response_accepts_rubric_without_verdicts():
    raw = json.dumps({"scores": {"x": 40, "y": 20, "z": 10}, "total": 70})
    score = parse_response(raw, NO_VERDICT_RUBRIC)
    assert score.total == 70
    assert score.verdict == ""


def test_parse_response_rejects_score_over_dimension_max():
    raw = json.dumps({"scores": {"x": 999, "y": 20, "z": 10}, "total": 70})
    with pytest.raises(JudgeContractError, match="x"):
        parse_response(raw, NO_VERDICT_RUBRIC)


def test_parse_response_validates_verdict_when_rubric_requires_it():
    raw = json.dumps({"scores": {"a": 50, "b": 30}, "total": 80, "verdict": "ok"})
    with pytest.raises(JudgeContractError, match="verdict"):
        parse_response(raw, VERDICT_RUBRIC)


class _StubTransport:
    def __init__(self, replies: list[str]):
        self.replies = list(replies)

    def complete(self, prompt: str, *, temperature: float) -> str:
        return self.replies.pop(0)


def test_judge_row_combines_multiple_samples_by_median():
    replies = [
        json.dumps({"scores": {"x": 40, "y": 20, "z": 10}, "total": 70}),
        json.dumps({"scores": {"x": 50, "y": 30, "z": 20}, "total": 100}),
        json.dumps({"scores": {"x": 30, "y": 10, "z": 0}, "total": 40}),
    ]
    outcome = judge_row(
        _StubTransport(replies),
        "prompt",
        NO_VERDICT_RUBRIC,
        temperature=0.0,
        samples=3,
        max_attempts=1,
    )
    assert outcome.status == "scored"
    assert outcome.combined.scores["x"] == 40  # median of 40/50/30


def test_judge_row_returns_judge_fail_after_exhausting_attempts():
    outcome = judge_row(
        _StubTransport(["not json", "still not json"]),
        "prompt",
        NO_VERDICT_RUBRIC,
        temperature=0.0,
        samples=1,
        max_attempts=2,
    )
    assert outcome.status == "judge_fail"
    assert outcome.combined is None
