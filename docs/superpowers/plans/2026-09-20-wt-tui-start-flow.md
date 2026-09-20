# wt: TUI start-on-select flow — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enter on a non-running local row starts the model through `lifecycle.Start` with live progress, a cancel key, and a replace-confirm dialog, then launches — instead of showing a `modelman start` hint.

**Architecture:** Row semantics become a three-way `action()` (launch / start / block). The start runs as a `tea.Cmd` streaming stage messages through a channel, in two new phases (`phaseStarting`, `phaseReplaceConfirm`) that reuse the repo's message + `list` choice-dialog patterns. The engine's own occupant check is reused (first call `AllowReplace: false`, confirm, second call `AllowReplace: true`). The table is rebuilt from a fresh `Inventory` after any start attempt that returns to the picker.

**Tech Stack:** Go 1.26 (module root `wt/`), Bubble Tea + Bubbles `list`, lipgloss, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-09-20-wt-tui-start-flow-design.md`

## Global Constraints

- `wt` writes nothing to modelman-owned state and never restarts the LiteLLM proxy. The engine (`internal/lifecycle`) is not modified by this plan.
- Row actions: **launch** = cloud rows and running local rows; **start** = a non-running local row of family ollama/omlx/mtplx whose status is not `absent` (`ok`, `new` or `unknown`); **block** = everything else (`absent` rows: "not on disk — pull or download it first"; providers with no start engine such as `mlx_lm_server`: the existing `modelman start` hint; a discovered model under LiteLLM routing: the existing "not in LiteLLM" message). Pulled ollama models are **start**, not a launch exception.
- The single-row auto-launch shortcut applies only to a row whose action is **launch**.
- Start flow: first call `lifecycle.Start` with `AllowReplace: false`. `*OccupiedError` and `*OccupancyUnknownError` open `phaseReplaceConfirm` (choices in this order: "Cancel" then "Replace and start", so the default cursor is Cancel); confirming re-issues `Start` with `AllowReplace: true`.
- Keys during `phaseStarting`: esc, q and ctrl+c all CANCEL (first press: `cancelling` state, the picker returns when the engine reports done); a further press while cancelling quits the program. Nothing else is handled.
- Results: success → `proceedToLaunch` (skip the ollama availability check: the model just loaded); cancelled → status `cancelled`; `*DaemonDownError` → `"<provider> is not answering at <origin> — start it first"`; `*BinaryMissingError` / `*PortBusyError` → the engine's own message; any other error → `"failed to start <model id>: <error>"`. After cancel or any failure other than the two confirm cases, rebuild the table from a fresh `runInventory` keeping the cursor on the same model id.
- Stale messages (wrong run id) are ignored.
- The non-TUI path, `-M` pin flow, `wt smoke`'s picker behavior and the launch/resume flow after a successful start are unchanged.
- Test seam: `var startModel = lifecycle.Start`; the package `TestMain` must stub it so no test can start a real process.
- Every `Test*` needs a top-level `//` comment stating what it tests and why it matters (repo rule, `wt/CLAUDE.md`).
- Run Go commands from `wt/`. Run `go vet ./...` and `gofmt -l .` before each commit (wt-ci gates on gofmt).
- Commit messages during execution end with `- completes plan item #N` and a `Co-Authored-By:` trailer naming the model that authored the commit.
- Execute in an isolated worktree (superpowers:using-git-worktrees), never with `main` checked out in a linked worktree. Do not push or open a PR without asking.

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/tui/modelrows.go` | Modify: add `rowAction`, `action()`, `blockReason()`; later remove `launchable()`/`notLaunchableHint()` |
| `wt/internal/tui/modeltable.go` | Modify: set `start`/`blocked` from `action()` (Task 4) |
| `wt/internal/tui/model_list.go` | Modify: `modelItem.start` |
| `wt/internal/tui/app.go` | Modify: phases, model fields, message + key handling, views, Enter wiring, table build extraction |
| `wt/internal/tui/start_flow.go` | Create: start state, messages, `runStart`, handlers, views, messages mapping |
| `wt/internal/tui/testhelpers_test.go` | Modify: `TestMain` stub for `startModel` |
| `wt/internal/tui/start_flow_test.go` | Create: flow tests |
| existing tui tests | Modify per Task 4's disposition table |
| `wt/CLAUDE.md` | Modify: docs |

---

### Task 1: Row actions (pure)

**Files:**
- Modify: `wt/internal/tui/modelrows.go`
- Test: `wt/internal/tui/modelrows_test.go`

**Interfaces:**
- Consumes: `tableRow`, `rowStatus` (`statusOK/Absent/Unknown/New`), `localmodels.Family`, `localgate.NotRunningError`.
- Produces:
  - `type rowAction int` with `actionLaunch`, `actionStart`, `actionBlock`
  - `func (r tableRow) action() rowAction`
  - `func (r tableRow) blockReason() string` — `""` unless `action() == actionBlock`
  - `launchable()` and `notLaunchableHint()` stay for now (removed in Task 4).

- [ ] **Step 1: Write the failing tests** (append to `modelrows_test.go`; reuse its imports)

```go
// TestRowActionRules verifies what Enter does per row kind: cloud and running
// local rows launch; a non-running local row of a provider wt can start
// (ollama, omlx, mtplx) starts — including a pulled ollama model, a discovered
// row and an unknown-presence row; an absent row and a provider with no start
// engine (mlx_lm_server) are blocked. Getting this wrong either launches a dead
// model or hides one wt could have started.
func TestRowActionRules(t *testing.T) {
	local := func(provider string, status rowStatus, running, discovered bool) tableRow {
		return tableRow{location: config.LocationLocal, status: status, running: running, discovered: discovered, model: config.Model{ID: provider + "/x", ProviderID: provider}}
	}
	cases := []struct {
		name string
		row  tableRow
		want rowAction
	}{
		{"cloud", tableRow{location: config.LocationCloud}, actionLaunch},
		{"running omlx", local("omlx", statusOK, true, false), actionLaunch},
		{"idle omlx on disk", local("omlx", statusOK, false, false), actionStart},
		{"idle mtplx discovered", local("mtplx", statusNew, false, true), actionStart},
		{"idle ollama pulled", local("ollama", statusOK, false, false), actionStart},
		{"idle ollama unknown presence", local("ollama", statusUnknown, false, false), actionStart},
		{"absent omlx", local("omlx", statusAbsent, false, false), actionBlock},
		{"absent ollama", local("ollama", statusAbsent, false, false), actionBlock},
		{"mlx_lm_server has no start engine", local("mlx_lm_server", statusOK, false, false), actionBlock},
		{"omlx-6bit shares omlx's engine", local("omlx-6bit", statusOK, false, false), actionStart},
	}
	for _, tc := range cases {
		if got := tc.row.action(); got != tc.want {
			t.Errorf("%s: action = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBlockReasonNamesTheFix verifies the status text for a blocked row names
// what to do: pull/download for an absent model, `modelman start <id>` for a
// provider wt cannot start, and "" for rows that are not blocked.
func TestBlockReasonNamesTheFix(t *testing.T) {
	absent := tableRow{location: config.LocationLocal, status: statusAbsent, model: config.Model{ID: "omlx/a", ProviderID: "omlx"}}
	if h := absent.blockReason(); !strings.Contains(h, "omlx/a") || !strings.Contains(h, "not on disk") {
		t.Errorf("absent reason = %q", h)
	}
	mlx := tableRow{location: config.LocationLocal, status: statusOK, model: config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server"}}
	if h := mlx.blockReason(); !strings.Contains(h, "modelman start mlx_lm_server/p") {
		t.Errorf("mlx_lm_server reason = %q", h)
	}
	if h := (tableRow{location: config.LocationCloud}).blockReason(); h != "" {
		t.Errorf("cloud reason = %q, want empty", h)
	}
	if h := (tableRow{location: config.LocationLocal, status: statusOK, model: config.Model{ProviderID: "omlx"}}).blockReason(); h != "" {
		t.Errorf("startable row reason = %q, want empty", h)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/tui -run 'RowActionRules|BlockReasonNamesTheFix' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement** (add to `modelrows.go`; keep `launchable`/`notLaunchableHint`)

```go
// rowAction is what Enter does on a row.
type rowAction int

const (
	actionLaunch rowAction = iota // cloud row, or a local model that is already running
	actionStart                   // a local model wt can start (its provider has a lifecycle backend)
	actionBlock                   // cannot proceed; blockReason says why
)

// action reports what Enter does on r. A non-running local row is startable
// when its provider has a lifecycle backend (ollama, omlx/omlx-6bit, mtplx)
// and the model is not known to be missing from disk.
func (r tableRow) action() rowAction {
	if r.location != config.LocationLocal || r.running {
		return actionLaunch
	}
	switch localmodels.Family(r.model.ProviderID) {
	case "ollama", "omlx", "mtplx":
		if r.status == statusAbsent {
			return actionBlock
		}
		return actionStart
	}
	return actionBlock
}

// blockReason is the status line for an actionBlock row ("" otherwise): a
// model missing from disk needs pulling; a provider wt cannot start needs
// modelman.
func (r tableRow) blockReason() string {
	if r.action() != actionBlock {
		return ""
	}
	switch localmodels.Family(r.model.ProviderID) {
	case "ollama", "omlx", "mtplx":
		return fmt.Sprintf("%s is not on disk — pull or download it first", r.model.ID)
	}
	return (&localgate.NotRunningError{ModelID: r.model.ID}).Error()
}
```
Add the `strings` import to the test file if missing.

- [ ] **Step 4: Run to verify pass** — `go build ./... && go vet ./... && go test -count=1 ./...` — Expected: PASS (nothing else changed).

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/tui
git commit -m "feat(tui): row actions - launch, start, block - completes plan item #1"
```

---

### Task 2: Extract the table build; add `refreshTable`

**Files:**
- Modify: `wt/internal/tui/app.go` (`enterModelPhase`; model fields)
- Test: `wt/internal/tui/table_refresh_test.go` (create)

**Interfaces:**
- Consumes: `buildTable`, `tableInput`, `runInventory`, `rotation`, `survey.AgentModelStats`, `newUsageStore`, `newRefcountStore`, `styleTableTitle`.
- Produces:
  - `model.tableModels []config.Model` — the (post-gate) models the current table was built from, set by `enterModelPhase`
  - `func (m model) tableFor(agent string, models []config.Model, snap *localmodels.Snapshot, rot *rotation.Rotation) (modelTable, lastID string)` — the exact block `enterModelPhase` builds today (rotation last id from `rot`, survey stats, `buildTable` with `hideDiscovered` from `m.activeTags/m.activeFamily`); `enterModelPhase` passes its own `rot` (so the config dir is scanned once), `refreshTable` passes `rotation.New()`
  - `func (m model) refreshTable() model` — re-probes `runInventory`, rebuilds the items in place (`SetItems`, title), keeps the cursor on the same model id when it still exists; a no-op when `tableModels` is empty.

- [ ] **Step 1: Write the failing test** (`table_refresh_test.go`; use the same fixtures as `TestEnterModelPhaseShowsNonRunningAndDiscoveredRows` in `modeltable_flow_test.go` — read it first: it builds a config with an omlx provider/model, calls `stubInventory`, and runs `model{cfg:…, width:80, height:24}.enterModelPhase(...)`)

```go
// TestRefreshTableReprobesAndKeepsCursor verifies refreshTable re-runs the
// live inventory and updates each row's RUNNING state in place while keeping
// the cursor on the same model id. After a failed replace the old occupant is
// no longer running; a table that still showed it as running would lie.
func TestRefreshTableReprobesAndKeepsCursor(t *testing.T) {
	// Arrange (copy the fixture shape from TestEnterModelPhaseShowsNonRunningAndDiscoveredRows):
	// a cfg with an omlx provider and two local models omlx/a and omlx/b; first
	// stubInventory reports omlx/a running.
	// Act: enterModelPhase; select the omlx/b row; then stubInventory reports
	// nothing running and call refreshTable().
	// Assert: omlx/a's item row.running flips true -> false; the selected item is
	// still omlx/b; runInventory was called once more (count via the stub).
}
```
(Write the body in full using the fixture helpers; count `runInventory` calls in the stub closure.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/tui -run TestRefreshTableReprobesAndKeepsCursor -v` — Expected: FAIL to compile (`refreshTable`, `tableModels` undefined).

- [ ] **Step 3: Implement**

In `app.go` add the `tableModels []config.Model` field to `model`. Extract from `enterModelPhase` the block that computes `rot`/`lastID`/`surveyStats`/`tbl` into:
```go
// tableFor builds the selector table for agent from models and an inventory
// snapshot, returning the table and the rotation's last-launched id.
func (m model) tableFor(agent string, models []config.Model, snap *localmodels.Snapshot, rot *rotation.Rotation) (modelTable, string) {
	lastID, _ := rot.Last()
	surveyStats := survey.AgentModelStats(newSurveyStore().Events(), agent, survey.Window30d, time.Now().UTC())
	tbl := buildTable(tableInput{
		cfg: m.cfg, agent: agent, models: models, inventory: snap,
		hideDiscovered: m.activeTags != "" || m.activeFamily != "",
		usage:          newUsageStore(), stats: surveyStats,
	}, newRefcountStore(), lastID)
	return tbl, lastID
}
```
`enterModelPhase` keeps its single `rotation.New()` (`rot`) for the cursor logic and passes it to `tableFor`. Set `m.tableModels = models` in `enterModelPhase` right after the (pinned-gate) `models` are final. Add:
```go
// refreshTable re-probes the live inventory and rebuilds the model list in
// place, keeping the cursor on the same model id when it still exists.
func (m model) refreshTable() model {
	if len(m.tableModels) == 0 {
		return m
	}
	sel := ""
	if it, ok := m.models.SelectedItem().(*modelItem); ok {
		sel = it.model.ID
	}
	inv := runInventory(m.cfg)
	tbl, _ := m.tableFor(m.agent, m.tableModels, &inv, rotation.New())
	items := make([]list.Item, len(tbl.items))
	idx := -1
	for i, it := range tbl.items {
		items[i] = it
		if it.model.ID == sel {
			idx = i
		}
	}
	m.models.SetItems(items)
	m.models.Title = tbl.header
	if idx >= 0 {
		m.models.Select(idx)
	}
	return m
}
```

- [ ] **Step 4: Run to verify pass** — `go build ./... && go vet ./... && go test -count=1 ./...` — Expected: PASS (the extraction is behavior-preserving; all existing picker tests still pass unchanged).

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/tui
git commit -m "refactor(tui): extract the table build; add refreshTable - completes plan item #2"
```

---

### Task 3: The start flow — phases, messages, handlers, views

**Files:**
- Create: `wt/internal/tui/start_flow.go`, `wt/internal/tui/start_flow_test.go`
- Modify: `wt/internal/tui/app.go` (phases, fields, `Update`, `View`, Enter/esc wiring), `wt/internal/tui/model_list.go` (`modelItem.start bool`), `wt/internal/tui/testhelpers_test.go` (`TestMain` stub)

**Interfaces:**
- Consumes: `lifecycle.Start`, `lifecycle.Options`, `lifecycle.Target`, `lifecycle.Stage*`, the typed errors, `refreshTable` (Task 2), `proceedToLaunch`, `choiceItem`, `ThemedListDelegate`.
- Produces (all in package `tui`):
  - `var startModel = lifecycle.Start`
  - phases `phaseStarting`, `phaseReplaceConfirm`
  - `model.start *startState`, `model.startRun int`, `model.replace *replaceState`
  - messages `startStageMsg{id int; stage lifecycle.Stage}`, `startDoneMsg{id int; err error}`, `startTickMsg{id int}`
  - `func (m model) beginStart(it *modelItem, allowReplace bool) (model, tea.Cmd)`
  - `func startErrorMessage(id string, err error) string`
  - `modelItem.start bool` (set by Task 4; here the Enter handler already honors it)

- [ ] **Step 1: Write the failing tests** (`start_flow_test.go`)

Fixtures: build the picker with the same helpers `modeltable_flow_test.go` uses (`gateTestConfig()`-style cfg with an omlx model, `stubInventory`, `model{cfg, width: 80, height: 24}.enterModelPhase(...)`), then mark the row to start: `it.start = true` (Task 4 will set this from `action()`; here it is set by hand). Stub `startModel` per test with a fake that records `(Target, Options)` and returns a scripted result; read real messages off `m.start.ch` with a 2-second timeout helper `recvStart(t, m) tea.Msg`, then feed them to `Update`. The package `TestMain` (below) stubs `startModel` to a failing default so an unstubbed test cannot start a process.

Add to `testhelpers_test.go` `TestMain`:
```go
	startModel = func(context.Context, *config.Config, lifecycle.Target, lifecycle.Options) error {
		return errors.New("startModel not stubbed in this test")
	}
```
Write these tests (each with a what/why comment; bodies must be complete):
1. `TestEnterOnStartRowBeginsStart` — Enter on a `start` row enters `phaseStarting`, calls `startModel` once with `Target{ProviderID:"omlx", ModelName:<the row's model name>}` and `AllowReplace == false`; feeding the `startStageMsg` from the channel updates the stage; feeding `startDoneMsg{nil}` calls `proceedToLaunch` (assert `launchModel.ID` equals the row id — `proceedToLaunch` sets it before building the command) and the ollama availability check is NOT invoked (use an ollama row and assert no `phaseOllamaWarn`).
2. `TestOccupiedOpensReplaceConfirmWithCancelDefault` — the fake returns `&lifecycle.OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}`; after `startDoneMsg` the phase is `phaseReplaceConfirm`, the dialog title names `omlx/old`, the choices are `[Cancel, Replace and start]` and the selected index is 0; `startModel` was called once.
3. `TestUnknownOccupancyOpensReplaceConfirm` — `*OccupancyUnknownError{ProviderID:"omlx", Origin:"http://localhost:8000"}` opens the dialog and the title mentions the provider and origin.
4. `TestReplaceConfirmProceedReissuesStartWithAllowReplace` and `TestReplaceConfirmCancelDoesNotStart` — Enter on "Replace and start" enters `phaseStarting` and the second `startModel` call has `AllowReplace == true`; Enter on Cancel (the default) returns to `phaseModel` with no second call; esc in the dialog also returns to `phaseModel`.
5. `TestKeysDuringStartCancelThenQuit` — for each of `esc`, `q`, `ctrl+c`: the first press cancels the context (the fake blocks on `ctx.Done()` and returns `ctx.Err()`), sets `cancelling`, and the view says "Cancelling"; delivering `startDoneMsg{err: context.Canceled}` returns to `phaseModel` with status `cancelled`; a SECOND press while cancelling returns a `tea.Quit` command (its message is `tea.QuitMsg`). Other keys are ignored.
6. `TestStartErrorsMapToMessages` — table test over `startErrorMessage`: `DaemonDownError` → contains `"is not answering at"` and the origin; `BinaryMissingError`, `PortBusyError` → their own `Error()`; a generic error → `"failed to start <id>: <error>"`.
7. `TestStartFailureRefreshesTable` — after a non-confirm failure the phase is `phaseModel`, `status` carries the message, and `runInventory` (counted via the test stub) was called one extra time; a `OccupiedError` (confirm case) does NOT refresh.
8. `TestStaleStartMessagesIgnored` — a `startStageMsg`/`startDoneMsg` with a non-matching `id` changes nothing.
9. `TestStartingViewShowsStageAndElapsed` — `View()` in `phaseStarting` contains the model id, the stage label for the current stage, and the elapsed seconds (set `start.began = time.Now().Add(-12 * time.Second)` and assert `"12s"`), plus `[esc] cancel`; while cancelling it contains `Cancelling`.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/tui -run 'EnterOnStartRow|Occupied|UnknownOccupancy|ReplaceConfirm|KeysDuringStart|StartErrors|StartFailure|StaleStart|StartingView' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement** (`start_flow.go`)

```go
package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startModel is a test seam: production starts the model through the lifecycle
// engine.
var startModel = lifecycle.Start

// startState is the in-flight start: which row, the current stage, when it
// began, how to cancel it, and the channel carrying its messages.
type startState struct {
	id         int
	item       *modelItem
	stage      lifecycle.Stage
	began      time.Time
	cancel     context.CancelFunc
	cancelling bool
	ch         <-chan tea.Msg
}

// replaceState is the replace-confirm dialog for the row that could not start.
type replaceState struct {
	item    *modelItem
	choices list.Model
}

type startStageMsg struct {
	id    int
	stage lifecycle.Stage
}
type startDoneMsg struct {
	id  int
	err error
}
type startTickMsg struct{ id int }

type replaceChoice int

const (
	replaceCancelChoice replaceChoice = iota // first: the default cursor position
	replaceProceedChoice
)

func buildReplaceChoices() []list.Item {
	return []list.Item{
		choiceItem{choice: replaceCancelChoice, title: "Cancel", desc: "Return to the model screen"},
		choiceItem{choice: replaceProceedChoice, title: "Replace and start", desc: "Stop the running model, then start this one"},
	}
}

// runStart runs the engine in a goroutine and streams its stages, then its
// result, on the returned channel (closed after the result).
func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow bool, id int) <-chan tea.Msg {
	ch := make(chan tea.Msg, 16)
	go func() {
		defer close(ch)
		err := startModel(ctx, cfg, t, lifecycle.Options{
			AllowReplace: allow,
			Progress: func(s lifecycle.Stage) {
				select {
				case ch <- startStageMsg{id: id, stage: s}:
				case <-ctx.Done():
				}
			},
		})
		ch <- startDoneMsg{id: id, err: err}
	}()
	return ch
}

// waitForStart turns the next channel message into a tea message; it must be
// re-armed after every stage message until startDoneMsg arrives.
func waitForStart(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func startTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return startTickMsg{id: id} })
}

// beginStart starts it through the engine and enters phaseStarting.
func (m model) beginStart(it *modelItem, allowReplace bool) (model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.startRun++
	id := m.startRun
	ch := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName}, allowReplace, id)
	m.start = &startState{id: id, item: it, began: time.Now(), cancel: cancel, ch: ch}
	m.phase = phaseStarting
	m.status = ""
	return m, tea.Batch(waitForStart(ch), startTick(id))
}

// handleStartKey handles keys in phaseStarting: esc/q/ctrl+c cancel (a further
// press while cancelling quits); every other key is ignored.
func (m model) handleStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		if m.start.cancelling {
			return m, tea.Quit
		}
		m.start.cancelling = true
		m.start.cancel()
	}
	return m, nil
}

// handleStartMsg processes the start flow's messages; ok is false for
// messages that are not the flow's.
func (m model) handleStartMsg(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case startStageMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil, true
		}
		m.start.stage = msg.stage
		return m, waitForStart(m.start.ch), true
	case startTickMsg:
		if m.start == nil || msg.id != m.start.id || m.phase != phaseStarting {
			return m, nil, true
		}
		return m, startTick(msg.id), true
	case startDoneMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil, true
		}
		nm, cmd := m.finishStart(msg)
		return nm, cmd, true
	}
	return m, nil, false
}

func (m model) finishStart(msg startDoneMsg) (model, tea.Cmd) {
	st := m.start
	m.start = nil
	st.cancel()
	back := func(status string) (model, tea.Cmd) {
		m.status = status
		m.phase = phaseModel
		return m.refreshTable(), nil
	}
	if st.cancelling {
		return back("cancelled")
	}
	if msg.err == nil {
		m.phase = phaseModel
		return m.proceedToLaunch()
	}
	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	title := ""
	switch {
	case errors.As(msg.err, &occ):
		title = fmt.Sprintf("Starting %s will stop %s, which is running", st.item.model.ID, occ.Occupant.ModelID)
	case errors.As(msg.err, &unk):
		title = fmt.Sprintf("Cannot tell whether %s at %s is already serving a model", unk.ProviderID, unk.Origin)
	default:
		return back(startErrorMessage(st.item.model.ID, msg.err))
	}
	choices := list.New(buildReplaceChoices(), ThemedListDelegate(m.theme), m.width-2, m.height-2)
	choices.Title = title
	m.replace = &replaceState{item: st.item, choices: choices}
	m.phase = phaseReplaceConfirm
	return m, nil
}

// startErrorMessage is the picker status line for a failed start.
func startErrorMessage(id string, err error) string {
	var down *lifecycle.DaemonDownError
	var bin *lifecycle.BinaryMissingError
	var busy *lifecycle.PortBusyError
	switch {
	case errors.As(err, &down):
		return fmt.Sprintf("%s is not answering at %s — start it first", down.Provider, down.Origin)
	case errors.As(err, &bin), errors.As(err, &busy):
		return err.Error()
	}
	return fmt.Sprintf("failed to start %s: %v", id, err)
}

func stageLabel(s lifecycle.Stage) string {
	switch s {
	case lifecycle.StageStoppingOccupant:
		return "stopping the running model"
	case lifecycle.StageStarting:
		return "starting the server"
	case lifecycle.StageWaiting:
		return "waiting for the model to load"
	case lifecycle.StageWarming:
		return "warming the model"
	}
	return "starting"
}

func (m model) startingView() string {
	elapsed := time.Since(m.start.began).Round(time.Second)
	if m.start.cancelling {
		return fmt.Sprintf("Cancelling %s… (%s)\n\n[esc] quit wt", m.start.item.model.ID, elapsed)
	}
	return fmt.Sprintf("Starting %s — %s (%s)\n\n[esc] cancel", m.start.item.model.ID, stageLabel(m.start.stage), elapsed)
}
```
`app.go` wiring:
- add `phaseStarting` and `phaseReplaceConfirm` to the `phase` consts; add fields `start *startState`, `startRun int`, `replace *replaceState`.
- top of `Update`'s `switch msg := msg.(type)`: `case startStageMsg, startTickMsg, startDoneMsg: if nm, cmd, ok := m.handleStartMsg(msg); ok { return nm, cmd }`.
- top of the `tea.KeyMsg` case, before everything else: `if m.phase == phaseStarting && m.start != nil { return m.handleStartKey(msg) }`.
- `WindowSizeMsg`: resize `m.replace.choices` when `phaseReplaceConfirm`.
- `esc` handling: `phaseReplaceConfirm` → `m.phase = phaseModel; m.status = ""` (same shape as the `phaseOllamaWarn` branch).
- `enter` handling: in `phaseModel`, right after the `blocked` check: `if highlighted.start { return m.beginStart(highlighted, false) }`; add `case phaseReplaceConfirm:` reading `m.replace.choices.SelectedItem().(choiceItem)`: `replaceCancelChoice` → `m.phase = phaseModel; return m, nil`; `replaceProceedChoice` → `return m.beginStart(m.replace.item, true)`.
- the trailing per-phase `Update` delegation: add `phaseReplaceConfirm` → `m.replace.choices, cmd = m.replace.choices.Update(msg)`.
- `View`: `phaseStarting` → `m.startingView()`; `phaseReplaceConfirm` → `m.replace.choices.View() + "\n[enter] choose   [esc] back"` (each with the `width <= 0` "waiting for window size" guard the neighbors use).
- `model_list.go`: add `start bool // Enter starts the model through the lifecycle engine` to `modelItem`.

- [ ] **Step 4: Run to verify pass** — `go build ./... && go vet ./... && go test -count=1 ./... && go test -race ./internal/tui -run 'Start|Replace|Occupied|StartingView' -count=1` — Expected: PASS, no data races.

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/tui
git commit -m "feat(tui): start-on-select flow - phases, progress, cancel, replace confirm - completes plan item #3"
```

---

### Task 4: Switch rows to actions; retire `launchable`; migrate tests

**Files:**
- Modify: `wt/internal/tui/modeltable.go` (`renderTable`), `wt/internal/tui/modelrows.go` (delete `launchable`, `notLaunchableHint`), `wt/internal/tui/app.go` (cursor + single-row shortcut wording)
- Modify tests per the disposition table below.
- Test: new end-to-end tests in `wt/internal/tui/start_flow_test.go`

**Interfaces:**
- Consumes: `tableRow.action()`, `blockReason()`, `modelItem.start`/`blocked`.
- Produces: `renderTable` sets, per row: `actionBlock` → `it.blocked = r.blockReason()`; `actionStart` → `it.start = true` (blocked empty); `actionLaunch` → neither. The discovered-row LiteLLM override still wins (sets `blocked` and clears `start`).

- [ ] **Step 1: Write the failing tests**

End-to-end tests (add to `start_flow_test.go`, driving the real path — stubbed inventory → `enterModelPhase` → Enter → the fake `startModel`):
- `TestEndToEndNonRunningOmlxRowStartsThenLaunches` — omlx model not running in the stub inventory: after `enterModelPhase` the row has `start == true` and `blocked == ""`; Enter enters `phaseStarting`; a success result reaches `proceedToLaunch` (`launchModel.ID`).
- `TestEndToEndPulledOllamaRowStartsInsteadOfLaunchingCold` — a pulled, not-loaded ollama row is `start == true` (the old launch exception is gone) and Enter begins a start (no `ollamacheck` call).
- `TestEndToEndAbsentRowIsBlocked` — an `absent` omlx row: `blocked` contains `not on disk`, `start == false`, Enter shows the status and does not call `startModel`.
- `TestSingleRowShortcutDoesNotFireForStartRow` — one non-running omlx row: `enterModelPhase` lands in `phaseModel` (no auto-launch, no auto-start).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/tui -run 'EndToEnd|SingleRowShortcutDoesNotFire' -v` — Expected: FAIL (rows are not yet start rows).

- [ ] **Step 3: Implement**

`modeltable.go` — replace
```go
		if !r.launchable() {
			it.blocked = r.notLaunchableHint()
		}
```
with
```go
		switch r.action() {
		case actionBlock:
			it.blocked = r.blockReason()
		case actionStart:
			it.start = true
		}
```
and in the discovered-row LiteLLM branch also set `it.start = false` next to `it.blocked = ...`. Delete `launchable()` and `notLaunchableHint()` from `modelrows.go` (drop the `localgate` import only if it becomes unused — `blockReason` still uses it). In `app.go`: the cursor block keeps `it.blocked == ""` (start rows are actionable), rename the local `launchable` slice to `actionable` and update its comment; the single-row shortcut becomes `len(tbl.items) == 1 && tbl.items[0].blocked == "" && !tbl.items[0].start`.

- [ ] **Step 4: Migrate the tests this breaks** — run `go test ./internal/tui` and resolve every failure with these dispositions (each kept test keeps its what/why comment, updated where the described behavior changed; NEVER weaken an assertion; list every change/replacement/deletion with a reason in your report):

| Existing test | Disposition |
|---|---|
| `TestLaunchableRules`, `TestNotLaunchableHint` (`modelrows_test.go`) | DELETE — superseded by Task 1's `TestRowActionRules` / `TestBlockReasonNamesTheFix`. |
| the two assertions using `.launchable()` near `modelrows_test.go:235-241` (unknown-presence / absent rows) | ADAPT to `action()`: the unknown ollama row is `actionStart`; the `omlx/nope` row keeps its meaning under the new rules (read the fixture: absent → `actionBlock`, unknown omlx → `actionStart`). |
| `TestRenderTableBlockedAndMarkers` (`modeltable_test.go`) | ADAPT: the non-running omlx `new` row is `start == true, blocked == ""`; the `absent` omlx row is `blocked != ""`; marker/ref assertions unchanged. |
| `TestRenderTableDiscoveredBlockedUnderLitellm`, `...LitellmUnconfigured` | KEEP (the override still blocks; add `start == false` to the litellm-on assertion). |
| `TestEnterModelPhaseShowsLocalModelsAsNonRunning`, `...MixedRunningState` (`local_gate_test.go`) | ADAPT: non-running omlx rows are `start == true` (not blocked); running rows launchable; counts unchanged. |
| `TestEnterModelPhaseAllLocalNoneRunningShowsBlockedRows` (`modeltable_flow_test.go`) | ADAPT + rename `...ShowsStartableRows`: every row is `start == true`, none blocked, phase `phaseModel`. |
| `TestEnterOnNonRunningRowShowsHintAndDoesNotLaunch` (`modeltable_flow_test.go`) | REPLACE with Task 3's `TestEnterOnStartRowBeginsStart` semantics (delete this one; the hint behavior now lives in `TestEndToEndAbsentRowIsBlocked`). |
| `TestPinnedPathTableReflectsRunningInventory`, `TestEnterModelPhasePinnedStaleLocalModelRejected` | KEEP (pin path unchanged). |
| tests in Task 3 that set `it.start = true` by hand | KEEP (they still exercise the handler directly). |

- [ ] **Step 5: Run to verify pass, then commit** — `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .` — Expected: all pass; `gofmt -l` prints nothing.
```bash
git add wt/internal/tui
git commit -m "feat(tui): rows launch, start or block; pulled ollama rows start - completes plan item #4"
```

---

### Task 5: Documentation

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Edit `wt/CLAUDE.md`**
  - TUI section: phases now include `phaseStarting` and `phaseReplaceConfirm`; describe the start-on-select flow (Enter on a non-running local row of ollama/omlx/mtplx starts it through `internal/lifecycle`, shows stage + elapsed, esc/q/ctrl+c cancels and a further press quits, occupied/unknown-occupancy opens a replace confirm with Cancel as the default, success launches).
  - Local-model section: replace the "non-running rows show a hint / pulled ollama models stay launchable" description with the three row actions (launch / start / block) and `blockReason` cases.
  - Package table: `internal/tui/` row — add `start_flow.go`; `internal/lifecycle/` row — replace "Not yet wired into a launch path" with "consumed by the TUI's start-on-select flow (`internal/tui/start_flow.go`); the non-TUI path, `-M` pin check and `wt smoke` (sub-project 4c) still use `localgate`".
  - Test seams list: add `startModel`; note the package `TestMain` stubs it (as it does `runInventory`).

- [ ] **Step 2: Verify** — from the repo root `make check-links && git grep -n "phaseStarting\|startModel" wt/CLAUDE.md` — Expected: links OK; matches present.

- [ ] **Step 3: Final verification** — from `wt/`: `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .` — Expected: all pass; `gofmt -l` prints nothing.

- [ ] **Step 4: Commit**
```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document the TUI start flow - completes plan item #5"
```

---

## Self-Review

- **Spec coverage:** row actions (launch/start/block, absent and unsupported-provider blocks, pulled ollama = start) -> Tasks 1 and 4; start flow with `AllowReplace:false` first, progress, `phaseStarting`, cancel keys, replace confirm with Cancel default -> Task 3; result mapping and error messages -> Task 3 (`startErrorMessage`); table refresh after failure/cancel -> Tasks 2-3; single-row shortcut only for launch -> Task 4; `startModel` seam + `TestMain` stub -> Task 3; docs -> Task 5. Out-of-scope items (non-TUI, `-M`, `wt smoke`, smoke gate, engine changes) untouched.
- **Ordering rationale:** Task 1 adds `action()` without changing behavior; Task 2 is a behavior-preserving extraction; Task 3 builds and tests the whole flow using hand-set `start` flags (dead in production until Task 4); Task 4 flips the rows over and migrates the tests — so no intermediate commit ships a picker that launches a dead model.
- **Spec-text deviations:** none. One refinement: the first cancel keypress enters a `cancelling` state and the picker returns only when the engine reports done, so a half-started mtplx server is torn down before the UI moves on; a second press quits.
- **Known risk:** Task 4 migrates ~10 existing tests (dispositions given); the Task 3/4 tests rely on a fixture helper pattern from `modeltable_flow_test.go` that the implementer must read first (stated in the task).
- **Placeholders:** Task 2's and Task 3's test bodies are specified by exact assertions rather than full code because they depend on the existing flow-test fixtures (`modeltable_flow_test.go`, `gateTestConfig`); each lists every assertion.
- **Type consistency:** `rowAction`/`action()`/`blockReason()`, `modelItem.start`/`blocked`, `startState`/`replaceState`, `startStageMsg`/`startDoneMsg`/`startTickMsg`, `beginStart`, `refreshTable`, `tableFor`, `startModel`, `startErrorMessage` are named identically wherever used.
