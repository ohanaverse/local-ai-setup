"""Baseline regression tests for kettlecomb, shipped with the task.

Every fixture here uses a day-15 billing anchor, which never touches the
add_months day-clamping edge case — these tests pass on both the buggy and
a fixed calendarlib, and exist only to prove an agent's fix didn't break
anything unrelated (spec gate 5, VISIBLE_REGRESSION).
"""

import unittest
from datetime import date

from kettlecomb import Ledger, add_months, month_length, prorate


class TestMonthLength(unittest.TestCase):
    def test_month_length_31_day_month(self):
        self.assertEqual(month_length(2025, 1), 31)

    def test_month_length_30_day_month(self):
        self.assertEqual(month_length(2025, 4), 30)


class TestAddMonthsMidMonth(unittest.TestCase):
    def test_add_one_month_mid_month(self):
        self.assertEqual(add_months(date(2025, 3, 15), 1), date(2025, 4, 15))

    def test_add_months_rolls_year(self):
        self.assertEqual(add_months(date(2025, 11, 15), 3), date(2026, 2, 15))


class TestProrate(unittest.TestCase):
    def test_full_month_from_day_one(self):
        self.assertAlmostEqual(prorate(1, 2025, 3, 100.0), 100.0, places=4)

    def test_mid_month_anchor_prorates_partial(self):
        result = prorate(15, 2025, 3, 100.0)
        self.assertAlmostEqual(result, 100.0 * (31 - 15 + 1) / 31, places=4)


class TestLedgerAccrual(unittest.TestCase):
    def test_accrue_three_cycles_from_day_15_anchor(self):
        ledger = Ledger(anchor=date(2025, 3, 15), credits_per_cycle=100.0)
        total = ledger.accrue_cycles(3)
        self.assertEqual(ledger.anchor, date(2025, 6, 15))
        self.assertGreater(total, 0)
        self.assertEqual(len(ledger.history), 3)


if __name__ == "__main__":
    unittest.main()
