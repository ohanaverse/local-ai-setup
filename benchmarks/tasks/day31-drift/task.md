# Bug: prorated credits drift for day-30/day-31 billing anchors

kettlecomb bills metered seats on a monthly cycle. Each cycle,
`Ledger.accrue_cycles` computes a prorated credit amount via
`billing.prorate` and advances the account's billing anchor to the next
month.

Customers whose billing anchor falls on day 30 or 31 are reporting that
their prorated credits are wrong, and it gets worse the longer the account
has been active. Several day-31 accounts have also reported their invoice
date silently creeping earlier in the month over time, especially for
accounts that have been through February.

Find the root cause and fix it. Add a regression test that would have
caught the bug.
