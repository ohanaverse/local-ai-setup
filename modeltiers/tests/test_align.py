import pytest

from modeltiers import align
from modeltiers.align import Aligned, Alignment, Fit

ANCHOR = "intelligence_index"


def _keys(**columns: list[float | None]) -> dict[str, dict[str, float]]:
    """One key per position, with the score each column gives it."""
    length = len(next(iter(columns.values())))
    return {
        f"k{i}": {name: values[i] for name, values in columns.items() if values[i] is not None}  # type: ignore[misc]
        for i in range(length)
    }


def test_a_line_through_exact_points_is_found_and_misses_nothing() -> None:
    # eci = 100 + 2 * index, so index = eci / 2 - 50.
    scores = _keys(intelligence_index=[10.0, 20.0, 30.0, 40.0], eci=[120.0, 140.0, 160.0, 180.0])
    alignment = align.fit_all(scores, min_shared=4)
    fit = alignment.fits["eci"]
    assert (fit.signal, fit.shared) == ("eci", 4)
    assert fit.slope == pytest.approx(0.5)
    assert fit.intercept == pytest.approx(-50.0)
    assert fit.typical_miss == pytest.approx(0.0)
    assert fit.translate(150.0) == pytest.approx(25.0)
    assert alignment.refused == {}


def test_the_typical_miss_is_the_root_mean_square_of_what_the_line_leaves() -> None:
    # A flat-ish cloud: the least-squares line through (0,0) (1,2) (2,1) (3,3).
    scores = _keys(intelligence_index=[0.0, 2.0, 1.0, 3.0], arena_agent=[0.0, 1.0, 2.0, 3.0])
    fit = align.fit_all(scores, min_shared=4).fits["arena_agent"]
    assert fit.slope == pytest.approx(0.8)
    assert fit.intercept == pytest.approx(0.3)
    # Residuals -0.3, 0.9, -0.9, 0.3: mean square 0.45.
    assert fit.typical_miss == pytest.approx(0.45**0.5)


def test_only_keys_with_both_scores_are_fitted_on() -> None:
    scores = _keys(
        intelligence_index=[10.0, 20.0, 30.0, None, 50.0],
        eci=[120.0, 140.0, 160.0, 999.0, None],
    )
    fit = align.fit_all(scores, min_shared=3).fits["eci"]
    assert fit.shared == 3
    assert fit.slope == pytest.approx(0.5)


def test_too_few_shared_keys_is_refused_with_the_count() -> None:
    scores = _keys(intelligence_index=[10.0, 20.0, 30.0], eci=[120.0, 140.0, 160.0])
    alignment = align.fit_all(scores, min_shared=4)
    assert alignment.fits == {}
    assert alignment.refused == {
        "eci": "3 models have both it and intelligence_index, under the minimum of 4"
    }
    # The default minimum is the design's.
    assert align.MIN_SHARED == 20
    assert align.fit_all(scores).fits == {}


def test_a_source_that_ranks_the_other_way_is_refused() -> None:
    scores = _keys(intelligence_index=[40.0, 30.0, 20.0, 10.0], eci=[120.0, 140.0, 160.0, 180.0])
    alignment = align.fit_all(scores, min_shared=4)
    assert alignment.fits == {}
    assert alignment.refused == {"eci": "it ranks the 4 shared models against intelligence_index"}


@pytest.mark.parametrize(
    ("index", "eci"),
    [
        ([10.0, 20.0, 30.0, 40.0], [150.0, 150.0, 150.0, 150.0]),  # nothing to fit on
        ([25.0, 25.0, 25.0, 25.0], [120.0, 140.0, 160.0, 180.0]),  # a flat line
    ],
)
def test_scores_that_do_not_vary_are_refused(index: list[float], eci: list[float]) -> None:
    alignment = align.fit_all(_keys(intelligence_index=index, eci=eci), min_shared=4)
    assert alignment.fits == {}
    assert alignment.refused == {
        "eci": "the 4 shared models do not vary in it or in intelligence_index"
    }


def test_each_signal_is_fitted_on_its_own_and_the_anchor_is_not() -> None:
    scores = _keys(
        intelligence_index=[10.0, 20.0, 30.0, 40.0],
        arena_agent=[0.1, 0.2, 0.3, None],
        eci=[120.0, 140.0, 160.0, 180.0],
    )
    alignment = align.fit_all(scores, min_shared=4)
    assert list(alignment.fits) == ["eci"]
    assert list(alignment.refused) == ["arena_agent"]
    assert ANCHOR not in alignment.fits
    assert ANCHOR not in alignment.refused


def test_a_signal_no_key_has_is_neither_fitted_nor_refused() -> None:
    alignment = align.fit_all(_keys(intelligence_index=[10.0, 20.0]), min_shared=2)
    assert (alignment.fits, alignment.refused) == ({}, {})


FITS = Alignment(
    fits={
        "arena_agent": Fit("arena_agent", slope=100.0, intercept=40.0, shared=30, typical_miss=3.0),
        "eci": Fit("eci", slope=0.5, intercept=-50.0, shared=90, typical_miss=4.0),
    },
    refused={},
)


def test_the_aligned_score_is_the_mean_of_the_index_and_each_translated_score() -> None:
    found = align.aligned({"intelligence_index": 44.0, "arena_agent": 0.02, "eci": 190.0}, FITS)
    # 44, 100 * 0.02 + 40 = 42, 190 / 2 - 50 = 45: mean 43.666..., one decimal.
    assert found == Aligned(
        score=43.7,
        sources=("intelligence_index", "arena_agent", "eci"),
        parts={"intelligence_index": 44.0, "arena_agent": 42.0, "eci": 45.0},
    )


def test_a_model_is_aligned_on_whatever_it_has() -> None:
    assert align.aligned({"intelligence_index": 33.7}, FITS) == Aligned(
        score=33.7, sources=("intelligence_index",), parts={"intelligence_index": 33.7}
    )
    arena_only = align.aligned({"arena_agent": -0.05}, FITS)
    assert arena_only is not None
    assert (arena_only.score, arena_only.sources) == (35.0, ("arena_agent",))
    # The scores that do not vote are not part of it.
    with_coding = align.aligned({"intelligence_index": 33.7, "coding_index": 90.0}, FITS)
    assert with_coding is not None
    assert with_coding.sources == ("intelligence_index",)


def test_a_score_whose_source_was_not_fitted_adds_nothing() -> None:
    only_eci = Alignment(fits={"eci": FITS.fits["eci"]}, refused={"arena_agent": "too few"})
    found = align.aligned({"intelligence_index": 44.0, "arena_agent": 0.02}, only_eci)
    assert found is not None
    assert (found.score, found.sources) == (44.0, ("intelligence_index",))
    # With nothing usable there is no score at all.
    assert align.aligned({"arena_agent": 0.02}, only_eci) is None
    assert align.aligned({}, FITS) is None
