# wt picker and list: row-aware key footer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Both worktree screens (the launch picker and `wt worktrees`) show, in a footer, exactly the keys that work on the highlighted row.

**Architecture:** A pure `rowKeys` method builds the hints from the same predicates the key handlers use; a pure `fitHints` renders them to at most two lines at the terminal width; `layout()` puts the result under the detail block as the last thing a short terminal gives up. The title line shrinks to the screen name. Handlers are untouched, and a parity test ties footer and handlers together.

**Tech Stack:** Go 1.26.7, Bubble Tea, lipgloss; package `wt/internal/tui`.

**Spec:** `docs/superpowers/specs/2026-11-13-wt-picker-footer-design.md`

## Global Constraints

- Work from `wt/`; run `go build ./...` / `go test ./...` there. Scope tests with `-run` (the full suite takes about 10 minutes; run it once at the end in the background).
- Every `Test*` has a top-level `//` comment saying what it tests and why.
- Assert on unexported functions directly; never parse rendered lipgloss output for colour.
- Test seams are per-call values, not package globals.
- No change to what any key does, to its status-line refusals, or to `RemoveFuncs`.
- An unavailable key is absent from the footer; nothing is dimmed.
- A view never exceeds the terminal height or width (`TestRowListFits80x24`).
- After renaming or removing `manageTitles`, `grep -rn` the name and the old legend strings over `CLAUDE.md`, `docs/`, `wt/docs/`, `../docs/contracts/` and test comments.
- The plan and spec are committed on the feature branch, never on `main`. Branch from `main` as `wt-list-footer` (use superpowers:using-git-worktrees; the spec file is currently uncommitted on `main` and goes with the branch).

## Review Focus

