# Narrow-screen worktree picker columns Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The worktree picker / `wt worktrees` row table keeps WORKTREE, BRANCH and PR on narrow terminals: it drops other columns, then the PR state word, then merges worktree and branch into one truncated `NAME` column that never loses the PR number.

**Architecture:** `internal/tui/rowtable.go` gains a `rowTable` type holding three `tuilayout.Columns` layouts (full, short, merged) and picks the first that fits each frame; `tuilayout.Columns` is not changed. `cleanup_list.go` uses `*rowTable` and its `detail()` names every fact the active layout hides.

**Tech Stack:** Go (module `wt/`), Bubble Tea / lipgloss, `internal/tuilayout`.

**Spec:** `docs/superpowers/specs/2026-11-20-narrow-picker-columns-design.md`

## Global Constraints

- `internal/tuilayout` and the Models screens (`tui/modeltable.go`, `modeleditor/models_tab.go`) stay byte-for-byte unchanged; `tuilayout/columns_test.go` must still pass untouched.
- Widths are rune counts (`utf8.RuneCountInString`, `tuilayout.PadRunes`), as the table does today.
- The header must fit with `tuilayout.TitleRoom` at every step; no row line is wider than the list unless the never-shrinkable floor is hit (then the existing `clip` applies).
- Ladder order: drop IGN, STATUS, DIFF, VERDICT (one whole column at a time), then the PR state word (`#12`, `?`, `-` and empty unchanged), then merge into `NAME`, then truncate `NAME` with a trailing `…`; floor for `NAME` is 6 runes.
- Merged cell: `cleanup.DisplayName(r)` for a worktree row, else the branch cell (`OneLine(branch)`, `remote/branch` for a remote row).
- Prefix widths stay `rowPrefixWidthLaunch` (4) and `rowPrefixWidthManage` (8); both are tested.
- Docs state no live model state; wording of the changed docs follows `docs/style-guide.md`.
- Run `make pre-pr` (about 12 minutes, run it with `run_in_background`, read `GATE_EXIT` from its log) before opening the PR. No closing keywords in the PR body.

## Review Focus

