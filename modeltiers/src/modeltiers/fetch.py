"""Downloads. One function per source, each behind the `Fetcher` seam.

A `Fetcher` takes a URL and returns the body, or raises FetchError. Tests pass
a fake; the CLI passes `http_fetcher`. Nothing here parses a source beyond
what paging Arena needs.
"""

import http.client
import json
import urllib.error
import urllib.request
from collections.abc import Callable

from modeltiers.errors import FetchError

Fetcher = Callable[[str], bytes]
Sleep = Callable[[float], None]
# Takes a URL and returns the address its redirects end at, or raises FetchError.
Resolver = Callable[[str], str]
# Raises FetchError(retryable=True) when a body that arrived is a failed download,
# and reads the body on the way: `fetch_checked` returns what it read.
type BodyCheck[T] = Callable[[bytes], T]

TIMEOUT_SECONDS = 30
RESOLVE_TIMEOUT_SECONDS = 10
RETRY_DELAY_SECONDS = 2
USER_AGENT = "modeltiers/0.1"

MODELSDEV_URL = "https://models.dev/api.json"
OPENROUTER_URL = "https://openrouter.ai/api/v1/models"
EPOCH_URL = "https://epoch.ai/data/benchmark_data.zip"
ARENA_PAGE_LENGTH = 100
ARENA_URL = (
    "https://datasets-server.huggingface.co/rows"
    "?dataset=lmarena-ai/leaderboard-dataset&config=agent&split=latest"
    "&offset={offset}&length={length}"
)


def http_fetcher(url: str) -> bytes:
    """GET `url` with a 30-second timeout. A timeout, a 5xx status or a body
    cut short is retryable; any other failure is not."""
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            body: bytes = response.read()
            return body
    except urllib.error.HTTPError as exc:
        raise FetchError(f"{url}: HTTP {exc.code}", retryable=500 <= exc.code < 600) from exc
    except TimeoutError as exc:
        raise FetchError(f"{url}: timed out after {TIMEOUT_SECONDS} s", retryable=True) from exc
    except urllib.error.URLError as exc:
        timed_out = isinstance(exc.reason, TimeoutError)
        raise FetchError(f"{url}: {exc.reason}", retryable=timed_out) from exc
    except http.client.HTTPException as exc:
        # A body cut short or a garbled status line: urllib passes these through raw.
        raise FetchError(f"{url}: {exc!r}", retryable=True) from exc
    except OSError as exc:
        raise FetchError(f"{url}: {exc}") from exc


def http_resolver(url: str) -> str:
    """Follow `url`'s redirects and return the address they end at. The body
    is never read. One attempt with a 10-second timeout: a vendor page that
    does not answer keeps the address it had."""
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=RESOLVE_TIMEOUT_SECONDS) as response:
            final: str = response.geturl()
            return final
    except urllib.error.HTTPError as exc:
        raise FetchError(f"{url}: HTTP {exc.code}") from exc
    # ValueError: a redirect to a host the resolver cannot encode (an empty or
    # over-long label) fails inside urllib as a UnicodeError, not an OSError.
    except (urllib.error.URLError, http.client.HTTPException, OSError, ValueError) as exc:
        raise FetchError(f"{url}: {exc}") from exc


def _retrying[T](attempt: Callable[[], T], sleep: Sleep) -> T:
    """`attempt`, tried once more after 2 seconds if it raised a FetchError
    with `retryable` set. Any other failure reaches the caller as it is."""
    try:
        return attempt()
    except FetchError as exc:
        if not exc.retryable:
            raise
    sleep(RETRY_DELAY_SECONDS)
    return attempt()


def fetch_with_retry(fetcher: Fetcher, url: str, sleep: Sleep) -> bytes:
    """Fetch `url`; after a retryable failure wait 2 seconds and try once more.
    For a body the caller can judge only by reading it, `fetch_checked` is the
    same call with the reading kept."""
    return _retrying(lambda: fetcher(url), sleep)


def fetch_checked[T](
    fetcher: Fetcher, url: str, sleep: Sleep, check: BodyCheck[T]
) -> tuple[bytes, T]:
    """Fetch `url` and what `check` made of the body, parsing it once.

    For a source that has to parse a body to know whether it downloaded at all:
    the parse is the retry check, so it belongs inside the retry, and its answer
    belongs to the caller rather than being worked out a second time."""

    def attempt() -> tuple[bytes, T]:
        body = fetcher(url)
        return body, check(body)

    return _retrying(attempt, sleep)


def fetch_modelsdev(fetcher: Fetcher, sleep: Sleep) -> bytes:
    return fetch_with_retry(fetcher, MODELSDEV_URL, sleep)


def fetch_openrouter(fetcher: Fetcher, sleep: Sleep) -> bytes:
    return fetch_with_retry(fetcher, OPENROUTER_URL, sleep)


def fetch_epoch(fetcher: Fetcher, sleep: Sleep) -> bytes:
    return fetch_with_retry(fetcher, EPOCH_URL, sleep)


def arena_page_url(offset: int) -> str:
    return ARENA_URL.format(offset=offset, length=ARENA_PAGE_LENGTH)


def _arena_page(body: bytes) -> tuple[int, int]:
    """(rows on this page, num_rows_total). An `error` body is retryable."""
    try:
        page = json.loads(body)
    except ValueError as exc:
        raise FetchError("arena: a page is not JSON") from exc
    if not isinstance(page, dict):
        raise FetchError("arena: a page is not a JSON object")
    if "error" in page:
        raise FetchError(
            f"arena: the rows API answered with an error: {page['error']}", retryable=True
        )
    rows = page.get("rows")
    total = page.get("num_rows_total")
    if not isinstance(rows, list) or not isinstance(total, int) or isinstance(total, bool):
        raise FetchError("arena: a page has no rows list or no num_rows_total")
    return len(rows), total


def wrap_arena_pages(pages: list[bytes]) -> bytes:
    """The one document an Arena snapshot is: each page body, unparsed."""
    doc = {"pages": [page.decode("utf-8") for page in pages]}
    return json.dumps(doc, indent=1).encode("utf-8")


def fetch_arena(fetcher: Fetcher, sleep: Sleep) -> bytes:
    """Page the rows API until `num_rows_total` rows are read, and return the
    pages as one wrapper document (`wrap_arena_pages`).

    `num_rows_total` is read from the first page and every later page must
    report the same one. A leaderboard being edited while its pages are read
    would otherwise raise the target on each request and page forever, and the
    parse's row count check (`len(records) != total`) would compare a count
    taken across the pages with a total from a different moment. Each page is
    parsed once, by its own retry check.
    """
    pages: list[bytes] = []
    offset = 0
    total: int | None = None
    while True:
        url = arena_page_url(offset)
        body, (rows, page_total) = fetch_checked(fetcher, url, sleep, _arena_page)
        if total is None:
            total = page_total
        elif page_total != total:
            raise FetchError(
                f"arena: num_rows_total moved from {total} to {page_total} while paging"
            )
        pages.append(body)
        offset += rows
        if offset >= total:
            return wrap_arena_pages(pages)
        if rows == 0:
            raise FetchError(f"arena: an empty page at offset {offset} of {total} rows")
