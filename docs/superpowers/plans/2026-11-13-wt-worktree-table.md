# wt worktree/branch table Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Both worktree/branch screens (the launch picker and `wt worktrees`) draw one fixed-column table line per entry, paint from cheap git calls first and fill details asynchronously, show uncommitted diff counts, ignored counts and live-session counts, and read squash-merged work as merged through a new content-containment proof.

**Architecture:** `internal/cleanup` gains a containment engine (a change-set must reverse-apply onto base's tree), a ladder rung 6 using it, new gate facts (diffstat, in-main, sessions) and a two-stage gather (`GatherFacts` then `FillDetails`) whose first stage yields `Pending` rows. `internal/tui` renders rows through a `rowtable.go` on `tuilayout.Columns` inside the existing row list, swaps stage results through the generation-numbered `withReport` path, anchors the footer to the bottom edge and wraps the cursor.

**Tech Stack:** Go 1.26.7, Bubble Tea, lipgloss, bubbles; packages `wt/internal/{cleanup,tui,tuilayout}`, `wt/cmd/wt`.

**Spec:** `docs/superpowers/specs/2026-11-13-wt-worktree-table-design.md` (read its "Review amendments" section: it wins over the text above it).

## Global Constraints

- Work from `wt/`. Scope tests with `-run` while iterating; run the full suite once at the end with `run_in_background` (about 10 minutes).
- Every `Test*` has a top-level `//` comment stating what it tests and why. Assert on unexported functions directly; never parse lipgloss colour output. Test seams are per-call values, not package globals.
- Real-git behaviour is tested with `internal/gittest` fixtures and `internal/cleanup`'s fixture tests; truth-table rows go in `classify_test.go`.
- Fail closed: a git failure, a change-set over `DirtMaxFiles`/`DirtMaxBytes`, an unreadable file or any `Undetermined` reads unknown, never contained, never proven.
- `wt worktrees --json`/`--plain` shapes are unchanged; the only contract edit is the `changes-in-base` value in the `evidence` enum of `docs/contracts/cleanup-list.schema.json`. New `Row` fields are unserialised (`TestCleanupJSONUnchangedByNewRowFields` keeps passing).
- What any key does, `RemoveFuncs`, the removal engine and the footer's key rules (`row_keys.go`) do not change. Only where the footer sits changes.
- No frame exceeds the terminal height or width (`TestEveryListPhaseFitsTheTerminal`, `TestRowListFits80x24`).
- Table drop order: `PR#`, then `IGN`, then `STATUS`, then `DIFF`; `WORKTREE`, `BRANCH`, `VERDICT` never drop. Unknown cell `-`, inapplicable cell blank. Prefix width 4 (launch) or 8 (manage).
- Docs updated in the same change: `wt/docs/internals/tui.md`, `wt/docs/wt-cleanup.md`, `docs/guides/08-maintenance-and-troubleshooting.md` §9, root `CLAUDE.md`'s `wt worktrees` blurb and `wt/CLAUDE.md`'s cleanup section. After renaming anything, `grep -rn` the old name over `CLAUDE.md`, `docs/`, `wt/docs/`, `docs/contracts/` and test comments.
- The plan and spec are committed on branch `wt-worktree-table`, never on `main`.

## Review Focus

1. A branch squash-merged, after which `main` edited a file the branch touched: must read `proven` (rung 6); and the same branch after `main` *reverted* the change must not (test in Task 2).
2. A worktree whose only changes are ignored files: `STATUS clean`, `DIFF +0/-0`, `IGN n`, never dirty (Task 3).
3. Binary, over-cap and unreadable changes: `DIFF -`, never `in main` (Task 3).
4. A fill that lands for a superseded load, or `F` pressed mid-fill: dropped, cursor and ticks kept, no removal offered from half-read rows (Tasks 5, 9, 10).
5. 80x24 and 40-column terminals with a long list, an open filter and a pending fill: the view fits, columns drop whole, the footer sits on the last line (Task 7).

---

### Task 1: Containment engine

**Files:**
- Create: `wt/internal/cleanup/contained.go`, `wt/internal/cleanup/contained_test.go`

**Interfaces:**
- Produces: `func tipContainedInBase(g Git, baseTip, tip string) Tri` — tip's change-set against `merge-base(baseTip, tip)` reverse-applies cleanly onto base's tree.
- Produces: `func worktreeContainedInBase(wg Git, baseTip string) Tri` — the worktree's uncommitted set (staged+unstaged net vs HEAD via `git diff --binary HEAD`, plus untracked non-ignored files) is contained in base.
- Produces: `func reverseApplies(g Git, baseTree, patch string) Tri` — shared helper (below).
- Consumes: `Git.Run/RunInput`, `Git.Env`, `dirtLimits`, `commitOf`, `treeOf`, `mergeBase`, `TriYes/TriNo/TriUnknown`.

- [ ] **Step 1: Write failing tests** in `contained_test.go` using real-git fixtures (`gittest.Seed`/`Clone`):
  - `TestContainedSquashAfterMainEditedSameFile`: branch edits `a.txt` line 1; `main` squash-applies it, then edits `a.txt` line 9; `tipContainedInBase` is `TriYes` while `merge-tree` conflicts or differs.
  - `TestContainedFailsWhenMainRevertedTheChange`: same, then main reverts line 1 → `TriNo`.
  - `TestContainedZeroContextHunk`: one-line files (`--unidiff-zero` path) → `TriYes` when main holds the same text.
  - `TestContainedBinaryAndRenames`: a binary file change contained in base → `TriYes`; a rename → judged (`--no-renames` diff), not unknown.
  - `TestWorktreeContainedUntrackedBlobEquality`: untracked `new.txt` equal to `base:new.txt` → `TriYes`; differing byte → `TriNo`; ignored file never considered.
  - `TestContainedUnknownOnGitFailureAndOverCaps`: a bogus tip → `TriUnknown`; lowering `dirtLimits` below the set → `TriUnknown`. Never `TriYes`.
  - `TestContainedLeavesTheRealIndexAlone`: `gittest.RawIndex` bytes unchanged around the call.
- [ ] **Step 2: Run** `go test ./internal/cleanup -run 'TestContained|TestWorktreeContained' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement** `reverseApplies`: a temp dir holds a throwaway index; `Git{Env: GIT_INDEX_FILE=<tmp>}` runs `read-tree <baseTree>` then `apply -R --check --cached --unidiff-zero --binary` fed the patch on stdin (`RunInput`). Exit 0 → `TriYes`; exit 1 with git's "patch does not apply" text → `TriNo`; any other failure → `TriUnknown`. Remove the temp dir in all paths. An empty patch is `TriYes`. `tipContainedInBase` builds the patch with `diff-tree --binary --no-renames --no-color --no-ext-diff -p <mb> <tip>` and checks the byte size against `dirtLimits.MaxBytes`. `worktreeContainedInBase` takes `diff --binary --no-renames --no-ext-diff HEAD` for the tracked part and, for each path from `ls-files --others --exclude-standard -z`, compares `hash-object <file>` with `rev-parse base:<path>`; counts beyond `DirtMaxFiles`, a symlink/special file or any read error → `TriUnknown`; a mismatch → `TriNo`; combine: both `TriYes` → `TriYes`, any `TriNo` → `TriNo`, else `TriUnknown`.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): containment check — a change-set reverse-applies onto base`.

### Task 2: Ladder rung 6 `changes-in-base`

**Files:**
- Modify: `wt/internal/cleanup/ladder.go` (`Prove`, constants), `wt/internal/cleanup/classify.go` (Ruling comments near Ruling 17), `docs/contracts/cleanup-list.schema.json:62`, `docs/contracts/cleanup-list.sample.json` only if it lists the enum, `wt/docs/wt-cleanup.md:110`, `docs/guides/08-maintenance-and-troubleshooting.md` §9
- Test: `wt/internal/cleanup/ladder_test.go`, `classify_test.go`, `wt/cmd/wt/cleanup_contract_test.go` stays green

**Interfaces:**
- Produces: `EvChangesInBase Evidence = "changes-in-base"`; `Prove` returns `(EvChangesInBase, Merged)` after rung 5 answered `NotMerged`.
- Consumes: `tipContainedInBase` (Task 1), `mb` already resolved in rung 4.

- [ ] **Step 1: Write failing tests**: `TestProveChangesInBaseAfterMainEditedSameFile` (the Task 1 squash fixture: today `merge-tree` conflicts → `NotMerged`; now `EvChangesInBase, Merged`); `TestProveRevertedChangeStaysNotMerged`; `TestProveMergeDriverStillUndetermined`; `TestProveContainmentUnknownStaysNotMerged` (an unknown containment returns `NotMerged`, not `Merged`); a `classify_test.go` truth-table row: evidence `changes-in-base` + Merged → `proven`, `unpushedOf` 0, not a `treeProof`, open PR still vetoes, pending PR still lowers to `less sure`, `base_is_local` and min-age rules unchanged.
- [ ] **Step 2: Run** `go test ./internal/cleanup -run 'TestProve|TestClassify' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** the rung: in `Prove`, at rung 5's `case 1` (conflict) and its `first != bt` branch, call `tipContainedInBase(g, b, t)`; `TriYes` → `EvChangesInBase, Merged`; otherwise the existing `NotMerged`. Add the constant to the proofs block, document Ruling "changes-in-base is a proof, cherry/patch-id are not" beside Ruling 17, add the enum value to the schema and its prose in `wt-cleanup.md` and guide 08 §9.
- [ ] **Step 4: Run** the command above plus `go test ./cmd/wt -run 'TestCleanupContract|TestCleanupJSON' -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): changes-in-base proof — squash merges read as merged`.

### Task 3: Gate facts — diffstat, in-main, sessions

**Files:**
- Modify: `wt/internal/cleanup/worktrees.go` (`Gates`, `CheckGates`), `wt/internal/cleanup/snapshot.go`, `wt/internal/cleanup/rescan.go`
- Create: `wt/internal/cleanup/diffstat.go`, `wt/internal/cleanup/diffstat_test.go`

**Interfaces:**
- Produces: `type DiffStat struct{ Added, Removed int; Unknown bool }`; `func readDiffStat(wg Git) DiffStat` (`git diff --numstat HEAD` summed; binary `-\t-` lines add nothing; each non-ignored untracked file adds its newline-counted lines, a final unterminated line counts one; over `DirtMaxFiles`/`DirtMaxBytes` or any error → `Unknown`; ignored files never read).
- Produces: `Gates.Diff DiffStat`, `Gates.InBase Tri` (set only when `Dirty`, `GateInputs.Base != ""` and `!Diff.Unknown`: `worktreeContainedInBase`; otherwise `TriUnknown`).
- Produces: `func (s Snapshot) SessionsOn(path string) int` = `len(HeldOn(path))`.

- [ ] **Step 1: Write failing tests**: `TestDiffStatCountsStagedUnstagedAndUntracked` (`+99/-55` shape: staged add, unstaged delete, untracked 3-line file); `TestDiffStatIgnoredFilesNeverCount` (only ignored changes → `+0/-0`, `Dirty` false, `IgnoredCount` n); `TestDiffStatBinaryAndOverCapAreUnknownOrZero` (binary adds nothing; lowered caps → `Unknown`); `TestGatesInBaseOnlyWhenDirty`; `TestRescanComputesTheSameGateFacts` (a `Rescan` of a dirty worktree returns the same `Diff`/`InBase` as `Gather`); `TestSessionsOnExcludesSelfAndDead` (claims fixture from `sessionclaim`).
- [ ] **Step 2: Run** `go test ./internal/cleanup -run 'TestDiffStat|TestGatesInBase|TestRescanComputes|TestSessionsOn' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** the above inside `CheckGates` after `ignored(wg)`; no new git runner (use `wg`). `Rescan` already reaches `gateState`, so parity follows; keep `TestRescanCostIsScoped` green (the seam `checkGates` counts calls, not git calls).
- [ ] **Step 4: Run** the command, plus `go test ./internal/cleanup -run 'TestRescanCostIsScoped' -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): gate facts for the table — diffstat, in-main, sessions`.

