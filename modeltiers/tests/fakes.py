"""Fakes for the seams: a Fetcher that answers from a queue, and a sleep that
only records."""


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
