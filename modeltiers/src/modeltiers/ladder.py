"""What `ladder` prints: one signal's resolved scores between two bounds,
highest first, with the gap under each. A floor is moved to the midpoint of
the widest gap, so the gaps are what a re-calibration reads."""

from collections.abc import Iterable
from math import inf

from modeltiers.contracts import CI_LOWER, SIGNALS
from modeltiers.identity import Match

# Every signal, and the lower bound of its interval.
CHOICES = tuple(key for name in SIGNALS for key in (name, f"{name}{CI_LOWER}"))


def ladder_lines(
    matches: Iterable[Match], *, lower_bound: bool, low: float = -inf, high: float = inf
) -> list[str]:
    """One line per key whose score is from `low` to `high`, both included:
    the score, the gap to the next score down and that gap's midpoint. The
    last line has no gap. `matches` is the best record of every key, which is
    what votes; with `lower_bound` its interval's lower bound is placed, and a
    record without one is not a rung."""
    scores = ((m.record.ci_lower if lower_bound else m.record.score, m.canonical) for m in matches)
    window = sorted(
        ((score, key) for score, key in scores if score is not None and low <= score <= high),
        reverse=True,
    )
    lines = [
        f"{score:9.4f}  gap below {score - below:7.4f}  midpoint {(score + below) / 2:9.4f}  {key}"
        for (score, key), (below, _) in zip(window, window[1:], strict=False)
    ]
    lines.extend(f"{score:9.4f}  {key}" for score, key in window[-1:])
    return lines