### Task 4: Row fields and classifier STATUS

**Files:**
- Modify: `wt/internal/cleanup/classify.go` (`Row`, worktree rule near lines 520–540), `wt/internal/cleanup/present.go` if detail text needs the in-main sentence
- Test: `wt/internal/cleanup/classify_test.go`, `rowfacts_test.go`, `json_test.go`

**Interfaces:**
- Produces: `type WorkState string` with `StateClean="clean"`, `StateDirty="dirty"`, `StateInMain="in main"`, `StateConflicted="conflicted"`, `StateUnknown="-"`; `Row.State WorkState`, `Row.Diff DiffStat`, `Row.Sessions int` (all unserialised; `Row.Diff`/`State`/`Sessions` zero on branch and remote rows).
- Consumes: `Gates.Diff`, `Gates.InBase`, `Snapshot.SessionsOn`.

- [ ] **Step 1: Write failing tests** (truth table): precedence `conflicted` (operation blocker or unmerged index) › `clean` (not Dirty, ignored files ignored) › `in main` (Dirty and `InBase == TriYes`) › `dirty`; `-` for branch, remote, bare and `GatesErr`. `TestWorktreeRuleContainedDirtDoesNotRaiseUniqueWork`: branch proven by any rung + `InBase TriYes` → verdict `proven`, detail says the uncommitted changes are in main; `TriNo`/`TriUnknown` keep today's `unique work`. `TestCleanupJSONUnchangedByNewRowFields` still passes; `TestRowSessionsCountsHolders`.
- [ ] **Step 2: Run** `go test ./internal/cleanup -run 'TestClassify|TestRowFacts|TestCleanupJSON|TestWorktreeRule|TestRowSessions' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** in `Classify`: fill the fields; at `if g.Dirty && !c.dirtCovered(ws)` add `&& g.InBase != TriYes` for the `ReasonDirty` uniq (only when the branch itself is proven, i.e. the row's other evidence stands). Keep `Losses.Dirty` raw (`losses: raw` marker rules, `TestEveryLossesReadSiteIsLabelled`).
- [ ] **Step 4: Run** the command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): row STATUS, DIFF and session facts`.

