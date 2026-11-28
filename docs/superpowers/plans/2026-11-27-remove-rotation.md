# Remove rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delete wt's model rotation (the picker cursor that follows `rotation.state`, `wt rotate`, the `internal/rotation` package) and fold its two surviving jobs, the last-picked marker and the usage event, into `internal/usage`, so the picker opens on the first row that is not blocked.

**Architecture:** `internal/usage` gains two methods on `*StoreImpl`: `RecordLaunch(agent, id)` (writes `last-picked.state`, always appends the usage event) and `LastPicked()`. The four launch-record call sites and the picker's marker read move to them. The picker's rotation cursor code, `wt rotate`, `catalog.RegisteredOrdered` and the package go. `wt rotate` stays only as a hidden removed-command stub (root accepts arbitrary positional args, so an unregistered `rotate` would launch the picker with "rotate code" as agent arguments).

**Tech Stack:** Go 1.26 (module `wt/`), Bubble Tea picker, cobra, `go test`. Markdown docs.

**Spec:** `docs/superpowers/specs/2026-11-27-remove-rotation-design.md`. Exact sites (files, lines, per-test classification, docs wording): `docs/superpowers/plans/2026-11-27-remove-rotation-sites.md` (the appendix; line numbers are as of base `30dcfaac`, find a site by its quoted text).

## Global Constraints

