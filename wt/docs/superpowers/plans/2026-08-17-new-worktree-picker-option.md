# "New Worktree" Option in the Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a sentinel item at the top of the worktree/branch picker and an `n` keypress shortcut that open a name-prompt screen, create a new worktree via the existing `worktree.EnsureForName`, then refresh the picker with the new entry highlighted.

**Architecture:** Extend `entryItem` with a `kind` discriminator so the picker list renders a sentinel row alongside real entries. Add a new `phaseNewWorktree` screen with a `bubbles/textinput` widget, owned by a new `internal/tui/new_worktree.go` file. On success, dispatch `loadEntriesCmd` to refresh the picker and select the new branch by name. Reuse `worktree.EnsureForName` for git operations — no new git logic.

**Tech Stack:** Go 1.26.3, charmbracelet/bubbles (`list`, `textinput`), `tea.Cmd` async pattern.

---

## File map

| File | Responsibility |
|---|---|
| `internal/tui/worktree_list.go` | Add `entryKind` discriminator; sentinel item rendering; prepending sentinel in `buildList` |
| `internal/tui/worktree_list_test.go` | Tests for sentinel rendering and `buildList` placement |
| `internal/tui/new_worktree.go` (new) | `phaseNewWorktree` state: `textinput` widget, validation, `ensureNewWorktreeCmd`, error style, message type |
| `internal/tui/new_worktree_test.go` (new) | Tests for the pure helpers in `new_worktree.go` |
| `internal/tui/app.go` | Add `phaseNewWorktree`, model fields, `n` keypress, sentinel Enter, esc, error/success handling, View, `WindowSizeMsg` resize, `entriesLoadedMsg` extension for `repoRoot` + `pendingHighlight` |
| `internal/tui/app_test.go` | Tests for app-level phase transitions and message handling |

---

## Task 1: Add `entryKind` discriminator and sentinel rendering to `entryItem`

**Files:**
- Modify: `internal/tui/worktree_list.go`
- Test: `internal/tui/worktree_list_test.go`

- [ ] **Step 1: Write the failing tests for the sentinel**

Add the following tests to `internal/tui/worktree_list_test.go` (append at end of file):

```go
// TestSentinelItemRendersAsNew asserts the sentinel's Title visibly
// signals "new" so users understand it isn't a regular branch. A
// regression to a generic label would make the row look like a normal
// entry and confuse users.
func TestSentinelItemRendersAsNew(t *testing.T) {
	item := entryItem{kind: kindNewWorktree}
	got := strings.ToLower(item.Title())
	if !strings.Contains(got, "new") {
		t.Errorf("sentinel Title = %q, want it to contain 'new'", item.Title())
	}
}

// TestSentinelItemHasEmptyFilterValue asserts the sentinel returns ""
// for FilterValue. bubbles/list hides items with empty FilterValue
// while the user is filtering; without this, the sentinel would
// clutter filtered results with a single-character match.
func TestSentinelItemHasEmptyFilterValue(t *testing.T) {
	item := entryItem{kind: kindNewWorktree}
	if got := item.FilterValue(); got != "" {
		t.Errorf("sentinel FilterValue = %q, want empty string", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run 'TestSentinelItem' -v`
Expected: FAIL — `kindNewWorktree` and the sentinel branch in `Title()`/`FilterValue()` don't exist yet.

- [ ] **Step 3: Implement the `entryKind` discriminator and sentinel behavior**

In `internal/tui/worktree_list.go`, replace the file contents with:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// entryKind discriminates sentinel rows from real entries in the picker.
type entryKind int

const (
	// kindEntry is a real worktree or branch row.
	kindEntry entryKind = iota
	// kindNewWorktree is the "+ New worktree…" sentinel that opens
	// the new-worktree prompt. The underlying entry field is unused.
	kindNewWorktree
)

// entryItem adapts a worktree.Entry to a list.Item. With kind =
// kindNewWorktree, it represents the create-new sentinel row.
type entryItem struct {
	kind  entryKind
	entry worktree.Entry
}

// FilterValue is used by the list's built-in filter. The sentinel
// returns "" so it is hidden when the user is filtering.
func (e entryItem) FilterValue() string {
	if e.kind == kindNewWorktree {
		return ""
	}
	return e.entry.Branch
}

// Title renders the branch name (remote prefix stripped for display)
// for real entries, and a "+ New worktree…" label for the sentinel.
func (e entryItem) Title() string {
	if e.kind == kindNewWorktree {
		return "+ New worktree…"
	}
	b := e.entry.Branch
	if i := strings.IndexByte(b, '/'); i >= 0 {
		b = b[i+1:] // strip remote prefix
	}
	return b
}

