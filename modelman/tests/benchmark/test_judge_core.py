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
    raw = 'Restating the schema: {"scores": {"a": 0, "b": 0}, "total": 0}\n' + json.dumps(
        {"scores": {"a": 50, "b": 30}, "total": 80, "verdict": "good"}
    )
    score = parse_response(raw, VERDICT_RUBRIC)
    assert score.total == 80
    assert score.verdict == "good"


class _StubTransport:
    def __init__(self, replies: list[str]):
        self.replies = list(replies)

    def complete(self, prompt: str, *, temperature: float) -> str:
        return self.replies.pop(0)


def test_parse_response_derives_total_from_scores():
    # The judge's own `total` (float, string, wrong sum, missing) must not
    # fail an otherwise valid reply: identical retries would fail the same
    # way and the row would show N/A. The total is recomputed from the
    # validated per-dimension scores, which also means a fabricated total
    # can never inflate the score.
    for bad in ("90", 90.0, 999, 80, None, {"v": 90}, [90]):
        raw = json.dumps({"scores": {"x": 40, "y": 30, "z": 20}, "total": bad})
        assert parse_response(raw, NO_VERDICT_RUBRIC).total == 90
    raw = json.dumps({"scores": {"x": 40, "y": 30, "z": 20}})
    assert parse_response(raw, NO_VERDICT_RUBRIC).total == 90


def test_parse_response_rejects_unhashable_verdict():
    # A list/dict verdict used to raise a bare TypeError (unhashable in the
    # `in` check) that escaped judge_row's retry loop and aborted the whole
    # sweep; it must be a JudgeContractError so the row retries/judge_fails.
    for bad in (["good"], {"v": "good"}, 3):
        raw = json.dumps({"scores": {"a": 60, "b": 30}, "total": 90, "verdict": bad})
        with pytest.raises(JudgeContractError, match="verdict"):
            parse_response(raw, VERDICT_RUBRIC)
        with pytest.raises(JudgeContractError, match="verdict"):
            parse_response(
                json.dumps({"scores": {"x": 40, "y": 30, "z": 20}, "verdict": bad}),
                NO_VERDICT_RUBRIC,
            )


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


def test_litellm_transport_retries_read_timeouts_by_default(monkeypatch):
    # The judge transport (120s timeout) must retry a read timeout once with
    # backoff like any other request error — a slow judge otherwise loses
    # one of its judge_row attempts to a single transient stall.
    import requests

    from modelman.benchmark.judge_core import JudgeTransportError, LiteLLMJudgeTransport

    calls = []

    def fake_post(*args, **kwargs):
        calls.append(1)
        raise requests.ReadTimeout("slow")

    monkeypatch.setattr(requests, "post", fake_post)
    transport = LiteLLMJudgeTransport("http://x", "k", "m", retry_backoff_s=0)
    with pytest.raises(JudgeTransportError, match="after retry"):
        transport.complete("p", temperature=0.0)
    assert len(calls) == 2


def test_litellm_transport_fail_fast_on_read_timeout_does_not_retry(monkeypatch):
    # A generation transport (900s timeout) opts out of the retry: a read
    # timeout already burned the full timeout_s, and retrying would double
    # the stall on a model that is likely looping.
    import requests

    from modelman.benchmark.judge_core import JudgeTransportError, LiteLLMJudgeTransport

    calls = []

    def fake_post(*args, **kwargs):
        calls.append(1)
        raise requests.ReadTimeout("slow")

    monkeypatch.setattr(requests, "post", fake_post)
    transport = LiteLLMJudgeTransport(
        "http://x", "k", "m", retry_backoff_s=0, fail_fast_on_read_timeout=True
    )
    with pytest.raises(JudgeTransportError, match="timed out"):
        transport.complete("p", temperature=0.0)
    assert len(calls) == 1


def test_judge_row_two_sample_median_rounds_half_up():
    # An even sample count gives a .5 median; round() would apply banker's
    # rounding (2.5 -> 2, 3.5 -> 4), scoring identical judge splits
    # differently by parity. Half-up keeps combined scores consistent.
    from modelman.benchmark.judge_core import judge_row

    replies = [
        json.dumps({"scores": {"x": 2, "y": 3, "z": 0}, "total": 5}),
        json.dumps({"scores": {"x": 3, "y": 4, "z": 0}, "total": 7}),
    ]
    outcome = judge_row(
        _StubTransport(replies), "p", NO_VERDICT_RUBRIC, temperature=0.0, samples=2, max_attempts=1
    )
    assert outcome.combined is not None
    assert outcome.combined.scores == {"x": 3, "y": 4, "z": 0}


def test_parse_response_accepts_integral_float_scores_and_rejects_fractional():
    # A judge that writes 40.0 means 40: rejecting it would fail every
    # identical temperature-0 retry and turn the item into judge_fail/N/A.
    # The scores come back as ints, so the derived total stays an int. A
    # genuinely fractional score (40.5) remains a contract violation.
    ok = parse_response(json.dumps({"scores": {"x": 40.0, "y": 20, "z": 10.0}}), NO_VERDICT_RUBRIC)
    assert ok.scores == {"x": 40, "y": 20, "z": 10}
    assert ok.total == 70 and isinstance(ok.total, int)
    with pytest.raises(JudgeContractError):
        parse_response(json.dumps({"scores": {"x": 40.5, "y": 20, "z": 10}}), NO_VERDICT_RUBRIC)