1. Width at or below the prefix (e.g. `Fit(1)`, `Fit(4)`, `Fit(8)`): no panic, no negative repeat count; lines are clipped by the existing `clip`.
2. A worktree row with an empty branch (detached) or a row whose display name is empty: merged cell falls back to the branch, then to `-`-free empty text; no crash.
3. Multibyte names (`é`, CJK) are cut on a rune boundary and the cut text plus `…` is exactly the name width in runes.
4. Pending rows (`Pending: true`) in merged mode: PR cell reads `-`, name still shows.
5. Widening the terminal after a truncated frame restores the full layout (the layouts' `Widths`/`Heads` are recomputed every `Fit`).

---

### Task 1: The `rowTable` ladder and its wiring

**Files:**
- Modify: `wt/internal/tui/rowtable.go`
- Modify: `wt/internal/tui/cleanup_list.go:275` (field type), `:385` (construction, unchanged call), `:991` (`rowLine(it.row, m.table)` unchanged call), `:1066` / `:1088` (Fit, Header unchanged calls), `:1256-1260` (`Shown` calls keep compiling)
- Test: `wt/internal/tui/rowtable_test.go`

**Interfaces:**
- Produces (all in package `tui`, file `rowtable.go`):
  - `type rowTable struct` with unexported fields: `full, short, merged *tuilayout.Columns`; `mode rowMode`; `nameFull, nameWidth int`; the original `fullHeads`-style data it needs to rebuild `merged.Widths[0]`/`Heads[0]` on every `Fit`.
  - `type rowMode int` with `rowModeFull`, `rowModeShortPR`, `rowModeMerged`.
  - `const rowNameFloor = 6`
  - `func newRowTable(rows []cleanup.Row, prefix int) *rowTable`
  - `func (t *rowTable) Fit(width int)`
  - `func (t *rowTable) Shown(col int) bool` (col is an `rt*` constant: full mode defers to `full.Shown`; short mode shows `rtWorktree`, `rtBranch`, `rtPR`; merged mode shows only `rtPR`)
  - `func (t *rowTable) PRState() bool` (true only in full mode)
  - `func (t *rowTable) Merged() bool`
  - `func (t *rowTable) Header() string`, `func (t *rowTable) Width() int` (of the active layout)
  - `func rowLine(r cleanup.Row, t *rowTable) string` (replaces the `*tuilayout.Columns` version)
  - `func shortPRCell(cell string) string` (cut at the first space: `#12 merged` -> `#12`, `?`/`-`/`""` unchanged)
  - `func mergedName(r cleanup.Row) string` (worktree row: `cleanup.DisplayName(r)`, falling back to the branch cell when that is empty; branch/remote row: the branch cell from `rowCells`)
  - `func truncateName(s string, w int) string` (`s` whole when `utf8.RuneCountInString(s) <= w`; `w <= 1` gives `"…"`; else first `w-1` runes + `"…"`)
- `rtDropOrder` becomes `[]int{rtIgn, rtStatus, rtDiff, rtVerdict}`.
- `rowCells` keeps its seven-cell shape and signature.

- [ ] **Step 1: Write the failing tests** in `rowtable_test.go`, using `tableFixture()`:
  - `TestShortPRCell`: `"#12 merged"`->`"#12"`, `"#3 open"`->`"#3"`, `"?"`, `"-"`, `""` unchanged.
  - `TestTruncateNameKeepsRunesWhole`: `truncateName("feature-login", 8)=="feature…"`; `truncateName("héllo-wörld", 6)=="héllo…"`; `truncateName("日本語のブランチ", 4)=="日本語…"`; `truncateName("abc", 3)=="abc"`; `truncateName("abc", 1)=="…"`; every result has `utf8.RuneCountInString <= w` and is valid UTF-8.
  - `TestMergedNamePicksWorktreeElseBranch`: fixture worktree `feat/x` -> `DisplayName`; `wt-loose` (detached, no branch) -> its display name; branch row `solo` -> `solo`; remote row -> `origin/feat/y`; a worktree row with empty `Name` and `Branch:"b"` -> `b`.
  - `TestRowTableLadder` (loop both prefixes): for `w` from 200 down to 1 call `Fit(w)`, record per width the shown set (`Shown(c)` for each `c`), `PRState()` and `Merged()`; assert the widest width at which each step first happens is strictly ordered: IGN hidden, then STATUS, then DIFF, then VERDICT, then `!PRState()` (while `Shown(rtWorktree)` and `Shown(rtBranch)` still true), then `Merged()`; assert `Shown(rtPR)` is true at every width; assert VERDICT is still shown at the width where DIFF is first hidden.
  - `TestRowTableNeverWiderThanTheList` (loop both prefixes, `w` from `prefix+rowNameFloor+1+3` up to 200 — the narrowest width the floor allows, computed from the layout, not hard-coded): after `Fit(w)`, `utf8.RuneCountInString(Header())+tuilayout.TitleRoom <= w` and for every fixture row `prefix + RuneCount(rowLine(r,t)) <= w`.
  - `TestRowTableTruncatesNameKeepsPR`: use a row set with one long worktree name (30 runes) and PR `#383 merged`; fit to a width one rune narrower than `merged` untruncated; assert the rendered line contains `…`, ends with `#383`, and the name part is shorter than the full name; assert `Merged()` true and `PRState()` false.
  - `TestRowTableWidensBack`: `Fit(200)`, record `rowLine` of the first fixture row; `Fit(prefix+12)` (truncated); `Fit(200)` again; the line equals the first one and `Header()` equals the original.
  - `TestRowTableTinyWidthsDoNotPanic`: `Fit(w)` and `rowLine` for every fixture row and `Header()` for `w` in `0..12` (both prefixes) return without panic.
  - Rewrite `TestRowTableDropsColumnsInOrderAndWhole` into the ladder test above (delete the old body); update `TestVerdictColumnIsShort` (VERDICT shown at width 80 only if it fits: assert against `Shown(rtVerdict)` at `Fit(200)` and that its header cell is `VERDICT`); update tests that read `cols.Widths`/`cols.Heads` to read `tbl.full.Widths`/`tbl.full.Heads`; `rowLine` calls now take the `*rowTable`.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/tui -run 'TestShortPRCell|TestTruncateName|TestMergedName|TestRowTable' 2>&1 | head -30`
Expected: compile errors (`undefined: shortPRCell`, `truncateName`, `mergedName`, `rowTable`) — that is the failing state.

- [ ] **Step 3: Implement** in `rowtable.go` per the Interfaces block.
  - `newRowTable`: compute `raw` cells as now; build `full` exactly as today with the new `rtDropOrder`; build `short` = `NewColumns` over heads/widths of `[WORKTREE, BRANCH, PR#]` where the PR width is the max rune count over `shortPRCell(cell)` of every row (at least `len("PR#")`), prefix and no drop order; build `merged` = `NewColumns` over `["NAME","PR#"]` with widths `[nameFull, shortPRWidth]` (`nameFull` = max rune count of `mergedName` over rows, at least `len("NAME")`), no drop order. Keep `nameFull` and the short PR width so `Fit` can rebuild `merged.Widths[0]` and `merged.Heads[0]` each call.
  - `Fit(width)`: define `fits(c) := c.Width() <= width && RuneCount(c.Header())+TitleRoom <= width`. `full.Fit(width)`; if `fits(full)` -> `rowModeFull`. Else `short.Fit(width)` (no drops, a plain check): if `fits(short)` -> `rowModeShortPR`. Else `rowModeMerged`: reset `merged.Widths[0]=nameFull` and `Heads[0]=PadRunes("NAME", nameFull)`; deficit = `max(merged.Width()-width, RuneCount(merged.Header())+TitleRoom-width, 0)`; `nameWidth = max(rowNameFloor, nameFull-deficit)`; set `merged.Widths[0]=nameWidth`, `Heads[0]=PadRunes("NAME", nameWidth)`.
  - `rowLine`: full mode — today's body (right-align DIFF/IGN, pad the rest) using `t.full`; short mode — pad `[wt, branch, shortPR]` to `t.short.Widths`; merged mode — `PadRunes(truncateName(mergedName(r), nameWidth), nameWidth)` and the padded short PR cell, joined by `merged.Line`. Pending rows: the PR cell is `"-"` already (via `rowCells`), `shortPRCell("-")=="-"`.
  - Use `max` guards so a width below the layout never produces a negative repeat count (`PadRunes` already returns longer strings unchanged).
  - `cleanup_list.go:275`: change the field type to `*rowTable`; update the field comment. The existing `Fit`, `Header`, `Shown(rtDiff)`, `Shown(rtPR)` calls compile unchanged. Leave `detail()`'s `Shown(rtPR)` use as is for now (Task 2 changes it).

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && gofmt -l internal/tui; go vet ./internal/tui && go test ./internal/tui ./internal/tuilayout 2>&1 | tail -15`
Expected: `ok` for both; `gofmt -l` prints nothing. `tuilayout` tests pass unmodified.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/rowtable.go wt/internal/tui/rowtable_test.go wt/internal/tui/cleanup_list.go
git commit -m "feat(wt): narrow worktree picker keeps worktree, branch and PR (ladder in rowtable)"
```

### Task 2: The detail block names what the table hides

**Files:**
- Modify: `wt/internal/tui/rowtable.go` (line helpers), `wt/internal/tui/cleanup_list.go:1254-1265` (`detail()`)
- Test: `wt/internal/tui/rowtable_test.go` (helpers), `wt/internal/tui/cleanup_list_test.go` (`detail()`; reuse the model-building helper the existing detail/`prLine` tests there use — find it with `grep -n "detail()" internal/tui/cleanup_list_test.go`)

**Interfaces:**
- Consumes: `rowTable.Shown`, `rowTable.PRState`, `rowTable.Merged` from Task 1; existing `verdictWord`, `prLine`, `diffLine`, `prCell`.
- Produces in `rowtable.go`:
  - `func verdictLine(r cleanup.Row) string` -> `"verdict " + verdictWord(r)`, `""` when `r.Pending`.
  - `func statusLine(r cleanup.Row) string` -> `"status " + string(state)` (state defaults to `cleanup.StateUnknown` as in `rowCells`), `""` when pending.
  - `func ignoredLine(r cleanup.Row) string` -> `"N ignored"` for a non-bare worktree row with known gates, `""` otherwise (pending, bare, non-worktree, `GatesUnknown`).
  - `func branchLine(r cleanup.Row) string` -> `"branch " + cleanup.OneLine(r.Branch)` for a worktree row with a branch, `""` otherwise.

- [ ] **Step 1: Write the failing tests**
  - Helper tests: `verdictLine` of the proven fixture row is `"verdict safe"`, of the pending row `""`; `statusLine` of the dirty row is `"status dirty"`-shaped (assert it starts with `"status "` and contains the row's `State` string); `ignoredLine` of the fixture row with 12 ignored entries is `"12 ignored"`, of the bare row `""`; `branchLine` of `feat/x` is `"branch feat/x"`, of a detached worktree `""`.
  - `TestDetailNamesHiddenColumns` (in `cleanup_list_test.go`): for the highlighted fixture worktree row, at a width where only IGN is hidden the detail contains `12 ignored` and not `verdict`; where VERDICT is hidden it contains `verdict safe`; where the state word is hidden it contains `PR #383 merged`; in merged mode it contains `branch feat/x`; at full width it contains none of those additions (`verdict `, `status `, `ignored`, `branch `). Pick the widths from the table's own layout (`tbl.full.Width()`, `tbl.short.Width()`, `tbl.merged.Width()` plus prefix) rather than constants.

- [ ] **Step 2: Run to verify they fail**

Run: `cd wt && go test ./internal/tui -run 'TestDetailNamesHidden|Line' 2>&1 | head -20`
Expected: FAIL / `undefined: verdictLine`.

- [ ] **Step 3: Implement** the four helpers in `rowtable.go`; in `detail()` append, each prefixed with `" · "` and in this order after the existing DIFF text: `diffLine` when `!Shown(rtDiff)` (unchanged condition); `ignoredLine` when `Shown(rtDiff) && !Shown(rtIgn)`; `statusLine` when `!Shown(rtStatus)`; `verdictLine` when `!Shown(rtVerdict)`; `prLine` when `!PRState()` (replaces the old `!Shown(rtPR)` condition: the PR number is always drawn now); `branchLine` when `Merged()`. Skip empty strings.

- [ ] **Step 4: Run to verify they pass**

Run: `cd wt && go test ./internal/tui 2>&1 | tail -10`
Expected: `ok`, including the existing `TestEveryListPhaseFitsTheTerminal` and `cleanup_list_test.go` header test (`m.table.Header()`).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui
git commit -m "feat(wt): the detail block names the verdict, status, ignored count, PR state and branch when the table hides them"
```

### Task 3: Docs, style guide, changelog, gate

**Files:**
- Modify: `docs/wt-cleanup.md:74-87`, `docs/guides/08-maintenance-and-troubleshooting.md:641`, `wt/docs/internals/tui.md:48`, `docs/style-guide.md:114-118` (L6/L8), `wt/CHANGELOG.md` (Unreleased, a `### Changed` entry)
- Locate each sentence with `grep -rn "never drop\|PR#\`, \`IGN\`" docs wt/docs`; the old text names the order `PR#`, `IGN`, `STATUS`, `DIFF` and says worktree, branch and verdict never drop.

- [ ] **Step 1: Update the docs** to the ladder: columns drop IGN, STATUS, DIFF, VERDICT; then the PR state word (`#12`); then WORKTREE and BRANCH merge into `NAME` (the worktree, else the branch); then `NAME` is cut with `…`, never the PR number; the detail block names the highlighted row's hidden facts (`verdict`, `status`, `N ignored`, `+N/-N`, `PR #N state`, `branch`). In `docs/style-guide.md` add one carve-out to L8 (and a matching clause in L6): the picker's `NAME` column and the PR state word may be cut because the detail block shows them whole. State no live data.

- [ ] **Step 2: Verify**

Run: `bin/check-links 2>&1 | tail -2; make lint 2>&1 | tail -3`
Expected: `ALL LINKS OK`, lint passes.

- [ ] **Step 3: Commit**

```bash
git add docs wt/docs wt/CHANGELOG.md
git commit -m "docs: narrow worktree picker ladder, style-guide carve-out, changelog"
```

- [ ] **Step 4: Run the gate** in the background: `make pre-pr > LOG 2>&1; echo "GATE_EXIT=$?" >> LOG` with `run_in_background`; read `GATE_EXIT` from the log (the notification's exit is the wrapper's). Expected: `GATE_EXIT=0` and `pre-pr: all green`.

- [ ] **Step 5: Look at the real screen** at 120, 80, 60, 45 and 30 columns (the pty driver, capturing `wt` in a scratch repo with a few worktrees; memory note `tui-screens-via-pty`): confirm each rung, no stray `…` in the header, the cursor row's detail line names the hidden facts. Report what was and was not looked at.
