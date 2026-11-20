# Narrow-screen layout for the worktree picker's row table

Date: 2026-11-20. Size: a small change (one table, its detail block, tests, docs).

## Intent

On a narrow terminal the worktree picker (launch mode) and the `wt worktrees`
list (manage mode), which share one row table (`wt/internal/tui/rowtable.go`),
should keep what the user needs to pick a row: the worktree, the branch and the
PR. Everything else gives way first, and at very narrow widths the table
degrades further instead of clipping the PR number off the edge.

## Goals

The table fits the list's width by taking the first step of this ladder that
fits (each step is tried only when the one before it does not fit):

1. All columns: `WORKTREE BRANCH PR# DIFF IGN STATUS VERDICT`.
2. Drop IGN, then STATUS, then DIFF, then VERDICT, one column at a time (whole
   columns, as today). VERDICT drops last of the four.
3. Drop the PR state word: the PR cell reads `#12` instead of `#12 merged`
   (`?` and the empty cell are unchanged).
4. Merge WORKTREE and BRANCH into one column headed `NAME`, showing the
   worktree if the row has one, otherwise the branch (a remote-only row shows
   `remote/branch`, as its branch cell does today).
5. Truncate the `NAME` cell with a trailing `…` to the width that is left. The
   PR cell is never truncated, so the PR number is always visible.

Widening the terminal walks back up the ladder (the fit is recomputed from the
whole table every frame, as now).

Facts that a step hides stay reachable for the highlighted row, in the detail
block under the list, which already does this for DIFF and PR:

- VERDICT hidden: `verdict safe|check|work|locked` (`verdictWord`).
- PR state hidden: `PR #12 merged` (`prLine`, as when the column is gone).
- STATUS hidden: `status <state>`; IGN hidden while DIFF is shown: `N ignored`
  (both are my additions; today nothing names them).
- Merged: `branch <name>`; and `name <full name>` when NAME is cut (added after
  the final review: the rule line above the detail is cut at the same width, so
  it does not restate the name).

## Non-goals and anti-goals

- No change to `tuilayout.Columns`, the Models screens (`wt-model model`, the
  launcher's model table) or `columns_test.go`: they stay byte-for-byte as they
  are.
- No change to `wt worktrees --plain` / `--json`, the filter (it matches the
  full text of name, branch, tags and the PR cell whatever is drawn), sorting,
  keys or the verdict logic.
- The tag text appended after a row (`dirty`, `unpushed`, ...) is outside the
  table and is still clipped at the edge on a narrow terminal first; fixing
  that is a separate change.
- Anti-goal: a hidden fact with no way to see it. The verdict, branch and PR
  state must be in the detail block whenever the table does not draw them.

## Constraints

- `internal/tuilayout` is a leaf and `Columns` is shared; the new logic lives
  in `internal/tui` (`rowtable.go`, `cleanup_list.go`'s `detail()`).
- Widths are terminal cells (a CJK character takes two), as style guide L5
  says. The design first kept the table's rune counts; the final review showed
  a wide name then pushes the PR number off the edge, which the goal forbids.
- The header must fit with `tuilayout.TitleRoom` at every step.
- The documented rule "tables drop whole columns, never part of one; no cell is
  abbreviated" (`docs/style-guide.md` L6/L8, `docs/wt-cleanup.md`, guide 08,
  `docs/internals/tui.md`) changes, as the user asked: the style guide gains
  one carve-out for this table's name column (cut with `…` only because the
  detail block shows the whole name) and for the PR state word.

## Approaches

- **A. Extend `tuilayout.Columns`** with a general degrade ladder (alternate
  narrow form, merge, shrink). Reusable, and the Models tab's hand-rolled
  shrink could adopt it. Touches the shared engine and its other consumers;
  larger API.
- **B. Keep `Columns`, build the variants in `rowtable.go`** (chosen by the
  user). `newRowTable` builds up to three `Columns` (full; short PR; merged),
  and a small `rowTable` wrapper picks the first that fits each frame. The
  Models screens and `columns_test.go` are untouched; precedent is
  `modeleditor/models_tab.go`, which already shrinks a column outside
  `Columns`.

## Design (approach B)

- `rtDropOrder` becomes `{rtIgn, rtStatus, rtDiff, rtVerdict}`. PR, WORKTREE
  and BRANCH are never dropped by `Columns`.
- `newRowTable` returns a `rowTable` (type in `rowtable.go`) holding three
  layouts built from the same rows: `full` (7 columns), `short` (the same
  columns with the PR cell cut at its first space, its width taken over all
  rows), `merged` (`NAME`, `PR#`). It exposes what `cleanup_list.go` uses
  today: `Fit(width)`, `Shown(i)`, `Header()`, `Width()` and a row renderer.
  `Shown(rtPR)` stays true in every mode (the PR number is always drawn); a new
  accessor says whether the state word is drawn.
- `Fit(width)`: `full.Fit` with the new drop order; if it leaves the table
  within the width (and header room) use it. Otherwise try `short` (all four
  droppable columns already dropped), then `merged`; if `merged` is still too
  wide, shrink the `NAME` width to what is left after the prefix, the
  separator and the PR column, not below a floor of 6 runes (my call). Below
  the floor the existing edge clip applies, as it does today for the
  never-dropped columns.
- Merged cell: `DisplayName` for a worktree row, else `OneLine(branch)`;
  truncation keeps the first runes and ends with `…` (my call: end cut, not
  middle).
- `detail()` gains the lines above, each only when its column is not drawn.
- Rows are rendered through the active variant; a pending row's cells keep
  reading `-` where they do today.

## Tests and docs

- Rewrite `TestRowTableDropsColumnsInOrderAndWhole` (new order, VERDICT drops)
  and `TestVerdictColumnIsShort` (width-dependent). Add: the ladder at
  boundary widths, widen-back, header room at every step, merged cell rule for
  worktree/branch/remote/detached rows, truncation keeps `#N` and ends with
  `…`, never wider than the list (`TestEveryListPhaseFitsTheTerminal`), and
  the detail lines for each hidden fact. Both prefix widths (launch 4, manage 8).
- Docs: `docs/wt-cleanup.md` (columns and narrow paragraph), guide 08 §9 (same
  sentence), `docs/internals/tui.md` L48, `docs/style-guide.md` L6/L8
  carve-out, `wt/CHANGELOG.md`. `make pre-pr` before the PR.

## Left to the builder

The exact detail-line wording, the heading text of the merged column if `NAME`
reads badly in review, and the truncation floor (6 runes is a starting point).
