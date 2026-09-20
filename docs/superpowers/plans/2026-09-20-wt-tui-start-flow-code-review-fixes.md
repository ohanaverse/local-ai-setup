# wt: TUI start-flow — code-review fixes (round 1)

Follow-up to `2026-09-20-wt-tui-start-flow.md` (plan items 1–5, all complete) and
`docs/superpowers/specs/2026-09-20-wt-tui-start-flow-design.md`. Seven findings from a
`/code-review` pass on the feature branch `wt-tui-start-flow`. This is post-execution work:
commits use a `fix(tui): …` scope tag, not "completes plan item N".

## Global Constraints

- Every `Test*` keeps a top-level `//` comment stating **what** it tests and **why** it
  matters (user-facing consequence of a regression) — `wt/CLAUDE.md`.
- Never weaken an assertion to make a test pass. Migrate, don't delete, in the same commit
  that breaks them.
- Test seams are package-level vars (`runInventory`, `startModel`) swapped by tests.
- All work in `wt/`; run `go build ./... && go vet ./... && go test -count=1 ./...` from there.
- Findings reference `wt/internal/tui/{app.go,start_flow.go,model_list.go}`.

## Findings and dispositions

| # | Finding | Disposition |
|---|---|---|
| 1 | `refreshTable` drops `SetItems`' cmd → filtered picker goes permanently empty | Task 1 |
| 4 | `refreshTable` probes the inventory inside `Update` (blocks the UI) | Task 1 |
| 5 | Success path skips `refreshTable` → stale `RUNNING=-` after a failed launch | Task 2 |
| 2 | Second esc/q/ctrl+c while cancelling quits wt, contradicting the spec | Task 3 (ctrl+c only) |
| 3 | `AllowReplace` assertion is precedence-broken and vacuous | Task 4 |
| 6 | Footer still says `[enter] launch`; three comments describe retired behavior | Task 5 |
| 7 | `handleStartMsg`'s `ok` return is unreachable dead code | Task 5 |

### Adjudicated decision (finding 2)

