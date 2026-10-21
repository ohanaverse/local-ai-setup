"""The probe's HTTP side, against the fake OpenAI endpoint in conftest.py."""

from llmbench.probe.thinking import (
    AnswerShape,
    Prompt,
    Sampling,
    Target,
    probe_one,
    run_probe,
)

from .conftest import ANSWER

PROMPT = Prompt(id="p", text="What is 17*23? Answer briefly.", source="builtin")
SAMPLING = Sampling(temperature=1.0, top_p=0.95, top_k=20, max_tokens=None)


def _target(base_url: str, model: str, key_env: str | None = None) -> Target:
    return Target(name=model, base_url=base_url, model=model, api_key_env=key_env)


def test_request_carries_the_sampling_and_no_stream(fake_openai):
    probe_one(_target(fake_openai.base_url, "reasoning-content"), PROMPT, SAMPLING, timeout_s=5)
    (request,) = fake_openai.requests
    assert request["path"] == "/v1/chat/completions"
    assert request["body"] == {
        "model": "reasoning-content",
        "messages": [{"role": "user", "content": PROMPT.text}],
        "temperature": 1.0,
        "top_p": 0.95,
        "top_k": 20,
        "stream": False,
    }
    assert "Authorization" not in request["headers"]


def test_zero_top_k_and_top_p_are_left_out_and_max_tokens_is_sent(fake_openai):
    sampling = Sampling(temperature=0.6, top_p=0, top_k=0, max_tokens=4096)
    probe_one(_target(fake_openai.base_url, "no-reasoning"), PROMPT, sampling, timeout_s=5)
    body = fake_openai.requests[0]["body"]
    assert "top_k" not in body
    assert "top_p" not in body
    assert body["max_tokens"] == 4096
    assert body["temperature"] == 0.6


def test_a_key_goes_only_into_the_authorization_header(fake_openai):
    result = probe_one(
        _target(fake_openai.base_url, "reasoning-content", "K"),
        PROMPT,
        SAMPLING,
        api_key="s3cret",
        timeout_s=5,
    )
    sent = fake_openai.requests[0]["headers"].get("Authorization") == "Bearer s3cret"
    assert sent
    assert result.answered


def test_reasoning_content_is_measured(fake_openai):
    result = probe_one(
        _target(fake_openai.base_url, "reasoning-content"), PROMPT, SAMPLING, timeout_s=5
    )
    assert result.status == 200
    assert result.error is None
    assert result.answered
    assert result.reasoning == "We need answer."
    assert result.reasoning_field == "reasoning_content"
    assert result.reasoning_chars == len("We need answer.")
    assert result.reasoning_head == "We need answer."
    assert result.reasoning_tokens is None
    assert result.answer == ANSWER
    assert result.answer_chars == len(ANSWER)
    assert result.completion_tokens == 42
    assert result.finish_reason == "stop"
    assert result.shape == AnswerShape(
        headings=1, bullets=2, numbered=0, table_lines=2, code_fences=0
    )
    assert result.elapsed_s >= 0


def test_reasoning_field_is_accepted(fake_openai):
    result = probe_one(
        _target(fake_openai.base_url, "reasoning-field"), PROMPT, SAMPLING, timeout_s=5
    )
    assert result.reasoning == "17*23 = 391, so 391."
    assert result.reasoning_field == "reasoning"


def test_no_reasoning_field_reads_as_empty(fake_openai):
    result = probe_one(_target(fake_openai.base_url, "no-reasoning"), PROMPT, SAMPLING, timeout_s=5)
    assert result.answered
    assert result.reasoning == ""
    assert result.reasoning_field is None
    assert result.reasoning_chars == 0
    assert result.completion_tokens is None


def test_reasoning_tokens_come_from_completion_tokens_details(fake_openai):
    result = probe_one(
        _target(fake_openai.base_url, "reasoning-tokens"), PROMPT, SAMPLING, timeout_s=5
    )
    assert result.reasoning_tokens == 24
    assert result.completion_tokens == 30


def test_reasoning_head_is_the_first_200_chars():
    from llmbench.probe.thinking import ProbeResult

    result = ProbeResult(target="t", prompt_id="p", reasoning="x" * 500)
    assert result.reasoning_head == "x" * 200


def test_http_500_is_an_error_row(fake_openai):
    result = probe_one(_target(fake_openai.base_url, "server-error"), PROMPT, SAMPLING, timeout_s=5)
    assert result.status == 500
    assert not result.answered
    assert result.error is not None
    assert "HTTP 500" in result.error
    assert "boom" in result.error


def test_non_json_is_an_error_row(fake_openai):
    result = probe_one(_target(fake_openai.base_url, "non-json"), PROMPT, SAMPLING, timeout_s=5)
    assert result.status == 200
    assert not result.answered
    assert result.error is not None
    assert "non-JSON" in result.error


def test_json_without_a_message_is_an_error_row(fake_openai):
    result = probe_one(_target(fake_openai.base_url, "no-choices"), PROMPT, SAMPLING, timeout_s=5)
    assert not result.answered
    assert result.error is not None
    assert "unexpected response" in result.error


def test_slow_reply_times_out(fake_openai):
    result = probe_one(_target(fake_openai.base_url, "slow"), PROMPT, SAMPLING, timeout_s=0.3)
    assert result.status is None
    assert not result.answered
    assert result.error is not None
    assert result.error == "timed out after 0.3s"


def test_unreachable_is_an_error_row(closed_port_url):
    result = probe_one(_target(closed_port_url, "any"), PROMPT, SAMPLING, timeout_s=2)
    assert result.status is None
    assert not result.answered
    assert result.error is not None
    assert "unreachable" in result.error


def test_an_error_text_never_carries_the_key(fake_openai):
    """A server that echoes the request back must not leak the key into a
    report: every error text is redacted."""
    from llmbench.probe.thinking import redact

    assert redact("Bearer s3cret rejected", "s3cret") == "Bearer *** rejected"
    assert redact("nothing here", None) == "nothing here"


def test_run_probe_continues_past_failures(fake_openai, closed_port_url):
    seen = []
    results = run_probe(
        [
            (_target(closed_port_url, "down"), None),
            (_target(fake_openai.base_url, "server-error"), None),
            (_target(fake_openai.base_url, "reasoning-content"), None),
        ],
        [PROMPT, Prompt(id="q", text="other", source="builtin")],
        SAMPLING,
        timeout_s=2,
        on_result=seen.append,
    )
    assert [(r.target, r.prompt_id) for r in results] == [
        ("down", "p"),
        ("down", "q"),
        ("server-error", "p"),
        ("server-error", "q"),
        ("reasoning-content", "p"),
        ("reasoning-content", "q"),
    ]
    assert seen == results
    assert [r.answered for r in results] == [False, False, False, False, True, True]
