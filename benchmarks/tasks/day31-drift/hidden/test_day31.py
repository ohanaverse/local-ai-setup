"""Hidden acceptance tests for day31-drift. Copied into the workspace only
after the agent's run has finished — never visible during the run."""

import calendar
import unittest
from datetime import date

from kettlecomb import Ledger, add_months, prorate


def _reference_add_months(d: date, n: int) -> date:
    """Independent oracle for the intended fix, deliberately not reusing
    kettlecomb.calendarlib so a still-buggy add_months can't pass by
    coincidence."""
    total = d.month - 1 + n
    year = d.year + total // 12
    month = total % 12 + 1
    day = min(d.day, calendar.monthrange(year, month)[1])
    return date(year, month, day)


class TestAddMonthsBoundaries(unittest.TestCase):
    def test_day31_anchor_into_february_non_leap(self):
        self.assertEqual(add_months(date(2025, 1, 31), 1), date(2025, 2, 28))

    def test_day31_anchor_into_february_leap(self):
        self.assertEqual(add_months(date(2024, 1, 31), 1), date(2024, 2, 29))

    def test_day31_anchor_into_30_day_month(self):
        self.assertEqual(add_months(date(2025, 1, 31), 3), date(2025, 4, 30))

    def test_december_anchor_rolls_year(self):
        self.assertEqual(add_months(date(2025, 12, 15), 1), date(2026, 1, 15))

    def test_february_29_anchor_twelve_months_later(self):
        self.assertEqual(add_months(date(2024, 2, 29), 12), date(2025, 2, 28))

    def test_eight_cycle_accumulation_from_day31_anchor_matches_independent_calc(self):
        ledger = Ledger(anchor=date(2025, 1, 31), credits_per_cycle=100.0)
        total = ledger.accrue_cycles(8)

        expected_dates = []
        current = date(2025, 1, 31)
        for _ in range(8):
            expected_dates.append(current)
            current = _reference_add_months(current, 1)
        expected_total = sum(prorate(d.day, d.year, d.month, 100.0) for d in expected_dates)

        self.assertEqual(ledger.anchor, current)
        self.assertAlmostEqual(total, expected_total, places=6)
        self.assertAlmostEqual(sum(ledger.history), expected_total, places=6)


if __name__ == "__main__":
    unittest.main()