// Description renders the metadata columns. The sentinel gets a
// static descriptor so users understand what the row does.
func (e entryItem) Description() string {
	if e.kind == kindNewWorktree {
		return "create a new branch and worktree"
	}
	path := e.entry.Path
	if path == "" {
		path = "(no worktree)"
	}
	return strings.TrimSpace(strings.Join([]string{
		pad("["+string(e.entry.Type)+"]", 9),
		path,
	}, " "))
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// selectedEntryMsg is emitted when the user picks a worktree or branch.
type selectedEntryMsg struct{ entry worktree.Entry }

// buildList constructs a list.Model from worktree entries, prepending
// the "+ New worktree…" sentinel so it's always reachable.
func buildList(entries []worktree.Entry, width, height int) list.Model {
	items := make([]list.Item, 0, len(entries)+1)
	items = append(items, entryItem{kind: kindNewWorktree})
	for _, e := range entries {
		items = append(items, entryItem{kind: kindEntry, entry: e})
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = "Pick a worktree or branch"
	l.SetShowStatusBar(true)
	return l
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestSentinelItem' -v`
Expected: PASS for both new tests. The pre-existing `TestEntryItemTitleLocalBranch` and similar tests still pass because they construct `entryItem{entry: ...}` without setting `kind`, which defaults to `kindEntry` (the zero value of `entryKind`).

- [ ] **Step 5: Run the full `tui` test suite to confirm no regressions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/worktree_list.go internal/tui/worktree_list_test.go
git commit -m "feat(tui): add sentinel item discriminator to entryItem"
```

---

## Task 2: Add `buildList` placement tests for the sentinel

**Files:**
- Test: `internal/tui/worktree_list_test.go`

- [ ] **Step 1: Write the failing tests**

Add the following tests to `internal/tui/worktree_list_test.go` (append at end of file):

```go
// TestBuildListPrependsSentinel asserts buildList always puts the
// sentinel as the first item. Without the sentinel, users would
// need to know the `n` shortcut to create worktrees from the picker.
func TestBuildListPrependsSentinel(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}
	l := buildList(entries, 80, 24)
	items := l.Items()
	if len(items) != len(entries)+1 {
		t.Fatalf("items = %d, want %d", len(items), len(entries)+1)
	}
	first, ok := items[0].(entryItem)
	if !ok {
		t.Fatalf("item 0 is %T, want entryItem", items[0])
	}
	if first.kind != kindNewWorktree {
		t.Errorf("item 0 kind = %d, want kindNewWorktree (%d)", first.kind, kindNewWorktree)
	}
	// Real entries follow in order.
	for i, want := range entries {
		got, ok := items[i+1].(entryItem)
		if !ok {
			t.Fatalf("item %d is %T, want entryItem", i+1, items[i+1])
		}
		if got.kind != kindEntry {
			t.Errorf("item %d kind = %d, want kindEntry (%d)", i+1, got.kind, kindEntry)
		}
		if got.entry != want {
			t.Errorf("item %d entry = %+v, want %+v", i+1, got.entry, want)
		}
	}
}

// TestBuildListSentinelFirstWhenEmpty asserts the picker still shows
// the sentinel even with zero real entries. A fresh repo with no
// worktrees must still let users create one from the TUI.
func TestBuildListSentinelFirstWhenEmpty(t *testing.T) {
	l := buildList(nil, 80, 24)
	items := l.Items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (just the sentinel)", len(items))
	}
	first, ok := items[0].(entryItem)
	if !ok {
		t.Fatalf("item 0 is %T, want entryItem", items[0])
	}
	if first.kind != kindNewWorktree {
		t.Errorf("sentinel kind = %d, want kindNewWorktree", first.kind)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestBuildListPrependsSentinel|TestBuildListSentinelFirstWhenEmpty' -v`
Expected: PASS — these tests should already pass after Task 1 since the sentinel prepending was added in Step 3 of Task 1. We're locking the behavior with a regression test.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/worktree_list_test.go
git commit -m "test(tui): lock sentinel prepending in buildList"
```

---

## Task 3: Create `internal/tui/new_worktree.go` with helpers and message

**Files:**
- Create: `internal/tui/new_worktree.go`
- Create: `internal/tui/new_worktree_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/new_worktree_test.go`:

```go
package tui

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateNewWorktreeNameRejectsEmpty asserts the validator
// rejects the empty string. An empty worktree name would create a
// malformed .worktrees/ directory; the TUI must surface this as a
// user-facing error rather than calling git.
func TestValidateNewWorktreeNameRejectsEmpty(t *testing.T) {
	if got := validateNewWorktreeName(""); got == "" {
		t.Error("validateNewWorktreeName(\"\") returned empty error, want non-empty")
	}
}

// TestValidateNewWorktreeNameRejectsWhitespace asserts pure
// whitespace is also rejected. Trim-then-check matches the same
// contract — git would reject this, but the TUI should pre-empt.
func TestValidateNewWorktreeNameRejectsWhitespace(t *testing.T) {
	if got := validateNewWorktreeName("   "); got == "" {
		t.Error("validateNewWorktreeName(\"   \") returned empty error, want non-empty")
	}
}

// TestValidateNewWorktreeNameAcceptsValid asserts the validator
// does not over-restrict. Slashed names like "feature/x" are valid
// branch names and EnsureForName handles them; the TUI's validator
// must defer to that without pre-rejecting.
func TestValidateNewWorktreeNameAcceptsValid(t *testing.T) {
	cases := []string{"my-feature", "feature/x", "a", "release-1.0"}
	for _, name := range cases {
		if got := validateNewWorktreeName(name); got != "" {
			t.Errorf("validateNewWorktreeName(%q) = %q, want empty", name, got)
		}
	}
}

// TestNewInputModelIsFocused asserts the input is focused on
// creation. An unfocused input would require an extra tab/click,
// breaking the muscle-memory contract that Enter submits.
func TestNewInputModelIsFocused(t *testing.T) {
	ti := newInputModel(80)
	if !ti.Focused() {
		t.Error("newInputModel(80).Focused() = false, want true")
	}
}

// TestNewInputModelHasPlaceholder asserts the placeholder matches
// the contract constant. Without a placeholder the prompt is a
// blank rectangle and users have no hint what to type.
func TestNewInputModelHasPlaceholder(t *testing.T) {
	ti := newInputModel(80)
	if ti.Placeholder != newWorktreePlaceholder {
		t.Errorf("Placeholder = %q, want %q", ti.Placeholder, newWorktreePlaceholder)
	}
}

// TestNewInputModelWidth asserts the input width fits the terminal.
// An over-wide input would wrap awkwardly; a too-narrow one would
// hide long branch names. The contract is width - 4 (room for
// padding/borders).
func TestNewInputModelWidth(t *testing.T) {
	ti := newInputModel(80)
	if ti.Width != 76 {
		t.Errorf("Width = %d, want 76", ti.Width)
	}
}

// TestEnsureNewWorktreeCmdSuccess is the happy-path integration
// test. In a fresh temp git repo, the command must create a
// worktree and emit a newWorktreeCreatedMsg with err == nil and
// a path under .worktrees/. A regression here means the feature
// silently does nothing.
func TestEnsureNewWorktreeCmdSuccess(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := ensureNewWorktreeCmd(dir, "fresh-feature")
	msg := cmd()
	got, ok := msg.(newWorktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want newWorktreeCreatedMsg", msg)
	}
	if got.err != nil {
		t.Fatalf("err = %v, want nil", got.err)
	}
	if got.name != "fresh-feature" {
		t.Errorf("name = %q, want fresh-feature", got.name)
	}
	wantPath := filepath.Join(dir, ".worktrees", "fresh-feature")
	if got.path != wantPath {
		t.Errorf("path = %q, want %q", got.path, wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("worktree not created at %q: %v", wantPath, err)
	}
}

// TestEnsureNewWorktreeCmdGitError asserts that git errors are
// surfaced to the caller rather than swallowed. The traversal
// name is rejected by EnsureForName's defensive guard.
func TestEnsureNewWorktreeCmdGitError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := ensureNewWorktreeCmd(dir, "../evil")
	msg := cmd()
	got, ok := msg.(newWorktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want newWorktreeCreatedMsg", msg)
	}
	if got.err == nil {
		t.Fatal("err = nil, want non-nil for traversal name")
	}
	if !strings.Contains(got.err.Error(), "path") {
		t.Errorf("err = %q, want it to mention 'path'", got.err.Error())
	}
}

// gitInit is a test helper that creates a fresh git repo in dir.
// Duplicated from internal/worktree/create_test.go to keep this
// package's tests self-contained.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Set identity so commits/worktree-add don't fail.
	for _, kv := range [][]string{
		{"user.email", "t@e"},
		{"user.name", "t"},
		{"commit.gpgsign", "false"},
		{"tag.gpgsign", "false"},
	} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v\n%s", kv[0], err, out)
		}
	}
	// Make an initial commit so the branch is created with a HEAD.
	c := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init")
	if out, err := c.CombinedOutput(); err != nil {
		// Some git versions need user config before any commit; the
		// git config above should cover that. If it still fails
		// the test setup is broken, not the code under test.
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run 'TestValidateNewWorktreeName|TestNewInputModel|TestEnsureNewWorktreeCmd' -v`
Expected: FAIL — `validateNewWorktreeName`, `newInputModel`, `ensureNewWorktreeCmd`, `newWorktreeCreatedMsg`, and `newWorktreePlaceholder` don't exist yet.

- [ ] **Step 3: Implement `new_worktree.go`**

Create `internal/tui/new_worktree.go`:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// newWorktreePlaceholder is the textinput placeholder shown in the
// new-worktree prompt. It signals that the user can type either a
// branch name or a path-like name (e.g. feature/x).
const newWorktreePlaceholder = "branch-or-worktree-name"

// newWorktreeErrorStyle is the lipgloss style used to render
// m.newError under the input. ANSI color 9 is bright red.
var newWorktreeErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

// newWorktreeCreatedMsg is emitted after a create attempt. On
// success, path is the worktree path and name is the branch name.
// On failure, err is set and the other fields are zero.
type newWorktreeCreatedMsg struct {
	path string
	name string
	err  error
}

// ensureNewWorktreeCmd returns a tea.Cmd that creates a worktree
// for name in the repo at root via EnsureForName and reports the
// result. Run it from the model via dispatching the returned
// command from Update.
func ensureNewWorktreeCmd(root, name string) tea.Cmd {
	return func() tea.Msg {
		path, err := worktree.EnsureForName(root, name)
		return newWorktreeCreatedMsg{path: path, name: name, err: err}
	}
}

// newInputModel builds a focused textinput sized to the terminal
// width. width is the full terminal width; the input is sized to
// width-4 to leave room for padding in the View.
func newInputModel(width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = newWorktreePlaceholder
	ti.Focus()
	ti.CharLimit = 100
	ti.Width = width - 4
	return ti
}

// validateNewWorktreeName returns an error string if name is
// invalid (empty or whitespace-only), or "" if OK. Deeper
// validation is delegated to EnsureForName; this validator only
// catches the cheap client-side check before shelling out to git.
func validateNewWorktreeName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "name cannot be empty"
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestValidateNewWorktreeName|TestNewInputModel|TestEnsureNewWorktreeCmd' -v`
Expected: PASS for all eight new tests.

- [ ] **Step 5: Run the full `tui` test suite to confirm no regressions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/new_worktree.go internal/tui/new_worktree_test.go
git commit -m "feat(tui): new-worktree prompt helpers and tea.Cmd"
```

---

## Task 4: Add `phaseNewWorktree` to the phase enum and model fields

**Files:**
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Add the phase constant**

In `internal/tui/app.go`, find the existing phase block:

```go
const (
	phaseList       phase = iota // worktree list (lesson 13)
	phaseModel                   // agent+model screen (lesson 14)
	phaseBrowser                 // model browser (lesson 15)
	phaseResume                  // resume prompt (lesson 16)
	phaseGuardWarn               // confirm before launching on default branch
	phaseOllamaWarn              // confirm before launching with unavailable ollama model
)
```

Add a new constant at the end of the block (preserving existing iota order — appending only):

```go
const (
	phaseList       phase = iota // worktree list (lesson 13)
	phaseModel                   // agent+model screen (lesson 14)
	phaseBrowser                 // model browser (lesson 15)
	phaseResume                  // resume prompt (lesson 16)
	phaseGuardWarn               // confirm before launching on default branch
	phaseOllamaWarn              // confirm before launching with unavailable ollama model
	phaseNewWorktree             // create-new-worktree prompt
)
```

- [ ] **Step 2: Add the new model fields**

In `internal/tui/app.go`, find the `model` struct. Add the four new fields. Place them with the other "new worktree prompt" state. A good spot is after the `ollamaWarnModel` field:

```go
	// new-worktree prompt (this lesson)
	newInput         textinput.Model
	newError         string
	pendingHighlight string // branch name to focus after re-enumerating
	repoRoot         string // cached from entriesLoadedMsg to avoid a second rev-parse
```

- [ ] **Step 3: Add the `textinput` import**

At the top of `internal/tui/app.go`, find the existing import block and add `"github.com/charmbracelet/bubbles/textinput"`:

```go
import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/guard"
	"github.com/ohanaverse/agent-worktree/internal/ollamacheck"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)
```

- [ ] **Step 4: Build to verify the changes compile**

Run: `go build ./...`
Expected: PASS — no compile errors. The new fields and constant are added but not yet wired up; that's the next task.

- [ ] **Step 5: Run tests to confirm no regressions from the constant/field additions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat(tui): add phaseNewWorktree and new-worktree model fields"
```

---

## Task 5: Wire `n` keypress and sentinel Enter to `phaseNewWorktree`

**Files:**
- Modify: `internal/tui/app.go`
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing tests**

Add the following tests to `internal/tui/app_test.go` (append at end of file):

```go
// TestNKeyOpensNewWorktreePhase asserts pressing `n` on the picker
// list transitions to phaseNewWorktree. Without this wiring, the
// keyboard shortcut is dead and only the sentinel is reachable.
func TestNKeyOpensNewWorktreePhase(t *testing.T) {
	m := model{phase: phaseList, ready: true, width: 80, height: 24}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
}

// TestNKeyIgnoredWhileLoading asserts `n` does not transition
// before the list is ready. A user mashing `n` while the picker is
// still loading would otherwise jump into an empty prompt and
// have to esc out.
func TestNKeyIgnoredWhileLoading(t *testing.T) {
	m := model{phase: phaseList, ready: false, width: 80, height: 24}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
}

// TestEnterOnSentinelOpensNewWorktreePhase asserts picking the
// sentinel and pressing Enter transitions to phaseNewWorktree. The
// sentinel must be Enter-able, not just `n`-able.
func TestEnterOnSentinelOpensNewWorktreePhase(t *testing.T) {
	// Build a list with just the sentinel as the selected item.
	l := buildList(nil, 80, 24)
	m := model{
		phase: phaseList, ready: true, width: 80, height: 24,
		list: l,
	}
	// Sentinel is the first (and only) item, so the cursor is on it.
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
}

// TestEnterOnSentinelIgnoredWhileLoading asserts the sentinel
// cannot be picked before the list is ready. Consistent with the
// existing list-unready guard and the `n` keypress guard.
func TestEnterOnSentinelIgnoredWhileLoading(t *testing.T) {
	// Even with a sentinel-bearing list, ready=false short-circuits.
	l := buildList(nil, 80, 24)
	m := model{
		phase: phaseList, ready: false, width: 80, height: 24,
		list: l,
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase == phaseNewWorktree {
		t.Errorf("phase = phaseNewWorktree, want phaseList (loading not complete)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run 'TestNKey|TestEnterOnSentinel' -v`
Expected: FAIL — the key handlers don't transition to `phaseNewWorktree` yet.

- [ ] **Step 3: Add the `n` keypress handler**

In `internal/tui/app.go`, find the `case tea.KeyMsg:` block. Inside it, locate the existing `case "r":` / `case "m":` / `case "d":` handlers (which are all `if m.phase == phaseModel` style). Add a new case before them:

```go
		case "n":
			if m.phase == phaseList && m.ready {
				m.phase = phaseNewWorktree
				m.newInput = newInputModel(m.width)
				m.newError = ""
				return m, nil
			}
```

Place the new case alphabetically (between `case "m":` and `case "r":` is fine, but ordering doesn't matter functionally — pick a logical spot).

- [ ] **Step 4: Update the Enter handler to dispatch on sentinel kind**

In `internal/tui/app.go`, find the `case "enter":` block under `tea.KeyMsg`. The existing structure is:

```go
		case "enter":
			switch m.phase {
			case phaseList:
				if !m.ready {
					return m, nil
				}
				item, ok := m.list.SelectedItem().(entryItem)
				if !ok {
					return m, nil
				}
				if isCurrentOnDefaultBranch(item.entry, m.defaultBranch) {
					// ... guard warn logic ...
				}
				return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
			// ... other phases ...
			}
```

Replace the `case phaseList:` block with the version that discriminates the sentinel first:

```go
			case phaseList:
				if !m.ready {
					return m, nil
				}
				item, ok := m.list.SelectedItem().(entryItem)
				if !ok {
					return m, nil
				}
				if item.kind == kindNewWorktree {
					m.phase = phaseNewWorktree
					m.newInput = newInputModel(m.width)
					m.newError = ""
					return m, nil
				}
				if isCurrentOnDefaultBranch(item.entry, m.defaultBranch) {
					installed := guard.Check() == guard.Installed
					m.guardWarnEntry = item.entry
					m.guardWarnModel = list.New(buildGuardChoices(item.entry.Branch, installed), list.NewDefaultDelegate(), m.width-2, m.height-2)
					m.guardWarnModel.Title = "Launch on default branch?"
					m.phase = phaseGuardWarn
					return m, nil
				}
				return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestNKey|TestEnterOnSentinel' -v`
Expected: PASS for all four new tests.

- [ ] **Step 6: Run the full `tui` test suite to confirm no regressions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): wire n keypress and sentinel enter to phaseNewWorktree"
```

---

## Task 6: Add `phaseNewWorktree` enter/esc handling and input forwarding

**Files:**
- Modify: `internal/tui/app.go`
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing tests**

Add the following tests to `internal/tui/app_test.go` (append at end of file):

```go
// TestEscOnNewWorktreeReturnsToList asserts pressing esc on the
// new-worktree prompt returns to the picker. Without this, the
// user is stuck in the prompt with no way out.
func TestEscOnNewWorktreeReturnsToList(t *testing.T) {
	m := model{
		phase:   phaseNewWorktree,
		width:   80,
		height:  24,
		newInput: newInputModel(80),
		newError: "previous error",
	}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList", gotModel.phase)
	}
	if gotModel.newError != "" {
		t.Errorf("newError = %q, want empty (esc clears it)", gotModel.newError)
	}
}

