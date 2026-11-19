package sessioneditor

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestQuit_CleanExits verifies that pressing 'q' with no unsaved changes
// returns tea.Quit immediately.
func TestQuit_CleanExits(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd, got nil")
	}
}

// TestQuit_DirtyPrompts verifies that pressing 'q' with unsaved changes
// transitions to phaseQuit instead of quitting immediately.
func TestQuit_DirtyPrompts(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.ready = true
	m.list = buildAgentsList(testTheme(), 80, 24, m.cfg)
	m.dirty = true
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	m2 := got.(*model)
	if m2.phase != phaseQuit {
		t.Fatalf("expected phaseQuit, got %d", m2.phase)
	}
}

// TestQuitPrompt_Save verifies that pressing 'y' on the quit prompt
// dispatches a save command (by checking dirty is handled).
func TestQuitPrompt_Save(t *testing.T) {
	m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
	m.phase = phaseQuit
	m.dirty = true

	// Stub saveCmd to succeed.
	stubSaveResult(t, nil)

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected save command, got nil")
	}
	// Process the save message.
	msg := cmd()
	got2, cmd2 := got.(*model).Update(msg)
	m2 := got2.(*model)
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
	m.phase = phaseQuit
	m.dirty = true

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m2 := got.(*model)
	if m2.phase != phaseList {
		t.Fatalf("expected phaseList, got %d", m2.phase)
	}
	if m2.quitting {
		t.Error("expected quitting=false after cancel")
	}

	// Also test Esc.
	m.phase = phaseQuit
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m3 := got.(*model)
	if m3.phase != phaseList {
		t.Fatalf("expected phaseList after Esc, got %d", m3.phase)
	}
}

// agentsEditor is the editor in a terminal of the given size, opened on the
// Agents tab with its config delivered.
func agentsEditor(t *testing.T, width, height int) *model {
	t.Helper()
	scratchConfigHome(t)
	cfg := &config.Config{DefaultTag: "code"}
	m := newModel(testTheme(), cfg, nil)
	m.home = testUserHome
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	return send(t, m, loadedMsg{cfg: cfg})
}

// TestEscOnTheAgentsListDoesNotQuit verifies esc on the Agents tab's list
// does not end the editor. esc is the list's own quit key, and that quit
// skipped the unsaved-changes prompt: esc pressed once too often threw away
// every unsaved agent edit. q still asks.
func TestEscOnTheAgentsListDoesNotQuit(t *testing.T) {
	m := agentsEditor(t, 80, 24)
	m.dirty = true
	next, cmd := m.Update(keyMsg("esc"))
	m = next.(*model)
	if cmd != nil || m.phase != phaseList {
		t.Fatalf("esc on the Agents list: a command = %v, phase = %d; want nothing to happen", cmd != nil, m.phase)
	}
	next, cmd = m.Update(keyMsg("q"))
	if cmd != nil || next.(*model).phase != phaseQuit {
		t.Error("q with unsaved agent edits should still reach the prompt")
	}
}

// TestAgentsFilterLetsCtrlCQuit verifies that while the Agents filter is
// being typed q is text, and ctrl+c still leaves the editor — by way of the
// unsaved-changes prompt when there are agent edits. The list's own
// force-quit is off, so a filter that swallowed ctrl+c left a user who
// reached for it with nothing happening. (The Agents half of the Models
// tab's TestModelsTabFilterTakesTheTabsKeys, which moved to modeleditor.)
func TestAgentsFilterLetsCtrlCQuit(t *testing.T) {
	agents := stillCursors(agentsEditor(t, 80, 24))
	agents = keys(t, agents, "/", "q")
	if agents.tab != TabAgents || agents.list.FilterState() != list.Filtering {
		t.Fatalf("tab = %d, filter state = %v; want the Agents filter being typed", agents.tab, agents.list.FilterState())
	}
	if _, cmd := agents.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Error("ctrl+c while an Agents filter is being typed should quit")
	}
	agents.dirty = true
	next, cmd := agents.Update(keyMsg("ctrl+c"))
	if cmd != nil || next.(*model).phase != phaseQuit {
		t.Error("ctrl+c in a filter with unsaved agent edits should ask about them first")
	}
}

