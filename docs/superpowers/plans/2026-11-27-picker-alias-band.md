# Picker alias band Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sort alias rows into their own band in the model picker, right after the slot profiles and ahead of the running local models, ordered by tier score.

**Architecture:** `sortRows` in `wt/internal/tui/modelrows.go` gains a fifth band (native 0, profile 1, **alias 2**, running local 3, other 4). An alias row is recognised by its wire `Kind`. Inside the band rows sort by their own score only (no family borrowing). Nothing else moves: rotation, the cursor, the catalog and the wire types stay as they are.

**Tech Stack:** Go 1.26 (module `wt/`), Bubble Tea picker, `go test`.

**Spec:** `docs/superpowers/specs/2026-11-27-picker-alias-band-design.md`

## Global Constraints

- Order, top to bottom: native (alphabetical by id), slot profiles (alphabetical by id), **aliases (tier score descending)**, running local models, every other model (both by score, current rules).
- An alias row is identified by `Wire.Kind == modelapi.KindAlias`, the test `catalog.RegisteredOrdered` uses; not by the id prefix.
- Inside the alias band: own `tier.Score()` descending, a row with no score after every scored one, ties and unscored rows by id. **No family borrowing** (every alias has family `alias`).
- A blocked or unrouted alias stays in the band; the alias case is tested before the running-local case.
- The default selection does not change in this PR: do not touch rotation, `models_client.go`'s cursor code, `RegisteredOrdered`, the non-TUI launch, the server catalog or any wire type.
- Every `Test*` carries a top-level `//` comment saying what it tests and why (`wt/CLAUDE.md`). Run Go from `wt/`.
- Gate: `make pre-pr` from the repo root, run with `run_in_background`, exit read from the log. Push nothing until it is green.
- Commits end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`. Work stays in this worktree; never commit in the primary checkout.

## Review Focus

- A scored alias sits above an unscored one, and the unscored one does not borrow the best alias score through the shared family `alias` (`TestSortRowsAliasesByScoreThenID`).
- With no `tiers.toml` every alias is unscored: they sort by id (`TestSortRowsUnscoredAliasesSortByID`).
- A running local alias is in the alias band, below a profile and above a running local model with a far higher score (`TestSortRowsAliasBandIgnoresStateAndOtherScores`).
- A blocked alias keeps its place by score (same test).
- Detection works on rows built the real way, `wireTableRows` over catalog rows, not only on hand-built ones (`TestWireTableRowsSortsAnAliasIntoItsBand`).

---

### Task 1: The alias band in `sortRows`

**Files:**
- Modify: `wt/internal/tui/modelrows.go` (`sortRows`, its doc comment)
- Test: `wt/internal/tui/modelrows_test.go`, `wt/internal/tui/slot_row_test.go`, `wt/internal/tui/models_client_test.go`

**Interfaces:**
- Consumes: `catalog.Row.Wire *modelapi.Row`, `modelapi.KindAlias`, `tableRow.tier.Score() (int, bool)`.
- Produces: `func isAlias(r tableRow) bool` in `modelrows.go` (true when `r.Wire != nil && r.Wire.Kind == modelapi.KindAlias`); the new band numbering. Test helper `sortAlias` in `modelrows_test.go`.

- [ ] **Step 1: Add the test helper and the new tests (failing)**

In `modelrows_test.go` add the `modelapi` import and, beside `sortProfile`:

```go
// sortAlias is an alias row as the server's catalog gives it: wire Kind
// alias, family "alias", at loc, with the given score.
func sortAlias(id string, loc config.Location, running bool, score float64) tableRow {
	return tableRow{
		Row: catalog.Row{
			Location: loc, Running: running,
			Model: config.Model{ID: id, Family: config.AliasProvider},
			Wire:  &modelapi.Row{ID: id, Kind: modelapi.KindAlias},
		},
		tier: sortEntry(score),
	}
}
```

Add these tests (each with the required `//` comment):

- `TestSortRowsAliasesByScoreThenID`: rows `alias/low` 20, `alias/top` 70, `alias/tie-b` 45, `alias/tie-a` 45, `alias/none-b` -1, `alias/none-a` -1 (all cloud); after `sortRows`, `assertOrder(t, rows, "alias/top,alias/tie-a,alias/tie-b,alias/low,alias/none-a,alias/none-b")`.
- `TestSortRowsUnscoredAliasesSortByID`: `alias/b`, `alias/a`, both -1; order `alias/a,alias/b`.
- `TestSortRowsAliasBandIgnoresStateAndOtherScores`: `sortProfile(0, "p")`, `sortAlias("alias/run", config.LocationLocal, true, 10)`, `sortAlias("alias/blocked", config.LocationCloud, false, 50)` with `r.Wire.State = modelapi.StateBlocked` and `r.Wire.Action = modelapi.ActionBlock`, `sortLocal("m/run", true, false, 99)`, `sortCloud("c", 98)`; order `profile/p,alias/blocked,alias/run,m/run,c`.
- In `models_client_test.go`, `TestWireTableRowsSortsAnAliasIntoItsBand`: build `[]modelapi.Row` of one cloud model with a high `Aligned` score and one `Kind: modelapi.KindAlias` row with none (follow the fixture style of the existing wire-row tests in that file), run `wireTableRows(rows, time.Time{})` then `sortRows`; the alias is first.