1. The `/` filter open in launch mode: every printable key goes into the query, so the footer must show only the filter's keys, not `x d o r R n q`.
2. Removal not wired (`RemoveFuncs.Off != ""`, or `Preview`/`Rescue`/`RestoreRoot` nil, as in the picker's early frames): the footer must not offer `x d o enter-remove`, `r` or `R`.
3. The cursor on the `+ New worktree…` item, on a bare-repository row, or with every row hidden by the filter: no row keys.
4. A short terminal (80x24 with a long list, and 40x12): the footer survives the title and the report header, and the view stays within the terminal.
5. A narrow terminal (50 columns) with every row key on offer: shortened forms, never a clipped key or a third line.

---

### Task 1: `rowKeys` and its parity test

**Files:**
- Create: `wt/internal/tui/row_keys.go`
- Test: `wt/internal/tui/row_keys_test.go`
- Commit with it: the spec and this plan (first commit on the branch).

**Interfaces:**
- Produces: `type keyHint struct{ key, long, short string }` (`key` is the keypress as the handler's `msg.String()` sees it, `"↑/↓"` and `"space"` aside; `long` e.g. `"x remove"`, `short` e.g. `"x"`).
- Produces: `func (m *cleanupListModel) rowKeys() []keyHint`: the hints that apply now, in display order: navigation first, then the row keys, then `F`, `q`.
- Consumes: `cleanupLocked(cleanup.Row) bool`, `(*cleanupListModel).originTarget`, `rescue.Offered`, `rootmove.Offered`, `m.hidden(i)`, `m.launch()`, `m.rm`, `m.filtering`.

**Rules (copied from the handlers, `cleanup_list.go` `Update`, `startRowAction`, `startRescue`, `startRestoreRoot`, `startRemovalOf`):**

- Filter open (`m.filtering`): only `↑/↓ move`, `enter keep filter`, `esc clear filter`, `ctrl+c quit`. Nothing else.
- Empty list: no hints (the screen says "press any key to quit").
- Always on: `↑/↓ move`, `F` (`F fetch` in launch mode, `F refresh` in manage mode), `q quit`.
- Launch mode adds `enter launch` (not on a heading, hidden row or `row.Bare`), `n new worktree`, `/ filter`.
- Manage mode adds `space tick` and `a proven` (cursor row not locked for `space`; `a` always), and `enter remove ticked`.
- Row keys apply only when the cursor is on a real row (not a heading, not hidden, not the `newWorktree` item):
  - `x remove` and `d +branch`: row not `locked`, `m.rm.Off == ""`, `m.rm.Preview != nil`.
  - `o origin`: `originTarget(row)` is ok and the target is not `cleanupLocked`, and the same `rm` conditions.
  - `r rescue`: `rescue.Offered(row)` true and `m.rm.Rescue != nil`.
  - `R restore root`: `rootmove.Offered(row)` true and `m.rm.RestoreRoot != nil`. It reads neither the row's lock nor `rm.Off` (see the comment on `startRestoreRoot`).

- [ ] **Step 1: Write the failing tests in `row_keys_test.go`**
  - `TestRowKeysMatchTheHandlers`: for `mode` in both modes, for every item of `cleanupFixture()` plus one item per case below, with a fully wired `RemoveFuncs` (Preview, Run, Refresh, Refetch, Rescue, RestoreRoot all non-nil stubs), set `m.cursor`, and for each key in `x d o r R` send it through `Update` and call the key "accepted" when the returned model has `phase != cleanupPhaseList` or `done` or `restoreRoot`; assert the key is in `rowKeys()` exactly when accepted. Needs one fixture row each for: a free worktree, a locked worktree, a branch with an `origin` upstream, a branch with none, a root row off its default (see `restoreRootReport`, line ~1544 of `cleanup_list_test.go`).
  - `TestRowKeysOfferNothingWhenRemovalIsNotWired`: `RemoveFuncs{}` and `RemoveFuncs{Off: "registry did not load"}`: no `x`, `d`, `o`, `r`; `R` also absent with `RestoreRoot == nil`.
  - `TestRowKeysOnTheNewWorktreeItemAndAHiddenCursor`: cursor on the `+ New worktree…` item: `enter launch`, `n new worktree` present, no row key; with a filter hiding every row: no row key and no `enter launch`.
  - `TestRowKeysWhileFiltering`: `m.filtering = true`: exactly `↑/↓ move`, `enter keep filter`, `esc clear filter`, `ctrl+c quit`.
  - `TestRowKeysManageModeNamesTickKeys`: manage mode, free row: `space tick`, `a proven`, `enter remove ticked`; locked row: no `space tick`.
- [ ] **Step 2: Run, expect a compile failure (`rowKeys` undefined)**

  Run: `go test ./internal/tui -run 'TestRowKeys' -count=1`
- [ ] **Step 3: Implement `keyHint` and `(*cleanupListModel).rowKeys` in `row_keys.go`** from the rules above. Reuse the handlers' own predicates; do not copy their logic when a function exists.
- [ ] **Step 4: Run to green**

  Run: `go test ./internal/tui -run 'TestRowKeys' -count=1`. Expected: PASS.
- [ ] **Step 5: Commit** (`git add` the spec, this plan, `row_keys.go`, `row_keys_test.go`): `feat(wt): row-aware key hints for the worktree list`.

### Task 2: footer layout and the slimmer title

**Files:**
- Modify: `wt/internal/tui/row_keys.go` (add `fitHints`), `wt/internal/tui/cleanup_list.go` (`layout()` around line 1114, `titleLine`, delete `manageTitles`)
- Test: `wt/internal/tui/row_keys_test.go`, and the tests that quote the old title: `cleanup_list_test.go:596` (`TestCleanupListTitleLineNamesAllActions`), `picker_rows_test.go:567` (`TestPickerTitleDoesNotOfferRemoval`), `app_test.go:206` (`TestViewReady`, wants `"enter launch"`)

**Interfaces:**
- Consumes: `keyHint`, `rowKeys` from Task 1.
- Produces: `func fitHints(h []keyHint, width int) string`: joins the hints with ` · ` and returns one or two lines no wider than `width`: all `long` forms on one line if they fit; else all `long` forms wrapped to two lines if they fit; else all `short` forms wrapped to two lines; else the short forms clipped to two lines (only below about 30 columns). Never drops a hint before shortening all of them. `width <= 0` returns the long forms on one line. A `short` form is the `key` plus a one-word action where the key alone is unclear (`n new`, `x`, `R restore`), so keep a one-word action for `enter`, `n`, `/`.
- Produces: `func (m *cleanupListModel) footer() string`: `fitHints(m.rowKeys(), m.width)` dimmed, "" with no hints.

- [ ] **Step 1: Write the failing tests**
  - `TestFitHintsShortensBeforeDropping`: with the five-row-key hint set, at widths 140, 80, 50, 40: result has at most two lines, no line wider than the width, and every hint's key (`x`, `d`, `o`, `r`, `R`, `F`, `q`) appears in each result.
  - `TestFooterIsTheLastThingDropped`: `newRowList(longRowsFixture(), …)` in both modes at 80x24 and 40x12, cursor walked over 30 items (the loop in `TestRowListFits80x24`): view height ≤ terminal height, no line wider, the highlighted `> ` row is on screen, and `q quit` or its short form `q` is in the last two lines of the view.
  - `TestTitleNamesOnlyTheScreen`: launch title contains `wt` and none of `enter`, `quit`; manage title contains `wt worktrees` and none of `space`, `quit`.
- [ ] **Step 2: Run, expect failures**

  Run: `go test ./internal/tui -run 'TestFitHints|TestFooter|TestTitleNames' -count=1`
- [ ] **Step 3: Implement `fitHints` and `footer`** in `row_keys.go`. Use `tuilayout.WrapText`'s approach only if it fits; otherwise pack hints greedily by width with `lipgloss.Width`.
- [ ] **Step 4: Edit `layout()`**: `bottom` becomes `detail` plus `"\n" + m.footer()` when the footer is not empty. Frames, fullest first: title+header / detail+footer; title / detail+footer; "" / detail+footer; "" / status block + footer; "" / "". Keep the existing fit test per frame. Note `detail` already begins with `"\n" + m.statusBlock()`; keep the status block in the fourth frame.
- [ ] **Step 5: Edit `titleLine`**: launch returns `accent("wt")` alone; manage returns `accent("wt worktrees")` alone; delete `manageTitles` and its comment (the "no form names `d o esc`" note goes with it).
- [ ] **Step 6: Update the three old tests.** `TestCleanupListTitleLineNamesAllActions` becomes a footer test at 100x30 and 80x24 (every key of a free worktree row, `q quit` or `q` visible, no clipping); rename it `TestCleanupListFooterNamesTheRowsKeys` and rewrite its comment. `TestPickerTitleDoesNotOfferRemoval` checks the footer on the picker with removal not wired (no `x`), renamed `TestPickerFooterDoesNotOfferRemoval`. `TestViewReady` still wants `"enter launch"` in the view, now from the footer (the fixture's cursor row must be launchable).
- [ ] **Step 7: Run the tui package**

  Run: `go test ./internal/tui -count=1` (about 2 minutes; run in the background). Expected: PASS, including `TestRowListFits80x24`.
- [ ] **Step 8: Commit**: `feat(wt): the worktree screens show the row's keys in a footer`.

### Task 3: docs and the sweep

**Files:**
- Modify: `wt/docs/wt-cleanup.md:166` (the sentence "The width-tier key legends do not yet list `d`, `o` (list) or `x d o r R` (picker)…"), `wt/docs/internals/tui.md` (the row-list bullet, line ~46: add one sentence naming the footer, `rowKeys`, and the parity test), `docs/guides/08-maintenance-and-troubleshooting.md` §9 if it quotes the legend
- Modify: `wt/CLAUDE.md` TUI section only if it names `manageTitles` (it does not today; the grep decides)

- [ ] **Step 1: Edit the three docs.** In `wt-cleanup.md` replace the "do not yet list" sentence with: the footer lists the keys that work on the highlighted row, and `/` open shows the filter's keys alone.
- [ ] **Step 2: Sweep.** Run `grep -rn "manageTitles\|width-tier key legend\|titleLine" CLAUDE.md docs wt --include='*.md' --include='*.go'`; every hit is current or fixed.
- [ ] **Step 3: Gates.** From `wt/`: `make check`, `go vet ./...`, then `go test ./... -count=1` in the background (about 10 minutes). Expected: all green. From the repo root: `make check-links`.
- [ ] **Step 4: Look at the real screens** (the `tui-screens-via-pty` memory): build `wt` into a temp dir (`D=$(mktemp -d)`, `go build -o "$D/wt" ./cmd/wt`), capture the launch picker and `wt worktrees` at 80x24 and 100x30 from a pty driver that answers the OSC 11 query; check the footer on a free worktree, the root row, a locked row, and with `/` open.
- [ ] **Step 5: Commit**: `docs(wt): name the worktree footer; drop the title-legend caveat`.
