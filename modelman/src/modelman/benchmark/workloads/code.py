"""Code generation workload."""

from __future__ import annotations

from .base import WorkloadSpec
from .chat_streaming import ChatStreamingWorkload


class CodeStreamingWorkload(ChatStreamingWorkload):
    spec = WorkloadSpec(  # type: ignore[assignment]
        name="code",
        display_name="Code (merge sorted lists)",
        prompt="Write a Python function that merges two sorted lists into one sorted list. Do not use built-in sort.",
        max_tokens=200,
        temperature=0.0,
        stream=True,
        stream_options={"include_usage": True},
    )


__all__ = ["CodeStreamingWorkload"]
