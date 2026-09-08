"""Short warmup / TTFT workload."""

from __future__ import annotations

from .base import WorkloadSpec
from .chat_streaming import ChatStreamingWorkload


class ShortStreamingWorkload(ChatStreamingWorkload):
    spec = WorkloadSpec(  # type: ignore[assignment]
        name="short",
        display_name="Short (hi)",
        prompt="hi",
        max_tokens=1,
        temperature=0.0,
        stream=True,
        stream_options={"include_usage": True},
    )


__all__ = ["ShortStreamingWorkload"]