The spec says esc/q/ctrl+c during a start "never quit the program"; the code quits on a
second press, and `TestKeysDuringStartCancelThenQuit` defends that deliberately ("a stray
key cannot orphan a half-started server"). **User decision: `ctrl+c` only** — while
cancelling, esc/q are ignored (they are the mashable keys the spec worries about) and
ctrl+c remains a deliberate escape hatch. This deliberately deviates from the spec's
"all three behave identically" wording; the spec is amended in Task 3.

Rationale: the cancel path depends on `lifecycle.Start`'s teardown completing. A UI that
ignores *every* key while an operation hangs is a trap — the same class of defect as
finding 1, which this plan also fixes. `mtplx stop --port N` recovers a server; a wedged
alt-screen TUI needs the terminal killed.

## File Structure

- `wt/internal/tui/app.go` — `refreshTable`, `tableRefreshedMsg`, `pendingSelect`, the
  `phaseModel` delegation hook, the footer/comment drift, `proceedToLaunch` bail paths.
- `wt/internal/tui/start_flow.go` — `handleStartKey`, `startingView`, `handleStartMsg`.
- `wt/internal/tui/model_list.go` — the picker footer string and the `blocked` comment.
- `wt/internal/tui/table_refresh_test.go`, `start_flow_test.go`, `agent_model_test.go` — tests.
- `docs/superpowers/specs/2026-09-20-wt-tui-start-flow-design.md` — the cancel-key bullet.

---

### Task 1: `refreshTable` returns a `tea.Cmd`; the probe leaves the update loop

Fixes findings 1 and 4 together — both are the same missing `Cmd` plumbing.

**Design.** `refreshTable() (model, tea.Cmd)`. The synchronous part captures the selected
id and returns a closure; the closure probes the inventory, rebuilds the table and returns
a `tableRefreshedMsg{items, header, sel}`. `Update` handles that message by calling
`SetItems` — **and returning its `Cmd`**, which is the finding-1 fix — then restoring the
cursor.

Cursor restore needs care: under an applied filter, `SetItems` nils `filteredItems` and the
repopulated order only exists after its `list.FilterMatchesMsg` lands, and
`Select`/`Index` are visible-item coordinates under a filter (`app.go:292-296`). So the
target id is parked in `m.pendingSelect` and resolved in the `phaseModel` delegation hook:
immediately when unfiltered, otherwise once the `FilterMatchesMsg` arrives. Resolution
clears the field whether or not the id was found (the model may have left the inventory).

- [x] **Step 1: Write the failing tests** (append to `table_refresh_test.go`; reuse its
      `runningCell` helper and the fixtures in `modeltable_flow_test.go`)
  - `TestRefreshTableCmdProbesOffTheUpdateLoop` — stub `runInventory` with a probe counter;
    call `refreshTable()` and assert `probes == 0` (nothing ran in-process), then execute
    the returned `tea.Cmd` and assert `probes == 1`. This is the finding-4 regression: the
    probe must not run on the update loop.
  - `TestRefreshTableKeepsFilteredRowsVisible` — enter the model phase, set
    `m.models.SetFilterText("qwen")` + `SetFilterState(list.FilterApplied)`, run a refresh
    and then execute every command it returns (including the `FilterMatchesMsg` round trip),
    asserting `VisibleItems()` is non-empty and `SelectedItem()` is non-nil. **This is the
    reproduction from the review** — it fails today with `visible == 0`.
  - `TestRefreshTableKeepsCursorUnderFilter` — same setup with two `qwen` rows; the cursor
    must land back on the same model id after the refresh.
- [x] **Step 2: Run to verify failure** —
      `go test ./internal/tui -run 'RefreshTableCmdProbes|RefreshTableKeepsFiltered|RefreshTableKeepsCursorUnderFilter' -v`
      — Expected: FAIL to compile (`refreshTable` still returns one value).
- [x] **Step 3: Implement** (`app.go`)
  - Add `tableRefreshedMsg`; add `pendingSelect string` to `model`.
  - `refreshTable() (model, tea.Cmd)`: keep the `len(m.tableModels) == 0` no-op; capture
    `cfg`/`agent`/`tableModels`/`sel`; return the closure (reuse `tableFor` via the captured
    model value — it only reads `cfg`/`agent`/`activeTags`/`activeFamily`).
  - `resolvePendingSelect()`: scan `VisibleItems()`, `Select(i)` on an id match, then clear.
  - `Update`: `case tableRefreshedMsg:` guarded on `m.phase == phaseModel` (a refresh can
    land after the user moved on) — `SetItems`, set `Title`, park `pendingSelect`, resolve,
    **return the `SetItems` cmd**.
  - Delegation hook: after `m.models, cmd = m.models.Update(msg)` in `phaseModel`, resolve
    when the msg is a `list.FilterMatchesMsg` or the list is unfiltered.
- [x] **Step 4: Migrate the callers this breaks** — `start_flow.go`'s `finishStart` `back()`
      must return the refresh cmd (`return m.refreshTable()`), and
      `TestRefreshTableReprobesAndKeepsCursor` must execute the returned cmd before asserting.
- [x] **Step 5: Run to verify pass** —
      `go build ./... && go vet ./... && go test -count=1 ./... && go test -race ./internal/tui -count=1`
      — Expected: PASS, no races.
- [x] **Step 6: Commit** — `fix(tui): refreshTable returns a Cmd; forward SetItems' filter cmd`

### Task 2: the success path refreshes what it cannot undo

Finding 5: `finishStart`'s `msg.err == nil` branch returns `proceedToLaunch()` without a
refresh, so a start that succeeds and then fails to launch (a session-check or launch
error) lands back on a picker that still says `RUNNING=-` for a model that is now serving.

`proceedToLaunch`'s three bail paths each `return m, nil` after setting `m.status`, while a
real launch returns `m.launchAndRecord(cmd)`'s non-nil cmd. Fix at the source: the bail
paths return `m.refreshTable()` — tied to the actual condition, no nil-cmd inference.

- [x] **Step 1: Write the failing test** — `TestSuccessfulStartThenFailedLaunchRefreshesTable`
      (`start_flow_test.go`): script `startModel` to succeed, make the launch fail
      (`launchAgent` seam / a fixture that errors), assert the returned model has a non-nil
      cmd and that executing it probes `runInventory` — the user must see the model as
      running rather than being told `RUNNING=-`.
- [x] **Step 2: Run to verify failure** — `go test ./internal/tui -run TestSuccessfulStartThenFailedLaunchRefreshesTable -v` — Expected: FAIL.
- [x] **Step 3: Implement** (`app.go`) — the `no model selected`, `session check failed`
      and `launch failed` bail paths in `proceedToLaunch` return `m.refreshTable()`.
- [x] **Step 4: Run to verify pass** — as Task 1 Step 5.
- [x] **Step 5: Commit** — `fix(tui): refresh the picker when a started model fails to launch`

### Task 3: ctrl+c is the only escape while cancelling

Finding 2, per the adjudicated decision above.

- [x] **Step 1: Rewrite the test** — `TestKeysDuringStartCancelThenQuit` becomes
      `TestKeysDuringStartCancelThenOnlyCtrlCQuits`, with an updated `//` comment stating the
      new invariant: esc/q cancel and are then ignored (teardown is draining), ctrl+c cancels
      on the first press and quits on the second. Assert: first esc/q press cancels;
      a second esc/q press returns a **nil** cmd and stays in `phaseStarting`; ctrl+c
      twice yields `tea.QuitMsg`; the engine's done message still returns to the picker with
      status `cancelled`.
- [x] **Step 2: Run to verify failure** — `go test ./internal/tui -run TestKeysDuringStartCancelThenOnlyCtrlCQuits -v` — Expected: FAIL.
- [x] **Step 3: Implement** (`start_flow.go`) — see the contribution request below;
      update `startingView`'s cancelling branch to advertise only `[ctrl+c] quit wt` and
      say teardown is in progress.
- [x] **Step 4: Amend the spec** — `docs/superpowers/specs/2026-09-20-wt-tui-start-flow-design.md`
      lines ~79-81 and ~117-118: esc/q/ctrl+c cancel; while cancelling esc/q are ignored and
      ctrl+c quits; the further press to quit is otherwise available from the picker. Update
      the `wt/CLAUDE.md` TUI paragraph to match.
- [x] **Step 5: Run to verify pass** — as Task 1 Step 5.
- [x] **Step 6: Commit** — `fix(tui): ctrl+c is the only escape while a start is cancelling`

### Task 4: pin `AllowReplace` on the first call

Finding 3: `!calls.at(0).opts.AllowReplace == true` parses as `(!AllowReplace) == true` and
the nested `if calls.len() != 1` means the branch can never report it — flippin
`beginStart(highlighted, true)` still passes.

- [x] **Step 1: Fix the assertion** (`start_flow_test.go:576`) — flatten to
      `if calls.len() != 1 { … }` then `if calls.at(0).opts.AllowReplace { t.Errorf(…) }`,
      and update the enclosing test's `//` comment to say it pins that the first call
      refuses to replace a running occupant.
- [x] **Step 2: Verify it actually fails when violated** — temporarily change `app.go:448`
      to `m.beginStart(highlighted, true)`, run
      `go test ./internal/tui -run TestEndToEndNonRunningOmlxRowStartsThenLaunches -v`,
      confirm it FAILS, then revert the flip. **A fix that cannot fail is not a fix.**
- [x] **Step 3: Run to verify pass** — as Task 1 Step 5.
- [x] **Step 4: Commit** — landed as `fix(tui): make the end-to-end AllowReplace assertion real`
      (`9a52f90`); the old form was additionally restored against the flipped code to confirm it
      still passed, which is the empirical proof that finding 3 was vacuous rather than merely
      suspicious.

**Tasks 1-4 are done:** `efa4b9b` (Tasks 1+2), `dec112f` (finding 5's `proceedToLaunch` bail
paths), `d71ac1f` (Task 3), `9a52f90` (Task 4). All four are green on
`go test -race ./internal/tui`.


### Task 5: picker footer, stale comments, dead code

Findings 6 and 7.

- [x] **Step 1: Footer** — `model_list.go:180`: the hint must distinguish the row actions,
      not claim Enter always launches. Replace with wording naming both, e.g.
      `[↑/↓] navigate   [enter] launch or start   [q] quit`; update the surrounding comment
      and the `agent_model_test.go:311` assertion in the same commit.
- [x] **Step 2: Comments** — `app.go:446` ("until Task 4's flip the legacy hint is still
      rendered on exactly these rows") and `model_list.go:43-48` ("blocked … e.g. a
      non-running local model") describe behavior this PR removed: a non-running local model
      is a `start` row, not a `blocked` one. Rewrite both to the current rule.
- [x] **Step 3: Dead code** — `handleStartMsg` returns `(model, tea.Cmd, bool)` whose `ok` is
      unreachable: `Update` (app.go:174) routes only the three start message types, and every
      arm returns `true`. Drop `ok` and the trailing `return m, nil, false`; update `Update`
      and any test caller.
- [x] **Step 4: Run to verify pass, then commit** —
      `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .` and from the
      repo root `make check-links` — Expected: all pass; `gofmt -l` prints nothing.
      Commit: `docs(tui): picker footer and stale comments; drop handleStartMsg's dead ok`

---

## Final verification (from `wt/`)

```bash
go build ./... && go vet ./... && go test -count=1 ./... \
  && go test -race ./internal/tui -count=1 && gofmt -l .
```

Expected: all pass; `gofmt -l` prints nothing. Then, from the repo root:
`make check-links && make lint`. Do **not** push or open a PR — the user controls that
(global CLAUDE.md).

## Self-Review

- Finding 1's fix must be *demonstrated*, not asserted: the pre-fix reproduction
  (`VisibleItems()==0` after refresh under an applied filter) is the test that must go red
  before Task 1 Step 3 and green after.
- Task 4 Step 2 is a mandatory falsification step, not optional polish.
- Tasks 1 and 2 both touch `finishStart`; Task 1 commits first so Task 2 rebases on the new
  `refreshTable` signature.
- Each finding in the review maps to exactly one task; nothing in the review is dropped.