// TestQuitPromptFitsANarrowTerminal verifies the unsaved-changes prompt is
// wrapped to the terminal: its two lines are 54 and 51 columns, and at 40
// the choices past the edge ([c] cancel among them) were cut off with no
// sign of it. At 80 it reads as it always did.
func TestQuitPromptFitsANarrowTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {40, 24}, {80, 24}, {120, 50}} {
		m := agentsEditor(t, size[0], size[1])
		m.dirty = true
		m = keys(t, m, "q")
		if m.phase != phaseQuit {
			t.Fatalf("phase = %d, want the quit prompt", m.phase)
		}
		view := m.View()
		assertFits(t, "quit prompt", view, size[0], size[1])
		for _, want := range []string{"unsaved agent changes", "[y] save and quit", "[n] discard and quit", "[c] cancel"} {
			if !strings.Contains(view, want) {
				t.Errorf("at %dx%d the prompt lost %q:\n%s", size[0], size[1], want, view)
			}
		}
		if size[0] >= 80 && view != "You have unsaved agent changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n" {
			t.Errorf("at %d columns the prompt changed:\n%q", size[0], view)
		}
	}
}

// TestAFailedQuitSaveIsShownOnTheAgentsTab verifies a save-before-quit that
// fails takes the unsaved-changes prompt down and brings the Agents tab
// forward: the reason is that tab's status line and is drawn nowhere else.
// Left where it was, the user saw a prompt that ignored y. Both failures are
// covered: the write's, and the validation that runs before it.
func TestAFailedQuitSaveIsShownOnTheAgentsTab(t *testing.T) {
	stubSaveResult(t, errors.New("disk full"))

	t.Run("the write fails", func(t *testing.T) {
		m := agentsEditor(t, 80, 24)
		m.dirty = true
		m = keys(t, m, "q", "y")
		if m.tab != TabAgents || m.phase != phaseList || m.quitting || !m.dirty {
			t.Fatalf("tab = %d, phase = %d, quitting = %v, dirty = %v; want the Agents tab's list, the quit called off and the edits still unsaved", m.tab, m.phase, m.quitting, m.dirty)
		}
		if view := m.View(); !strings.Contains(view, "[Agents]") || !strings.Contains(view, "save failed: disk full") {
			t.Errorf("the failure should be on screen, on the Agents tab:\n%s", view)
		}
		assertFits(t, "failed quit save", m.View(), 80, 24)
	})
	t.Run("validation fails", func(t *testing.T) {
		m := agentsEditor(t, 80, 24)
		bad := *m.cfg
		bad.DefaultTag = ""
		m.cfg, m.dirty = &bad, true
		if err := m.cfg.ValidateAll(); err == nil {
			t.Fatal("the fixture should not validate")
		}
		m = keys(t, m, "q", "y")
		if m.tab != TabAgents || m.phase != phaseList || m.quitting || !m.dirty {
			t.Fatalf("tab = %d, phase = %d, quitting = %v, dirty = %v; want the Agents tab's list, the quit called off and the edits still unsaved", m.tab, m.phase, m.quitting, m.dirty)
		}
		if view := m.View(); !strings.Contains(view, "[Agents]") || !strings.Contains(view, "validation: ") {
			t.Errorf("the failure should be on screen, on the Agents tab:\n%s", view)
		}
	})
}

// TestAgentsStatusFollowsTheLastSave verifies the Agents tab draws the status
// of the latest save, not the one it first rendered: a failed ctrl+s says
// "save failed: ...", and the save that then succeeds replaces it with
// "saved". The rendered status was cached by the terminal's width alone, so
// at a width that never changed the screen went on showing the first status
// drawn — usually none, and a failed save looked like a save.
func TestAgentsStatusFollowsTheLastSave(t *testing.T) {
	fail := true
	old := saveCmd
	saveCmd = func(*config.Config) tea.Cmd {
		return func() tea.Msg {
			if fail {
				return saveMsg{err: errors.New("disk full")}
			}
			return saveMsg{}
		}
	}
	t.Cleanup(func() { saveCmd = old })

	m := agentsEditor(t, 80, 24)
	if view := m.View(); m.tab != TabAgents || strings.Contains(view, "save") {
		t.Fatalf("want the Agents tab with no status yet:\n%s", view)
	}
	m.dirty = true
	m = keys(t, m, "ctrl+s")
	if view := m.View(); !strings.Contains(view, "save failed: disk full") {
		t.Fatalf("a failed save should say so:\n%s", view)
	}
	fail = false
	m = keys(t, m, "ctrl+s")
	if view := m.View(); strings.Contains(view, "save failed") || !strings.Contains(view, "saved") {
		t.Errorf("the save that worked should replace the failure:\n%s", view)
	}
	assertFits(t, "agents tab with a status", m.View(), 80, 24)
}
