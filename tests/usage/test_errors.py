from __future__ import annotations

from modelman.usage.errors import UsageError


# UsageError must subclass RuntimeError so the CLI's `except UsageError` in
# cli.py catches it without needing a separate except clause per raise site.
def test_usage_error_is_runtime_error():
    exc = UsageError("something went wrong")
    assert isinstance(exc, RuntimeError)
    assert str(exc) == "something went wrong"
