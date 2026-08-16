package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestUpdateWindowSizeMsg asserts the model records the terminal's reported
// dimensions on a WindowSizeMsg. Without this, the centered View would lay
// out at zero width and the user would see no content when the TUI starts
// before Bubble Tea has dispatched the initial window-size event.
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
	cases := []struct {
		key tea.KeyMsg
	}{
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}},
		{tea.KeyMsg{Type: tea.KeyEsc}},
		{tea.KeyMsg{Type: tea.KeyCtrlC}},
	}
	for _, c := range cases {
		m := model{status: "ready"}
		_, cmd := m.Update(c.key)
		if cmd == nil {
			t.Errorf("key %q: got nil cmd, want tea.Quit", c.key.String())
		}
	}
}

// TestUpdateOtherKeyIgnored asserts that pressing a non-quit key returns nil
// (no quit) and leaves state untouched. If unknown keys accidentally quit,
// the TUI is unusable; if they accidentally mutate state, behavior is
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

// TestViewContainsStatusAndHint asserts View emits the status string and the
// quit hint. These are the two pieces of user-visible feedback: without the
// status, the screen is blank; without the hint, the user has no idea how to
// exit.
func TestViewContainsStatusAndHint(t *testing.T) {
	m := model{status: "ready", width: 80, height: 24}
	view := m.View()
	if !strings.Contains(view, "ready") {
		t.Errorf("View missing status %q: %q", "ready", view)
	}
	if !strings.Contains(view, "Press q to quit") {
		t.Errorf("View missing quit hint: %q", view)
	}
}

// TestViewBeforeWindowSizeDoesNotPanic asserts View is safe to call when no
// WindowSizeMsg has been received yet. The model's width/height start at
// zero; lipgloss must accept zero dimensions without panicking — otherwise
// the first frame (before Bubble Tea's initial WindowSizeMsg) would crash.
func TestViewBeforeWindowSizeDoesNotPanic(t *testing.T) {
	m := model{status: "ready"}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked with no WindowSizeMsg: %v", r)
		}
	}()
	_ = m.View()
}
