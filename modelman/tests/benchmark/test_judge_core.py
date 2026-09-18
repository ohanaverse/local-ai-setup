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
    # verdicts=None means a "verdict" key is neither required nor validated
    # against a closed set — this covers a category whose rubric has no
    # verdict enum at all (the eval-benchmark categories), not just the
    # agent benchmark's verdict-bearing rubric.
    raw = json.dumps({"scores": {"x": 40, "y": 20, "z": 10}, "total": 70})
    score = parse_response(raw, NO_VERDICT_RUBRIC)
    assert score.total == 70
    assert score.verdict == ""


def test_parse_response_rejects_score_over_dimension_max():
    # A per-dimension score must stay within its rubric-declared max points —
    # an out-of-range value (here x=999 against a max of 50) is a strict
    # contract violation, not silently clamped, so a malformed judge reply
    # can't inflate a category's score.
    raw = json.dumps({"scores": {"x": 999, "y": 20, "z": 10}, "total": 70})
    with pytest.raises(JudgeContractError, match="x"):
        parse_response(raw, NO_VERDICT_RUBRIC)


def test_parse_response_validates_verdict_when_rubric_requires_it():
    # When a rubric DOES declare a closed verdict set, a verdict outside
    # that set ("ok" vs. the declared {"good", "bad"}) must be rejected —
    # this is the complement of the no-verdict test above, proving the
    # validation is genuinely rubric-driven rather than always-off.
    raw = json.dumps({"scores": {"a": 50, "b": 30}, "total": 80, "verdict": "ok"})
    with pytest.raises(JudgeContractError, match="verdict"):
        parse_response(raw, VERDICT_RUBRIC)


def test_parse_response_skips_a_scores_total_decoy_for_a_verdict_rubric():
    # Regression test: a judge reply can contain a decoy JSON fragment (an
    # illustrative example, or the model echoing the schema) that happens to
    # have "scores"/"total" keys but no "verdict" before its real answer.
    # For a rubric that declares a closed verdict set, the disambiguation
    # must require "verdict" too, or the decoy wins the `>=` superset check
    # and the real, verdict-bearing answer later in the text is never read.
    raw = (
        'Restating the schema: {"scores": {"a": 0, "b": 0}, "total": 0}\n'
        + json.dumps({"scores": {"a": 50, "b": 30}, "total": 80, "verdict": "good"})
    )
    score = parse_response(raw, VERDICT_RUBRIC)
    assert score.total == 80
    assert score.verdict == "good"


class _StubTransport:
    def __init__(self, replies: list[str]):
        self.replies = list(replies)

    def complete(self, prompt: str, *, temperature: float) -> str:
        return self.replies.pop(0)


def test_parse_response_rejects_non_int_total():
    # `total` must be a true int: a numeric string ("90") must not be
    # silently coerced by int(), and a non-numeric value must raise
    # JudgeContractError (which judge_row retries) instead of a raw
    # ValueError/TypeError that escaped the retry loop and failed the
    # whole category.
    for bad in ("90", 90.5, None, {"v": 90}, [90]):
        raw = json.dumps({"scores": {"x": 40, "y": 30, "z": 20}, "total": bad})
        with pytest.raises(JudgeContractError, match="total"):
            parse_response(raw, NO_VERDICT_RUBRIC)


def test_parse_response_rejects_total_not_matching_score_sum():
    # The declared total must equal the sum of the declared scores — a
    # fabricated total (999 when scores sum to 90) would otherwise inflate
    # the category score. Covers both directions (over and under).
    raw = json.dumps({"scores": {"a": 60, "b": 30}, "total": 999, "verdict": "good"})
    with pytest.raises(JudgeContractError, match="total"):
        parse_response(raw, VERDICT_RUBRIC)
    raw2 = json.dumps({"scores": {"a": 60, "b": 30}, "total": 80, "verdict": "good"})
    with pytest.raises(JudgeContractError, match="total"):
        parse_response(raw2, VERDICT_RUBRIC)


def test_judge_row_combines_multiple_samples_by_median():
    # Multi-sample judging (samples > 1) combines per-dimension scores by
    # MEDIAN, not mean — a median is far less sensitive to one outlier
    # sample, which matters since a judge's reply quality can vary run to
    # run; this pins the exact combination rule.
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
    # After max_attempts consecutive malformed replies, judge_row must give
    # up cleanly with status="judge_fail" and combined=None rather than
    # raising or retrying forever — this is what lets a category's mean
    # calculation treat the item as "no score" instead of crashing the run.
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