// TestEnterOnNewWorktreeEmptyNameKeepsPhase asserts an empty name
// does not dispatch a create command. The user must see the inline
// error and try again; an empty-name dispatch would race a
// `worktree add` for a malformed path.
func TestEnterOnNewWorktreeEmptyNameKeepsPhase(t *testing.T) {
	m := model{
		phase:   phaseNewWorktree,
		width:   80,
		height:  24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
	}
	// newInput is empty by default.
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil (empty name should not dispatch)", cmd)
	}
	if gotModel.newError == "" {
		t.Error("newError = empty, want validation error message")
	}
}

// TestEnterOnNewWorktreeDispatchesCmd asserts a non-empty name
// triggers the ensureNewWorktreeCmd with the repo root and the
// input's current value. The cmd is what kicks off the async git
// work; this is the contract that wires the input to the worker.
func TestEnterOnNewWorktreeDispatchesCmd(t *testing.T) {
	m := model{
		phase:    phaseNewWorktree,
		width:    80,
		height:   24,
		repoRoot: "/tmp/repo",
		newInput: newInputModel(80),
	}
	m.newInput.SetValue("my-feature")

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree (cmd runs async; phase stays)", gotModel.phase)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want ensureNewWorktreeCmd")
	}
	// Run the cmd and verify the message type.
	msg := cmd()
	if _, ok := msg.(newWorktreeCreatedMsg); !ok {
		t.Errorf("cmd returned %T, want newWorktreeCreatedMsg", msg)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run 'TestEscOnNewWorktree|TestEnterOnNewWorktree' -v`
Expected: FAIL — esc falls through to `tea.Quit` (existing behavior), and Enter under `phaseNewWorktree` is unhandled.

- [ ] **Step 3: Add `phaseNewWorktree` to the esc handler**

In `internal/tui/app.go`, find the `case "esc":` block. The existing structure is a chain of `if m.phase == ... { ... return m, nil }` checks. Add a new branch for `phaseNewWorktree` before the final `return m, tea.Quit`:

```go
		case "esc":
			// esc is phase-aware: pop back from a nested screen, else quit.
			if m.phase == phaseBrowser {
				m.phase = phaseModel
				return m, nil
			}
			if m.phase == phaseResume {
				m.phase = phaseModel
				return m, nil
			}
			if m.phase == phaseGuardWarn {
				m.phase = phaseList
				return m, nil
			}
			if m.phase == phaseOllamaWarn {
				m.phase = phaseModel
				return m, nil
			}
			if m.phase == phaseNewWorktree {
				m.phase = phaseList
				m.newError = ""
				return m, nil
			}
			return m, tea.Quit
```

- [ ] **Step 4: Add the `phaseNewWorktree` Enter handler**

In `internal/tui/app.go`, find the `case "enter":` block. Inside its `switch m.phase`, add a new case for `phaseNewWorktree`. Place it after `phaseOllamaWarn`:

```go
			case phaseNewWorktree:
				if errMsg := validateNewWorktreeName(m.newInput.Value()); errMsg != "" {
					m.newError = errMsg
					return m, nil
				}
				m.newError = ""
				return m, ensureNewWorktreeCmd(m.repoRoot, m.newInput.Value())
```

- [ ] **Step 5: Forward `tea.Msg` to the textinput on `phaseNewWorktree`**

In `internal/tui/app.go`, find the bottom of the `Update` method where other phases forward messages to their list widgets. Add a new forwarding block for the textinput, alongside the others:

```go
	if m.phase == phaseNewWorktree && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.newInput, cmd = m.newInput.Update(msg)
		return m, cmd
	}
	return m, nil
