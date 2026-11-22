package tui

import (
	"syscall"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// pickerForRows builds the picker model with its row list in the named
// phase — the smallest hand-set state Update routes on.
func pickerForRows(rowsPhase cleanupPhase) model {
	m := newRunModel(false, false, "", "", "", "", "", nil, themes.Theme{}, "", nil, &config.Config{}, nil)
	m.ready = true
	m.phase = phaseList
	m.rows.phase = rowsPhase
	return m
}

// quitsNow reports whether cmd is tea.Quit.
func quitsNow(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestPickerOSSignalWaitsOutARemovalRun pins #577: an OS signal arriving
// while the picker's row list runs a removal does not end wt — the list
// remembers it (Ruling T, the cleanup list's own rule) and the program quits
// once the run ends, after its outcomes print (the outcomes themselves are
// pinned by the list's TestSignalDuringRunWaitsAndKeepsOutcomes). Until the
// fix the picker's Update had no cleanupSignalMsg case, so a forwarded
// signal never reached the list and Bubble Tea's own handler ended the
// program mid-run, losing the removal log.
func TestPickerOSSignalWaitsOutARemovalRun(t *testing.T) {
	m := pickerForRows(cleanupPhaseRunning)

	next, cmd := m.Update(cleanupSignalMsg{sig: syscall.SIGTERM})
	p := next.(model)
	if quitsNow(cmd) {
		t.Fatal("a signal during a removal run ended wt; the run must be waited out (Ruling T)")
	}
	if !p.rows.quitAfterRun {
		t.Error("the signal during the run was not remembered: rows.quitAfterRun = false")
	}
	if p.rows.phase != cleanupPhaseRunning {
		t.Errorf("the run was disturbed: rows.phase = %v", p.rows.phase)
	}
}

// TestPickerOSSignalAtRestQuits pins the signal's other half: with the row
// list at rest the signal quits the whole picker — never a hang — exactly
// what Bubble Tea's own handler did before #577.
func TestPickerOSSignalAtRestQuits(t *testing.T) {
	m := pickerForRows(cleanupPhaseList)

	next, cmd := m.Update(cleanupSignalMsg{sig: syscall.SIGINT})
	p := next.(model)
	if !quitsNow(cmd) || !p.rows.done {
		t.Fatalf("signal with the list at rest: quit %v, rows.done %v — want the picker to quit now", quitsNow(cmd), p.rows.done)
	}
}

// TestPickerOSSignalOutsideTheListQuits pins the signal everywhere else in
// the picker (a start, a launch prep, a popup): it quits now, unchanged from
// Bubble Tea's own handler.
func TestPickerOSSignalOutsideTheListQuits(t *testing.T) {
	m := pickerForRows(cleanupPhaseRunning)
	m.phase = phaseStarting

	next, cmd := m.Update(cleanupSignalMsg{sig: syscall.SIGTERM})
	if !quitsNow(cmd) {
		t.Fatal("a signal outside the row list did not quit the picker")
	}
	_ = next
}
