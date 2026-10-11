import pytest

from llmbench.benchmark.errors import BenchmarkError
from llmbench.benchmark.workloads import get_workload, list_workloads


def test_list_workloads_includes_defaults():
    names = list_workloads()
    assert set(names) >= {"short", "chat", "long", "code"}


def test_get_workload_chat():
    workload = get_workload("chat")
    assert workload.spec.name == "chat"


def test_get_workload_unknown_raises():
    with pytest.raises(BenchmarkError):
        get_workload("nonexistent")
