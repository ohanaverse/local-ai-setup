"""Metered-seat credit ledger for kettlecomb."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date

from .billing import prorate
from .calendarlib import add_months


@dataclass
class Ledger:
    anchor: date
    credits_per_cycle: float
    history: list[float] = field(default_factory=list)

    def accrue_cycles(self, n: int) -> float:
        """Accrue n billing cycles, advancing the anchor by one month each
        time. Returns total credits earned across all n cycles."""
        total = 0.0
        current = self.anchor
        for _ in range(n):
            credits = prorate(current.day, current.year, current.month, self.credits_per_cycle)
            self.history.append(credits)
            total += credits
            current = add_months(current, 1)
        self.anchor = current
        return total
