"""Tests for time-windowed pricing rows (time_pricing.py)."""

from __future__ import annotations

from datetime import UTC, datetime

import pytest

WEEKDAYS = ["mon", "tue", "wed", "thu", "fri"]


def _offpeak(**prices):
    from modelman.time_pricing import TimePrice, Window

    return TimePrice(
        label="off-peak",
        timezone="UTC",
        windows=[
            Window(days=WEEKDAYS, start="00:00", end="12:00"),
            Window(days=WEEKDAYS, start="18:00", end="24:00"),
            Window(days=["sat", "sun"], start="00:00", end="24:00"),
        ],
        **prices,
    )


def _cost(time_prices):
    from modelman.registry import Cost

    return Cost(
        input_price_per_million=1.32,
        cache_price_per_million=0.044,
        output_price_per_million=3.96,
        time_prices=time_prices,
    )


# 2026-09-28 is a Monday.
@pytest.mark.parametrize(
    ("at", "expected"),
    [
        (datetime(2026, 9, 28, 11, 59, tzinfo=UTC), (0.66, 0.022, 1.98)),
        (datetime(2026, 9, 28, 12, 0, tzinfo=UTC), (1.32, 0.044, 3.96)),
        (datetime(2026, 9, 28, 17, 59, 59, tzinfo=UTC), (1.32, 0.044, 3.96)),
        (datetime(2026, 9, 28, 18, 0, tzinfo=UTC), (0.66, 0.022, 1.98)),
        (datetime(2026, 10, 3, 14, 0, tzinfo=UTC), (0.66, 0.022, 1.98)),  # Saturday
    ],
)
def test_price_at_offpeak_boundaries(at, expected):
    from modelman.time_pricing import price_at

    cost = _cost(
        [
            _offpeak(
                input_price_per_million=0.66,
                cache_price_per_million=0.022,
                output_price_per_million=1.98,
            )
        ]
    )
    assert price_at(cost, at) == expected


def test_price_at_non_utc_timezone():
    from modelman.time_pricing import TimePrice, Window, price_at

    # 09:00-10:00 in Honolulu (UTC-10) is 19:00-20:00 UTC.
    tp = TimePrice(
        timezone="Pacific/Honolulu",
        windows=[Window(days=["mon"], start="09:00", end="10:00")],
        input_price_per_million=0.5,
    )
    cost = _cost([tp])
    assert price_at(cost, datetime(2026, 9, 28, 19, 30, tzinfo=UTC))[0] == 0.5
    assert price_at(cost, datetime(2026, 9, 28, 9, 30, tzinfo=UTC))[0] == 1.32


def test_price_at_per_field_fallback_to_default():
    from modelman.time_pricing import price_at

    cost = _cost([_offpeak(input_price_per_million=0.66)])  # cache/output omitted
    assert price_at(cost, datetime(2026, 10, 3, 1, 0, tzinfo=UTC)) == (0.66, 0.044, 3.96)


def test_price_at_first_matching_row_wins():
    from modelman.time_pricing import TimePrice, Window, price_at

    everything = [Window(days=["sat", "sun"], start="00:00", end="24:00")]
    first = TimePrice(timezone="UTC", windows=everything, input_price_per_million=0.1)
    second = TimePrice(timezone="UTC", windows=everything, input_price_per_million=0.2)
    cost = _cost([first, second])
    assert price_at(cost, datetime(2026, 10, 3, 1, 0, tzinfo=UTC))[0] == 0.1


def test_price_at_rejects_naive_datetime():
    from modelman.time_pricing import price_at

    with pytest.raises(ValueError, match="timezone-aware"):
        price_at(_cost([]), datetime(2026, 9, 28, 12, 0))


@pytest.mark.parametrize(
    ("kwargs", "match"),
    [
        ({"days": [], "start": "00:00", "end": "01:00"}, "non-empty"),
        ({"days": ["monday"], "start": "00:00", "end": "01:00"}, "drawn from"),
        ({"days": ["mon"], "start": "1:00", "end": "02:00"}, "HH:MM"),
        ({"days": ["mon"], "start": "24:00", "end": "24:00"}, "before 24:00"),
        ({"days": ["mon"], "start": "10:00", "end": "09:00"}, "before `end`"),
        ({"days": ["mon"], "start": "10:00", "end": "24:30"}, "HH:MM"),
    ],
)
def test_window_validation(kwargs, match):
    from modelman.time_pricing import Window

    with pytest.raises(ValueError, match=match):
        Window(**kwargs)


def test_time_price_validation():
    from modelman.time_pricing import TimePrice, Window

    w = [Window(days=["mon"], start="00:00", end="01:00")]
    with pytest.raises(ValueError, match="IANA"):
        TimePrice(timezone="Mars/Olympus", windows=w)
    with pytest.raises(ValueError, match="windows"):
        TimePrice(timezone="UTC", windows=[])
    with pytest.raises(ValueError, match="non-negative"):
        TimePrice(timezone="UTC", windows=w, input_price_per_million=-1.0)


def test_parse_and_serialize_round_trip_preserves_unknown_keys():
    from modelman.time_pricing import parse_time_prices, time_price_to_dict

    raw = [
        {
            "label": "off-peak",
            "timezone": "UTC",
            "input_price_per_million": 1,
            "note": "hand-edited",
            "windows": [{"days": ["sat"], "start": "00:00", "end": "24:00", "why": "x"}],
        }
    ]
    parsed = parse_time_prices(raw)
    assert parsed[0].input_price_per_million == 1.0
    out = time_price_to_dict(parsed[0])
    assert out["note"] == "hand-edited"
    assert out["windows"][0]["why"] == "x"
    assert "cache_price_per_million" not in out


def test_parse_time_prices_shape_errors():
    from modelman.time_pricing import parse_time_prices

    assert parse_time_prices(None) == []
    with pytest.raises(ValueError, match="array"):
        parse_time_prices({"timezone": "UTC"})
    with pytest.raises(ValueError, match=r"time_prices\[0\]"):
        parse_time_prices(["nope"])
    with pytest.raises(ValueError, match="windows"):
        parse_time_prices([{"timezone": "UTC"}])