```

(Place this block alongside the existing `m.list.Update` / `m.browser.Update` / `m.resume.choices.Update` / etc. blocks — typically just before the final `return m, nil`.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestEscOnNewWorktree|TestEnterOnNewWorktree' -v`
Expected: PASS for all three new tests.

- [ ] **Step 7: Run the full `tui` test suite to confirm no regressions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): handle enter/esc and input forwarding on phaseNewWorktree"
```

---

## Task 7: Handle `newWorktreeCreatedMsg` and the `pendingHighlight` reload path

**Files:**
- Modify: `internal/tui/app.go`
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing tests**

Add the following tests to `internal/tui/app_test.go` (append at end of file):

```go
// TestNewWorktreeCreatedErrorStaysOnPrompt asserts a git failure
// surfaces inline and keeps the user on the prompt. A silent
// failure would leave the user with no feedback.
func TestNewWorktreeCreatedErrorStaysOnPrompt(t *testing.T) {
	m := model{phase: phaseNewWorktree, width: 80, height: 24}
	got, _ := m.Update(newWorktreeCreatedMsg{err: errMock("git worktree add: boom")})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseNewWorktree {
		t.Errorf("phase = %v, want phaseNewWorktree (stays on error)", gotModel.phase)
	}
	if gotModel.newError == "" {
		t.Error("newError = empty, want error from msg")
	}
	if gotModel.pendingHighlight != "" {
		t.Errorf("pendingHighlight = %q, want empty (no success)", gotModel.pendingHighlight)
	}
}

