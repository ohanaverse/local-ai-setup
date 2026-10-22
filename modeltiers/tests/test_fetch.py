import http.client
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


def test_a_check_that_reads_the_body_gives_its_answer_back_reading_it_once() -> None:
    fetcher = FakeFetcher({"u": [b"body"]})
    reads: list[bytes] = []

    def check(body: bytes) -> str:
        reads.append(body)
        return body.decode().upper()

    assert fetch.fetch_checked(fetcher, "u", Sleeps(), check) == (b"body", "BODY")
    assert reads == [b"body"]


def test_a_check_that_reads_a_body_is_retried_on_a_retryable_failure() -> None:
    """The whole point of a check: a body that arrived but is a failed download
    is worth one more request. The answer comes from the body that was kept."""
    fetcher = FakeFetcher({"u": [b"502", b"good"]})

    def check(body: bytes) -> str:
        if body == b"502":
            raise FetchError("u: 502", retryable=True)
        return body.decode()

    sleeps = Sleeps()
    assert fetch.fetch_checked(fetcher, "u", sleeps, check) == (b"good", "good")
    assert fetcher.calls == ["u", "u"]
    assert sleeps.calls == [2]


def test_a_check_error_raised_twice_reaches_the_caller() -> None:
    fetcher = FakeFetcher({"u": [b"502", b"502"]})

    def check(body: bytes) -> str:
        raise FetchError("u: 502", retryable=True)

    with pytest.raises(FetchError, match="502"):
        fetch.fetch_checked(fetcher, "u", Sleeps(), check)
    assert fetcher.calls == ["u", "u"]


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


def test_every_arena_page_is_parsed_exactly_once(
    read_fixture: Callable[[str], bytes], monkeypatch: pytest.MonkeyPatch
) -> None:
    """Parsing a page is the check that makes a bad one retryable, so it happens
    inside the retry; the caller gets that same reading rather than taking the
    body apart a second time. Paging a leaderboard of 5,000 rows means 50
    downloads, and doubling their parse is the largest cost in the Arena fetch."""
    page1 = read_fixture("arena_page1.json")  # 3 rows of 5
    page2 = read_fixture("arena_page2.json")  # 2 rows of 5
    read: list[bytes] = []
    parse = fetch._arena_page

    def counting(body: bytes) -> tuple[int, int]:
        read.append(body)
        return parse(body)

    monkeypatch.setattr(fetch, "_arena_page", counting)
    fetcher = FakeFetcher({fetch.arena_page_url(0): [page1], fetch.arena_page_url(3): [page2]})
    assert json.loads(fetch.fetch_arena(fetcher, Sleeps())) == {
        "pages": [page1.decode(), page2.decode()]
    }
    assert read == [page1, page2]


def test_two_arena_error_bodies_are_a_failed_download(
    read_fixture: Callable[[str], bytes],
) -> None:
    error = read_fixture("arena_error.json")
    fetcher = FakeFetcher({fetch.arena_page_url(0): [error, error]})
    with pytest.raises(FetchError, match="the dataset index is loading"):
        fetch.fetch_arena(fetcher, Sleeps())


def test_an_arena_total_that_moves_while_paging_is_a_failed_download(
    read_fixture: Callable[[str], bytes],
) -> None:
    """A leaderboard edited between two page requests must not page forever."""
    page1 = read_fixture("arena_page1.json")  # 3 rows of 6
    grown = json.dumps({"rows": [{"row": {"model_name": "M", "score": 1.0}}], "num_rows_total": 7})
    fetcher = FakeFetcher(
        {fetch.arena_page_url(0): [page1], fetch.arena_page_url(3): [grown.encode()]}
    )
    with pytest.raises(FetchError, match="num_rows_total moved from 6 to 7"):
        fetch.fetch_arena(fetcher, Sleeps())
    assert fetcher.calls == [fetch.arena_page_url(0), fetch.arena_page_url(3)]


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


class _DroppedResponse(FakeResponse):
    def read(self) -> bytes:
        raise http.client.IncompleteRead(b"1234567890", 990)


def test_http_fetcher_makes_a_body_cut_short_a_retryable_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def urlopen(request: urllib.request.Request, timeout: float) -> Any:
        return _DroppedResponse(b"")

    monkeypatch.setattr(urllib.request, "urlopen", urlopen)
    with pytest.raises(FetchError, match="IncompleteRead") as caught:
        fetch.http_fetcher("https://example.test/x")
    assert caught.value.retryable is True
