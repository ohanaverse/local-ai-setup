# `wt stats` sort order

## Summary

Make `wt stats` sort its output by agent name with the per-model `(all)`
aggregate rows first, then by model id.

## Background

`wt stats` prints one `(all)` aggregate row per model, plus one row per
observed `(agent, model)` combo. The sort should be easy to scan: all
aggregate rows first (they give a high-level model overview), then one
section per agent with models listed alphabetically within it. The
`(all)` label sorts before any real agent name because `"("` precedes
letters in ASCII.

## Design

### Code change

In `wt/cmd/wt/stats.go`, replace the `sort.SliceStable` comparator in
`buildStatsRows` with a two-level lexicographic sort by agent, then model:

```go
sort.SliceStable(rows, func(i, j int) bool {
    if rows[i].Agent != rows[j].Agent {
        return rows[i].Agent < rows[j].Agent
    }
    return rows[i].ModelID < rows[j].ModelID
})
```

This removes the explicit `(all)` special cases. `(all)` naturally sorts
first because `(` precedes `a-z`.

### Regression test

Add a test in `wt/cmd/wt/stats_test.go` that seeds events for multiple
models and agents out of order, then asserts that the rendered table
appears in the expected sorted order:

- `(all)` rows first, sorted by model id.
- Per-agent rows next, sorted by agent name, with models sorted by id
  within each agent.

### Documentation

Update `docs/wt-stats.md` to describe the new sort order and update the
example table to show multiple models under `(all)` and a real agent.

## Scope

- Only affects `wt stats` output ordering.
- No changes to the survey store, aggregation logic, CLI flags, or the
  TUI.

## Testing

- New unit test asserting sort order for multi-model, multi-agent input.
- Existing `wt/cmd/wt/stats_test.go` tests continue to pass.
- Manual check: run `wt stats` against real survey data and confirm
  `(all)` rows precede per-agent rows, and that each agent section is
  sorted by model id.