// TestNewWorktreeCreatedSuccessTriggersReload asserts a successful
// create sets pendingHighlight, transitions back to phaseList, and
// dispatches loadEntriesCmd. The reload + highlight flow is what
// brings the user back to the picker on the new entry.
func TestNewWorktreeCreatedSuccessTriggersReload(t *testing.T) {
	m := model{phase: phaseNewWorktree, width: 80, height: 24}
	got, cmd := m.Update(newWorktreeCreatedMsg{path: "/repo/.worktrees/x", name: "x"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList (return to picker)", gotModel.phase)
	}
	if gotModel.pendingHighlight != "x" {
		t.Errorf("pendingHighlight = %q, want x", gotModel.pendingHighlight)
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want loadEntriesCmd")
	}
}

// TestEntriesLoadedAppliesPendingHighlight asserts that after a
// reload, the list cursor moves to the entry matching the
// pendingHighlight branch. This is the "you just created this,
// here it is" feedback.
func TestEntriesLoadedAppliesPendingHighlight(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
		{Type: worktree.TypeWorktree, Branch: "x", Path: "/repo/.worktrees/x"},
	}
	m := model{
		width:            80,
		height:           24,
		pendingHighlight: "x",
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	// Find the index of "x" — should be selected.
	items := gotModel.list.Items()
	var xIdx int = -1
	for i, it := range items {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && ei.entry.Branch == "x" {
			xIdx = i
			break
		}
	}
	if xIdx < 0 {
		t.Fatal("entry x not found in reloaded list")
	}
	if gotModel.list.Index() != xIdx {
		t.Errorf("list.Index() = %d, want %d (x)", gotModel.list.Index(), xIdx)
	}
	if gotModel.pendingHighlight != "" {
		t.Errorf("pendingHighlight = %q, want cleared after applying", gotModel.pendingHighlight)
	}
}

// TestEntriesLoadedNoPendingHighlightLeavesCursorAtZero asserts
// that without a pendingHighlight, the list cursor stays at the
// default (top). This is the normal reload path — cursor reset is
// fine.
func TestEntriesLoadedNoPendingHighlightLeavesCursorAtZero(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
	}
	m := model{width: 80, height: 24}
	got, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main"})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	// After a fresh build, the cursor is at 0 (the sentinel). That's
	// acceptable behavior — this test just guards against
	// pendingHighlight being unexpectedly set.
	if gotModel.pendingHighlight != "" {
		t.Errorf("pendingHighlight = %q, want empty", gotModel.pendingHighlight)
	}
}

