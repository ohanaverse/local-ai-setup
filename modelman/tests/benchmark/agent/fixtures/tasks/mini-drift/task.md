# Off-by-one in `pkg.add_one`

`pkg.add_one(n)` is supposed to return `n + 1`. Callers report it sometimes
returns `n` unchanged. Find and fix the bug; add a regression test.
