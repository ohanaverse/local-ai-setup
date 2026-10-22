from unittest.mock import MagicMock

from llmbench.benchmark.workloads.base import BenchmarkMetrics, RawRun
from llmbench.benchmark.workloads.chat_streaming import ChatStreamingWorkload
from llmbench.benchmark.workloads.code import CodeStreamingWorkload
from llmbench.benchmark.workloads.long import LongStreamingWorkload
from llmbench.benchmark.workloads.short import ShortStreamingWorkload


def _fake_response(lines: list[str]):
    resp = MagicMock()
    resp.status_code = 200
    resp.raise_for_status = MagicMock()
    resp.iter_lines.return_value = [line.encode("utf-8") for line in lines]
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def test_chat_workload_builds_payload():
    workload = ChatStreamingWorkload()
    payload = workload.build_payload("ollama/ornith-1.5:35b")
    assert payload["model"] == "ollama/ornith-1.5:35b"
    assert payload["max_tokens"] == 200
    assert payload["temperature"] == 0.0


def test_chat_workload_run_counts_tokens():
    workload = ChatStreamingWorkload()
    session = MagicMock()
    lines = [
        'data: {"choices":[{"delta":{"content":"Hello"}}]}',
        'data: {"choices":[],"usage":{"completion_tokens":50,"prompt_tokens":10}}',
        "data: [DONE]",
    ]
    session.post.return_value = _fake_response(lines)

    raw = workload.run(
        session, "http://localhost:4000/v1/chat/completions", workload.build_payload("x")
    )

    assert raw.error is None
    assert raw.completion_tokens == 50
    assert raw.prompt_tokens == 10
    assert raw.first_token_time is not None


def test_short_workload_one_token():
    workload = ShortStreamingWorkload()
    assert workload.spec.max_tokens == 1


def test_code_workload_prompt():
    workload = CodeStreamingWorkload()
    assert "two sorted lists" in workload.spec.prompt


def test_long_workload_inherits_chat():
    workload = LongStreamingWorkload()
    assert workload.spec.max_tokens == 1024


def test_raw_run_defaults():
    raw = RawRun(start_time=0.0, end_time=1.0, first_token_time=0.2, chunks=[])
    assert raw.completion_tokens is None
    assert raw.prompt_tokens is None
    assert raw.error is None


def test_metrics_computes_throughput():
    m = BenchmarkMetrics(ttft_ms=100, total_ms=500, completion_tokens=100, prompt_tokens=10)
    assert m.throughput_tok_s == 250.0


def test_first_token_counts_thinking_tokens():
    """A thinking model streams its reasoning before any answer text (omlx
    and mtplx as `reasoning_content`, ollama as `reasoning`). Those are
    generated tokens and completion_tokens counts them, so the first one
    starts the clock. Waiting for answer text instead leaves TTFT empty when
    the token cap ends the stream inside the thinking, or folds the whole
    thinking into TTFT and inflates throughput."""
    workload = ChatStreamingWorkload()
    for field in ("reasoning_content", "reasoning"):
        session = MagicMock()
        session.post.return_value = _fake_response(
            [
                'data: {"choices":[{"delta":{"role":"assistant"}}]}',
                f'data: {{"choices":[{{"delta":{{"{field}":"Let me think"}}}}]}}',
                "data: [DONE]",
            ]
        )
        raw = workload.run(session, "http://localhost:8000/v1/chat/completions", {})
        assert raw.first_token_time is not None, field

    # A role-only chunk carries no generated token.
    session = MagicMock()
    session.post.return_value = _fake_response(
        ['data: {"choices":[{"delta":{"role":"assistant"}}]}', "data: [DONE]"]
    )
    raw = workload.run(session, "http://localhost:8000/v1/chat/completions", {})
    assert raw.first_token_time is None
