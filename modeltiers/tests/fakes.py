"""Fakes for the seams: a Fetcher that answers from a queue, a Resolver that
follows no redirect unless told to, and a sleep that only records."""

from modeltiers.errors import FetchError


class FakeFetcher:
    """Answers each URL from a queue of bodies or exceptions, in order."""

    def __init__(self, answers: dict[str, list[bytes | Exception]]) -> None:
        self.answers = {url: list(queue) for url, queue in answers.items()}
        self.calls: list[str] = []

    def __call__(self, url: str) -> bytes:
        self.calls.append(url)
        answer = self.answers[url].pop(0)
        if isinstance(answer, Exception):
            raise answer
        return answer


class Sleeps:
    def __init__(self) -> None:
        self.calls: list[float] = []

    def __call__(self, seconds: float) -> None:
        self.calls.append(seconds)


class FakeResolver:
    """Ends each address where `moves` says, or where it started; an address in
    `down` fails the way an unreachable page does."""

    def __init__(self, moves: dict[str, str] | None = None, down: frozenset[str] = frozenset()):
        self.moves = moves or {}
        self.down = down
        self.calls: list[str] = []

    def __call__(self, url: str) -> str:
        self.calls.append(url)
        if url in self.down:
            raise FetchError(f"{url}: HTTP 503")
        return self.moves.get(url, url)
