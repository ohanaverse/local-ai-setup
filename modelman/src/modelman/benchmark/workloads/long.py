"""Long sustained-throughput workload."""

from __future__ import annotations

from .base import WorkloadSpec
from .chat_streaming import ChatStreamingWorkload


class LongStreamingWorkload(ChatStreamingWorkload):
    spec = WorkloadSpec(  # type: ignore[assignment]
        name="long",
        display_name="Long (REST vs GraphQL, 1024 tokens)",
        prompt=(
            "Explain in detail the differences between REST and GraphQL APIs, "
            "including trade-offs in caching, partial responses, and tooling. Be thorough."
        ),
        max_tokens=1024,
        temperature=0.0,
        stream=True,
        stream_options={"include_usage": True},
    )


__all__ = ["LongStreamingWorkload"]