### Task 5: Two-stage gather and Pending rows

**Files:**
- Modify: `wt/internal/cleanup/snapshot.go` (`Gather`, `Snapshot`), `wt/internal/cleanup/classify.go`
- Test: `wt/internal/cleanup/snapshot_test.go`, `classify_test.go`

**Interfaces:**
- Produces: `func GatherFacts(g Git, in GatherInputs) (Snapshot, error)` — worktree list, refs, base, claims only; sets `Snapshot.FactsOnly = true`.
- Produces: `func FillDetails(g Git, s Snapshot, in GatherInputs) (Snapshot, error)` — runs `worktreeState` gates, `Records`, `prove`, `OwnRepo`/`SkipPRs` handling and clears `FactsOnly`. `Gather` = `GatherFacts` + `FillDetails`, behaviour identical (existing tests unchanged).
- Produces: `Row.Pending bool`; for a `FactsOnly` snapshot `Classify` returns every row `Pending`, `Status` unknown-floor (never proven), `Sessions` filled, `State "-"`.

- [ ] **Step 1: Write failing tests**: `TestGatherEqualsFactsThenFill` (deep-equal snapshots on a fixture); `TestGatherFactsRunsNoGatesProofsOrGh` (a counting `Git`/`checkGates` seam sees zero gate calls, zero `merge-tree`); `TestFactsOnlyRowsArePendingAndNeverProven`; `TestFactsOnlyKeepsRowOrderOfTheFilledReport` (same kind groups and display names, same order after fill).
- [ ] **Step 2: Run** `go test ./internal/cleanup -run 'TestGatherEquals|TestGatherFacts|TestFactsOnly' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** by splitting `Gather` at the `worktreeState` loop; the `NoDefault` early return stays in `GatherFacts` (rows then read Unknown as today).
- [ ] **Step 4: Run** the command plus `go test ./internal/cleanup -count=1` (about 2 min). Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): two-stage gather — facts first, details after`.

