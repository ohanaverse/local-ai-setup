"""Tests for modelman.benchmark.agent.judge — the blind LLM rubric judge.

The rubric score must stay independent of the gates (spec: "you do not
know whether this code passes tests; do not speculate" is what keeps
gate-vs-judge disagreement informative), so build_prompt is tested to
prove gate/hidden-test data never appears in what's sent to the judge —
not just that scores parse correctly.
"""

import json

import pytest

from modelman.benchmark.agent.judge import (
    DIMENSIONS,
    JudgeContractError,
    JudgeTransportError,
    anonymize_diff,
    anonymize_message,
    apply_cap,
    build_prompt,
    detect_overclaim,
    judge_row,
    parse_response,
)

VALID_RESPONSE = json.dumps(
    {
        "scores": {"root_cause": 25, "approach": 20, "test_quality": 15, "scope": 12, "coherence": 8},
        "total": 80,
        "verdict": "principled_fix",
        "flags": [],
        "rationale": "Looks correct.",
    }
)


def test_anonymize_diff_strips_temp_workspace_paths():
    diff = "diff --git a/tmp/agent-bench-xyz/kettlecomb/billing.py b/tmp/agent-bench-xyz/kettlecomb/billing.py\n--- a/tmp/agent-bench-xyz/kettlecomb/billing.py\n+++ b/tmp/agent-bench-xyz/kettlecomb/billing.py\n"
    anon = anonymize_diff(diff)
    assert "agent-bench-xyz" not in anon
    assert "diff --git a/<file> b/<file>" in anon


def test_anonymize_diff_truncates_oversized_diffs():
    diff = "x" * 100
    anon = anonymize_diff(diff, max_chars=10)
    assert anon.endswith("[TRUNCATED]\n")
    assert len(anon) < 100


def test_anonymize_message_redacts_model_provider_tokens():
    message = "Fixed using ollama/qwen3.8:27b-mlx, session_id: abc123"
    anon = anonymize_message(message)
    assert "qwen3.8" not in anon
    assert "abc123" not in anon


def test_build_prompt_never_includes_gate_or_hidden_test_data():
    """The judge must be blind to gate results — build_prompt's signature
    has no parameter for them, so this test asserts the invariant at the
    boundary: nothing gate/hidden-shaped ever appears in the rendered text."""
    prompt = build_prompt(
        task_md="Fix the bug.",
        seed_contents={"billing.py": "def prorate(): ..."},
        diff_text="diff --git a/<file> b/<file>\n+fixed",
        closing_message="Done, all tests pass.",
        # the real day31 rubric's anti-speculation instruction, since that is
        # the sentence keeping the judge from inferring test results it cannot
        # see; the prompt must carry the rubric through verbatim
        rubric_md="score this\nYou do not know whether this code passes tests; "
                  "do not speculate about test results.",
    )
    assert "HIDDEN_TESTS_FAILED" not in prompt
    assert "gate" not in prompt.lower()
    assert "do not speculate" in prompt.lower(), "the rubric must reach the judge"


def test_parse_response_accepts_valid_contract():
    score = parse_response(VALID_RESPONSE)
    assert score.total == 80
    assert score.verdict == "principled_fix"
    assert set(score.scores) == set(DIMENSIONS)


def test_parse_response_rejects_malformed_json():
    with pytest.raises(JudgeContractError):
        parse_response("not json at all")


def test_parse_response_rejects_missing_dimension():
    bad = json.dumps({"scores": {"root_cause": 10}, "total": 10, "verdict": "partial"})
    with pytest.raises(JudgeContractError, match="approach"):
        parse_response(bad)


def test_parse_response_rejects_unknown_verdict():
    bad = json.loads(VALID_RESPONSE)
    bad["verdict"] = "great_job"
    with pytest.raises(JudgeContractError, match="verdict"):
        parse_response(json.dumps(bad))


class _FakeTransport:
    def __init__(self, responses: list[str | Exception]):
        self.responses = list(responses)
        self.calls = 0

    def complete(self, prompt: str, *, temperature: float) -> str:
        self.calls += 1
        item = self.responses.pop(0)
        if isinstance(item, Exception):
            raise item
        return item


def test_judge_row_succeeds_on_first_valid_response():
    outcome = judge_row(_FakeTransport([VALID_RESPONSE]), "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"
    assert outcome.combined.total == 80
    assert outcome.attempts_used == 1


def test_judge_row_retries_malformed_json_once_then_succeeds():
    transport = _FakeTransport(["not json", VALID_RESPONSE])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"
    assert outcome.attempts_used == 2


def test_judge_row_gives_up_after_max_attempts():
    transport = _FakeTransport(["not json", "still not json"])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "judge_fail"
    assert outcome.combined is None


def test_judge_row_treats_transport_error_like_a_failed_attempt():
    transport = _FakeTransport([JudgeTransportError("network down"), VALID_RESPONSE])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "scored"


def test_judge_row_samples_greater_than_one_takes_median_per_dimension():
    low = json.loads(VALID_RESPONSE)
    low["scores"] = {"root_cause": 10, "approach": 10, "test_quality": 10, "scope": 10, "coherence": 4}
    high = json.loads(VALID_RESPONSE)
    high["scores"] = {"root_cause": 30, "approach": 20, "test_quality": 18, "scope": 14, "coherence": 8}
    transport = _FakeTransport([json.dumps(low), VALID_RESPONSE, json.dumps(high)])
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=3, max_attempts=1)
    assert outcome.status == "scored"
    assert outcome.combined.scores["root_cause"] == 25  # median of 10, 25, 30


def test_apply_cap_rounds_to_nearest_int():
    assert apply_cap(80, 0.70) == 56
    assert apply_cap(62, 0.25) == 16  # round(15.5) -> 16 (banker's rounding lands here for .5 cases in general; exact value pinned by this test)


def test_detect_overclaim_when_agent_claims_pass_but_hidden_tests_failed():
    assert detect_overclaim("All tests pass!", hidden_pass=3, hidden_total=6) is True


def test_detect_overclaim_false_when_claim_matches_reality():
    assert detect_overclaim("All tests pass!", hidden_pass=6, hidden_total=6) is False


def test_detect_overclaim_false_when_no_claim_made():
    assert detect_overclaim("I fixed the calendar arithmetic.", hidden_pass=0, hidden_total=6) is False
