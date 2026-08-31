"""Short warmup / TTFT workload."""

from __future__ import annotations

from typing import Any

import requests

from .base import BenchmarkMetrics, RawRun, WorkloadSpec, _streaming_run

SPEC = WorkloadSpec(
    name="short",
    display_name="Short (hi)",
    prompt="hi",
    max_tokens=1,
    temperature=0.0,
    stream=True,
    stream_options={"include_usage": True},
)


class ShortStreamingWorkload:
    spec = SPEC

    def build_payload(self, model_id: str) -> dict[str, Any]:
        return {
            "model": model_id,
            "messages": [{"role": "user", "content": self.spec.prompt}],
            "max_tokens": self.spec.max_tokens,
            "temperature": self.spec.temperature,
            "stream": self.spec.stream,
            "stream_options": self.spec.stream_options,
        }

    def run(self, session: requests.Session, url: str, payload: dict[str, Any]) -> RawRun:
        return _streaming_run(session, url, payload)

    def metrics(self, raw: RawRun) -> BenchmarkMetrics:
        if raw.error:
            return BenchmarkMetrics(
                ttft_ms=None,
                total_ms=int((raw.end_time - raw.start_time) * 1000),
                completion_tokens=None,
                prompt_tokens=None,
            )
        ttft_ms = (
            int((raw.first_token_time - raw.start_time) * 1000) if raw.first_token_time else None
        )
        total_ms = int((raw.end_time - raw.start_time) * 1000)
        return BenchmarkMetrics(
            ttft_ms=ttft_ms,
            total_ms=total_ms,
            completion_tokens=raw.completion_tokens,
            prompt_tokens=raw.prompt_tokens,
        )


__all__ = ["ShortStreamingWorkload"]
