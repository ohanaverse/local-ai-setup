"""The aligned score: every voting score of a model put on one scale, the
intelligence index's, and averaged.

The three signals are on scales of their own and each source scores a
different set of models, so neither the raw scores nor a percentile within a
source compares two models that different sources scored. Where two sources
score the same models they agree closely on the order, so each other signal
is translated to index points by a straight line fitted over the models it
shares with the index, once per run. The score is advisory and decides
nothing: no tier, gate or sentinel reads it.
"""

from collections.abc import Mapping
from dataclasses import dataclass
from math import isfinite
from statistics import StatisticsError, fmean, linear_regression

from modeltiers.contracts import SIGNALS

ANCHOR = "intelligence_index"  # the scale every other signal is translated to
MIN_SHARED = 20  # the fewest shared models a line is fitted on
DECIMALS = 1  # the fit misses by whole points; more places would claim more


@dataclass(frozen=True)
class Fit:
    """One signal's line: `ANCHOR` is about `slope * score + intercept`,
    within the index values this run's models have."""

    signal: str
    slope: float
    intercept: float
    shared: int  # the models the line was fitted on
    typical_miss: float  # root mean square of what the line leaves, in index points
    floor: float  # the lowest index any model of the run has
    ceiling: float  # the highest

    def translate(self, score: float) -> float:
        """The score in index points, never under `floor` or over `ceiling`.
        The sources do not stay on one line at the ends of the scale, so a
        line read far enough gives an index no model has: a weak model's
        Epoch score comes out below zero."""
        return min(max(self.slope * score + self.intercept, self.floor), self.ceiling)


@dataclass(frozen=True)
class Alignment:
    """This run's fits. A signal some model has, and no line was fitted for,
    is in `refused` with the reason; its scores then add to no aligned score."""

    fits: Mapping[str, Fit]
    refused: Mapping[str, str]


@dataclass(frozen=True)
class Aligned:
    """One model's aligned score, and what it rests on."""

    score: float  # index points, to DECIMALS places
    sources: tuple[str, ...]  # the signals averaged, in SIGNALS order
    parts: Mapping[str, float]  # signal -> its estimate in index points, unrounded


def _fit(
    signal: str, pairs: list[tuple[float, float]], min_shared: int, floor: float, ceiling: float
) -> Fit | str:
    """The least-squares line over (signal score, anchor score) pairs, or why
    there is none. `floor` and `ceiling` bound what the line may answer."""
    count = len(pairs)
    if count < min_shared:
        return f"{count} models have both it and {ANCHOR}, under the minimum of {min_shared}"
    scores = [score for score, _ in pairs]
    anchors = [anchor for _, anchor in pairs]
    if len(set(scores)) == 1 or len(set(anchors)) == 1:
        return f"the {count} shared models do not vary in it or in {ANCHOR}"
    unfit = f"no finite line fits the {count} shared models"
    try:
        slope, intercept = linear_regression(scores, anchors)
        misses = [anchor - (slope * score + intercept) for score, anchor in pairs]
        typical_miss = fmean(miss * miss for miss in misses) ** 0.5
    except (StatisticsError, OverflowError):
        # Scores that differ by less than the arithmetic can tell, or by more.
        return unfit
    if not (isfinite(slope) and isfinite(intercept) and isfinite(typical_miss)):
        return unfit
    if slope <= 0:
        return f"it ranks the {count} shared models against {ANCHOR}"
    return Fit(
        signal=signal,
        slope=slope,
        intercept=intercept,
        shared=count,
        typical_miss=typical_miss,
        floor=floor,
        ceiling=ceiling,
    )


def fit_all(
    scores: Mapping[str, Mapping[str, float]], *, min_shared: int = MIN_SHARED
) -> Alignment:
    """A line for every signal but the anchor, over the keys that have both.
    `scores` maps a key to its score per signal. A signal no key has is
    neither fitted nor refused."""
    fits: dict[str, Fit] = {}
    refused: dict[str, str] = {}
    every = [found[ANCHOR] for found in scores.values() if ANCHOR in found]
    for signal in SIGNALS:
        if signal == ANCHOR or not any(signal in found for found in scores.values()):
            continue
        pairs = [
            (found[signal], found[ANCHOR])
            for found in scores.values()
            if signal in found and ANCHOR in found
        ]
        # With no index at all there are no pairs, and the fit is refused.
        result = _fit(signal, pairs, min_shared, min(every, default=0.0), max(every, default=0.0))
        if isinstance(result, Fit):
            fits[signal] = result
        else:
            refused[signal] = result
    return Alignment(fits=fits, refused=refused)


def aligned(scores: Mapping[str, float], alignment: Alignment) -> Aligned | None:
    """The mean of the model's index and of each of its other voting scores
    translated to index points, with equal weight. None when it has no index
    and no score a line was fitted for."""
    parts: dict[str, float] = {}
    for signal in SIGNALS:
        if signal not in scores:
            continue
        if signal == ANCHOR:
            parts[signal] = scores[signal]
        elif signal in alignment.fits:
            parts[signal] = alignment.fits[signal].translate(scores[signal])
    if not parts:
        return None
    return Aligned(score=round(fmean(parts.values()), DECIMALS), sources=tuple(parts), parts=parts)
