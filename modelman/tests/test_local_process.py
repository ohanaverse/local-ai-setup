"""Unit tests for modelman.local_process's HTTP probe helpers."""

import urllib.error
from unittest.mock import MagicMock

import pytest

from modelman import local_process
from modelman.local_process import connection_refused

URL = "http://localhost:8000/health"


def _urlopen_raising(monkeypatch, exc):
    monkeypatch.setattr(local_process.urllib.request, "urlopen", MagicMock(side_effect=exc))


def test_connection_refused_is_true_for_a_refused_connection(monkeypatch):
    # What urlopen raises when nothing listens on the port: the one failure
    # that proves no server is there.
    _urlopen_raising(monkeypatch, urllib.error.URLError(ConnectionRefusedError(61, "refused")))
    assert connection_refused(URL) is True


@pytest.mark.parametrize(
    "exc",
    [
        urllib.error.URLError(TimeoutError("timed out")),  # connect timed out
        TimeoutError("timed out"),  # connected, then the read timed out
        ConnectionResetError(54, "reset"),
        urllib.error.URLError("nodename nor servname provided"),
        ValueError("unknown url type"),
    ],
    ids=["connect-timeout", "read-timeout", "reset", "unresolvable", "bad-url"],
)
def test_connection_refused_is_false_for_any_other_failure(monkeypatch, exc):
    # A server that is slow, mid-load or unreachable for another reason may
    # well be running: that is "cannot tell", never "nothing is there".
    _urlopen_raising(monkeypatch, exc)
    assert connection_refused(URL) is False


def test_connection_refused_is_false_for_an_http_error_status(monkeypatch):
    # A 401 or a 503 is the server answering.
    _urlopen_raising(monkeypatch, urllib.error.HTTPError(URL, 503, "busy", None, None))  # type: ignore[arg-type]
    assert connection_refused(URL) is False


def test_connection_refused_is_false_for_a_200(monkeypatch):
    resp = MagicMock()
    resp.__enter__.return_value.read.return_value = b"{}"
    monkeypatch.setattr(local_process.urllib.request, "urlopen", MagicMock(return_value=resp))
    assert connection_refused(URL) is False
