# Row detail screen and flat worktree list Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Flatten the `wt` row list (no headings, short verdicts, fewer tags) and add one detail screen that `i`, `x`, `d`, `r`, `R`, `o` open, showing everything about a row and offering only the actions that apply, with `x`/`d`/`o` confirming on that screen.

**Architecture:** A new `cleanupPhaseDetail` in `cleanupListModel` (`wt/internal/tui`). It renders a shared `detailBlock` (the same renderer the single-row confirm uses). New read-only git listings (local-only commits, uncommitted files, ignored files) come from `cleanup.ReadRowDetail`, reached through a new `RemoveFuncs.Detail` and loaded off the update goroutine. `x`/`d`/`o` reuse the existing preview → confirm → run flow, which returns to the detail screen on `esc`; `r`/`R` keep the quit-and-hand-off flow.

**Tech Stack:** Go 1.26.7, Bubble Tea / lipgloss, `internal/cleanup`, `internal/removal`, `internal/tuilayout`.

**Spec:** `docs/superpowers/specs/2026-11-14-wt-row-detail-screen-design.md` (mockups: `.superpowers/brainstorm/16800-1794676086/content/list-and-detail-v2.html`, git-ignored).

## Global Constraints

- Removal, rescue and restore-root behavior, hard stops and approvals do not change (`internal/removal`, `internal/rescue`, `internal/rootmove` untouched).
- Frozen wire strings (`proven`, `less_sure`, `unique_work`, `in_use`, reason and blocker codes) and `--json`/`--plain` output do not change; verdict words are a TUI display mapping only.
- An action that cannot apply to the row is not shown (detail screen or footer); the reason a row cannot be removed is a fact in the Verdict line.
- Order: root first; then local worktrees and branches together by `DisplayName`; then remote branches by `DisplayName`. No headings.
- Verdict words: `proven`→`safe`, `less_sure`→`check`, `unique_work`→`work`, a hard-stopped row with no other verdict→`locked`. `in_use` has no word of its own (the session digit covers it).
- List tags dropped: `root`, `current`, `default`, `+remote`, `held by session`. `dirty`/`unpushed`/`upstream gone` show on the list only when the row's status is not `proven`; the detail screen always lists them. The root-drift badge stays.
- Every new git call goes through `cleanup.Git` (`GIT_OPTIONAL_LOCKS=0`) and writes no ref, index or file. Lists are capped at `DetailListCap = 20` with an "and N more" line.
- Every `Test*` carries a top-level `//` comment saying what it tests and why. Use per-call seams, not package globals. Tests that make worktrees build a fresh repo per case.
- A view fits the terminal at 80x24 (`TestEveryListPhaseFitsTheTerminal`); the detail screen scrolls.
- Docs change with behavior: `wt/CLAUDE.md` (TUI keys bullet), root `CLAUDE.md` (`wt worktrees` entry), `wt/docs/wt-cleanup.md`, `docs/guides/08-maintenance-and-troubleshooting.md` §9; run `make check-links`.
- Run from `wt/`. The full suite is ~10 min: scope with `-run` while iterating; run `go test ./internal/tui ./internal/cleanup ./cmd/wt` before the last commit.

## Review Focus

Inputs and conditions the spec implies that no task's happy-path tests would exercise, most likely first. Each has its test in the owning task.

1. A path or commit subject containing a newline, tab or ANSI escape (Task 3 sanitises; Task 4 renders through `cleanup.OneLine`).
2. A worktree with hundreds of uncommitted or ignored entries: capped, "and N more", no slow frame (Task 3).
3. A worktree whose directory is missing or unreadable: lists say "could not read", the screen still opens, `x` is absent (Tasks 3, 4).
4. `i` pressed while the report is still filling (`Pending` rows): the screen opens with the facts it has and says the rest is loading; no removal actions (Task 4).
5. The detail row disappears from a refreshed report (after `F` or a hand-off return): the screen closes with a status line (Task 4).
6. 80x24 and a narrower terminal: nothing wraps the list columns, the detail screen scrolls (Tasks 2, 4).
7. A detached worktree, a bare entry, and a remote row: no blank `branch`, no empty sections (Task 4).

## File Structure

