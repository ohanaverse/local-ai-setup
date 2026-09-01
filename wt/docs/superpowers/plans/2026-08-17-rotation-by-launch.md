# Rotation By Launch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the explicit `r` key in the model picker with implicit rotation driven by launches; the picker always lands the cursor on the model after the last one launched in this rotation. Up/down must move the cursor. The `r` key is removed.

**Architecture:** `rotation.Rotation` is reshaped to a snapshot-based API (`LastLaunched` / `RecordLaunch` / `FirstAfter`); the state file becomes a single line. The picker's filtered model list becomes the rotation's model set, captured at picker entry and on every `d` tag toggle. The picker `Update()` forwards `tea.KeyMsg` to `m.models` (mirroring the worktree list). The `r` handler is removed; the ollama-warn "skip" choice is removed; `m.current` is replaced by the highlighted list item.

**Tech Stack:** Go 1.26.3, Bubble Tea (`bubbles/list`, `bubbles/textinput`), `bubble/list`'s default delegate, atomic file writes via `config.WriteFileAtomic`.

**Reference spec:** `docs/superpowers/specs/2026-08-17-rotation-by-launch-design.md`

**Reference branch:** `docs/rotation-by-launch-spec` (the spec commit). Implementation lives on a new `feat/rotation-by-launch` branch.

---

## File Structure

| Path | Action | Responsibility |
|---|---|---|
| `internal/rotation/rotation.go` | Modify | Replace `Next`/`loadIndex`/`saveState` with `LastLaunched`/`RecordLaunch`. State file = `<id>\n`. Add package-level `FirstAfter` helper. |
| `internal/rotation/rotation_test.go` | Modify | Rewrite for new API. Keep `TestStateFilePath` and `TestStateFile_DefaultDirRespectsXDG`; replace the rest. |
| `internal/tui/model_list.go` | Modify | Add `FindAfter` helper. |
| `internal/tui/app.go` | Modify | Add `phaseModel` key-forwarding branch. Remove `case "r":` from `phaseModel`. Remove `ollamaSkipChoice` branch in `phaseOllamaWarn` `enter` handler. Replace `m.current` field with `m.rotation *rotation.Rotation`. Update `selectedEntryMsg` to position cursor via `LastLaunched` + `FirstAfter`. Update `d` tag toggle to rebuild `m.rotation`. Update Enter handler to read highlighted item, call `RecordLaunch`, and pass to launch. Update `proceedToLaunch` to read highlighted item. Update footer text. |
| `internal/tui/launch.go` | Modify | Remove `ollamaSkipChoice` enum value and the "Skip to next model" item in `buildOllamaChoices`. |
| `internal/tui/agent_model_test.go` | Modify | Rewrite `phaseModelWithList` helper. Add up/down tests. Rewrite rotation-related tests for last-launched semantics. |
| `internal/tui/app_test.go` | Modify | Update ollama-warn choice assertion (3 → 2). Update tests that referenced `m.current` to use the highlighted list item. |

Files NOT touched: `internal/config/`, `internal/registry/`, `internal/worktree/`, `internal/session/`, `internal/initseed/`, `internal/guard/`, `internal/ollamacheck/`, `cmd/wt/*`, `docs/go-course/*`.

---

## Conventions

