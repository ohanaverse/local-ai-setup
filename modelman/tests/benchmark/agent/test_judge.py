"""Tests for modelman.benchmark.agent.judge — the blind LLM rubric judge.

The rubric score must stay independent of the gates (spec: "you do not
know whether this code passes tests; do not speculate" is what keeps
gate-vs-judge disagreement informative), so build_prompt is tested to
prove gate/hidden-test data never appears in what's sent to the judge —
not just that scores parse correctly.
"""

import json

import pytest
import requests

import modelman.benchmark.agent.judge as judge_module
from modelman.benchmark.agent.judge import (
    DIMENSIONS,
    JudgeContractError,
    JudgeTransportError,
    LiteLLMJudgeTransport,
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




class _FakeResponse:
    def __init__(self, status=200, payload=None):
        self.status_code = status
        self._payload = payload or {"choices": [{"message": {"content": "hi"}}]}

    def raise_for_status(self):
        if self.status_code >= 400:
            raise requests.HTTPError(f"status {self.status_code}")

    def json(self):
        return self._payload


def test_litellm_transport_posts_expected_payload(monkeypatch):
    captured = {}

    def fake_post(url, headers=None, json=None, timeout=None):
        captured["url"] = url
        captured["headers"] = headers
        captured["json"] = json
        return _FakeResponse(payload={"choices": [{"message": {"content": '{"total": 1}'}}]})

    monkeypatch.setattr(judge_module.requests, "post", fake_post)
    transport = LiteLLMJudgeTransport(
        base_url="http://localhost:4000/v1", api_key="sk-test", model="openrouter/anthropic/claude-opus-4"
    )
    result = transport.complete("prompt text", temperature=0.0)

    assert result == '{"total": 1}'
    assert captured["url"] == "http://localhost:4000/v1/chat/completions"
    assert captured["headers"]["Authorization"] == "Bearer sk-test"
    assert captured["json"]["model"] == "openrouter/anthropic/claude-opus-4"
    assert captured["json"]["temperature"] == 0.0


def test_litellm_transport_retries_once_then_succeeds(monkeypatch):
    calls = {"n": 0}

    def flaky_post(url, headers=None, json=None, timeout=None):
        calls["n"] += 1
        if calls["n"] == 1:
            raise requests.ConnectionError("boom")
        return _FakeResponse(payload={"choices": [{"message": {"content": "ok"}}]})

    monkeypatch.setattr(judge_module.requests, "post", flaky_post)
    transport = LiteLLMJudgeTransport(base_url="http://localhost:4000/v1", api_key="k", model="m", retry_backoff_s=0.0)
    assert transport.complete("p", temperature=0.0) == "ok"
    assert calls["n"] == 2


def test_litellm_transport_raises_judge_transport_error_after_retry_exhausted(monkeypatch):
    def always_fails(url, headers=None, json=None, timeout=None):
        raise requests.ConnectionError("still down")

    monkeypatch.setattr(judge_module.requests, "post", always_fails)
    transport = LiteLLMJudgeTransport(base_url="http://localhost:4000/v1", api_key="k", model="m", retry_backoff_s=0.0)
    with pytest.raises(JudgeTransportError):
        transport.complete("p", temperature=0.0)


def test_judge_fail_carries_the_reason():
    """`judge_fail` alone is not actionable: a 404 (the suite names a model this
    gateway does not serve), a 401 (no key) and a malformed reply are three
    different fixes, and the row's own gates and speed data are fine in all
    three cases — so the reason has to survive to judge.json."""
    transport = _FakeTransport(
        [JudgeTransportError("HTTP 404: no deployment for model x")] * 2
    )
    outcome = judge_row(transport, "prompt", temperature=0.0, samples=1, max_attempts=2)
    assert outcome.status == "judge_fail"
    assert outcome.error and "404" in outcome.error


def test_http_error_reports_the_response_body(monkeypatch):
    """The status says the request failed; the body says why, and the body is
    the part that names the model the suite got wrong."""

    class _Resp:
        status_code = 404
        text = '{"error": "No deployment found for model openrouter/anthropic/claude-opus-4"}'

    monkeypatch.setattr(judge_module.requests, "post", lambda *a, **k: _Resp())
    transport = judge_module.LiteLLMJudgeTransport(base_url="http://x/v1", api_key="k", model="m")
    with pytest.raises(JudgeTransportError, match="No deployment found"):
        transport.complete("prompt", temperature=0.0)


def test_parse_response_tolerates_a_markdown_fence():
    """Every key and range is still validated; only the wrapper is forgiven. A
    judge that fences its JSON is answering correctly, and with max_attempts
    re-sending the identical prompt a wrapper-intolerant parser makes that a
    permanent JUDGE_FAIL on every row."""
    fenced = "```json\n" + VALID_RESPONSE + "\n```"
    assert parse_response(fenced).total == 80


def test_parse_response_tolerates_a_preamble_sentence():
    prose = "Here is my assessment of this diff:\n" + VALID_RESPONSE + "\nHope that helps."
    assert parse_response(prose).verdict == "principled_fix"


def test_parse_response_still_rejects_a_valid_wrapper_with_bad_scores():
    wrapped = "note: ```json\n" + json.dumps(
        {
            "scores": {"root_cause": 99, "approach": 25, "test_quality": 20, "scope": 15, "coherence": 10},
            "total": 169, "verdict": "principled_fix",
        }
    ) + "\n```"
    with pytest.raises(JudgeContractError, match="root_cause"):
        parse_response(wrapped)


def test_parse_response_still_rejects_text_with_no_object_at_all():
    with pytest.raises(JudgeContractError, match="not valid JSON"):
        parse_response("I cannot score this diff without more context.")
