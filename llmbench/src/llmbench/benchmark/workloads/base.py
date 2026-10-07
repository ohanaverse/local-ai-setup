"""Workload abstraction for benchmark runs."""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from typing import Any, Protocol, runtime_checkable

import requests


@dataclass
class WorkloadSpec:
    name: str
    display_name: str
    prompt: str
    max_tokens: int
    temperature: float
    stream: bool
    stream_options: dict[str, Any] | None = None


@dataclass
class RawRun:
    start_time: float
    end_time: float
    first_token_time: float | None
    chunks: list[dict[str, Any]]
    completion_tokens: int | None = None
    prompt_tokens: int | None = None
    error: str | None = None


@dataclass
class BenchmarkMetrics:
    ttft_ms: int | None
    total_ms: int
    completion_tokens: int | None
    prompt_tokens: int | None
    throughput_tok_s: float | None = field(init=False)

    def __post_init__(self) -> None:
        gen_ms = self.total_ms - (self.ttft_ms or 0)
        if self.completion_tokens and gen_ms > 0:
            self.throughput_tok_s = round(self.completion_tokens / (gen_ms / 1000), 2)
        else:
            self.throughput_tok_s = None


@runtime_checkable
class Workload(Protocol):
    @property
    def spec(self) -> WorkloadSpec: ...

    def build_payload(self, model_id: str) -> dict[str, Any]: ...

    def run(self, session, url: str, payload: dict[str, Any]) -> RawRun: ...

    def metrics(self, raw: RawRun) -> BenchmarkMetrics: ...


def _streaming_run(
    session: requests.Session,
    url: str,
    payload: dict[str, Any],
) -> RawRun:
    """Execute a streaming chat request and return raw timing + chunks."""
    start = time.perf_counter()
    first_token: float | None = None
    chunks: list[dict[str, Any]] = []
    completion_tokens: int | None = None
    prompt_tokens: int | None = None
    error: str | None = None

    try:
        with session.post(url, json=payload, stream=True, timeout=600) as resp:
            resp.raise_for_status()
            for line in resp.iter_lines():
                if not line:
                    continue
                decoded = line.decode("utf-8", errors="replace")
                if not decoded.startswith("data: "):
                    continue
                data = decoded[6:]
                if data == "[DONE]":
                    break
                try:
                    chunk = json.loads(data)
                except json.JSONDecodeError:
                    continue
                chunks.append(chunk)
                if first_token is None and _chunk_has_content(chunk):
                    first_token = time.perf_counter()
                usage = chunk.get("usage")
                if isinstance(usage, dict):
                    if "completion_tokens" in usage:
                        completion_tokens = usage["completion_tokens"]
                    if "prompt_tokens" in usage:
                        prompt_tokens = usage["prompt_tokens"]
    except requests.RequestException as exc:
        error = str(exc)
        start = start or time.perf_counter()

    end = time.perf_counter()
    return RawRun(
        start_time=start,
        end_time=end,
        first_token_time=first_token,
        chunks=chunks,
        completion_tokens=completion_tokens,
        prompt_tokens=prompt_tokens,
        error=error,
    )


def _chunk_has_content(chunk: dict[str, Any]) -> bool:
    choices = chunk.get("choices")
    if not isinstance(choices, list) or not choices:
        return False
    first_choice = choices[0]
    if not isinstance(first_choice, dict):
        return False
    delta = first_choice.get("delta", {})
    return bool(delta.get("content"))