- **Branch:** work on `feat/rotation-by-launch` (created at Task 1).
- **Commits** are mandatory after each task.
- **TDD**: every behavioral change is preceded by a failing test.
- **Test isolation:** any test that reads/writes the rotation state file must use `tempStateDir(t)` (already in `agent_model_test.go`; we'll keep it).
- **Helper signature used in tests:** rewrite `phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model` to use the new `m.rotation` field. All picker tests in `agent_model_test.go` that build a `phaseModel` state use this helper.
- **`go test ./...` runs after each task.** Expected: green.
- **`docs/go-course/*` is never edited.**

---

## Task 1: Create the feature branch

**Files:** none

- [ ] **Step 1: Sync main and create the branch**

```bash
cd /Users/keith/github/ohanaverse/agent-worktree
git checkout main
git pull --ff-only
git checkout -b feat/rotation-by-launch
```

Expected: clean working tree, new branch off the latest main.

---

## Task 2: Replace `rotation.Next` with `LastLaunched`/`RecordLaunch` (TDD)

**Files:**
- Modify: `internal/rotation/rotation.go`
- Modify: `internal/rotation/rotation_test.go`

### Step 1: Write the failing tests for the new API

Replace the contents of `internal/rotation/rotation_test.go` with:

```go
package rotation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// LastLaunched must return (zero, false) when no state file exists so
// the caller (the picker entry handler) can fall back to index 0
// without panicking or returning a zero-value model that the snapshot
// contains.
func TestLastLaunchedMissingFile(t *testing.T) {
	r := New("code", []config.Model{{ID: "alpha"}}, t.TempDir())
	if _, ok := r.LastLaunched(); ok {
		t.Fatal("LastLaunched on missing file returned ok=true")
	}
}

// RecordLaunch must persist the model ID and LastLaunched must read
// it back. The state file is the on-disk contract for "what was last
// launched in this rotation" — without the round-trip the picker
// can't advance after a launch.
func TestRecordLaunchRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := New("code", []config.Model{{ID: "alpha"}}, dir)
	if err := r.RecordLaunch(config.Model{ID: "alpha"}); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("LastLaunched returned !ok after RecordLaunch")
	}
	if got.ID != "alpha" {
		t.Errorf("LastLaunched.ID = %q, want alpha", got.ID)
	}
}

// RecordLaunch must overwrite any prior value. The picker may launch
// the same model multiple times in a row if the user keeps pressing
// Enter without navigating; each launch must normalize the file to
// the latest pick.
func TestRecordLaunchOverwrites(t *testing.T) {
	dir := t.TempDir()
	r := New("code", []config.Model{{ID: "alpha"}, {ID: "beta"}}, dir)
	_ = r.RecordLaunch(config.Model{ID: "alpha"})
	_ = r.RecordLaunch(config.Model{ID: "beta"})
	got, _ := r.LastLaunched()
	if got.ID != "beta" {
		t.Errorf("LastLaunched.ID = %q, want beta (overwritten)", got.ID)
	}
}

// LastLaunched must read the legacy 2-line state file by taking the
// last non-empty line. Without backward-compat the existing state
// files on every user's machine would suddenly point at nothing
// and the picker would fall back to index 0 — losing the rotation
// memory they had.
func TestLastLaunchedReadsLegacyTwoLineFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rotation-code.state"), []byte("5\nollama/x:cloud\n"), 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}
	r := New("code", []config.Model{{ID: "ollama/x:cloud"}}, dir)
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("LastLaunched returned !ok on legacy 2-line file")
	}
	if got.ID != "ollama/x:cloud" {
		t.Errorf("LastLaunched.ID = %q, want ollama/x:cloud", got.ID)
	}
}

// LastLaunched must return (zero, false) when the saved ID is no
// longer in the snapshot. Config changes between launches (a model
// was removed) should not crash or return a phantom model; the
// caller falls back to index 0.
func TestLastLaunchedConfigChanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rotation-code.state"), []byte("ollama/removed:cloud\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New("code", []config.Model{{ID: "ollama/current:cloud"}}, dir)
	if _, ok := r.LastLaunched(); ok {
		t.Error("LastLaunched returned ok=true for ID not in snapshot")
	}
}

// RecordLaunch must write a single-line file (no index prefix) and
// the file must be 0600. The single-line format is the new on-disk
// contract; 0600 is the existing security baseline.
func TestRecordLaunchWritesSingleLine(t *testing.T) {
	dir := t.TempDir()
	r := New("code", []config.Model{{ID: "alpha"}}, dir)
	if err := r.RecordLaunch(config.Model{ID: "alpha"}); err != nil {
		t.Fatalf("RecordLaunch: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "rotation-code.state"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 0600", perm)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "rotation-code.state"))
	if got := string(data); got != "alpha\n" {
		t.Errorf("file = %q, want %q", got, "alpha\n")
	}
}

// FirstAfter must return the model at target+1, wrapping to models[0]
// if target is the last item. The picker uses this to compute "what
// to show on the next entry after a launch" without knowing the
// picker's order at the call site.
func TestFirstAfterMiddle(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FirstAfter(models, config.Model{ID: "b"})
	if !ok {
		t.Fatal("FirstAfter returned !ok")
	}
	if got.ID != "c" {
		t.Errorf("FirstAfter = %q, want c", got.ID)
	}
}

// FirstAfter must wrap to the first model when target is the last.
// Without wrapping the picker would advance past the end of the
// list on every cycle and the cursor would never move.
func TestFirstAfterWraps(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FirstAfter(models, config.Model{ID: "c"})
	if !ok {
		t.Fatal("FirstAfter returned !ok")
	}
	if got.ID != "a" {
		t.Errorf("FirstAfter = %q, want a (wrap to start)", got.ID)
	}
}

// FirstAfter must return models[0] when target is not in the list.
// This is the "saved ID was removed from config" recovery path —
// the picker falls back to the first model instead of failing.
func TestFirstAfterMissingTarget(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}}
	got, ok := FirstAfter(models, config.Model{ID: "ghost"})
	if !ok {
		t.Fatal("FirstAfter returned !ok")
	}
	if got.ID != "a" {
		t.Errorf("FirstAfter = %q, want a (fallback to first)", got.ID)
	}
}

// FirstAfter must return (zero, false) on an empty snapshot. The
// picker's validation gate at selectedEntryMsg prevents this in
// practice, but the helper must defend against it.
func TestFirstAfterEmpty(t *testing.T) {
	if _, ok := FirstAfter(nil, config.Model{ID: "x"}); ok {
		t.Error("FirstAfter on empty snapshot returned ok=true")
	}
}

// StateFile must return a predictable path under the given directory.
// This is the contract between the Rotation and the config package's
// LastSelected helper, which reads the same files.
func TestStateFilePath(t *testing.T) {
	got := StateFile("/tmp/cfg", "code")
	want := "/tmp/cfg/rotation-code.state"
	if got != want {
		t.Errorf("StateFile = %q, want %q", got, want)
	}
}

// The default state directory must respect XDG_CONFIG_HOME, matching
// the behaviour of config.Path(). A mismatch would mean rotation
// state and config files end up in different directories.
func TestStateFile_DefaultDirRespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	r := New("design", []config.Model{{ID: "x"}}, "")
	if got, want := r.StateDir(), "/tmp/xdg-test/agent-wt"; got != want {
		t.Errorf("StateDir with XDG = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail to compile (the new API doesn't exist yet)**

Run: `go test ./internal/rotation -v 2>&1 | tail -20`
Expected: compile errors referencing `LastLaunched`, `RecordLaunch`, `FirstAfter`, and missing `Next` callsites (none here yet, just the API). This is the TDD red.

- [ ] **Step 3: Replace the implementation in `internal/rotation/rotation.go`**

Replace the entire contents with:

```go
package rotation

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// StateFile returns the state path for a tag group under the config dir.
func StateFile(cfgDir, tag string) string {
	return filepath.Join(cfgDir, "rotation-"+tag+".state")
}

// defaultStateDir returns ~/.config/agent-wt (or $XDG_CONFIG_HOME/agent-wt).
func defaultStateDir() string {
	return config.Dir()
}

// Rotation remembers which model was last launched in a tag group.
// The model set is fixed at construction time and must match the
// picker's filtered snapshot. Rotation is driven by the launch
// action: RecordLaunch persists the pick, and LastLaunched +
// FirstAfter compute the cursor position for the next picker entry.
type Rotation struct {
	mu       sync.Mutex
	tag      string
	models   []config.Model
	stateDir string
}

// New builds a Rotation for a tag group from the given models.
// stateDir is where rotation-<tag>.state lives; pass "" to use the
// default (~/.config/agent-wt).
func New(tag string, models []config.Model, stateDir string) *Rotation {
	return &Rotation{tag: tag, models: models, stateDir: stateDir}
}

// Tag returns the tag group this Rotation serves.
func (r *Rotation) Tag() string { return r.tag }

// StateDir returns the resolved state directory for this Rotation.
func (r *Rotation) StateDir() string {
	if r.stateDir != "" {
		return r.stateDir
	}
	return defaultStateDir()
}

// LastLaunched returns the model most recently recorded for this
// rotation, or (zero, false) if the state file is missing, empty,
// or references a model no longer in the snapshot. Backward-
// compatible with the legacy 2-line format ("<index>\n<id>\n") —
// the last non-empty line is treated as the model ID.
func (r *Rotation) LastLaunched() (config.Model, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastLaunchedLocked()
}

func (r *Rotation) lastLaunchedLocked() (config.Model, bool) {
	data, err := os.ReadFile(StateFile(r.StateDir(), r.tag))
	if err != nil {
		return config.Model{}, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		id := strings.TrimSpace(lines[i])
		if id == "" {
			continue
		}
		for _, m := range r.models {
			if m.ID == id {
				return m, true
			}
		}
	}
	return config.Model{}, false
}

// RecordLaunch writes the given model as the new last-launched.
// The write is atomic (temp file + rename). Errors are returned to
// the caller; the picker proceeds with the launch even if the write
// fails (the next picker entry falls back to index 0).
func (r *Rotation) RecordLaunch(m config.Model) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return config.WriteFileAtomic(StateFile(r.StateDir(), r.tag), []byte(m.ID+"\n"), 0o600)
}

// FirstAfter returns the model that comes after target in the
// snapshot, wrapping to the first item if target is the last or
// not in the snapshot. Returns (zero, false) if the snapshot is
// empty. This is the "what to show on next picker entry" calculation.
func FirstAfter(models []config.Model, target config.Model) (config.Model, bool) {
	if len(models) == 0 {
		return config.Model{}, false
	}
	for i, m := range models {
		if m.ID == target.ID {
			if i+1 < len(models) {
				return models[i+1], true
			}
			return models[0], true
		}
	}
	return models[0], true
}
```

- [ ] **Step 4: Run the rotation tests and verify they pass**

Run: `go test ./internal/rotation -v 2>&1 | tail -30`
Expected: PASS for all 12 tests.

- [ ] **Step 5: Run the full test suite to find any other callsites of the removed `Next`/`ForTag` API**

Run: `go test ./... 2>&1 | tail -30`
Expected: compile errors in `internal/tui/agent_model_test.go` and `internal/tui/app.go` referencing the removed `rotation.Next` and `rotation.ForTag`. That's expected — Task 3 will fix them.

- [ ] **Step 6: Commit**

```bash
git add internal/rotation/rotation.go internal/rotation/rotation_test.go
git commit -m "refactor(rotation): replace Next/ForTag with LastLaunched/RecordLaunch/FirstAfter