- The picker cursor is the `-M` pin, else the first row of the sorted list that is not blocked (`newModelList`'s existing rule; a row that needs a start counts as unblocked, so the first alias can be a stopped one and Enter starts it). If every row is blocked the cursor is row 0.
- `last-picked.state` drives only the `> ` marker, never the cursor. No fallback to `rotation.state`.
- Nothing reads, writes or deletes `rotation.state` or the old `rotation-*.state` files: leftovers stay on disk, unread.
- Unchanged: `usage.jsonl` format and counts (1D/7D/30D, `wt stats`, `wt profile slots usage`), the non-TUI launch with no `-M` (never rotated), every wire type and the server catalog.
- `*StoreImpl.RecordFor` stays the usage-event-only append (about 20 test call sites). The `usage.Store` interface does not change (decision: the design said `usage.Store`; the four callers all hold a concrete `*StoreImpl`, so the interface and its test doubles stay untouched).
- `wt/CLAUDE.md`: every `Test*` has a top-level `//` comment saying what and why; tests stay off the developer's machine (use `usage.NewStoreAt(t.TempDir())`, `usage` has no `TestMain`); history (`docs/superpowers/**`, `docs/archive/**`, dated CHANGELOG entries) is never edited; key/token/log "rotation" is not a site (appendix lists them).
- Run Go with `go -C wt ...` or from `wt/`. In this worktree use repo-rooted git pathspecs (`':/docs/...'`) when your cwd is `wt/`.
- Gate: `make -C .. pre-pr` from `wt/` (or `make pre-pr` at the root), run with `run_in_background`, nothing else running; if it fails only in `internal/modelclient` timing tests, run that test alone and rerun the gate idle (memory note); never push on red.
- Commits end with `Co-Authored-By: Claude Code <noreply@anthropic.com>`. Never commit in the primary checkout.

## Decisions beyond the approved design (flag these to the user at hand-off)

1. `wt rotate <tag>` becomes a hidden removed-command stub (exit 1, "was removed", changes nothing) in the style of `removedLitellmCmd`, not an unregistered name.
2. `catalog.RegisteredOrdered` and its tests are deleted: its only production callers were `wt rotate` and the cursor code.
3. `resolveModelRemote`'s second result (named `rotation`, "the list rotation walks") is not rotation code: `launch_remote.go` uses `len(...) > 0` to choose the picker over the "multiple models match" error. Keep that behavior; rename it `registered`.
4. `RecordLaunch` is on `*StoreImpl`, not the `usage.Store` interface.
5. The #752 CHANGELOG line ("the cursor still starts where rotation puts it") becomes false and is rewritten.

## Review Focus

- `wt rotate code` after the change exits 1 with the removed message and never opens a picker or launches an agent (Task 4).
- A leftover `rotation.state` is never read or touched: `LastPicked` with only `rotation.state` reports no marker; a launch writes `last-picked.state` and usage, creates no `rotation.state` and leaves an existing one byte-identical (Tasks 1, 2).
- `RecordLaunch` still appends the usage event when `last-picked.state` cannot be written, and returns an error naming `last-picked.state` (Task 1).
- The cursor: a stopped alias first in the list is the cursor (Enter starts it); a `-M` pin wins; all rows blocked gives row 0 (Task 3).
- The non-TUI no-pin launch is unchanged: one launchable row launches, several registered rows hand off to the picker on a terminal, several non-registered rows still say "multiple models match" (Task 4, existing `resolve_remote_test.go` cases kept).
- A slot-profile launch leaves the `> ` marker on the profile row (Task 2).

---

### Task 1: `usage.RecordLaunch` and `usage.LastPicked`

**Files:**
- Modify: `wt/internal/usage/usage.go` (add methods and a `pickedPath` helper; reword the `Store` interface comment that says "rotation records launches")
- Test: `wt/internal/usage/usage_test.go` (or a new `wt/internal/usage/launch_test.go`)

**Interfaces:**
- Produces:
  - `func (s *StoreImpl) RecordLaunch(agent, id string) error`: writes `<dir>/last-picked.state` (`id` plus newline, mode 0600, `config.WriteFileAtomic`, which creates the directory); then always calls `s.RecordFor(agent, id)` and ignores its error (best-effort, as the old `RecordFor`/`RecordProfile` did); returns `fmt.Errorf("last-picked.state not saved: %w", err)` only when the marker write failed.
  - `func (s *StoreImpl) LastPicked() (string, bool)`: reads `last-picked.state`, trims space, `("", false)` when missing or empty. No other file is read.
- Consumes: existing `NewStore()`, `NewStoreAt(dir)`, `(*StoreImpl).RecordFor`, `config.WriteFileAtomic`.

- [ ] **Step 1: Write the failing tests** (each with the what-and-why comment), all on `NewStoreAt(t.TempDir())`:
  - `TestRecordLaunchWritesTheMarkerAndTheUsageEvent`: after `RecordLaunch("claude", "openrouter/a")`, `LastPicked()` is `("openrouter/a", true)`, the file is one line with mode `0600`, and `CountsForAgent("claude", ["openrouter/a"])` shows one launch.
  - `TestRecordLaunchOverwritesTheMarker`: a second id replaces the first; counts are per id.
  - `TestRecordLaunchKeepsUsageWhenTheMarkerCannotBeWritten`: make `last-picked.state` a directory so the write fails; the error text contains `last-picked.state not saved`; the usage event is still counted. (Port of `TestRecordKeepsUsageWhenLastPickedFails` in `internal/rotation/rotation_test.go`.)
  - `TestLastPickedMissingOrEmpty`: no file, and an empty or whitespace-only file, both `("", false)`.
  - `TestLastPickedIgnoresALeftoverRotationState`: a directory holding only `rotation.state` (`"openrouter/a\n"`) gives `("", false)`, and `RecordLaunch` neither creates nor modifies a `rotation.state` (byte-compare an existing one, and assert none is created when absent).
  - `TestRecordLaunchAcceptsAProfileID`: `RecordLaunch("claude", "profile/work")` stores that id as the marker (a profile launch and a model launch are the same call now).

- [ ] **Step 2: Run to verify they fail**

Run: `go -C wt test ./internal/usage -run 'TestRecordLaunch|TestLastPicked' -count=1`
Expected: FAIL to compile (`RecordLaunch`, `LastPicked` undefined).

- [ ] **Step 3: Implement `RecordLaunch`, `LastPicked` and `pickedPath` on `*StoreImpl` in `usage.go`** per the Interfaces block. Add the `fmt` and `strings` imports. Reword the `Store` interface comment (launches are recorded through `RecordLaunch`/`RecordFor`; the picker reads counts); the interface members do not change.

- [ ] **Step 4: Run the package**

Run: `go -C wt test ./internal/usage -count=1`
Expected: PASS.

- [ ] **Step 5: Commit** (`feat(wt): usage records the last-picked marker and the usage event in one call`).

---

### Task 2: Launch records go through `usage.RecordLaunch`

**Files:**
- Modify: `wt/cmd/wt-session/launch_remote.go` (the `rotation.New().RecordFor(agent, m.ID)` call, its import, and the comments at the launchNative doc, steps 2 and 5 of the launch doc, and the `len(rot) > 0` comment: "records no usage or last-picked state", "the picker opens on the first row that is not blocked", "record the launch (last-picked marker and usage)"), `wt/cmd/wt-session/slot_launch.go` (`RecordProfile`), `wt/internal/tui/slot_flow.go` (`RecordProfile`), `wt/internal/tui/remote_start.go` (`RecordFor`)
- Test: `wt/cmd/wt-session/slot_launch_test.go`, `launch_remote_test.go`, `native_test.go`, `cwd_norepo_test.go`, `wt/internal/tui/pinned_flags_test.go`, `app_test.go`

**Interfaces:**
- Consumes: `usage.NewStore().RecordLaunch(agent, id string) error` (Task 1). Each site keeps its existing `wt: note: %v` print on error and its best-effort handling.
- Produces: no launch path writes `rotation.state` any more.

- [ ] **Step 1: Rewrite the tests that read or assert `rotation.state` (failing first).** Per the appendix (Tests section): `slot_launch_test.go` (the two cases asserting a profile launch leaves `rotation.state` byte-identical) becomes `TestLaunchSlotProfileRecordsTheProfileAsLastPicked`: after the launch `last-picked.state` holds `profile/<name>`, the usage event for the profile is counted, no `rotation.state` exists and a pre-seeded one is byte-identical. `launch_remote_test.go` and `app_test.go:~1394` (`rotation.New().Last()`): assert the model id in `last-picked.state` and the usage count instead. `native_test.go`, `cwd_norepo_test.go` and `pinned_flags_test.go`: keep the absent-file lists but name `last-picked.state` and `usage.jsonl` (keep `rotation.state` in the absent list: it now proves nothing writes it).
- [ ] **Step 2: Run them to verify they fail** (the old code still writes `rotation.state`).
  Run: `go -C wt test ./cmd/wt-session ./internal/tui -run 'LaunchSlotProfile|LaunchRemote|Native|Cwd|Pinned|Record' -count=1`
  Expected: FAIL on the new assertions.
- [ ] **Step 3: Replace the four call sites** with `usage.NewStore().RecordLaunch(agent, id)`, swap the `internal/rotation` import for `internal/usage` in each file (none imports it yet, and no local identifier named `usage` shadows it; check `remote_start.go`), and fix the listed comments.
- [ ] **Step 4: Run both packages**
  Run: `go -C wt test ./cmd/wt-session ./internal/tui -count=1`
  Expected: PASS (the `tui` package takes about 2 minutes; run to a file and read the tail).
- [ ] **Step 5: Commit** (`refactor(wt): launches record through usage.RecordLaunch`).

---

### Task 3: The picker cursor is the first row that is not blocked

**Files:**
- Modify: `wt/internal/tui/models_client.go` (the catalog command closure, about lines 274-293)
- Delete: `wt/internal/tui/slot_cursor_test.go` (no `Test*` functions, dead helpers importing rotation)
- Test: `wt/internal/tui/models_client_test.go`, `agent_model_test.go`, `testhelpers_test.go`

**Interfaces:**
- Consumes: `usage.NewStore().LastPicked()` (Task 1). Use the concrete store, not the `newUsageStore` seam (tests point the seam at an unrelated directory).
- Produces: the catalog command sets `msg.cursor` to the `-M` pin or leaves it empty; `newModelList` is unchanged.

- [ ] **Step 1: Write and rewrite the cursor tests (failing first).** In `models_client_test.go`: rewrite `TestPickerRotationCursorSkipsStartRows` (about line 609) as `TestPickerCursorStartsOnTheFirstRowThatIsNotBlocked`: with a start row (a stopped local model or alias) first in the sorted list and a launchable row below it, the cursor is the start row. Add `TestPickerCursorIsThePinnedRow` (a pin beats the first row) and `TestPickerCursorIsRowZeroWhenEveryRowIsBlocked`. Add `TestPickerMarkerFollowsLastPicked` if no existing test covers the `> ` marker drawing from `last-picked.state` through the real catalog command (`TestModelPickerMarksLastLaunchedRow` may; reword it). In `agent_model_test.go`: rewrite the `phaseModelWithList` helper (lines ~67-137) so it positions the cursor with the first-unblocked rule (no `rotation.NewAt`, `RecordFor`, `Last`, `NextFromOrder`); delete `TestSelectedEntryPositionsAfterLast`, `TestSelectedEntryLastLastInListWrapsToZero`, `TestSelectedEntryLastMissingFallsBackToZero` and `TestSelectedEntryLastDiscoveredIDFallsBackToFirstRow` (they test only the removed rotation guard); reword `TestSelectedEntryMsgPositionsCursorAtNextToUse`, `TestPhaseModelWithListBuildsAndPositionsCursor` and `TestModelPickerMarksLastLaunchedRow` so they describe the first-row rule and the marker (re-derive expected ids by running them; appendix, cursor notes).
- [ ] **Step 2: Run to verify they fail**
  Run: `go -C wt test ./internal/tui -run 'TestPickerCursor|TestPickerMarker|TestSelectedEntry|TestPhaseModelWithList|TestModelPickerMarks' -count=1`
  Expected: FAIL (the old command still positions the cursor with rotation).
- [ ] **Step 3: Edit the catalog command in `models_client.go`.** Replace `rot := rotation.New(); lastID, _ := rot.LastPicked()` with `lastID, _ := usage.NewStore().LastPicked()`; keep `msg.tbl = renderTable(...)` and the `if r.pinned != "" { msg.cursor = r.pinned; return msg }`; delete everything after that up to `return msg`: the comment, the `launchable []modelapi.Row` loop (`catalog.Launchable(eligible)`) and the `rot.NextFromOrder(...)` block. `eligible` and `m.tag`/`config.FirstTag` stay (they feed other code). Remove the now-unused `rotation` import. Fix the `modelrows.go:~173` comment naming `catalog.RegisteredOrdered` (Task 4 deletes it) and the `sortRows` doc's "enterModelPhase" wording if still stale.
- [ ] **Step 4: Run the package**
  Run: `go -C wt test ./internal/tui -count=1`
  Expected: PASS.
- [ ] **Step 5: Commit** (`feat(wt): the picker cursor starts on the first row that is not blocked`).

---

### Task 4: `wt rotate` becomes a removed stub; `RegisteredOrdered` goes

**Files:**
- Modify: `wt/cmd/wt-session/commands.go` (replace `rotateCmd` and `rotateModelsRemote` with `removedRotateCmd()`; the file may keep just that, or move it beside `removedLitellmCmd` in `litellm.go` and delete `commands.go`: either, keep `packages.md` and `README.md` rows consistent), `wt/cmd/wt-session/main.go` (the `AddCommand(rotateCmd(a), ...)` entry and the two comments at about lines 280 and 570), `wt/cmd/wt-session/resolve_remote.go` and `launch_remote.go` (rename the second result and local `rot`/`rotation` to `registered`; reword its doc and the "nor a rotation entry" comment; behavior unchanged), `wt/internal/catalog/wire.go` (delete `RegisteredOrdered`; drop imports that become unused; keep `Launchable`)
- Test: `wt/cmd/wt-session/flip_test.go` (row ~165), `mode_gate_test.go` (row ~307), `main_test.go` (list ~1062), `stats_remote_test.go` (the `rotateCmd`/`rotateModelsRemote` cases, ~251-295), `resolve_remote_test.go` (call sites that name the second result), `wt/internal/catalog/wire_test.go` (delete `TestRotationSkipsAliasRows` and the `RegisteredOrdered` half of `TestLaunchableAndRegisteredOrdered`; keep and rename the `Launchable` half), `wire_subscription_test.go` (delete `TestRotationSkipsSubscriptionRows`)

**Interfaces:**
- Produces: `func removedRotateCmd() *cobra.Command`: `Use: "rotate"`, `Hidden: true`, `DisableFlagParsing: true`, `SilenceUsage: true`, `Args: cobra.ArbitraryArgs`, `Annotations: {removedCmdAnnotation: "2026-11-27 (rotation removed)"}`; `RunE` returns `exitcode.New(1, errors.New("rotate was removed: the model picker no longer rotates, it opens on the first row that is not blocked; nothing was changed"), false)`. Registered in place of `rotateCmd(a)`; it needs no model server (mode gate: not model-needing).
- Consumes: `removedCmdAnnotation` (`moved.go`); `TestNoWtCommandIsHidden` already skips annotated stubs.

- [ ] **Step 1: Write the failing tests.** `TestRemovedRotateExitsOneAndChangesNothing` (in `moved_test.go` or beside the litellm stub tests): `wt rotate code` and `wt rotate --json` exit 1, print the message once, and neither open a picker nor build an agent command (use the fake launch hooks other root tests use). Update the three table rows (`flip_test.go`, `mode_gate_test.go`, `main_test.go`) to the stub's expectation (no model server needed, exit 1, message). Remove the `stats_remote_test.go` cases for the deleted functions. In `catalog`: delete the two `RegisteredOrdered` tests and keep a test that `Launchable` still excludes what it did.
- [ ] **Step 2: Run to verify they fail**
  Run: `go -C wt test ./cmd/wt-session ./internal/catalog -run 'Rotate|Flip|ModeGate|Launchable|Moved|Hidden' -count=1`
  Expected: FAIL (the old `rotate` still works; the stub test fails).
- [ ] **Step 3: Implement.** Add `removedRotateCmd`; swap it into `main.go`; delete `rotateModelsRemote`; delete `RegisteredOrdered` from `wire.go` (fix its `RegisteredOrdered`-naming comments in `wire.go`); do the `resolve_remote.go`/`launch_remote.go` renames. `main.go:~280`'s comment lists subcommand names: replace `rotate` with `smoke`. Confirm with a grep that nothing else calls `RegisteredOrdered`.
- [ ] **Step 4: Run the packages**
  Run: `go -C wt test ./cmd/wt-session ./internal/catalog -count=1`
  Expected: PASS (the existing `resolve_remote_test.go` cases for one, several registered and several non-registered rows are the proof that the non-TUI path is unchanged).
- [ ] **Step 5: Commit** (`feat(wt): wt rotate is removed; RegisteredOrdered goes with its last callers`).

---

### Task 5: Delete `internal/rotation` and its guards

**Files:**
- Delete: `wt/internal/rotation/` (`rotation.go`, `rotation_test.go`: every surviving behavior is already tested in `internal/usage` by Task 1; classify each `Test*` against the appendix and port any behavior test that has no counterpart)
- Modify: `wt/internal/removal/deletionscan_test.go` (delete the allow-list row for `internal/rotation/rotation.go`), `wt/internal/depcheck/depcheck_test.go` (remove `internal/rotation` from `sessionBanned`; leave `internal/usage` where it is), `wt/internal/config/fortest.go` (comment about overwriting the real `rotation.state`), and the wording-only Go comments listed in the appendix and by the critic: `internal/agents/native.go` ("no rotation, usage or survey record"), `internal/agents/shell.go` ("shell has no model rotation"), `internal/localmodels/catalog.go`, `internal/modeladmin/apply.go`, `cmd/wt-model/model_write.go` (error text "rotation and launch profiles key on the id" becomes "usage history and launch profiles key on the id"; no test pins it), `internal/config/config.go` (`FirstTag` comment: it is "the shared form of the slot tag component", not rotation; `FirstTag` stays) and the test comments in `internal/localmodels/catalog_rows_test.go`, `internal/modeleditor/models_form_test.go`, `internal/config/registry_doc_test.go`, `internal/config/config_test.go`
- Test: the whole module

**Interfaces:**
- Consumes: Tasks 1 to 4 (nothing imports `internal/rotation` any more).
- Produces: no package named rotation; the wording left in comments says usage history, launch history or the last-picked marker.

- [ ] **Step 1: Prove nothing imports the package.** Run `grep -rn 'internal/rotation' wt --include='*.go'`; expected: only the files under `wt/internal/rotation/` and the two guard tests.
- [ ] **Step 2: Delete the package and fix the two guard tests and the comments.**
- [ ] **Step 3: Build, vet and run the guards**
  Run: `go -C wt build ./... && go -C wt vet ./... && go -C wt test ./internal/depcheck ./internal/removal -run 'TestDeletion|TestEveryPackage|Test.*Banned|Test.*Allow' -count=1`
  Expected: build and vet clean; guard tests PASS (the deletion scan fails on a stale allow-list row, which is the check that the row is gone).
- [ ] **Step 4: Grep for leftovers.** Run `grep -rniE 'rotation|NextFromOrder|FirstAfter|RecordProfile' wt --include='*.go'`; every remaining hit must be key/token/log rotation (appendix non-sites) or the `removedRotateCmd` text and its test.
- [ ] **Step 5: Commit** (`refactor(wt): delete internal/rotation`).

---

### Task 6: Docs, changelog and the gate

**Files:**
- Modify (full list with lines and wording in the appendix, Docs sections): `wt/CLAUDE.md` (the "Rotation (Go)" section becomes a short "Launch history" note: `usage.RecordLaunch`, `last-picked.state` drives only the marker, usage unchanged; the "Which model" line; the native paragraph; the package list and the `local-models.md#rotation-go` link), `CLAUDE.md` (root, "rotation/usage state"), `wt/README.md` (the `wt rotate` section and rows, the default-rotation-tag phrase, the package row), `wt/docs/internals/{local-models,launch-flow,tui,testing,packages}.md` (`local-models.md` and `launch-flow.md` also claim a non-TUI launch walks `NextFromOrder`: that was already false, correct it to "several launchable rows hand off to the picker on a terminal, else `multiple models match`"), `wt/docs/{wt-session,wt-config,wt-model-server,wt-start-stop}.md`, `wt/docs/wt-agents/{README,slot-profiles,claude-wt,codex-wt,copilot-wt,opencode-wt,pi-wt,agy-wt}.md`, `docs/guides/{00-config-map,03-model-families,06-wt-agents-and-models,07,02,04,12}.md` (guide 03 Step 5 and its Gotcha deleted, guide 06 §5 and its two verification lines shrunk to a launch-history note with another probe such as `wt model list --server` and the picker, guide 00's `rotation.state` section and table row deleted), `docs/reference/worktrunk-comparison.md`, `litellm-session-logs/session-log-sources.md` (one phrase), `bin/check-config-dirs-untouched` (header comment), `wt/CHANGELOG.md` (rewrite the #752 line that says the cursor still follows rotation; add one entry under Changed for the removal and one that `wt rotate` is removed, and that a leftover `rotation.state` is ignored)
- Test: link check and the full gate

**Interfaces:** Consumes the final behavior of Tasks 1 to 5; produces docs that state it.

- [ ] **Step 1: Edit each doc place from the appendix** (match by the quoted phrase; many lines are very long). Say once, in guide 06, that leftover `rotation.state` and `rotation-*.state` files stay on disk unread. Keep the legacy bash `models.conf` "rotation arrays" in guide 00 (history, not wt rotation).
- [ ] **Step 2: Leftover grep.** Run `grep -rniE 'rotation\.state|wt rotate|NextFromOrder|FirstAfter|internal/rotation|Rotation \(Go\)' CLAUDE.md wt docs/guides docs/reference litellm-session-logs bin --include='*.md' --include='check-config-dirs-untouched' | grep -v 'docs/superpowers\|docs/archive'`
  Expected: only the CHANGELOG entries that describe the removal, the guide-06 leftover-files sentence and dated CHANGELOG history.
- [ ] **Step 3: Check links.** Run `bin/check-links`. Expected: `ALL LINKS OK` (the `local-models.md#rotation-go` fragment link is stripped by the checker but fix it anyway).
- [ ] **Step 4: Run the gate in the background**
  Run: `make -C .. pre-pr > /private/tmp/claude-501/pre-pr-remove-rotation.log 2>&1` (from `wt/`) with `run_in_background`; nothing else running.
  Expected: ends `pre-pr: all green` and exit 0. On a red limited to `internal/modelclient` timing, rerun that test alone, then the gate idle.
- [ ] **Step 5: Commit** (`docs(wt): rotation is gone; the picker opens on the first row that is not blocked`).

---

## Self-review

- Spec coverage: cursor (Task 3), `rotation.state`/`Last`/`NextFromOrder`/`FirstAfter`/migrate (Tasks 3, 5), `wt rotate` (Task 4), one recording call and `LastPicked` without fallback (Tasks 1, 2), package deletion and the `usage` fold (Tasks 1, 5), leftovers untouched (Task 1 test, deletion-scan row removed in Task 5), docs and changelog (Task 6), non-goals (usage format, non-TUI launch, wire types) guarded by the unchanged resolve tests and Global Constraints.
- Beyond-the-design decisions are listed once above for the user.
- Type consistency: `RecordLaunch(agent, id string) error`, `LastPicked() (string, bool)` on `*StoreImpl`, `removedRotateCmd()`, `registered` (renamed second result) are used identically in every task.
- Proportion: six tasks; code appears only for signatures and exact assertions; the per-site detail lives in the appendix.