// errMock is a small helper to make error literals readable in tests.
type errMock string

func (e errMock) Error() string { return string(e) }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui -run 'TestNewWorktreeCreated|TestEntriesLoaded' -v`
Expected: FAIL — `newWorktreeCreatedMsg` and `pendingHighlight` aren't handled yet.

- [ ] **Step 3: Add the `newWorktreeCreatedMsg` handler in `Update`**

In `internal/tui/app.go`, find the message-type switch at the top of `Update` (the `switch msg := msg.(type)` block). Add a new case alongside the other `case ...Msg:` handlers. Place it after the existing `case selectedEntryMsg:` block:

```go
	case newWorktreeCreatedMsg:
		if msg.err != nil {
			m.newError = msg.err.Error()
			return m, nil
		}
		m.pendingHighlight = msg.name
		m.phase = phaseList
		return m, loadEntriesCmd()
```

- [ ] **Step 4: Extend `entriesLoadedMsg` to carry `repoRoot` and update `loadEntriesCmd` to populate it**

The new-worktree cmd needs a repo root to pass to `EnsureForName`. The existing `loadEntriesCmd` already calls `worktree.RepoRoot()` to drive `Enumerate`; we extend the message to carry that root so the model can cache it without a second `git rev-parse` per create.

In `internal/tui/app.go`, find the `entriesLoadedMsg` type definition (at the bottom of the file, near the other message types). Replace it with:

```go
// entriesLoadedMsg carries the enumeration result to Update.
// repoRoot is the git repo root, captured at load time so the
// new-worktree prompt can use it without re-resolving.
type entriesLoadedMsg struct {
	entries       []worktree.Entry
	defaultBranch string
	repoRoot      string
	err           error
}
```

Then find the `loadEntriesCmd` function (also in `app.go`, near the other cmd helpers). Replace it with:

```go
// loadEntriesCmd returns a command that enumerates worktrees/branches
// and captures the repo root for the new-worktree prompt.
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		entries, err := worktree.Enumerate(root, root)
		defaultBranch, _ := worktree.DefaultBranch(root)
		return entriesLoadedMsg{entries: entries, defaultBranch: defaultBranch, repoRoot: root, err: err}
	}
}
```

- [ ] **Step 5: Cache `repoRoot` in the `entriesLoadedMsg` handler**

In `internal/tui/app.go`, find the existing `case entriesLoadedMsg:` block. It currently looks like:

```go
	case entriesLoadedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.entries = msg.entries
		m.defaultBranch = msg.defaultBranch
		m.list = buildList(msg.entries, m.width-2, m.height-2)
		m.ready = true

		if len(msg.entries) == 1 && isCurrentOnDefaultBranch(msg.entries[0], msg.defaultBranch) {
			m.list.Title = "WARNING: you are on the default branch (" + msg.defaultBranch + ")"
		}
		return m, nil
