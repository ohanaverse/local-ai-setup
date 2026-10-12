import io
import json
import urllib.error
import urllib.request
from collections.abc import Callable
from typing import Any

import pytest
from fakes import FakeFetcher, Sleeps

from modeltiers import fetch
from modeltiers.errors import FetchError


def test_a_first_success_is_returned_without_waiting() -> None:
    fetcher = FakeFetcher({"u": [b"body"]})
    sleeps = Sleeps()
    assert fetch.fetch_with_retry(fetcher, "u", sleeps) == b"body"
    assert fetcher.calls == ["u"]
    assert sleeps.calls == []


def test_a_retryable_failure_is_retried_once_after_two_seconds() -> None:
    fetcher = FakeFetcher({"u": [FetchError("HTTP 502", retryable=True), b"body"]})
    sleeps = Sleeps()
    assert fetch.fetch_with_retry(fetcher, "u", sleeps) == b"body"
    assert fetcher.calls == ["u", "u"]
    assert sleeps.calls == [2]


def test_a_second_retryable_failure_is_raised() -> None:
    failures: list[bytes | Exception] = [
        FetchError("HTTP 502", retryable=True),
        FetchError("HTTP 503", retryable=True),
    ]
    fetcher = FakeFetcher({"u": failures})
    with pytest.raises(FetchError, match="HTTP 503"):
        fetch.fetch_with_retry(fetcher, "u", Sleeps())
    assert fetcher.calls == ["u", "u"]


def test_a_failure_that_is_not_retryable_is_raised_at_once() -> None:
    fetcher = FakeFetcher({"u": [FetchError("HTTP 404"), b"never read"]})
    sleeps = Sleeps()
    with pytest.raises(FetchError, match="HTTP 404"):
        fetch.fetch_with_retry(fetcher, "u", sleeps)
    assert fetcher.calls == ["u"]
    assert sleeps.calls == []


@pytest.mark.parametrize(
    ("function", "url"),
    [
        (fetch.fetch_modelsdev, "https://models.dev/api.json"),
        (fetch.fetch_openrouter, "https://openrouter.ai/api/v1/models"),
        (fetch.fetch_epoch, "https://epoch.ai/data/benchmark_data.zip"),
    ],
)
def test_each_single_request_source_fetches_its_url(
    function: Callable[[fetch.Fetcher, fetch.Sleep], bytes], url: str
) -> None:
    fetcher = FakeFetcher({url: [b"body"]})
    assert function(fetcher, Sleeps()) == b"body"
    assert fetcher.calls == [url]


def test_arena_page_url_names_the_agent_config_and_the_offset() -> None:
    assert fetch.arena_page_url(100) == (
        "https://datasets-server.huggingface.co/rows"
        "?dataset=lmarena-ai/leaderboard-dataset&config=agent&split=latest"
        "&offset=100&length=100"
    )


def test_arena_is_paged_to_num_rows_total_into_one_document(
    read_fixture: Callable[[str], bytes],
) -> None:
    page1 = read_fixture("arena_page1.json")  # 3 rows of 5
    page2 = read_fixture("arena_page2.json")  # 2 rows of 5
    fetcher = FakeFetcher({fetch.arena_page_url(0): [page1], fetch.arena_page_url(3): [page2]})
    doc = json.loads(fetch.fetch_arena(fetcher, Sleeps()))
    assert fetcher.calls == [fetch.arena_page_url(0), fetch.arena_page_url(3)]
    # Each page body is kept unparsed, byte for byte.
    assert doc == {"pages": [page1.decode(), page2.decode()]}


def test_an_arena_error_body_is_retried_once(read_fixture: Callable[[str], bytes]) -> None:
    error = read_fixture("arena_error.json")
    page = read_fixture("arena_too_few.json")  # 1 row of 1
    fetcher = FakeFetcher({fetch.arena_page_url(0): [error, page]})
    sleeps = Sleeps()
    doc = json.loads(fetch.fetch_arena(fetcher, sleeps))
    assert doc == {"pages": [page.decode()]}
    assert sleeps.calls == [2]


def test_two_arena_error_bodies_are_a_failed_download(
    read_fixture: Callable[[str], bytes],
) -> None:
    error = read_fixture("arena_error.json")
    fetcher = FakeFetcher({fetch.arena_page_url(0): [error, error]})
    with pytest.raises(FetchError, match="the dataset index is loading"):
        fetch.fetch_arena(fetcher, Sleeps())


@pytest.mark.parametrize(
    ("body", "message"),
    [
        (b"<html>502 Bad Gateway</html>", "not JSON"),
        (b"[]", "not a JSON object"),
        (b'{"rows": []}', "no rows list or no num_rows_total"),
        (b'{"rows": [], "num_rows_total": 5}', "an empty page at offset 0 of 5"),
    ],
)
def test_an_arena_page_in_another_shape_is_a_failed_download(body: bytes, message: str) -> None:
    fetcher = FakeFetcher({fetch.arena_page_url(0): [body]})
    with pytest.raises(FetchError, match=message):
        fetch.fetch_arena(fetcher, Sleeps())


class FakeResponse(io.BytesIO):
    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()


def _urlopen(answer: Any, seen: list[Any]) -> Callable[..., Any]:
    def urlopen(request: urllib.request.Request, timeout: float) -> Any:
        seen.append((request.full_url, request.get_header("User-agent"), timeout))
        if isinstance(answer, Exception):
            raise answer
        return FakeResponse(answer)

    return urlopen


def test_http_fetcher_reads_the_body_with_a_30_second_timeout(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    seen: list[Any] = []
    monkeypatch.setattr(urllib.request, "urlopen", _urlopen(b"body", seen))
    assert fetch.http_fetcher("https://example.test/x") == b"body"
    assert seen == [("https://example.test/x", "modeltiers/0.1", 30)]


def _http_error(code: int) -> urllib.error.HTTPError:
    return urllib.error.HTTPError("https://example.test/x", code, "status", {}, None)  # type: ignore[arg-type]


@pytest.mark.parametrize(
    ("failure", "retryable", "message"),
    [
        (_http_error(502), True, "HTTP 502"),
        (_http_error(500), True, "HTTP 500"),
        (_http_error(404), False, "HTTP 404"),
        (_http_error(429), False, "HTTP 429"),
        (TimeoutError("timed out"), True, "timed out after 30 s"),
        (urllib.error.URLError(TimeoutError("timed out")), True, "timed out"),
        (urllib.error.URLError("name not known"), False, "name not known"),
        (ConnectionResetError("reset by peer"), False, "reset by peer"),
    ],
)
def test_http_fetcher_marks_only_timeouts_and_5xx_retryable(
    monkeypatch: pytest.MonkeyPatch, failure: Exception, retryable: bool, message: str
) -> None:
    monkeypatch.setattr(urllib.request, "urlopen", _urlopen(failure, []))
    with pytest.raises(FetchError, match=message) as caught:
        fetch.http_fetcher("https://example.test/x")
    assert caught.value.retryable is retryable
