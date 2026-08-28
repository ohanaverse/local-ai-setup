from modelman.benchmark.errors import BenchmarkError


def test_benchmark_error_is_exception():
    exc = BenchmarkError("boom")
    assert str(exc) == "boom"
    assert isinstance(exc, Exception)