### Task 6: The table renderer

**Files:**
- Create: `wt/internal/tui/rowtable.go`, `wt/internal/tui/rowtable_test.go`

**Interfaces:**
- Produces: `func newRowTable(rows []cleanup.Row, prefix int) *tuilayout.Columns` (headings `WORKTREE BRANCH PR# DIFF IGN STATUS VERDICT`, widths from cells and headings in runes, drop order indices for `PR#, IGN, STATUS, DIFF`, `Prefix` = `prefix`).
- Produces: `func rowCells(r cleanup.Row) []string` (7 cells; remote row's BRANCH is `remote/name`; blank where inapplicable; `-` while `r.Pending` or unknown; `DIFF` `+99/-55`, `+0/-0` clean, `-` unknown; numbers right-aligned by padding at table build; `VERDICT` via `verdictText`).
- Produces: `func rowPrefix(sessions int, cursor bool, box string) string` (digit via `refColumn` clamped at 9, `"> "`/`"  "`, then box + space in manage mode).
- Produces: `func diffLine(r cleanup.Row) string` = `"+N/-N · N ignored"` for the detail block when DIFF is dropped (shown whole or not at all).

- [ ] **Step 1: Write failing tests** with a fixture of rows: `TestRowCellsPerKind` (worktree, detached, branch, remote, bare, pending), `TestRowTableWidthsAndRightAlignment`, `TestRowTableDropsColumnsInOrderAndWhole` (widths 120 → 80 → 60 → 40: PR#, IGN, STATUS, DIFF go in that order; the three never-drop columns stay), `TestRowPrefixWidths` (4 launch, 8 manage; digit clamps at 9), `TestDiffLineWhenDropped`.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'TestRowCells|TestRowTable|TestRowPrefix|TestDiffLine' -count=1`. Expected: compile failure.
- [ ] **Step 3: Implement** on `tuilayout.NewColumns`/`Fit`/`Header`/`Line`/`PadRunes` exactly as `modeltable.go` does (read its `newModelColumns` for the pattern). Cell text is sanitised through `cleanup.OneLine` (as `DisplayName` is).
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): row table renderer for the worktree screens`.

### Task 7: Table in the list; footer anchored to the bottom

**Files:**
- Modify: `wt/internal/tui/cleanup_list.go` (`itemLines` 1006, `newRowList` 414, `layout` 1115, `detail` 1268, `filterText`), `wt/internal/tui/row_list_launch.go`
- Test: `wt/internal/tui/cleanup_list_test.go`, `row_keys_test.go`, `layout_test.go`; restate the tests that pin the wrapping prose rows (find them with `grep -n 'rowUnits\|flowUnits\|nameWidth' internal/tui/*_test.go`)

**Interfaces:**
- Consumes: Task 6 functions; `m.table *tuilayout.Columns` built in `newRowList` from `r.Rows` and `o.Mode` (prefix 4/8) and `Fit(m.width)` whenever the width changes (`fitLists`' call site).
- Produces: `itemLines` returns exactly one line per row (clipped to width); the table header (`table.Header()`) sits in the top block as a dim line; `detail()` keeps the sentence and age and appends `diffLine(row)` when `DIFF` is not shown. Headings and `+ New worktree…` stay single lines. `rowUnits`/`flowUnits` stay only if `detail` or tests still use them; delete what nothing uses.

- [ ] **Step 1: Write failing tests**: `TestRowListRendersOneLinePerRow` (launch and manage, widths 120/80/40); `TestRowListHeaderLineIsTheTableHeader`; `TestFooterSitsOnTheLastTerminalLine` (rows few: `lipgloss.Height(View())==m.height` and the last line is the footer; filler blank lines between the last row and the detail block); `TestEveryListPhaseFitsTheTerminal` and `TestRowListFits80x24` extended with the table and a long list; `TestGiveUpLadderUnchanged` (short terminal drops header, then title, then detail, footer last); `TestFilterMatchesWorktreeBranchAndPR`.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'TestRowList|TestFooter|TestEveryListPhase|TestGiveUp|TestFilter' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** the swap; in `layout()` after choosing a frame, when `m.height > 0` and the list shows fewer lines than `listH`, insert `listH - shown` blank lines before `l.bottom` (the hint line counts as list). Keep the frames array and its order.
- [ ] **Step 4: Run** the command plus `go test ./internal/tui -count=1` (about 2 min). Fix restated tests until PASS.
- [ ] **Step 5: Commit** `feat(wt): table layout and a bottom-anchored footer on both screens`.

### Task 8: Cursor wraps

**Files:**
- Modify: `wt/internal/tui/cleanup_list.go` (`move`), `wt/internal/tui/app.go` (model phase), `wt/internal/tui/pick_model.go`, `wt/internal/tui/agent_picker.go`
- Test: `wt/internal/tui/cleanup_list_test.go`, `app_test.go`, `agent_picker_test.go`, `pick_model_test.go`

**Interfaces:**
- Produces: `move(dir, 1)` wraps past either end over the items the cursor can sit on (headings and filter-hidden rows skipped; `+ New worktree…` included); `move(dir, n>1)` (paging) still stops at the ends. Models and agent lists use bubbles' `list.Model.InfiniteScrolling = true` (or a key handler that wraps if the list there is custom).

- [ ] **Step 1: Write failing tests**: `TestRowListCursorWrapsSingleSteps` (down on the last item → first, up on the first → last, skipping headings and hidden rows); `TestRowListPagingStopsAtTheEnds`; `TestModelSelectorWrapsInAppAndStandalone`; `TestAgentPickerWraps`.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'Wrap|Paging' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** as described; set the bubbles option where each list is built (`grep -n 'list.New' internal/tui/*.go`).
- [ ] **Step 4: Run** the command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): the cursor wraps in the row list, model selector and agent picker`.

### Task 9: Async fill in the picker

**Files:**
- Modify: `wt/internal/tui/picker_rows.go`, `wt/internal/tui/row_list_launch.go` (`withReport` tick rule), `wt/internal/tui/row_keys.go` (no removal keys while filling), `wt/internal/tui/app.go`
- Test: `wt/internal/tui/picker_rows_test.go`, `cleanup_list_test.go`, `row_keys_test.go`

**Interfaces:**
- Produces: `loadRowsCmd` runs `cleanup.GatherFacts` + `Classify` (Pending rows) and returns `rowsLoadedMsg` as today; `gotRows` then dispatches `loadRowFillCmd(root, snap, in, gen) tea.Cmd` returning `rowsFilledMsg{Report, Snap, gen}` (runs `FillDetails` with `SkipPRs`, classifies); `gotFilled` drops a stale `gen`, swaps via `withReport` and `swapRemoveFuncs`, then dispatches the existing `loadRowPRsCmd`. At most two repaints after the first paint (filled, then PRs).
- Produces: `cleanupListModel.filling bool` (true while the shown report has `Pending` rows): row keys `x d o r R` and manage-mode enter-remove are unavailable with their existing "unavailable" status (nil `RemoveFuncs` behaviour), absent from `rowKeys()`. Launch, filter, `n`, `F`, `q` keep working.
- Rule change: in `withReport`, a row that was `Pending` in the old list is unseen for tick bookkeeping (gets the new default tick); choices made on non-pending rows survive as today.

- [ ] **Step 1: Write failing tests**: `TestPickerPaintsFactsThenFillsThenPRs` (message order; first paint has `-` cells, digits and names); `TestStaleFillIsDropped` (`gen` mismatch leaves the list untouched); `TestFillKeepsTheHighlightedRowByKey`; `TestRemovalKeysUnavailableWhileFilling` (status line, `rowKeys` lacks them; launching works); `TestFReloadReplacesAnInFlightFill` (`rowsRefreshing`/`reloadGen` rule); `TestTicksDefaultAfterFillAndKeepLaterChoices`; `TestRowOrderStableAcrossStages`.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'TestPicker|TestStaleFill|TestFill|TestRemovalKeysUnavailable|TestFReload|TestTicks|TestRowOrderStable' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** as above; `rowsGen` increments on each accepted stage; removal funcs are built from the filled snapshot only.
- [ ] **Step 4: Run** the command plus `go test ./internal/tui -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): the picker paints from facts and fills in asynchronously`.

### Task 10: Async fill in `wt worktrees`

**Files:**
- Modify: `wt/internal/tui/cleanup_list.go` (`RunCleanupList`, `RemoveFuncs`), `wt/cmd/wt/cleanup.go` (`executeCleanup` interactive branch, `cleanupRemoveFuncs`)
- Test: `wt/internal/tui/cleanup_list_test.go`, `wt/cmd/wt/cleanup_test.go`

**Interfaces:**
- Produces: `RemoveFuncs.Fill func() (cleanup.Report, RemoveFuncs, error)` — runs `FillDetails` and PR fills off the update goroutine, returning the filled report and the funcs built from its snapshot. `RunCleanupList(r, th, rm)` is called with the facts-only report when `rm.Fill != nil`; the list dispatches `Fill` from `Init`, drops a stale result by generation, swaps with `withReport` (Task 9's tick rule) and applies the returned funcs (`applyRemoveFuncs`). With `Fill == nil` behaviour is unchanged.
- `executeCleanup`: interactive path uses `GatherFacts` and passes a `Fill` closure; `--json`/`--plain` keep the one synchronous `cleanupGather` (fetch-first, claim prune) untouched.

- [ ] **Step 1: Write failing tests**: `TestRunCleanupListFillsAfterFirstPaint` (fake `Program`/`Fill`); `TestManageEnterRemoveUnavailableUntilFill`; `TestRefreshDuringFillIsReplaced`; `TestExecuteCleanupJSONAndPlainStaySynchronous` (no `GatherFacts` call on those paths); `TestFillErrorShowsOnTheListNotPanics`.
- [ ] **Step 2: Run** `go test ./internal/tui ./cmd/wt -run 'TestRunCleanupListFills|TestManageEnterRemove|TestRefreshDuringFill|TestExecuteCleanup|TestFillError' -count=1`. Expected: FAIL.
- [ ] **Step 3: Implement** as above; `Refetch`/`Refresh` (the `F` re-read) keep returning full reports.
- [ ] **Step 4: Run** the command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): wt worktrees fills in asynchronously too`.

### Task 11: Docs, real-screen check, full gate

**Files:**
- Modify: `wt/docs/internals/tui.md`, `wt/docs/wt-cleanup.md`, `docs/guides/08-maintenance-and-troubleshooting.md` §9, `CLAUDE.md` (the `wt worktrees` blurb), `wt/CLAUDE.md` (cleanup list rules, evidence list, async picker note), `wt/docs/internals/packages.md` for new files

- [ ] **Step 1: Update the docs** to describe: the table and its drop order, DIFF/IGN/STATUS meaning, the session digit, `changes-in-base` and Ruling text, `wt remove --yes` now removing squash-merged rows without the typed phrase, the two-stage load and the removal keys' unavailability during fill, the footer anchoring, the cursor wrap.
- [ ] **Step 2: Grep** `grep -rn 'rowUnits\|flowUnits\|manageTitles\|merge-adds-nothing' CLAUDE.md docs wt/docs ../docs/contracts internal cmd` and fix stale prose.
- [ ] **Step 3: Check real screens** with the pty driver (memory: tui-screens-via-pty; answer the OSC 11 query) for `wt` and `wt worktrees` at 80x24 and 40x12 in a throwaway repo with a squash-merged branch and a dirty worktree. Expected: one line per row, footer on the last line, first frame shows `-` cells, later frame the filled cells.
- [ ] **Step 4: Run the gate**: `make check` and `go vet ./...` in `wt/`, `bin/check-links` at the root, then `go test ./...` with `run_in_background` and read the log for the exit status (memory: background-gate-capture-exit). Expected: all pass.
- [ ] **Step 5: Commit** `docs(wt): worktree table, containment proof and async fill`.