```

Replace it with the version that caches `repoRoot` and applies the pending highlight:

```go
	case entriesLoadedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.entries = msg.entries
		m.defaultBranch = msg.defaultBranch
		m.repoRoot = msg.repoRoot
		m.list = buildList(msg.entries, m.width-2, m.height-2)
		m.ready = true

		if len(msg.entries) == 1 && isCurrentOnDefaultBranch(msg.entries[0], msg.defaultBranch) {
			m.list.Title = "WARNING: you are on the default branch (" + msg.defaultBranch + ")"
		}
		// Apply a pending highlight (set after a successful
		// new-worktree create) by selecting the matching entry. If
		// the branch isn't found (shouldn't happen post-create),
		// leave the cursor at its default.
		if m.pendingHighlight != "" {
			for i, it := range m.list.Items() {
				if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && ei.entry.Branch == m.pendingHighlight {
					m.list.Select(i)
					break
				}
			}
			m.pendingHighlight = ""
		}
		return m, nil
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/tui -run 'TestNewWorktreeCreated|TestEntriesLoaded' -v`
Expected: PASS for all four new tests.

- [ ] **Step 7: Run the full `tui` test suite to confirm no regressions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests. (Some pre-existing tests construct `entriesLoadedMsg` without `repoRoot` — those still work because the new field defaults to `""`, which is harmless on the non-create path.)

- [ ] **Step 8: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): handle newWorktreeCreatedMsg, pendingHighlight, repoRoot caching"
```

---

## Task 8: Add the `phaseNewWorktree` View branch and `WindowSizeMsg` resize

**Files:**
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Add the View branch for `phaseNewWorktree`**

In `internal/tui/app.go`, find the `View` method. The existing phase branches are checked in order: `phaseResume`, `phaseOllamaWarn`, `phaseGuardWarn`, `phaseBrowser`, `phaseModel`, and the fallback list view. Add a new branch for `phaseNewWorktree`. Place it before the `phaseResume` branch (so it renders at the top, matching its priority as a focused-input screen):

```go
	if m.phase == phaseNewWorktree {
		if m.width <= 0 || m.height <= 0 {
			return "new worktree prompt (waiting for window size)"
		}
		body := m.newInput.View()
		if m.newError != "" {
			body += "\n" + newWorktreeErrorStyle.Render(m.newError)
		}
		return body + "\n[enter] create   [esc] cancel"
	}
```

- [ ] **Step 2: Add `WindowSizeMsg` resize for the textinput**

In `internal/tui/app.go`, find the `case tea.WindowSizeMsg:` block at the top of `Update`. The existing structure updates `m.width`, `m.height`, the list, and (conditionally) the browser/resume.choices. Add a new branch for `phaseNewWorktree`:

```go
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseBrowser {
			m.refreshBrowser()
		}
		if m.phase == phaseResume {
			m.resume.choices.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseNewWorktree {
			m.newInput.Width = msg.Width - 4
		}
```

- [ ] **Step 3: Run the full `tui` test suite to confirm no regressions**

