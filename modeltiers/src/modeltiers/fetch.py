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
# Raises FetchError(retryable=True) when a body that arrived is a failed download.
BodyCheck = Callable[[bytes], None]

TIMEOUT_SECONDS = 30
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


def fetch_with_retry(
    fetcher: Fetcher, url: str, sleep: Sleep, check: BodyCheck | None = None
) -> bytes:
    """Fetch `url`; after a retryable failure wait 2 seconds and try once more."""
    for attempt in (1, 2):
        try:
            body = fetcher(url)
            if check is not None:
                check(body)
            return body
        except FetchError as exc:
            if attempt == 2 or not exc.retryable:
                raise
            sleep(RETRY_DELAY_SECONDS)
    raise AssertionError("unreachable")  # pragma: no cover


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


def _check_arena_page(body: bytes) -> None:
    _arena_page(body)


def wrap_arena_pages(pages: list[bytes]) -> bytes:
    """The one document an Arena snapshot is: each page body, unparsed."""
    doc = {"pages": [page.decode("utf-8") for page in pages]}
    return json.dumps(doc, indent=1).encode("utf-8")


def fetch_arena(fetcher: Fetcher, sleep: Sleep) -> bytes:
    """Page the rows API until `num_rows_total` rows are read, and return the
    pages as one wrapper document (`wrap_arena_pages`)."""
    pages: list[bytes] = []
    offset = 0
    while True:
        body = fetch_with_retry(fetcher, arena_page_url(offset), sleep, _check_arena_page)
        rows, total = _arena_page(body)
        pages.append(body)
        offset += rows
        if offset >= total:
            return wrap_arena_pages(pages)
        if rows == 0:
            raise FetchError(f"arena: an empty page at offset {offset} of {total} rows")
