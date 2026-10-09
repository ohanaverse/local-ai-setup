"""`http_models_ids`: the /v1/models probe every lifecycle backend asks
"is it serving, and what?" through. Every other test replaces it."""

import io
import urllib.error

import pytest

from llmbench import local_process


def _serve(monkeypatch, body: bytes, seen: list | None = None):
    def urlopen(url, timeout):
        if seen is not None:
            seen.append((url, timeout))
        return io.BytesIO(body)

    monkeypatch.setattr(local_process.urllib.request, "urlopen", urlopen)


def test_http_models_ids_reads_the_ids_of_a_models_listing(monkeypatch):
    """The ids, in the server's order, and the caller's timeout reaches the
    request: a probe that ignored it would hang a `provider isolate` on a
    server that accepts the connection and never answers."""
    seen: list = []
    _serve(monkeypatch, b'{"object": "list", "data": [{"id": "a"}, {"id": "b/c"}]}', seen)
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models", timeout=0.5) == [
        "a",
        "b/c",
    ]
    assert seen == [("http://127.0.0.1:8000/v1/models", 0.5)]


@pytest.mark.parametrize(
    "body",
    [
        b"<html>502 Bad Gateway</html>",
        b"[]",
        b'{"data": "nope"}',
        # A string can be iterated, so the per-entry filter alone would read it
        # as no models; a number cannot, so this one needs the list check itself.
        b'{"data": 7}',
        b'{"error": {"message": "no key"}}',
        b"\xff\xfe",
    ],
    ids=[
        "not-json",
        "a-list",
        "data-a-string",
        "data-a-number",
        "an-error-body",
        "not-utf8",
    ],
)
def test_http_models_ids_reads_an_unusable_body_as_nothing_serving(monkeypatch, body):
    """Anything that is not a models listing is "no models", never an
    exception: the backends call this in a wait loop, and a traceback there
    would replace the lifecycle's own "did not come up" error."""
    _serve(monkeypatch, body)
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models") == []


def test_http_models_ids_skips_entries_without_a_string_id(monkeypatch):
    """One malformed entry must not hide the models listed beside it."""
    _serve(monkeypatch, b'{"data": [{"id": "ok"}, {"id": 7}, "x", {"name": "no-id"}]}')
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models") == ["ok"]


@pytest.mark.parametrize(
    "exc",
    [urllib.error.URLError("refused"), ConnectionRefusedError(), TimeoutError()],
    ids=["urlerror", "refused", "timeout"],
)
def test_http_models_ids_reads_a_dead_server_as_nothing_serving(monkeypatch, exc):
    """A server that is down, refusing or too slow is "nothing serving": the
    wait loops poll through exactly these while a provider starts."""

    def urlopen(url, timeout):
        raise exc

    monkeypatch.setattr(local_process.urllib.request, "urlopen", urlopen)
    assert local_process.http_models_ids("http://127.0.0.1:8000/v1/models") == []