Run: `go test ./internal/tui -v`
Expected: PASS for all tests. (The View branch and resize handler don't need new tests — they're straightforward composition, and the existing tests already cover the message types and phase transitions.)

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go
git commit -m "feat(tui): render phaseNewWorktree and resize its input"
```

---

## Task 9: Final verification — `go vet`, full test suite, and manual smoke

**Files:** (no file changes; verification only)

- [ ] **Step 1: Run `go vet`**

Run: `go vet ./...`
Expected: PASS — no static analysis issues.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: PASS for all packages.

- [ ] **Step 3: Build the `wt` binary**

Run: `go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt`
Expected: builds without error.

- [ ] **Step 4: Manual smoke test — interactive TUI**

In a terminal session (this needs a real TTY):

```bash
wt
```

Expected: the picker shows the "+ New worktree…" sentinel as the first item. Press `n` → the prompt appears with a focused input. Type a name like `smoke-test`, press Enter → the picker reappears with `smoke-test` selected. Press `q` to quit. Verify the worktree was created at `.worktrees/smoke-test/`:

```bash
ls .worktrees/smoke-test
```

Expected: a populated worktree directory (`.git` file pointing at the parent repo's `.git`).

- [ ] **Step 5: Manual smoke test — sentinel selection**

In a terminal session:

```bash
wt
```

Press Down arrow to keep the cursor on the sentinel (already at index 0). Press Enter → the prompt appears. Type a name, press Enter → returns to picker. Quit with `q`.

Expected: same outcome as Step 4. This exercises the Enter-on-sentinel path.

- [ ] **Step 6: Manual smoke test — empty name validation**

In a terminal session:

```bash
wt
```

Press `n` → prompt appears. Press Enter without typing anything → an error "name cannot be empty" appears below the input. Press `esc` → returns to picker.

Expected: validation works; user can recover.

- [ ] **Step 7: Manual smoke test — git error path**

In a terminal session:

```bash
wt
```

Press `n` → prompt appears. Type a name with traversal characters: `../evil`. Press Enter → the prompt stays and shows an error mentioning "path separators" (or similar wording from `EnsureForName`'s guard). Press `esc` to bail.

Expected: error surfaced inline; user not thrown back to picker on failure.

- [ ] **Step 8: Clean up the smoke-test worktree**

```bash
git worktree remove .worktrees/smoke-test
git branch -D smoke-test
```

Expected: worktree removed; no leftover artifacts.

- [ ] **Step 9: Commit (only if smoke tests surfaced a tweak)**

If Steps 4–8 surfaced any minor fix (typo, misrouted key, etc.), commit it. Otherwise, no commit — the implementation is complete.

```bash
git status  # should be clean
```

---

## Self-review

**Spec coverage check** (after writing the plan):

| Spec section | Implemented by task |
|---|---|
| Sentinel rendering (`Title`, `FilterValue`, `Description`) | Task 1 |
| Sentinel prepended in `buildList`, including empty-repo case | Task 1 (impl) + Task 2 (tests) |
| `new_worktree.go` helpers (`validateNewWorktreeName`, `newInputModel`, `ensureNewWorktreeCmd`, `newWorktreeCreatedMsg`, `newWorktreePlaceholder`, `newWorktreeErrorStyle`) | Task 3 |
| `phaseNewWorktree` constant | Task 4 |
| Model fields (`newInput`, `newError`, `pendingHighlight`, `repoRoot`) | Task 4 |
| `n` keypress on `phaseList && m.ready` | Task 5 |
| Enter on sentinel transitions to `phaseNewWorktree` | Task 5 |
| Enter on `phaseNewWorktree` validates and dispatches | Task 6 |
| `esc` on `phaseNewWorktree` returns to `phaseList`, clears error | Task 6 |
| `newWorktreeCreatedMsg` error stays on prompt; success dispatches `loadEntriesCmd` and sets `pendingHighlight` | Task 7 |
| `entriesLoadedMsg` applies `pendingHighlight` and clears it | Task 7 |
| `WindowSizeMsg` resizes input on `phaseNewWorktree` | Task 8 |
| View renders input + error + keybinding hint | Task 8 |
| Test coverage (sentinel rendering, buildList placement, validator, input model, ensureNewWorktreeCmd success/error, app-level n-key/sentinel/esc/enter/success/error/highlight tests) | Tasks 1, 2, 3, 5, 6, 7 |
| Manual smoke tests | Task 9 |

No gaps. All spec requirements have a task.

**Placeholder scan:** No "TBD", "TODO", "implement later", or "similar to Task N" references. Every test has its full code. Every code change shows the exact diff.

**Type consistency check:**

- `newWorktreeCreatedMsg{path, name, err}` — defined Task 3, returned by `ensureNewWorktreeCmd` (Task 3), received by `Update` handler (Task 7). Fields match across all uses. ✓
- `pendingHighlight string` — set in the `newWorktreeCreatedMsg` success branch (Task 7 Step 3), read and cleared in the `entriesLoadedMsg` handler (Task 7 Step 5). ✓
- `repoRoot string` — declared as a model field (Task 4), populated from the extended `entriesLoadedMsg.repoRoot` field (Task 7 Step 4), read in the `phaseNewWorktree` Enter handler to dispatch `ensureNewWorktreeCmd` (Task 6). All three legs are wired. ✓
- `phaseNewWorktree` constant — declared Task 4, transitioned to in `n` keypress and Enter-on-sentinel (Task 5), esc/enter inside the phase (Task 6), `newWorktreeCreatedMsg` success returns to `phaseList` (Task 7), `View` branch and resize (Task 8). All phase uses are consistent. ✓
- `entryKind` discriminator — defined Task 1, used in `Title`/`Description`/`FilterValue` switches (Task 1), in the `phaseList` Enter handler to detect the sentinel (Task 5 Step 4), and in the `entriesLoadedMsg` highlight loop to skip the sentinel when matching (Task 7 Step 5). ✓

All types and fields are wired consistently across the plan.