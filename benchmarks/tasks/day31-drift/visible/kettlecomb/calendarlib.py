"""Calendar arithmetic for kettlecomb billing cycles."""

from __future__ import annotations

from datetime import date


def month_length(year: int, month: int) -> int:
    """Number of days in year-month, accounting for leap years."""
    if month == 2:
        is_leap = year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
        return 29 if is_leap else 28
    if month in (4, 6, 9, 11):
        return 30
    return 31


def add_months(d: date, n: int) -> date:
    """Advance d by n months, keeping the same day-of-month where valid."""
    total = d.month - 1 + n
    year = d.year + total // 12
    month = total % 12 + 1
    try:
        return date(year, month, d.day)
    except ValueError:
        return date(year, month, 30)
