"""Built-in benchmark workloads."""

from __future__ import annotations

from typing import Any

from modelman.benchmark.errors import BenchmarkError

from .base import Workload
from .chat_streaming import ChatStreamingWorkload
from .code import CodeStreamingWorkload
from .long import LongStreamingWorkload
from .short import ShortStreamingWorkload

_WORKLOADS: dict[str, Any] = {
    "short": ShortStreamingWorkload,
    "chat": ChatStreamingWorkload,
    "long": LongStreamingWorkload,
    "code": CodeStreamingWorkload,
}


def list_workloads() -> list[str]:
    """Return the names of all registered workloads."""
    return sorted(_WORKLOADS.keys())


def get_workload(name: str) -> Workload:
    """Return a workload instance by name."""
    cls = _WORKLOADS.get(name)
    if cls is None:
        raise BenchmarkError(f"unknown workload: {name}")
    return cls()


__all__ = ["get_workload", "list_workloads", "Workload"]
