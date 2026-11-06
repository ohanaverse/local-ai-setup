package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// Review round 2 of the removal flow (Ruling U): what `delete` is typed
// for is always on screen, in count and kind, and a toggle never lands on
// an entry the user cannot see.

var (
	pgDownKey = tea.KeyMsg{Type: tea.KeyPgDown}
)

// TestTabAfterPgdownTogglesOnlyAVisibleEntry pins Ruling U against the
// round-1 data loss: after pgdown scrolled the highlighted entry (k1) off
// screen, tab must not toggle its branch deletion unseen. It brings k1 back
// into view first; every entry whose branch is toggled shows its "[x]" line
// on screen; and after `delete` only the branch toggled in view is gone.
func TestTabAfterPgdownTogglesOnlyAVisibleEntry(t *testing.T) {
	r := newRmRepo(t)
	names := []string{"k1", "k2", "k3", "k4", "k5", "k6"}
	for _, n := range names {
		r.unpushed(n)
	}
	h := r.harness()
	m := press(t, setTicks(t, h.list(80, 14), names...), enterKey)
	if m.conf.entries[m.conf.cursor].pv.Item.Name != "k1" {
		t.Fatalf("cursor on %s", m.conf.entries[m.conf.cursor].pv.Item.Name)
	}
	m = press(t, m, pgDownKey)
	m = press(t, m, pgDownKey)
	if strings.Contains(m.View(), "worktree k1") {
		t.Fatalf("k1 still on screen after 2x pgdown; the fixture does not scroll it away:\n%s", m.View())
	}
	toggledVisible := func(step string) {
		t.Helper()
		v := m.View()
		for _, e := range m.conf.entries {
			if e.delBranch && !strings.Contains(squash(v), squash("[x] also delete unmerged branch "+e.pv.Item.Name)) {
				t.Errorf("%s: %s's branch is toggled for deletion but its [x] line is not on screen:\n%s", step, e.pv.Item.Name, v)
			}
		}
	}
	m = press(t, m, tabKey)
	toggledVisible("first tab")
	if !strings.Contains(m.View(), "worktree k1") {
		t.Errorf("tab did not bring the highlighted entry back into view:\n%s", m.View())
	}
	for _, e := range m.conf.entries {
		if e.delBranch {
			t.Errorf("the first tab toggled %s while its toggle line was out of sight", e.pv.Item.Name)
		}
	}
	m = press(t, m, tabKey)
	toggledVisible("second tab")
	if !m.conf.entries[m.conf.cursor].delBranch {
		t.Errorf("the second tab, with k1 in view, did not toggle it")
	}
	m = typeWord(t, m, "delete")
	m = press(t, m, enterKey)
	for _, n := range names {
		want := false
		for _, it := range h.ran {
			if it.Name == n && it.Approved.Delete {
				want = true
			}
		}
		if gone := !r.refExists("refs/heads/" + n); gone != want {
			t.Errorf("%s: branch gone %v, toggled %v", n, gone, want)
		}
	}
}

// TestPromptAlwaysCountsWhatDeleteApproves pins Ruling U's prompt: on a
// short narrow terminal (40x8, where the summary and most entries are off
// screen) and at 80x24, the prompt states how many items `delete` removes,
// how many need the --force and -D equivalents, and how many are not shown.
func TestPromptAlwaysCountsWhatDeleteApproves(t *testing.T) {
	r := newRmRepo(t)
	r.dirty("d1")
	r.unpushed("k2")
	r.merged("m3")
	h := r.harness()
	for _, tc := range []struct {
		w, ht  int
		want   string
		hidden bool
	}{
		{40, 8, "removes 3 (1 force, 0 -D)", true},
		{80, 24, "remove 3 items (1 force, 0 -D)", false},
	} {
		m := press(t, setTicks(t, h.list(tc.w, tc.ht), "d1", "k2", "m3"), enterKey)
		v := m.View()
		checkRemovalScreen(t, fmt.Sprintf("prompt %dx%d", tc.w, tc.ht), v, tc.w, tc.ht)
		if !strings.Contains(squash(v), squash(tc.want)) {
			t.Errorf("%dx%d: prompt lacks %q:\n%s", tc.w, tc.ht, tc.want, v)
		}
		if got := strings.Contains(v, "not shown"); got != tc.hidden {
			t.Errorf("%dx%d: says not shown = %v, want %v:\n%s", tc.w, tc.ht, got, tc.hidden, v)
		}
		// The -D count follows a toggle.
		m = confirmEntryAt(t, m, "k2")
		m = press(t, m, tabKey)
		if m.conf.entries[m.conf.cursor].delBranch {
			if v := m.View(); !strings.Contains(squash(v), squash("1 force, 1 -D")) {
				t.Errorf("%dx%d: -D count not updated after the toggle:\n%s", tc.w, tc.ht, v)
			}
		}
	}
}

// TestConfirmKeepsTitleSummaryHintsWhenTheyFit pins that the title, the
// per-group summary and the key hints give way only to a terminal too
// short for them, never at 80x24 with a small selection.
func TestConfirmKeepsTitleSummaryHintsWhenTheyFit(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	r.unpushed("kept")
	h := r.harness()
	m := press(t, setTicks(t, h.list(80, 24), "done", "kept"), enterKey)
	v := m.View()
	for _, want := range []string{"wt cleanup · confirm", "remove 2 of 2 ticked (1 proven, 1 not proven", "tab: also delete"} {
		if !strings.Contains(squash(v), squash(want)) {
			t.Errorf("80x24 confirm screen lacks %q:\n%s", want, v)
		}
	}
}

// TestRestoreIDNeverCutBelow40Columns pins that at 30 columns the results
// screen still fits and still holds the whole 40-character commit id (split
// over lines, never clipped).
func TestRestoreIDNeverCutBelow40Columns(t *testing.T) {
	r := newRmRepo(t)
	r.merged("done")
	tip := r.git("rev-parse", "refs/heads/done")
	h := r.harness()
	m := press(t, setTicks(t, h.list(30, 60), "done"), enterKey)
	m = press(t, m, keyRunes("y"))
	v := m.View()
	for _, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > 30 {
			t.Errorf("line %d wide: %q", w, l)
		}
	}
	if !strings.Contains(squash(v), tip) {
		t.Errorf("the restore id %s is not whole on the 30-column screen:\n%s", tip, v)
	}
	_ = removal.WtRemoved
}