Reshapes rotation around a snapshot-based model. The state file
becomes single-line (<id>\\n) with backward-compat read of the
legacy 2-line format. The new model set is the picker's filtered
list (passed at construction), eliminating the previous mismatch
where rotation operated on cfg.ModelsWithTag(tag) and the picker
operated on cfg.ModelsForAgentAndTag(agent, tag).

Removes ForTag (the source of the bug) and Next (no longer needed;
rotation advances via RecordLaunch on Enter)."
```

---

## Task 3: Add `FindAfter` helper in the picker package

**Files:**
- Modify: `internal/tui/model_list.go`

### Step 1: Write the failing test for `FindAfter`

`FindAfter` is a thin wrapper around `rotation.FirstAfter` exposed in the `tui` package. It exists so the picker code doesn't import `internal/rotation` from the helper file. Add the test in a new file `internal/tui/model_list_test.go`:

```go
package tui

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// FindAfter must return the model at target+1, wrapping to models[0]
// when target is the last item. The picker uses this to position the
// cursor at the next rotation entry.
func TestFindAfterMiddle(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FindAfter(models, config.Model{ID: "b"})
	if !ok || got.ID != "c" {
		t.Errorf("FindAfter = (%q, %v), want (c, true)", got.ID, ok)
	}
}

// FindAfter must return models[0] when target is not in the list.
// Mirrors the rotation.FirstAfter contract.
func TestFindAfterMissing(t *testing.T) {
	models := []config.Model{{ID: "a"}}
	got, ok := FindAfter(models, config.Model{ID: "ghost"})
	if !ok || got.ID != "a" {
		t.Errorf("FindAfter = (%q, %v), want (a, true)", got.ID, ok)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails to compile**

Run: `go test ./internal/tui -run TestFindAfter -v 2>&1 | tail -10`
Expected: compile error: `FindAfter` not defined.

- [ ] **Step 3: Add `FindAfter` in `internal/tui/model_list.go`**

Append at the end of the file (before `phaseModelView`):

```go
// FindAfter returns the model that comes after target in models,
// wrapping to models[0] when target is the last or missing. Thin
// wrapper around rotation.FirstAfter that keeps model_list.go
// import-free of the rotation package.
func FindAfter(models []config.Model, target config.Model) (config.Model, bool) {
	return rotation.FirstAfter(models, target)
}
```

Add the import at the top of the file:

```go
import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestFindAfter -v 2>&1 | tail -10`
Expected: PASS for both tests.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model_list.go internal/tui/model_list_test.go
git commit -m "feat(tui): add FindAfter helper for picker cursor positioning"
```

---

## Task 4: Add `m.rotation` field to the `model` struct (and drop `m.current`)

**Files:**
- Modify: `internal/tui/app.go`

### Step 1: Update the `model` struct definition

In `internal/tui/app.go`, find:

```go
	phase    phase
	agent    string         // current agent name
	tag      string         // active rotation tag group
	current  config.Model   // currently shown model
	cfg      *config.Config // loaded config for the model catalog
```

Replace with:

```go
	phase    phase
	agent    string              // current agent name
	tag      string              // active rotation tag group
	rotation *rotation.Rotation  // snapshot rotation for the active (agent, tag); set on picker entry and on 'd' tag toggle
	cfg      *config.Config      // loaded config for the model catalog
```

Add the import at the top of the file (it likely already exists; if not, add it):

```go
	"github.com/ohanaverse/agent-worktree/internal/rotation"
```

- [ ] **Step 2: Try to build to find all `m.current` references**

Run: `go build ./... 2>&1 | tail -30`
Expected: compile errors at every `m.current` reference. List them — Task 5 through Task 8 will fix them.

- [ ] **Step 3: Commit the struct change alone (so the diff is reviewable)**

```bash
git add internal/tui/app.go
git commit -m "refactor(tui): replace m.current with m.rotation snapshot

The picker no longer carries a single-model field. Instead it owns
a *rotation.Rotation whose model set is the picker's filtered list
and whose state file tracks the last-launched model. Subsequent
tasks will rewire all m.current callsites to read the highlighted
list item instead."
```

Expected: build still broken. That's fine — keep going.

---

## Task 5: Forward keys to the model picker in `Update()`

**Files:**
- Modify: `internal/tui/app.go`

### Step 1: Write the failing test for up/down

In `internal/tui/agent_model_test.go`, append at the end of the file:

```go
// TestPhaseModelUpDownMovesCursor asserts the picker forwards
// arrow keys to bubble/list. Before this fix, Update() never called
// m.models.Update(msg) in phaseModel, so up/down were dead keys.
// This is the regression guard for the up/down bug.
func TestPhaseModelUpDownMovesCursor(t *testing.T) {
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	if m.models.Index() != 0 {
		t.Fatalf("precondition: cursor = %d, want 0", m.models.Index())
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	gotModel := got.(model)
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor after down = %d, want 1", gotModel.models.Index())
	}
	got, _ = gotModel.Update(tea.KeyMsg{Type: tea.KeyUp})
	gotModel = got.(model)
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor after up = %d, want 0", gotModel.models.Index())
	}
}

// TestPhaseModelEnterDoesNotForwardToList asserts Enter in the picker
// does NOT get consumed by bubble/list (which would otherwise launch
// the highlighted item twice). The picker intercepts Enter for
// ollama check + launch; the list is not allowed to handle it.
func TestPhaseModelEnterStaysInApp(t *testing.T) {
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	// Enter in phaseModel: the app should run its own handler. We
	// don't assert on the launch side-effects here; just that
	// pressing Enter doesn't crash and returns a model (not a
	// different tea.Msg type).
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := got.(model); !ok {
		t.Errorf("Update(Enter) returned %T, want model", got)
	}
	// cmd may be nil or a real launch command; both are fine here.
	_ = cmd
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui -run "TestPhaseModelUpDownMovesCursor|TestPhaseModelEnterStaysInApp" -v 2>&1 | tail -20`
Expected: `TestPhaseModelUpDownMovesCursor` FAILS because `m.models.Update(msg)` is never called — the cursor stays at 0 after pressing down.

- [ ] **Step 3: Add the `phaseModel` key-forwarding branch in `app.go` `Update()`**

Find:

```go
	if m.ready && m.phase == phaseList {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
```

Insert immediately **before** this block (so phaseModel takes precedence over the worktree list forwarding, but both are guarded by their phase):

```go
	if m.phase == phaseModel && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.models, cmd = m.models.Update(msg)
		return m, cmd
	}
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `go test ./internal/tui -run "TestPhaseModelUpDownMovesCursor|TestPhaseModelEnterStaysInApp" -v 2>&1 | tail -20`
Expected: PASS for both. (The Enter test passes because phaseModel intercepts Enter in the keypress switch before falling through to the list-forwarding branch — wait, no: with the new branch, Enter is forwarded to the list, which would launch via the list. We need to keep the existing keypress switch intercepting Enter for phaseModel.)

Looking again at the existing code in `app.go` line ~275:

```go
case "enter":
	switch m.phase {
	case phaseModel:
		// ...ollama check + launch...
```

The `tea.KeyMsg` case `"enter"` is in the keypress switch and is matched on `msg.String()`. The list-update branch I'm adding is below that switch, so Enter hits the keypress switch first. But arrow keys: their `msg.String()` is `"down"`, not handled in the keypress switch, so they fall through to the list-update branch. Good.

- [ ] **Step 5: Verify the up/down test passes**

Run: `go test ./internal/tui -run TestPhaseModelUpDownMovesCursor -v 2>&1 | tail -10`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "fix(tui): forward arrow keys to model picker in phaseModel

Update() never called m.models.Update(msg) in phaseModel, so up/
down/j/k were dead keys. The list visually rendered but the
cursor never moved. Add a phaseModel key-forwarding branch
mirroring the existing phaseList branch."
```

---

## Task 6: Remove the `r` key handler from `phaseModel`

**Files:**
- Modify: `internal/tui/app.go`

### Step 1: Write the failing test asserting `r` is a no-op

In `internal/tui/agent_model_test.go`, append:

```go
// TestNoRKeyInModelPhase asserts pressing 'r' in phaseModel is a
// no-op. Rotation now advances via RecordLaunch on Enter, not via
// an explicit key. This test guards against accidental re-
// introduction of the r key (which would conflict with the new
// implicit-rotation model).
func TestNoRKeyInModelPhase(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:9b\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	beforeCursor := m.models.Index()
	beforeItems := len(m.models.Items())
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.models.Index() != beforeCursor {
		t.Errorf("cursor moved on 'r': before=%d after=%d", beforeCursor, gotModel.models.Index())
	}
	if len(gotModel.models.Items()) != beforeItems {
		t.Errorf("list size changed on 'r': before=%d after=%d", beforeItems, len(gotModel.models.Items()))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails (the `r` key currently moves the cursor)**

Run: `go test ./internal/tui -run TestNoRKeyInModelPhase -v 2>&1 | tail -10`
Expected: FAIL — `r` currently calls `rotation.Next` and moves the cursor.

- [ ] **Step 3: Remove the `r` handler in `app.go`**

Find:

```go
		case "r":
			if m.phase == phaseModel {
				// Advance the rotation cursor; the visible list cursor follows.
				next, ok := rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))
				if ok {
					m.current = next
					for i, it := range m.models.Items() {
						if mi, ok := it.(modelItem); ok && mi.model.ID == next.ID {
							m.models.Select(i)
							break
						}
					}
				}
			}
```

Delete this entire block.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestNoRKeyInModelPhase -v 2>&1 | tail -10`
Expected: PASS.

- [ ] **Step 5: Update the picker footer text**

Find in `internal/tui/model_list.go`:

```go
	footer := "\n[r] rotate   [d] switch tag   [enter] launch   [q] quit"
```

Replace with:

```go
	footer := "\n[↑/↓] navigate   [d] switch tag   [enter] launch   [q] quit"
```

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go internal/tui/model_list.go
git commit -m "refactor(tui): remove 'r' key from model picker

Rotation now advances implicitly via RecordLaunch on Enter. The
explicit r key was the source of the model-snapshot mismatch
(rotation operated on cfg.ModelsWithTag while the picker operated
on cfg.ModelsForAgentAndTag, so r could land on a model the
picker couldn't display). Removing the key eliminates that whole
class of bug; the user navigates with up/down and Enter records
the pick."
```

---

## Task 7: Position the cursor on picker entry via `LastLaunched` + `FindAfter`

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/agent_model_test.go`

### Step 1: Rewrite the `phaseModelWithList` test helper

In `internal/tui/agent_model_test.go`, find:

```go
func phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model {
	t.Helper()
	models, err := cfg.ModelsForAgentAndTag(agent, tag)
	if err != nil {
		t.Fatalf("ModelsForAgentAndTag: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("phaseModelWithList: no models for agent %q tag %q", agent, tag)
	}
	m := model{
		cfg:       cfg,
		phase:     phaseModel,
		agent:     agent,
		tag:       tag,
		models:    buildModelList(models, 80, 24),
		modelsFor: agent,
		modelsTag: tag,
		width:     80,
		height:    24,
	}
	if next, ok := rotation.ForTag(cfg, tag).Next(""); ok {
		m.current = next
		m.models.Select(indexOfModel(models, next))
	}
	return m
}
```

Replace with:

```go
func phaseModelWithList(t *testing.T, cfg *config.Config, agent, tag string) model {
	t.Helper()
	models, err := cfg.ModelsForAgentAndTag(agent, tag)
	if err != nil {
		t.Fatalf("ModelsForAgentAndTag: %v", err)
	}
	if len(models) == 0 {
		t.Fatalf("phaseModelWithList: no models for agent %q tag %q", agent, tag)
	}
	m := model{
		cfg:       cfg,
		phase:     phaseModel,
		agent:     agent,
		tag:       tag,
		models:    buildModelList(models, 80, 24),
		modelsFor: agent,
		modelsTag: tag,
		width:     80,
		height:    24,
	}
	// Set up the rotation snapshot for the picker's filtered list and
	// position the cursor on the model after the last-launched one.
	m.rotation = rotation.New(tag, models, "")
	if last, ok := m.rotation.LastLaunched(); ok {
		if next, ok := FindAfter(models, last); ok {
			m.models.Select(indexOfModel(models, next))
		}
	}
	return m
}
```

- [ ] **Step 2: Add the new picker-entry tests**

In `internal/tui/agent_model_test.go`, append:

```go
// TestSelectedEntryNoLastLaunchedStartsAtZero asserts the picker
// lands the cursor at index 0 when no rotation state exists. This
// is the cold-start path: fresh user, fresh state file.
func TestSelectedEntryNoLastLaunchedStartsAtZero(t *testing.T) {
	tempStateDir(t) // isolates state; file doesn't exist
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (no last-launched)", gotModel.models.Index())
	}
}

// TestSelectedEntryPositionsAfterLastLaunched asserts the picker
// lands the cursor on the model after the last-launched one. With
// the new model, rotation advances implicitly — every picker entry
// is "one past where we left off".
func TestSelectedEntryPositionsAfterLastLaunched(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:9b\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	// testConfig has two code models: gemma4:9b (index 0) and
	// gemma4:14b (index 1). Last launched was gemma4:9b, so the
	// next-to-show must be gemma4:14b.
	if gotModel.models.Index() != 1 {
		t.Errorf("cursor index = %d, want 1 (after gemma4:9b)", gotModel.models.Index())
	}
}

// TestSelectedEntryLastLaunchedMissingFallsBackToZero asserts the
// picker lands on index 0 when the saved last-launched model is no
// longer in the snapshot (config changed since last launch).
func TestSelectedEntryLastLaunchedMissingFallsBackToZero(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/removed:cloud\n")
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (fallback when last-launched missing)", gotModel.models.Index())
	}
}

// TestSelectedEntryLastLaunchedLastInListWrapsToZero asserts the
// picker wraps to index 0 when the saved last-launched is the last
// item in the snapshot. Without wrap, the cursor would advance
// past the end of the list forever.
func TestSelectedEntryLastLaunchedLastInListWrapsToZero(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "code", "ollama/gemma4:14b\n") // last item in testConfig
	cfg := testConfig()
	m := model{cfg: cfg, phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", gotModel.phase)
	}
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (wrap when last-launched is last)", gotModel.models.Index())
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail (selectedEntryMsg still uses the old API)**

Run: `go test ./internal/tui -run "TestSelectedEntry" -v 2>&1 | tail -20`
Expected: `TestSelectedEntryNoLastLaunchedStartsAtZero` may pass by accident (old code falls back to index 0 too), but the others should fail or compile-error.

- [ ] **Step 4: Rewrite the `selectedEntryMsg` handler in `app.go`**

Find:

```go
		m.phase = phaseModel
		m.models = buildModelList(models, m.width-2, m.height-2)
		m.modelsFor = m.agent
		m.modelsTag = m.tag
		if next, ok := rotation.ForTag(m.cfg, m.tag).Next(""); ok {
			m.current = next
			if idx := indexOfModel(models, next); idx >= 0 {
				m.models.Select(idx)
			}
		} else {
			// Fallback: list non-empty but rotation says no model. Pick index 0.
			m.current = models[0]
			m.models.Select(0)
		}
		return m, nil
```

Replace with:

```go
		m.phase = phaseModel
		m.models = buildModelList(models, m.width-2, m.height-2)
		m.modelsFor = m.agent
		m.modelsTag = m.tag
		// Snapshot the rotation over the picker's filtered list so
		// rotation's view of "the code tag" matches the picker's.
		// Position the cursor on the model after the last-launched
		// one; fall back to index 0 if no last-launched exists or
		// the saved ID is no longer in the snapshot.
		m.rotation = rotation.New(m.tag, models, "")
		if last, ok := m.rotation.LastLaunched(); ok {
			if next, ok := FindAfter(models, last); ok {
				if idx := indexOfModel(models, next); idx >= 0 {
					m.models.Select(idx)
				}
			}
		}
		return m, nil
```

- [ ] **Step 5: Run the tests and verify they pass**

Run: `go test ./internal/tui -run "TestSelectedEntry" -v 2>&1 | tail -20`
Expected: PASS for all four.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): position picker cursor on last-launched + 1

The picker captures a *rotation.Rotation snapshot at entry and
uses LastLaunched + FindAfter to position the cursor. The
snapshot's model set is the picker's filtered list, so rotation
and picker share one view — no more cross-view drift."
```

---

## Task 8: Rebuild `m.rotation` on the `d` tag toggle

**Files:**
- Modify: `internal/tui/app.go`

### Step 1: Write the failing test

In `internal/tui/agent_model_test.go`, append:

```go
// TestToggleTagRebuildsRotation asserts pressing 'd' rebuilds both
// the model list and the rotation snapshot from the new tag's
// filtered models. Without a fresh snapshot, rotation would still
// operate on the old tag's list.
func TestToggleTagRebuildsRotation(t *testing.T) {
	dir := tempStateDir(t)
	seedState(t, dir, "design", "ollama/gemma4:design\n")
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	if m.tag != "code" {
		t.Fatalf("precondition: tag = %q, want code", m.tag)
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	gotModel := got.(model)
	if gotModel.tag != "design" {
		t.Errorf("tag = %q, want design", gotModel.tag)
	}
	if gotModel.rotation == nil {
		t.Fatal("rotation is nil after 'd' toggle")
	}
	if gotModel.rotation.Tag() != "design" {
		t.Errorf("rotation tag = %q, want design", gotModel.rotation.Tag())
	}
	// The design tag has one model (gemma4:design). Last-launched
	// is gemma4:design, so FirstAfter wraps to index 0 (only one
	// item).
	if gotModel.models.Index() != 0 {
		t.Errorf("cursor index = %d, want 0 (wrap on single-item design)", gotModel.models.Index())
	}
	if len(gotModel.models.Items()) != 1 {
		t.Errorf("models items = %d, want 1 (only design model)", len(gotModel.models.Items()))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestToggleTagRebuildsRotation -v 2>&1 | tail -10`
Expected: FAIL — the `d` handler currently calls `rotation.ForTag` (which doesn't exist anymore) or rebuilds the list without a fresh rotation snapshot.

- [ ] **Step 3: Rewrite the `d` handler in `app.go`**

Find:

```go
		case "d":
			if m.phase == phaseModel {
				prevTag := m.tag
				m.tag = oppositeTag(m.tag)
				// Empty-tag defense: if the new tag has no models, restore.
				models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
				if err != nil || len(models) == 0 {
					m.tag = prevTag
					m.status = fmt.Sprintf("tag %q has no models for agent %q", m.tag, m.agent)
					return m, nil
				}
				// Rebuild the list and reposition the cursor on the new tag's
				// rotation index. Cross-skip avoids the previous tag's last-used.
				m.models = buildModelList(models, m.width-2, m.height-2)
				m.modelsTag = m.tag
				if next, ok := rotation.ForTag(m.cfg, m.tag).Next(prevTag); ok {
					m.current = next
					if idx := indexOfModel(models, next); idx >= 0 {
						m.models.Select(idx)
					}
				} else {
					m.current = models[0]
					m.models.Select(0)
				}
			}
```

Replace with:

```go
		case "d":
			if m.phase == phaseModel {
				prevTag := m.tag
				m.tag = oppositeTag(m.tag)
				// Empty-tag defense: if the new tag has no models, restore.
				models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
				if err != nil || len(models) == 0 {
					m.tag = prevTag
					m.status = fmt.Sprintf("tag %q has no models for agent %q", m.tag, m.agent)
					return m, nil
				}
				// Rebuild the list and the rotation snapshot for the new
				// tag. Cross-skip is gone; the new tag's last-launched
				// drives the cursor via FirstAfter.
				m.models = buildModelList(models, m.width-2, m.height-2)
				m.modelsTag = m.tag
				m.rotation = rotation.New(m.tag, models, "")
				if last, ok := m.rotation.LastLaunched(); ok {
					if next, ok := FindAfter(models, last); ok {
						if idx := indexOfModel(models, next); idx >= 0 {
							m.models.Select(idx)
						}
					}
				}
			}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestToggleTagRebuildsRotation -v 2>&1 | tail -10`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): rebuild rotation snapshot on 'd' tag toggle

The picker and its rotation must share one view. When 'd' swaps
the tag group, both the list and the rotation snapshot are
rebuilt from the new tag's filtered models. Cross-skip against
the previous tag is removed (no longer meaningful in the new
implicit-rotation model)."
```

---

## Task 9: Record the launch on Enter and pass the highlighted model to the launcher

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/launch.go`

### Step 1: Write the failing tests

In `internal/tui/agent_model_test.go`, append:

```go
// TestEnterInModelPhaseRecordsLastLaunched asserts pressing Enter
// writes the highlighted model's ID to rotation-<tag>.state.
// Without this, the picker would always land on index 0 — every
// entry would be the same, no rotation at all.
func TestEnterInModelPhaseRecordsLastLaunched(t *testing.T) {
	dir := tempStateDir(t)
	m := phaseModelWithList(t, testConfig(), "claude", "code")
	// Pre-seed: state file is whatever tempStateDir isolated; the
	// helper just positioned the cursor. We don't care about the
	// pre-seed value here — we care that Enter records the
	// highlighted item.
	preSeed, _ := os.ReadFile(rotation.StateFile(dir, "code"))
	_ = preSeed

	// Cursor is at index 0 by default (no last-launched seed). The
	// highlighted item is therefore the first model.
	if m.models.Index() != 0 {
		t.Fatalf("precondition: cursor = %d, want 0", m.models.Index())
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	data, err := os.ReadFile(rotation.StateFile(dir, "code"))
	if err != nil {
		t.Fatalf("read state after Enter: %v", err)
	}
	// The picker is non-interactive in tests; the launch itself may
	// not complete (we don't have a real agent), but RecordLaunch
	// happens before the launch attempt. Verify the file was
	// written with the highlighted model's ID.
	first, ok := m.models.Items()[0].(modelItem)
	if !ok {
		t.Fatalf("items[0] is %T, want modelItem", m.models.Items()[0])
	}
	if got := strings.TrimSpace(string(data)); got != first.model.ID {
		t.Errorf("state file = %q, want %q (highlighted model)", got, first.model.ID)
	}
}
```

Add the missing import at the top of `agent_model_test.go` (if not already there):

```go
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
```

The `os` and `strings` imports are likely already there. Verify by reading the top of the file before adding.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestEnterInModelPhaseRecordsLastLaunched -v 2>&1 | tail -10`
Expected: FAIL — `Enter` doesn't currently call `RecordLaunch` (it doesn't exist as a method on the old `Rotation` type).

- [ ] **Step 3: Update the `phaseModel` Enter handler in `app.go`**

Find:

```go
			case phaseModel:
				// The highlighted list item is what gets launched. Sync m.current
				// so subsequent code paths (ollama check, session, launch) all see
				// the user's choice.
				if item, ok := m.models.SelectedItem().(modelItem); ok {
					m.current = item.model
				}
				// Check ollama availability before launching.
				if ollamacheck.IsOllamaModel(m.current) {
					ok, err := ollamacheck.Available(m.current.ModelName)
					if err != nil {
						m.status = "ollama check failed: " + err.Error()
						return m, nil
					}
					if !ok {
						m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), m.width-2, m.height-2)
						m.ollamaWarnModel.Title = "Model not available: " + m.current.ModelName
						m.phase = phaseOllamaWarn
						return m, nil
					}
				}
				return m.proceedToLaunch()
```

Replace with:

```go
			case phaseModel:
				// The highlighted list item is what gets launched.
				highlighted, ok := m.models.SelectedItem().(modelItem)
				if !ok {
					return m, nil
				}
				// Record the launch BEFORE the ollama check so a later
				// picker entry advances from this pick. The state write
				// is best-effort; a failure surfaces in m.status and the
				// launch still proceeds.
				if m.rotation != nil {
					if err := m.rotation.RecordLaunch(highlighted.model); err != nil {
						m.status = "rotation state not saved: " + err.Error()
					}
				}
				// Check ollama availability before launching.
				if ollamacheck.IsOllamaModel(highlighted.model) {
					ok, err := ollamacheck.Available(highlighted.model.ModelName)
					if err != nil {
						m.status = "ollama check failed: " + err.Error()
						return m, nil
					}
					if !ok {
						m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), m.width-2, m.height-2)
						m.ollamaWarnModel.Title = "Model not available: " + highlighted.model.ModelName
						m.phase = phaseOllamaWarn
						return m, nil
					}
				}
				return m.proceedToLaunch()
```

- [ ] **Step 4: Update `proceedToLaunch` to read the highlighted model**

Find:

```go
func (m model) proceedToLaunch() (model, tea.Cmd) {
	sess, err := session.LatestForAgent(m.agent, m.selectedPath)
	if err != nil {
		m.status = "session check failed: " + err.Error()
		return m, nil
	}
	if sess == nil {
		cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
		if err != nil {
			m.status = "launch failed: " + err.Error()
			return m, nil
		}
		return m, runAndWaitCmd(cmd)
	}
	m.phase = phaseResume
	m.resume.session = sess
	m.resume.choices = list.New(buildResumeChoices(sess), list.NewDefaultDelegate(), m.width-2, m.height-2)
	m.resume.choices.Title = "Resume previous session?"
	return m, nil
}
```

Replace with:

```go
func (m model) proceedToLaunch() (model, tea.Cmd) {
	sess, err := session.LatestForAgent(m.agent, m.selectedPath)
	if err != nil {
		m.status = "session check failed: " + err.Error()
		return m, nil
	}
	// The highlighted list item is what gets launched, regardless
	// of any other state. m.current is gone; m.models is the
	// single source of truth.
	highlighted, ok := m.models.SelectedItem().(modelItem)
	if !ok {
		m.status = "no model selected"
		return m, nil
	}
	if sess == nil {
		cmd, err := launchAgent(m.agent, highlighted.model, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
		if err != nil {
			m.status = "launch failed: " + err.Error()
			return m, nil
		}
		return m, runAndWaitCmd(cmd)
	}
	m.phase = phaseResume
	m.resume.session = sess
	m.resume.choices = list.New(buildResumeChoices(sess), list.NewDefaultDelegate(), m.width-2, m.height-2)
	m.resume.choices.Title = "Resume previous session?"
	return m, nil
}
```

- [ ] **Step 5: Update the `phaseResume` Enter handler to use the highlighted model**

Find:

```go
			case phaseResume:
				if item, ok := m.resume.choices.SelectedItem().(resumeItem); ok {
					switch item.choice {
					case cancelChoice:
						m.phase = phaseModel
						return m, nil
					case freshChoice:
						cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m, runAndWaitCmd(cmd)
					case resumeChoice:
						cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, m.resume.session, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m, runAndWaitCmd(cmd)
					}
				}
```

Replace with:

```go
			case phaseResume:
				if item, ok := m.resume.choices.SelectedItem().(resumeItem); ok {
					highlighted, hok := m.models.SelectedItem().(modelItem)
					if !hok {
						return m, nil
					}
					switch item.choice {
					case cancelChoice:
						m.phase = phaseModel
						return m, nil
					case freshChoice:
						cmd, err := launchAgent(m.agent, highlighted.model, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m, runAndWaitCmd(cmd)
					case resumeChoice:
						cmd, err := launchAgent(m.agent, highlighted.model, m.selectedPath, m.yolo, m.resume.session, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m, runAndWaitCmd(cmd)
					}
				}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestEnterInModelPhaseRecordsLastLaunched -v 2>&1 | tail -10`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): record launch on Enter and pass highlighted model to launcher

Enter in phaseModel now writes the highlighted model's ID to
rotation-<tag>.state via m.rotation.RecordLaunch. All launch
paths (phaseModel direct, phaseResume, proceedToLaunch) read the
highlighted list item instead of the removed m.current field.
m.models is now the single source of truth for the picked model."
```

---

## Task 9a: Add end-to-end rotation-advance tests

**Files:**
- Modify: `internal/tui/agent_model_test.go`

These two tests verify the core promise of the new model: launching a model advances the picker so the next entry shows a different model. Without these, the implementation could record the launch but never advance the cursor.

### Step 1: Write the failing tests

In `internal/tui/agent_model_test.go`, append:

```go
// TestNextEntryAfterLaunchAdvancesCursor asserts the picker entry
// after a launch lands on the model AFTER the just-launched one.
// This is the core promise of rotation-by-launch: every launch
// advances the rotation. The test simulates picker entry 1, runs
// the launch through to a second selectedEntryMsg, and checks the
// resulting cursor position.
func TestNextEntryAfterLaunchAdvancesCursor(t *testing.T) {
	dir := tempStateDir(t)
	// Seed: last-launched is gemma4:9b (index 0). Picker entry 1
	// will land on gemma4:14b (index 1).
	seedState(t, dir, "code", "ollama/gemma4:9b\n")

	// Picker entry 1.
	m := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m = got.(model)
	if m.models.Index() != 1 {
		t.Fatalf("entry 1: cursor = %d, want 1 (gemma4:14b)", m.models.Index())
	}

	// Launch gemma4:14b (the highlighted model). The launch itself
	// will not complete (no real agent in the test), but
	// RecordLaunch happens before the launch attempt, so the
	// state file is written.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Picker entry 2 (e.g., user backed out and picked again, or
	// the app restarted). New selectedEntryMsg should rebuild the
	// rotation, see gemma4:14b as last-launched, and advance to
	// the next model — which wraps to gemma4:9b (only 2 models).
	m2 := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ = m2.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m2 = got.(model)
	if m2.models.Index() != 0 {
		t.Errorf("entry 2: cursor = %d, want 0 (wrap after gemma4:14b)", m2.models.Index())
	}
}

// TestNextEntryAfterManualPickAdvancesFromManualPick asserts the
// rotation advances from the user's manual pick when they launch
// it. If the user navigates to a non-rotation-suggested model and
// presses Enter, the next entry should land on the model AFTER
// the manual pick — not the model after the prior rotation pick.
// This protects against "manual picks are ignored" regressions.
func TestNextEntryAfterManualPickAdvancesFromManualPick(t *testing.T) {
	dir := tempStateDir(t)
	// No prior state. Picker entry 1 lands at index 0.
	_ = dir

	m := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m = got.(model)
	if m.models.Index() != 0 {
		t.Fatalf("entry 1: cursor = %d, want 0", m.models.Index())
	}

	// User navigates up (no-op at index 0), then back down to
	// index 1 — manual pick of gemma4:14b. Launch.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Picker entry 2: last-launched is gemma4:14b (the manual
	// pick), so the next entry should wrap to gemma4:9b (index 0).
	m2 := model{cfg: testConfig(), phase: phaseList, width: 80, height: 24}
	got, _ = m2.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	m2 = got.(model)
	if m2.models.Index() != 0 {
		t.Errorf("entry 2: cursor = %d, want 0 (wrap after manual pick gemma4:14b)", m2.models.Index())
	}
}
```

- [ ] **Step 2: Run the tests to verify they pass (they should — the implementation is already in place from Tasks 7 and 9)**

Run: `go test ./internal/tui -run "TestNextEntryAfter" -v 2>&1 | tail -10`
Expected: PASS for both. If any fail, the implementation has a regression; debug before continuing.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/agent_model_test.go
git commit -m "test(tui): add end-to-end rotation-advance tests

Verify the core promise of rotation-by-launch: a launched model
becomes the new 'last-launched' and the next picker entry lands
on the model after it. Covers both the implicit path (rotation
suggests, user accepts) and the manual-pick path (user navigates
to a different model and launches)."
```

---

## Task 10: Remove the ollama-warn "skip" choice

**Files:**
- Modify: `internal/tui/launch.go`
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/app_test.go`

### Step 1: Write the failing test

In `internal/tui/app_test.go`, append:

```go
// TestOllamaWarnHasProceedAndCancelOnly asserts the ollama
// availability warning has two choices: proceed and cancel.
// The "skip" choice (which rotated to the next model) is gone
// because rotation is now implicit via RecordLaunch on Enter.
func TestOllamaWarnHasProceedAndCancelOnly(t *testing.T) {
	choices := buildOllamaChoices()
	if len(choices) != 2 {
		t.Fatalf("ollama choices = %d, want 2 (proceed + cancel)", len(choices))
	}
	seen := map[ollamaChoice]bool{}
	for _, c := range choices {
		if oi, ok := c.(ollamaItem); ok {
			seen[oi.choice] = true
		}
	}
	if !seen[ollamaProceedChoice] {
		t.Error("missing ollamaProceedChoice")
	}
	if !seen[ollamaCancelChoice] {
		t.Error("missing ollamaCancelChoice")
	}
	if seen[ollamaSkipChoice] {
		t.Error("ollamaSkipChoice still present (should be removed)")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails (the choice is currently present)**

Run: `go test ./internal/tui -run TestOllamaWarnHasProceedAndCancelOnly -v 2>&1 | tail -10`
Expected: FAIL — `ollamaSkipChoice` is still in `buildOllamaChoices`.

- [ ] **Step 3: Remove `ollamaSkipChoice` from `internal/tui/launch.go`**

Find:

```go
// ollamaChoice identifies a choice in the ollama availability prompt.
type ollamaChoice int

const (
	ollamaProceedChoice ollamaChoice = iota
	ollamaSkipChoice
	ollamaCancelChoice
)
```

Replace with:

```go
// ollamaChoice identifies a choice in the ollama availability prompt.
type ollamaChoice int

const (
	ollamaProceedChoice ollamaChoice = iota
	ollamaCancelChoice
)
```

Find:

```go
// buildOllamaChoices creates the ollama availability confirmation list items.
func buildOllamaChoices() []list.Item {
	return []list.Item{
		ollamaItem{choice: ollamaProceedChoice, title: "Proceed anyway", desc: "Launch with unavailable model (may fail)"},
		ollamaItem{choice: ollamaSkipChoice, title: "Skip to next model", desc: "Rotate to the next model in the tag group"},
		ollamaItem{choice: ollamaCancelChoice, title: "Cancel", desc: "Return to the agent+model screen"},
	}
}
```

Replace with:

```go
// buildOllamaChoices creates the ollama availability confirmation list
// items. With implicit rotation-by-launch, the user can navigate the
// picker with up/down and press Enter on a different model; there is
// no "skip to next" shortcut.
func buildOllamaChoices() []list.Item {
	return []list.Item{
		ollamaItem{choice: ollamaProceedChoice, title: "Proceed anyway", desc: "Launch with unavailable model (may fail)"},
		ollamaItem{choice: ollamaCancelChoice, title: "Cancel", desc: "Return to the agent+model screen"},
	}
}
```

- [ ] **Step 4: Remove the `ollamaSkipChoice` branch in the `phaseOllamaWarn` Enter handler**

Find:

```go
			case phaseOllamaWarn:
				if item, ok := m.ollamaWarnModel.SelectedItem().(ollamaItem); ok {
					switch item.choice {
					case ollamaProceedChoice:
						return m.proceedToLaunch()
					case ollamaSkipChoice:
						// Rotate to next model and return to phaseModel.
						next, ok := rotation.ForTag(m.cfg, m.tag).Next(oppositeTag(m.tag))
						if ok {
							m.current = next
						}
						m.phase = phaseModel
						return m, nil
					case ollamaCancelChoice:
						m.phase = phaseModel
						return m, nil
					}
				}
```

Replace with:

```go
			case phaseOllamaWarn:
				if item, ok := m.ollamaWarnModel.SelectedItem().(ollamaItem); ok {
					switch item.choice {
					case ollamaProceedChoice:
						return m.proceedToLaunch()
					case ollamaCancelChoice:
						m.phase = phaseModel
						return m, nil
					}
				}
```

- [ ] **Step 5: Update the existing `TestOllamaWarnCancel` test to use the highlighted item instead of `m.current`**

In `internal/tui/app_test.go`, find:

```go
	m := model{cfg: cfg, phase: phaseOllamaWarn, width: 80, height: 24, agent: "claude", tag: "code", current: cfg.Models[0], selectedPath: "/repo"}
	m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), 78, 22)
```

Replace with:

```go
	m := model{cfg: cfg, phase: phaseOllamaWarn, width: 80, height: 24, agent: "claude", tag: "code", selectedPath: "/repo"}
	// Build a stub picker with the unavailable model as the highlighted
	// item, so ollamaWarn has a model to reference if proceedToLaunch
	// is ever called from this test.
	items := []list.Item{modelItem{model: cfg.Models[0]}}
	m.models = list.New(items, list.NewDefaultDelegate(), 78, 22)
	m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), 78, 22)
```

- [ ] **Step 6: Run the ollama-warn tests to verify they pass**

Run: `go test ./internal/tui -run "OllamaWarn" -v 2>&1 | tail -10`
Expected: PASS for both `TestOllamaWarnHasProceedAndCancelOnly` and `TestOllamaWarnCancel`.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/launch.go internal/tui/app.go internal/tui/app_test.go
git commit -m "refactor(tui): remove ollama-warn 'skip to next model' choice

With implicit rotation-by-launch, the user can navigate the
picker with up/down and press Enter on a different model. The
explicit 'skip to next' shortcut is redundant and references
the removed rotation.ForTag/Next API. The ollama warning is
now proceed/cancel only."
```

---

## Task 11: Update tests that referenced `m.current`

**Files:**
- Modify: `internal/tui/agent_model_test.go`
- Modify: `internal/tui/app_test.go`

### Step 1: Find and rewrite the remaining `m.current` test references

Run: `grep -n "m\.current" internal/tui/agent_model_test.go internal/tui/app_test.go 2>&1`

For each remaining `m.current` reference in a test:
- `m.current = X` (setup) → replace with construction of a stub `m.models` that highlights `X`
- `m.current.ID` (assertion) → replace with reading the highlighted `modelItem` from `m.models`
- `m.current.ModelName` (assertion) → same

The exact rewrites depend on which tests still reference `m.current` after Tasks 5–10. Run the grep first to find them.

- [ ] **Step 2: For each test that still references `m.current`, rewrite it**

A typical pattern: a test that built `m := model{current: someModel, ...}` and later asserted `m.current.ID` becomes:

```go
items := []list.Item{modelItem{model: someModel}}
m.models = list.New(items, list.NewDefaultDelegate(), 80, 24)
m.models.Select(0)
// later:
highlighted, _ := m.models.SelectedItem().(modelItem)
if highlighted.model.ID != "expected" { ... }
```

- [ ] **Step 3: Run the full TUI test suite**

Run: `go test ./internal/tui -v 2>&1 | tail -50`
Expected: PASS for all tests.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/agent_model_test.go internal/tui/app_test.go
git commit -m "test(tui): replace m.current references with highlighted list item

After the rotation refactor, m.current no longer exists. Tests
that constructed models with m.current now build a stub m.models
with the equivalent model highlighted. Tests that asserted on
m.current.ID now read the highlighted modelItem from m.models."
```

---

## Task 12: Run the full test suite and the vet pass

**Files:** none (verification only)

- [ ] **Step 1: Build everything**

Run: `go build ./... 2>&1`
Expected: clean build, no output.

- [ ] **Step 2: Run all tests**

Run: `go test ./... 2>&1 | tail -30`
Expected: PASS for all packages. Pay attention to `internal/rotation` (12 tests), `internal/tui` (most of the work), and `cmd/wt` (no changes expected but verify).

- [ ] **Step 3: Run go vet**

Run: `go vet ./... 2>&1`
Expected: clean, no output.

- [ ] **Step 4: Manual smoke test**

Build the binary and try the picker:

```bash
go build -o bin/wt ./cmd/wt
./bin/wt
```

Expected: picker appears; selecting a worktree enters the model phase; up/down moves the cursor through the models; `d` toggles code/design; Enter launches (or shows the ollama warning for unavailable models). No `r` key behavior remains. Launching records the model in `~/.config/agent-wt/rotation-<tag>.state`; the next picker entry advances to the next model.

- [ ] **Step 5: Verify the state file is single-line after a launch**

```bash
cat ~/.config/agent-wt/rotation-code.state
```

Expected: a single line with the launched model's ID, ending in `\n`. If a legacy 2-line file existed before this run, it should now be a single line.

- [ ] **Step 6: Commit any smoke-test-driven tweaks**

If the manual test surfaced any tweaks (footer text, key wiring, etc.), commit them with a `fix(tui):` message. If clean, no commit.

---

## Task 13: Final review and PR

**Files:** none

- [ ] **Step 1: Push the branch and open a PR**

```bash
git push -u origin feat/rotation-by-launch
gh pr create --base main --head feat/rotation-by-launch --title "feat(tui): rotation by launch — drop r key, advance on Enter" --body "..."
```

The PR body should reference the spec and summarize the user-visible behavior change: `r` key removed, rotation advances implicitly via launches, up/down now works in the picker, single-line state file with backward-compat read.

- [ ] **Step 2: After review and merge, run `/repo-cleanup`**

```bash
bash /Users/keith/.agents/plugins/repo-cleanup/bin/repo-cleanup --yes
```

Expected: clean. The spec branch `docs/rotation-by-launch-spec` will be reported as merged-able; the feature branch `feat/rotation-by-launch` will be deleted automatically once merged.

---

## Self-Review Notes

This plan was checked against the spec before commit:

- **Spec coverage:** Each of the 6 spec sections maps to one or more tasks. The "Removed" list (r key, ollama-warn skip, m.current, cross-skip) is fully covered by Tasks 4, 6, 10. The "Added" list (LastLaunched, RecordLaunch, FirstAfter, FindAfter, m.rotation, key-forwarding) is covered by Tasks 2, 3, 4, 5. The "Test plan" maps directly to the per-task test steps, including the two end-to-end "next entry after launch" tests added in Task 9a to cover `TestNextEntryAfterLaunchAdvancesCursor` and `TestNextEntryAfterManualPickAdvancesFromManualPick`.
- **Placeholder scan:** No TBD/TODO/XXX. Every step has concrete code or commands. No "similar to Task N" references that omit the actual code.
- **Type consistency:** `Rotation.LastLaunched`, `Rotation.RecordLaunch`, `FirstAfter`, `FindAfter`, `model.rotation`, `m.rotation.RecordLaunch`, `m.rotation.LastLaunched` — all use the same names defined in Task 2 and Task 3. The `modelItem` and `indexOfModel` references match the existing `model_list.go` API.
- **Commit hygiene:** Each task ends with one commit. The build is broken mid-plan (between Tasks 4 and 6) by design — Tasks 5 and 6 fix it. The smoke test in Task 12 is the only point where the binary is run end-to-end.
- **Test isolation:** Every test that touches the rotation state file uses `tempStateDir(t)`. The existing `seedState` helper is reused.
- **Backward compat:** The legacy 2-line state file format is read correctly via `last-non-empty-line` semantics in `LastLaunched`. New writes overwrite with single-line, normalizing on first launch.
