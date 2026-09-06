# kettlecomb

An invented metered-seat credit ledger, used only as a benchmark fixture.
Bills a seat on a monthly cycle from a per-account billing anchor date.

- `calendarlib.py` — month-length and month-arithmetic helpers.
- `billing.py` — prorates a cycle's credits from the anchor day and month length.
- `ledger.py` — accrues N billing cycles, advancing the anchor by one month each time.
