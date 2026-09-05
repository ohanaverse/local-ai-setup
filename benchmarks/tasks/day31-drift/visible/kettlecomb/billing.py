"""Billing calculations for kettlecomb seat credits."""

from __future__ import annotations

from .calendarlib import month_length


def prorate(anchor_day: int, year: int, month: int, credits_per_cycle: float) -> float:
    """Fraction of credits_per_cycle earned for a cycle starting at
    anchor_day in year-month, prorated by the month's actual length."""
    length = month_length(year, month)
    effective_day = min(anchor_day, length)
    days_active = length - effective_day + 1
    return round(credits_per_cycle * days_active / length, 4)
