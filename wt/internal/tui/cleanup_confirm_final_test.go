package tui

import (
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Final review fixes on the removal flow: untoggling past the screen, the
// all-proven prompt's Y, and SIGHUP during a run.

// TestTabUntogglesAnEntryTallerThanTheScreen: once an entry is toggled and
// the terminal shrinks so its decision lines no longer fit, tab could not
// turn the toggle off (it only scrolled), leaving esc as the only way out.
// Turning a toggle OFF never adds an approval, so it applies whether or not
// the entry is in view; turning it ON still needs it in view (Ruling U).
func TestTabUntogglesAnEntryTallerThanTheScreen(t *testing.T) {
	r := newRmRepo(t)
	r.unpushed("kept")
	h := r.harness()
	m := press(t, setTicks(t, h.list(80, 20), "kept"), enterKey)
	m = press(t, m, tabKey)
	if !m.conf.entries[m.conf.cursor].delBranch {
		t.Fatal("tab at 80x20 did not toggle the entry on")
	}
	m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 30, Height: 3})
	if m.frame().visible(m.conf.cursor) {
		t.Fatal("fixture: the entry still fits at 30x3")
	}
	m = press(t, m, tabKey)
	if m.conf.entries[m.conf.cursor].delBranch {
		t.Error("tab at 30x3 did not turn the toggle off")
	}
	m = press(t, m, tabKey)
	if m.conf.entries[m.conf.cursor].delBranch {
		t.Error("tab turned the toggle on for an entry not in view")
	}
}

// TestAllProvenConfirmAcceptsUppercaseY: the CLI's y/N prompt lowercases
// the answer; the list's all-proven confirmation took only a lowercase y
// and cancelled on Y (caps lock), which surprised a user who answered yes.
func TestAllProvenConfirmAcceptsUppercaseY(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	h := r.harness()
	m := press(t, setTicks(t, h.list(80, 24), "one"), enterKey)
	m = press(t, m, keyRunes("Y"))
	if m.phase != cleanupPhaseRunning && m.phase != cleanupPhaseResults {
		t.Fatalf("Y on the all-proven confirmation left phase %v (status %q)", m.phase, m.status)
	}
}

// TestForwardSignalsIncludesSIGHUP: a terminal closed mid-run sends SIGHUP,
// whose default action killed wt and lost the run's outcomes; it is
// forwarded like SIGINT and SIGTERM, so the run finishes (Ruling T).
func TestForwardSignalsIncludesSIGHUP(t *testing.T) {
	if !slices.Contains(forwardedSignals, os.Signal(syscall.SIGHUP)) {
		t.Fatalf("forwardedSignals = %v, want SIGHUP among them", forwardedSignals)
	}
	got := make(chan tea.Msg, 1)
	stop := forwardSignals(func(m tea.Msg) { got <- m })
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if s, ok := m.(cleanupSignalMsg); !ok || s.sig != syscall.SIGHUP {
			t.Errorf("forwarded %#v, want cleanupSignalMsg{SIGHUP}", m)
		}
	case <-time.After(5 * time.Second):
		t.Error("SIGHUP was not forwarded")
	}
}
