package configeditor

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestQuit_CleanExits verifies that pressing 'q' with no unsaved changes
// returns tea.Quit immediately.
func TestQuit_CleanExits(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	m.ready = true
	m.lists[sectionAgents] = buildAgentsList(testTheme(), 80, 24, m.cfg)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd, got nil")
	}
}

// TestQuit_DirtyPrompts verifies that pressing 'q' with unsaved changes
// transitions to phaseQuit instead of quitting immediately.
func TestQuit_DirtyPrompts(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	m.ready = true
	m.lists[sectionAgents] = buildAgentsList(testTheme(), 80, 24, m.cfg)
	m.dirty = true
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	m2 := got.(model)
	if m2.phase != phaseQuit {
		t.Fatalf("expected phaseQuit, got %d", m2.phase)
	}
}

// TestQuitPrompt_Save verifies that pressing 'y' on the quit prompt
// dispatches a save command (by checking dirty is handled).
func TestQuitPrompt_Save(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{DefaultTag: "code"}
	m.phase = phaseQuit
	m.dirty = true

	// Stub saveCmd to succeed.
	old := saveCmd
	saveCmd = func(cfg *config.Config) tea.Cmd {
		return func() tea.Msg { return saveMsg{} }
	}
	defer func() { saveCmd = old }()

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected save command, got nil")
	}
	// Process the save message.
	msg := cmd()
	got2, cmd2 := got.(model).Update(msg)
	m2 := got2.(model)
	if m2.dirty {
		t.Error("expected dirty=false after save-and-quit")
	}
	if cmd2 == nil {
		t.Fatal("expected tea.Quit after successful save")
	}
}

// TestQuitPrompt_Discard verifies that pressing 'n' on the quit prompt
// returns tea.Quit immediately without saving.
func TestQuitPrompt_Discard(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	m.phase = phaseQuit
	m.dirty = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd, got nil")
	}
}

// TestQuitPrompt_Cancel verifies that pressing 'c' or Esc returns to the
// list view and clears the quit prompt.
func TestQuitPrompt_Cancel(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{}
	m.phase = phaseQuit
	m.dirty = true

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m2 := got.(model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList, got %d", m2.phase)
	}
	if m2.quitting {
		t.Error("expected quitting=false after cancel")
	}

	// Also test Esc.
	m.phase = phaseQuit
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := got.(model)
	if m3.phase != phaseList {
		t.Fatalf("expected phaseList after Esc, got %d", m3.phase)
	}
}