- Modify `wt/internal/cleanup/present.go`: replace `KindGroups`/headings with `ListOrder`.
- Create `wt/internal/cleanup/rowdetail.go` (+ `rowdetail_test.go`): the read-only listings.
- Modify `wt/internal/tui/cleanup_list.go`: drop headings, verdict words, list tags, detail-phase entry keys, `View` dispatch.
- Create `wt/internal/tui/detail.go` (+ `detail_test.go`): phase, state, `detailBlock`, actions, keys, footer.
- Modify `wt/internal/tui/cleanup_confirm.go`: `RemoveFuncs.Detail`, single-row confirm uses `detailBlock`, `esc` returns to the detail screen.
- Modify `wt/internal/tui/row_keys.go`, `rowtable.go`, `row_list_launch.go`: footer `i`, verdict cell, launch tags.
- Modify `wt/cmd/wt/cleanup.go` (and the picker's builder wherever it builds `RemoveFuncs`): wire `Detail`.
- Docs listed in Global Constraints.

---

### Task 1: Flat list order, no headings

**Files:**
- Modify: `wt/internal/cleanup/present.go` (`KindGroup`, `Heading*`, `KindGroups`, around lines 512-565)
- Modify: `wt/internal/tui/cleanup_list.go` (`cleanupItem.heading`, `newRowList`, every `it.heading` check, `itemLines`)
- Test: `wt/internal/cleanup/present_test.go` (`TestKindGroupsOrder` becomes `TestListOrder`), `wt/internal/tui/cleanup_list_test.go`, `wt/internal/tui/picker_rows_test.go:858`, `wt/internal/cleanup/classify_test.go:955`

**Interfaces:**
- Produces: `func ListOrder(r Report) []Row` in `internal/cleanup` — root first (`Row.Primary` or the `BlockPrimary` blocker), then every local worktree and local branch row sorted by `DisplayName`, then remote-only rows (`remoteOnly`) sorted by `DisplayName`; stable, never ordered by verdict.
- Removes: `KindGroup`, `HeadingWorktrees`, `HeadingLocal`, `HeadingRemote`, `KindGroups`, `cleanupItem.heading`.

- [ ] **Step 1: Write the failing test** `TestListOrder` in `present_test.go`: a report with root `main`, worktrees `zeta` and `alpha`, branch rows `mid` and `beta`, remote rows `origin/b`, `origin/a`; assert `names(ListOrder(rep)) == ["main","alpha","beta","mid","zeta","origin/a","origin/b"]`, and that a root named `zzz` still leads. Add `TestListOrderStableAcrossFill`: facts report and filled report (differing verdicts) give the same name order.
- [ ] **Step 2: Run** `go test ./internal/cleanup -run TestListOrder -v`. Expected: FAIL, `ListOrder` undefined.
- [ ] **Step 3: Implement `ListOrder`** in `present.go` beside `byDisplayName`, delete `KindGroup`, the `Heading*` constants and `KindGroups`. Update the two non-test callers (`newRowList`, `classify_test.go:955`, `picker_rows_test.go:858`) to `ListOrder`.
- [ ] **Step 4: Write the failing TUI test** `TestListHasNoHeadings` in `cleanup_list_test.go`: render `newCleanupList` over a mixed report; assert the view contains none of `WORKTREES`, `LOCAL BRANCHES`, `REMOTE BRANCHES`; assert the cursor starts on the root row and `down` visits rows in `ListOrder` order. Update every existing test that asserts headings (`cleanup_list_test.go:93-119, 460, 991`) to the new behavior (delete heading assertions; keep the ordering ones).
- [ ] **Step 5: Implement in `cleanup_list.go`:** remove `cleanupItem.heading` and every `it.heading == ""` / `it.heading != ""` check (about 18 sites: `grep -n "\.heading" internal/tui/*.go`); `newRowList` appends one `cleanupItem` per row of `ListOrder`; `m.rows` counts them. `move`, `hidden`, `selectedRow`, `filling` lose their heading branches.
- [ ] **Step 6: Run** `go test ./internal/cleanup ./internal/tui -run 'ListOrder|Headings|CleanupList|Picker' -v`, then `go vet ./...`. Expected: PASS.
- [ ] **Step 7: Commit** `git add -A wt && git commit -m "feat(wt): flat row list, no kind headings"`

---

### Task 2: Short verdicts and quieter list tags

**Files:**
- Modify: `wt/internal/tui/cleanup_list.go` (`verdictText`, `rowTags`), `wt/internal/tui/row_list_launch.go` (`launchTags`), `wt/internal/tui/rowtable.go` (`rowCells` verdict cell)
- Test: `wt/internal/tui/rowtable_test.go`, `wt/internal/tui/cleanup_list_test.go`

**Interfaces:**
- Produces: `func verdictWord(r cleanup.Row) string` in `internal/tui`: `safe` / `check` / `work` from `Status`; `locked` when `cleanupLocked(r)` and the status is `in_use` (or `HardStop != ""`) with no better word; `-` while `Pending`. `verdictText(Status)` is removed and `rowCells` calls `verdictWord`.
- Produces: `rowTags(r, drift)` returns, in order: root-drift badge (manage mode, as now), then `dirty`, `unpushed`, `upstream gone` **only when `r.Status != cleanup.StatusProven`**. `root`, `bare`, `#N state`, `+remote`, `held by session…`, `locked` and (via `launchTags`) `current` / `default` are no longer drawn on the list. `cleanup.RowTags` itself is unchanged (the confirm screen still reads its `held by session` tag); `launchTags` returns nil.

- [ ] **Step 1: Write failing tests** in `rowtable_test.go` / `cleanup_list_test.go`:
  - `TestVerdictWords`: rows with status proven, less_sure, unique_work, in_use+primary blocker, pending → `safe`, `check`, `work`, `locked`, `-`.
  - `TestListTagsHideRiskOnSafeRows`: a proven row with `Losses.Unpushed=2` and `Upstream{Gone:true}` draws no `unpushed`/`upstream gone`; the same facts on a `less_sure` row draw both.
  - `TestListDropsIdentityTags`: the line of a root, current and default-branch row contains none of `root`, `current`, `default`, `+remote`, `held by session`.
  - `TestVerdictColumnIsShort`: at width 80 the widest verdict cell is 6 runes (`locked`); header `VERDICT` still fits.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'VerdictWords|ListTags|ListDrops|VerdictColumn' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `verdictWord`, replace `verdictText` uses (`rowtable.go:65`), change `rowTags` and `launchTags` as above. Update existing tests that assert old verdict text or tags (`grep -rn "less sure\|unique work\|\"proven\"" internal/tui/*_test.go`).
- [ ] **Step 4: Run** `go test ./internal/tui -v -run 'Row|Cleanup|Picker|Fit'`. Expected: PASS, including `TestEveryListPhaseFitsTheTerminal`.
- [ ] **Step 5: Commit** `git commit -am "feat(wt): short verdict words, quieter list tags"`

---

### Task 3: Read-only row detail listings

**Files:**
- Create: `wt/internal/cleanup/rowdetail.go`, `wt/internal/cleanup/rowdetail_test.go`

**Interfaces:**
- Produces:
  ```go
  const DetailListCap = 20
  type DetailCommit struct{ Hash, Subject string }   // Hash is 8 hex chars
  type DetailFile struct{ Status, Path string }      // Status: "M", "A", "D", "R", "??"…
  type RowDetail struct {
      Commits         []DetailCommit; CommitsMore     int
      Uncommitted     []DetailFile;   UncommittedMore int
      Ignored         []string;       IgnoredMore     int
      Err             string // "" when every listing was read; one sanitised line otherwise
  }
  func ReadRowDetail(g Git, row Row) RowDetail
  ```
  `g.Dir` is the repository directory the caller has; for a worktree row the function runs in `row.Path`. Branch and remote rows: `Commits` only for a branch row (`git log --format=%h%x00%s -z <row.Ref> --not --remotes`), no file lists. Worktree rows: commits via `rev-list`-equivalent against `HEAD`; uncommitted via `StatusArgs()` parsed (porcelain v2 `-z`, ordinary, renamed and untracked entries); ignored via `ls-files -z --others --ignored --exclude-standard --directory`. All text passes `OneLine`. A failing listing sets `Err` and leaves that section empty; it never panics and never returns a Go error.

- [ ] **Step 1: Write failing tests** (fresh temp repo per case, `gittest` helpers):
  - `TestReadRowDetailListsLocalWork`: repo with a pushed commit, 2 unpushed commits, one modified tracked file, one untracked file, one ignored file (`.gitignore` entry) → exact `Commits` subjects newest first, `Uncommitted` = `[{"M","a.txt"},{"??","new.txt"}]`, `Ignored == [".env.local"]`.
  - `TestReadRowDetailCapsLists`: 45 untracked files → `len(Uncommitted)==20`, `UncommittedMore==25`.
  - `TestReadRowDetailSanitisesNames`: a file named with an embedded newline/escape and a commit subject with a tab → no control characters in any returned string.
  - `TestReadRowDetailMissingDirectory`: row with a nonexistent `Path` → `Err != ""`, sections empty, no panic.
  - `TestReadRowDetailWritesNothing`: index bytes, `refs/` and the file tree are unchanged after the call (same technique as the fingerprint tests in `rescue`).
  - `TestReadRowDetailBranchRow`: branch row gets commits, no file lists.
- [ ] **Step 2: Run** `go test ./internal/cleanup -run ReadRowDetail -v`. Expected: FAIL, undefined.
- [ ] **Step 3: Implement `ReadRowDetail`** with `Git.Run`; reuse the status parser if `internal/cleanup` already has one (look in `dirt.go`/`worktrees.go`) rather than writing a second. Ignored entries are listed by name only (a wholly ignored directory once, trailing `/`).
- [ ] **Step 4: Run** `go test ./internal/cleanup -run ReadRowDetail -v` and `go test ./internal/cleanup -run 'DeletionStrings|NoDeletion' -v` (the deletion scans must still pass). Expected: PASS.
- [ ] **Step 5: Commit** `git add wt/internal/cleanup && git commit -m "feat(wt): read-only row detail listings"`

---

### Task 4: The detail screen

**Files:**
- Create: `wt/internal/tui/detail.go`, `wt/internal/tui/detail_test.go`
- Modify: `wt/internal/tui/cleanup_confirm.go` (`RemoveFuncs.Detail`, `cleanupPhaseDetail`), `wt/internal/tui/cleanup_list.go` (`Update`, `View`), `wt/internal/tui/row_keys.go`, `wt/cmd/wt/cleanup.go` and the picker's `RemoveFuncsBuilder` wiring

**Interfaces:**
- Consumes: `cleanup.ReadRowDetail`, `RemoveFuncs.Preview`, `rescue.Offered`, `rootmove.Offered`, `cleanupLocked`, `originTarget`, `startRowAction`, `startRescue`, `startRestoreRoot`.
- Produces:
  - `RemoveFuncs.Detail func(row cleanup.Row) cleanup.RowDetail` (nil: lists read "not available here").
  - `const cleanupPhaseDetail cleanupPhase`.
  - `type rowDetail struct { row cleanup.Row; pv *removal.ItemPreview; lists *cleanup.RowDetail; loading bool; offset int }` held in `cleanupListModel.detail`... (name the field `rowDetail`, since `detail()` is the existing list-footer method).
  - `type detailLoadedMsg struct{ key rowKey; pv *removal.ItemPreview; lists cleanup.RowDetail }`.
  - `func (m cleanupListModel) openDetail() (cleanupListModel, tea.Cmd)` — opens on the highlighted row, loads the preview (through `previewCmd`'s guards) and lists off the update goroutine.
  - `func detailBlock(row cleanup.Row, d *rowDetail, th themes.Theme, width int) []string` — the shared renderer: `worktree`, `branch`, `path` (one line each, no duplicate name), PR/age/sessions/holder line, `Verdict` line (verdict word + the reason; for a locked row the reason it cannot be removed), then `Local-only commits`, `Uncommitted files`, `Ignored files` sections, then the preview's `will lose:` / `warning:` / `note:` lines. Remote rows: remote/branch/tip/PR/verdict and the origin-removal sentence only.
  - `func (m cleanupListModel) detailActions() []detailAction` where `type detailAction struct{ Key, Label, Does string }`; one entry per applicable action only: `x`, `d`, `r`, `R`, `o` (same predicates as `rowKeys`) plus `esc back`. `Does` sentences are exactly those in the spec's Actions section.

- [ ] **Step 1: Write failing tests** in `detail_test.go`:
  - `TestIKeyOpensDetailWithoutActing`: `i`, `x`, `d`, `r`, `R`, `o` each leave `phase == cleanupPhaseDetail`, set `done == false`, and call no removal/hand-off func.
  - `TestDetailBlockShowsRowFacts`: for a worktree row with commits, uncommitted and ignored files, `detailBlock` contains the full `Path` exactly once, branch, each commit `Hash Subject`, `M  a.txt`, the ignored path; for `Name == directory name` the worktree name is not printed twice.
  - `TestDetailActionsOnlyWhatApplies`: root on `main` → actions are exactly `[esc]`; root on a feature branch (restore offered) → `[R, esc]`; a clean worktree with no local work → `[x, d, esc]` (no `r`); a dirty worktree → `[x, d, r, esc]`; a branch-only row → `[x, d, esc]` (no `r`, no `R`); a local row with an origin ref → includes `o`; a locked non-root row → `[esc]` with the reason in the Verdict line.
  - `TestRootDetailVerdictLineSaysWhy`: the root's block contains `the root cannot be removed` and no line starting `x `, `d `, `r `.
  - `TestDetailWhileFillingHasNoRemovalActions`: a `Pending` row opens the screen, actions are `[esc]`, a "loading" line is shown.
  - `TestDetailEscReturnsToList`; `TestDetailKeysRoute`: on the screen `x`→`startRowAction(rowActionRemove)`, `d`→`rowActionDeleteBranch`, `o`→`rowActionDeleteOrigin`, `r`→rescue hand-off, `R`→restore-root hand-off.
  - `TestDetailClosesWhenRowVanishes`: after a `cleanupRefetchMsg` whose report lacks the row, `phase == cleanupPhaseList` and `status` says the row is gone.
  - `TestDetailScrollsAndFits80x24`: a row with 60 uncommitted files renders at most 24 lines at width 80, `pgdown` moves `offset`, and the Actions section stays reachable.
  - `TestRowKeysListI`: `rowKeys()` contains `i` on every row (including root and remote); `TestRowKeysMatchTheHandlers` extended for `i`.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'Detail|IKey|RowKeys' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `detail.go` as specified; in `Update` route `i x d r R o` (list phase, a row selected, `filter` not owning keys) to `openDetail` instead of calling the handlers directly; add the detail phase's key switch (esc, up/down/pgup/pgdown scroll, `x d o r R` → the existing handlers); `View` dispatches `cleanupPhaseDetail` to a `detailView` that renders `detailBlock`, the Actions section from `detailActions`, and a footer made from the same actions. Handle `detailLoadedMsg` by `rowKey` so a stale load is dropped. The detail screen's `x`/`d`/`o` set `m.confFromDetail = true` before calling `startRowAction` (Task 5 reads it).
- [ ] **Step 4: Wire `RemoveFuncs.Detail`** in `cmd/wt/cleanup.go` (`cleanupRemoveFuncs`): `func(row) cleanup.RowDetail { return cleanup.ReadRowDetail(cleanup.Git{Dir: in.CWD}, row) }`; confirm the picker's `RemoveFuncsBuilder` path returns the same field (it builds through `cleanupRemoveFuncs`).
- [ ] **Step 5: Add `i` to `rowKeys`** (`row_keys.go`) in the same place as `x`/`d`, always present on a row; no change to the other predicates.
- [ ] **Step 6: Run** `go test ./internal/tui ./cmd/wt -run 'Detail|IKey|RowKeys|Fit|Cleanup' -v`. Expected: PASS.
- [ ] **Step 7: Commit** `git add -A wt && git commit -m "feat(wt): row detail screen opened by i x d r R o"`

---

### Task 5: Confirm on the detail screen

**Files:**
- Modify: `wt/internal/tui/cleanup_confirm.go` (`entryLines`, `confirmView`, `cancelConfirm`, `gotRunDone`/results return), `wt/internal/tui/detail.go`
- Test: `wt/internal/tui/cleanup_confirm_test.go`, `wt/internal/tui/detail_test.go`

**Interfaces:**
- Consumes: `detailBlock`, `rowDetail`, the existing confirm model.
- Produces: `cleanupListModel.confFromDetail bool`. When the confirm has exactly one entry and `!chosenByTicks`, `confirmView` renders `detailBlock(...)` (from the entry's `pv.Fresh` and the loaded lists) above the prompt instead of `entryLines`; the batch confirm (`chosenByTicks`) keeps `entryLines`, with the worktree name and path shown once (path via `shortPath`, name not repeated when it equals the path's base).

- [ ] **Step 1: Write failing tests:**
  - `TestSingleRowConfirmShowsTheSameDetailBlockAsI`: for one row, the lines of `detailBlock` printed by `i` and by the confirm after `x` are identical down to the prompt (same renderer, same row facts).
  - `TestXOnDetailConfirmsInPlace`: from the detail screen, `x` → previewing → confirm; typing `delete` + `enter` calls `RemoveFuncs.Run` once with that row's item; a proven row confirms with `y`.
  - `TestDDetailSeedsBranchToggle`: `d` on a worktree row opens the confirm with the "also delete unmerged branch" box ticked and `tab` unticks it.
  - `TestEscInConfirmReturnsToDetail`: after `x`, `esc` → `phase == cleanupPhaseDetail` (not the list); a second `esc` → list; with `confFromDetail == false` (list `enter` batch) `esc` still returns to the list.
  - `TestConfirmEntryDoesNotRepeatTheWorktreeName`: batch `entryLines` for a worktree at `.worktrees/wt-pr-column` contains `wt-pr-column` once in the head line.
  - `TestRunFinishReturnsToList`: after a successful single-row run the results screen and refresh return to the list as today; the detail screen is not re-opened on a removed row.
- [ ] **Step 2: Run** `go test ./internal/tui -run 'SingleRowConfirm|XOnDetail|DDetail|EscInConfirm|RepeatTheWorktree|RunFinish' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the single-row render path in `confirmView`, the `confFromDetail` handling in `cancelConfirm` (return `cleanupPhaseDetail` and keep its state), and the batch head-line fix in `entryLines`. Clear `confFromDetail` at every flow start other than the detail screen's (`startRemoval`, list-phase `startRowAction` paths) so it never survives into another flow.
- [ ] **Step 4: Run** `go test ./internal/tui -v` (about 2 min). Expected: PASS, including the existing confirm tests (`cleanup_confirm_*_test.go`), updated where they assert the old single-row layout.
- [ ] **Step 5: Commit** `git commit -am "feat(wt): confirm removal on the detail screen"`

---

### Task 6: Docs, full verification, screens

**Files:**
- Modify: `wt/CLAUDE.md` (the "TUI keys" bullet: add `i` and the detail screen; verdict words; no headings), root `CLAUDE.md` (`wt worktrees` entry: columns, verdict words, `i`), `wt/docs/wt-cleanup.md`, `docs/guides/08-maintenance-and-troubleshooting.md` §9, `wt/docs/internals/tui.md` (detail phase, `RemoveFuncs.Detail`)
- Modify: `docs/superpowers/specs/2026-11-14-wt-row-detail-screen-design.md` (only if behavior changed during the build)

- [ ] **Step 1: Update the docs** listed above; grep for stale prose: `grep -rn "WORKTREES\|LOCAL BRANCHES\|REMOTE BRANCHES\|less sure\|unique work\|KindGroups" CLAUDE.md wt/CLAUDE.md wt/docs docs/guides ../docs/contracts`. Each hit is rewritten or confirmed unrelated (JSON status strings stay).
- [ ] **Step 2: Run** `make -C .. check-links` and `make check` (gofmt, shellcheck). Expected: clean.
- [ ] **Step 3: Run the scoped suites in the background:** `go test ./internal/tui ./internal/cleanup ./cmd/wt ./internal/removal -count=1` with `run_in_background`; read the log's final lines for the pass/fail line (do not trust the wrapper's exit status alone). Expected: `ok` for each package.
- [ ] **Step 4: Check real screens through a pty at 80x24** (see memory `tui-screens-via-pty`): build a throwaway binary (`D=$(mktemp -d); go build -o "$D/wt" ./cmd/wt`), run `wt worktrees` against a scratch repo with a root on a feature branch, a merged-PR-style clean worktree with one ignored file, and a dirty worktree with an unpushed commit. Compare against the v2 mockups: flat list, digit first, verdict words, detail screens for each row, root shows only `R` and `esc`. Record deviations and fix them before finishing.
- [ ] **Step 5: Commit** `git add -A && git commit -m "docs(wt): row detail screen and flat list"`; leave the PR to the user.

---

## Self-review

- **Spec coverage:** flat order (T1); session digit already present, kept (T1/T2 tests render it); verdict words and tag rules (T2); detail contents — worktree/branch/full path, local-only commits, uncommitted, ignored (T3, T4); `i` plus `x d r R o` all open it unarmed (T4); confirm on the detail screen, `esc` steps (T5); only applicable actions, root reason in Verdict line (T4); batch confirm keeps compact layout with the duplicate name fixed (T5); remote rows, stale screen, fill-in-flight (T4); docs (T6). Sync with remote is a spec non-goal: no task.
- **Type consistency:** `ListOrder`, `verdictWord`, `RowDetail`/`ReadRowDetail`, `RemoveFuncs.Detail`, `cleanupPhaseDetail`, `rowDetail`, `detailBlock`, `detailActions`, `confFromDetail` are named identically wherever used.
- **Proportion:** six tasks; bodies are left to the implementer except the new listing contract, signatures and the exact assertions.