Update the two existing band tests to five bands:
- `TestSortRowsBands`: add `sortAlias("alias/a", config.LocationCloud, false, 1)` to the rows; want `native,profile/p,alias/a,local-run,cloud-top,local-idle`; fix its comment ("five bands").
- `TestSortRowsPutsProfilesBetweenNativeAndTheRest` (`slot_row_test.go`): add an alias row (`sortAlias("alias/x", config.LocationCloud, false, 5)`); want `claude/native,profile/alpha,profile/start,profile/zeta,alias/x,omlx/idle,openrouter/cheap,openrouter/dear`; fix its comment.

- [ ] **Step 2: Run them to see them fail**

Run: `cd wt && go test ./internal/tui -run 'TestSortRows|TestWireTableRowsSortsAnAlias' -count=1`
Expected: FAIL (the new tests and the two updated ones; the alias rows land in the running-local or other band).

- [ ] **Step 3: Implement the band**

In `modelrows.go`: add `isAlias(r tableRow) bool`. In `sortRows`'s `band`, order the cases profile 1, native 0, `isAlias(r)` 2, local-and-ready 3, default 4. In the comparator, the family-borrowing branch now applies to bands 3 and above (`band(a) >= 3`); for band 2 compare own scores only: a scored row before an unscored one, higher score first, equal or both unscored fall through to the id comparison. Keep `place`/`familyBest` as they are (an alias never reaches them). Rewrite the `sortRows` doc comment as five bands and say aliases take no family score.

- [ ] **Step 4: Run the package tests**

Run: `cd wt && go test ./internal/tui -count=1`
Expected: PASS (the whole package, so no other test relied on an alias's old place).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui
git commit -m "feat(wt): the model picker sorts aliases right after slot profiles

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: Docs and the gate

**Files:**
- Modify: `wt/docs/internals/local-models.md` (the "Sort: four bands" bullet), `wt/docs/internals/tui.md` (TIER paragraph, "within the model bands"), `wt/CLAUDE.md` (TUI bullet "within the running-local and other bands"), `docs/guides/06-wt-agents-and-models.md` (the picker-order sentences in the model table paragraph)

**Interfaces:**
- Consumes: the band order from Task 1.
- Produces: docs that state it.

- [ ] **Step 1: Update the four passages**

- `local-models.md`: "four bands" becomes five: 0 native, 1 slot profiles, **2 alias rows (wire `Kind` alias), by their own tier score descending, a row with no score after the scored ones, ties by id, no family borrowing; state (running, blocked) does not move them**, 3 running local, 4 every other model; the "Within bands 2 and 3" rule becomes "bands 3 and 4". Leave the rotation and default-selection sentences as they are.
- `tui.md`: "within the model bands" becomes "within the alias, running-local and other bands".
- `wt/CLAUDE.md`: "within the running-local and other bands" becomes "within the alias, running-local and other bands".
- Guide 06: after the slot-profile sentence add "then alias rows, by tier score, highest first (an alias shows the score of its first-choice target)"; "Within the last two groups" becomes "Within the alias group and the last two groups". Leave the cursor/rotation sentences untouched (they stay true until rotation is removed). No model counts or live state go in the guide.

- [ ] **Step 2: Find any other quote of the old order**

Run: `grep -rnE "four bands|running-local and other|last two groups|bands 2 and 3" wt/docs wt/CLAUDE.md docs CLAUDE.md wt/internal`
Expected: no hit on a passage that still describes four bands (history under `docs/superpowers/` and `docs/archive/` may keep old wording; do not edit it).

- [ ] **Step 3: Check links**

Run: `bin/check-links`
Expected: exit 0.

- [ ] **Step 4: Run the gate in the background**

Run `make pre-pr > /private/tmp/claude-501/pre-pr-alias-band.log 2>&1; echo GATE_EXIT=$? >> /private/tmp/claude-501/pre-pr-alias-band.log` with `run_in_background` (no inner `&`). When it ends, read the log for `GATE_EXIT=0`.
Expected: `GATE_EXIT=0`. On a failure, fix it in this task and rerun; do not push on red.

- [ ] **Step 5: Commit**

```bash
git add wt/docs wt/CLAUDE.md docs/guides/06-wt-agents-and-models.md
git commit -m "docs(wt): the picker's alias band

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Self-review

- Spec coverage: band and order (Task 1), identification by Kind (Task 1, `isAlias`), no family borrowing (test 1), blocked stays (test 3), five docs places incl. the comment (Tasks 1, 2), non-goals respected (Global Constraints), next PR out of scope.
- Types: `isAlias`, `sortAlias`, band numbers 0 to 4 are used the same way in both tasks.
- Proportion: two tasks for one function; code appears only where a signature or an exact assertion fixes a choice.
