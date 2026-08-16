package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// TestInitLoadsEntries asserts Init starts background worktree enumeration.
// Without this command, the TUI would sit forever at the loading screen.
func TestInitLoadsEntries(t *testing.T) {
	m := model{loading: true, status: "loading worktrees..."}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned nil cmd; expected loadEntriesCmd")
	}
}

// TestUpdateWindowSizeMsg asserts the model records the terminal's reported
// dimensions on a WindowSizeMsg. Without this, the list would lay out at zero
// size and render nothing usable.
func TestUpdateWindowSizeMsg(t *testing.T) {
	m := model{status: "ready"}
	got, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("dimensions = (%d, %d), want (80, 24)", gotModel.width, gotModel.height)
	}
}

// TestUpdateQuitKeys asserts that q, esc, and ctrl+c all return tea.Quit.
// These three are the universal TUI exit affordances; if any one is missing
// the user can get stuck in the alternate screen with no way back to the
// shell.
func TestUpdateQuitKeys(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	}
	for _, c := range cases {
		m := model{status: "ready"}
		_, cmd := m.Update(c)
		if cmd == nil {
			t.Errorf("key %q: got nil cmd, want tea.Quit", c.String())
		}
	}
}

// TestUpdateOtherKeyIgnored asserts that pressing a non-quit key returns nil
// (no quit) and leaves state untouched while the list is not yet ready. If
// unknown keys accidentally quit or mutate state, behavior is
// non-deterministic across terminals.
func TestUpdateOtherKeyIgnored(t *testing.T) {
	m := model{status: "ready", width: 80, height: 24}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Errorf("key 'x': got non-nil cmd, want nil")
	}
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.status != "ready" || gotModel.width != 80 || gotModel.height != 24 {
		t.Errorf("state mutated by non-quit key: %+v", gotModel)
	}
}

// TestViewLoading asserts View shows a loading message while worktrees are
// being enumerated. This is the first feedback the user sees after launching.
func TestViewLoading(t *testing.T) {
	m := model{loading: true, status: "loading worktrees..."}
	view := m.View()
	if !strings.Contains(view, "loading worktrees") {
		t.Errorf("View missing loading message: %q", view)
	}
}

// TestViewNotReady asserts View renders the status string when the list is
// not ready. This covers error states and any pre-load messages.
func TestViewNotReady(t *testing.T) {
	m := model{status: "ready"}
	view := m.View()
	if !strings.Contains(view, "ready") {
		t.Errorf("View missing status %q: %q", "ready", view)
	}
}

// TestViewBeforeWindowSizeDoesNotPanic asserts View is safe to call when no
// WindowSizeMsg has been received yet. The model's width/height start at
// zero; the simple string views must not panic.
func TestViewBeforeWindowSizeDoesNotPanic(t *testing.T) {
	m := model{status: "ready"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked with no WindowSizeMsg: %v", r)
		}
	}()
	_ = m.View()
}

// TestEntriesLoadedMsg asserts receiving worktree data builds the list,
// marks the model ready, and clears the loading flag. Without this the TUI
// would never transition from the loading screen to the picker.
func TestEntriesLoadedMsg(t *testing.T) {
	m := model{loading: true, width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	gotModel, ok := got.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", got)
	}
	if gotModel.loading {
		t.Errorf("loading = true, want false")
	}
	if !gotModel.ready {
		t.Errorf("ready = false, want true")
	}
	if len(gotModel.entries) != 1 {
		t.Errorf("entries = %d, want 1", len(gotModel.entries))
	}
}

// TestViewReady asserts the rendered list contains the title. This confirms
// the list widget was built and is visible once worktrees are loaded.
func TestViewReady(t *testing.T) {
	m := model{loading: true, width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	gotModel := got.(model)
	view := gotModel.View()
	if !strings.Contains(view, "Pick a worktree or branch") {
		t.Errorf("View missing list title: %q", view)
	}
}

// TestEnterSelectsEntry asserts that pressing Enter when the list is ready
// emits a selectedEntryMsg carrying the current entry. This is the primary
// selection affordance for the worktree picker.
func TestEnterSelectsEntry(t *testing.T) {
	m := model{loading: true, width: 80, height: 24}
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}
	got, _ := m.Update(entriesLoadedMsg{entries: entries})
	m = got.(model)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter returned nil cmd, want selectedEntryMsg")
	}
	msg := cmd()
	selected, ok := msg.(selectedEntryMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want selectedEntryMsg", msg)
	}
	if selected.entry.Branch != "main" {
		t.Errorf("selected branch = %q, want main", selected.entry.Branch)
	}
}

// TestSelectedEntryMsgUpdatesStatus asserts that a selectedEntryMsg updates
// the status line. This confirms the message is handled and provides user
// feedback even before lesson 14 wires selection to a launch action.
func TestSelectedEntryMsgUpdatesStatus(t *testing.T) {
	m := model{ready: true, width: 80, height: 24}
	m.list = buildList([]worktree.Entry{
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}, 78, 22)

	got, _ := m.Update(selectedEntryMsg{entry: worktree.Entry{Branch: "feature"}})
	gotModel := got.(model)
	if !strings.Contains(gotModel.status, "feature") {
		t.Errorf("status missing selected branch: %q", gotModel.status)
	}
}
