package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// allRowKeys is every hint the footer can show, for the width tests.
func allRowKeys() []keyHint {
	return []keyHint{
		{"↑/↓", "↑/↓ move", "↑/↓"}, {"enter", "enter launch", "enter"}, {"n", "n new worktree", "n new"}, {"/", "/ filter", "/"},
		{"x", "x remove", "x"}, {"d", "d +branch", "d"}, {"o", "o delete origin", "o"}, {"r", "r rescue", "r"},
		{"R", "R restore root", "R restore"}, {"F", "F fetch", "F"}, {"q", "q quit", "q"},
	}
}

// TestFitHintsShortensBeforeDropping pins fitHints at four widths with every
// key on offer: at most two lines, none wider than the terminal, and every
// key still named. Why: a hint that is cut or dropped at the edge is a key
// the user cannot find, which is what the footer exists to prevent.
func TestFitHintsShortensBeforeDropping(t *testing.T) {
	for _, width := range []int{140, 80, 50, 40} {
		got := fitHints(allRowKeys(), width)
		lines := strings.Split(got, "\n")
		if len(lines) > 2 {
			t.Errorf("width %d: %d lines:\n%s", width, len(lines), got)
		}
		for _, line := range lines {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: line is %d wide: %q", width, w, line)
			}
		}
		for _, h := range allRowKeys() {
			if !strings.Contains(got, h.short) && !strings.Contains(got, h.long) {
				t.Errorf("width %d: footer lost %q:\n%s", width, h.key, got)
			}
		}
	}
	if got := fitHints(allRowKeys(), 140); strings.Contains(got, "\n") || !strings.Contains(got, "R restore root") {
		t.Errorf("width 140 should be one line of long forms:\n%s", got)
	}
}

// TestFooterIsTheLastThingDropped walks both modes over the long-rows
// fixture at 80x24 and 40x12: the view never exceeds the terminal, the
// highlighted row stays on screen, and the footer's last hint (q) is on one
// of the last two lines. Why: the footer is the key legend, so it is the last
// thing a short terminal gives up, after the header and the title.
func TestFooterIsTheLastThingDropped(t *testing.T) {
	for _, mode := range []listMode{modeManage, modeLaunch} {
		for _, size := range [][2]int{{80, 24}, {40, 12}} {
			m := newRowList(longRowsFixture(), rowListOpts{Mode: mode, Theme: themes.Default, Now: time.Now(), RemoveFuncs: wiredRemoveFuncs()})
			m = cleanupSend(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			for i := 0; i < 30; i++ {
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) > size[1] {
					t.Fatalf("mode %v %v step %d: %d lines:\n%s", mode, size, i, len(lines), view)
				}
				for _, line := range lines {
					if w := lipgloss.Width(line); w > size[0] {
						t.Fatalf("mode %v %v step %d: line %d wide: %q", mode, size, i, w, line)
					}
				}
				if !strings.Contains(view, "> ") {
					t.Fatalf("mode %v %v step %d: no highlighted item:\n%s", mode, size, i, view)
				}
				// A row taller than the room (a 40-column terminal wraps the long
				// sentences to ten lines) takes the whole screen: the footer is
				// the last thing dropped, and that is the last case.
				tail := strings.Join(lines[max(0, len(lines)-2):], "\n")
				if len(m.itemLines(m.cursor)) <= size[1]/2 && !strings.Contains(tail, "q quit") && !strings.Contains(tail, "· q") {
					t.Fatalf("mode %v %v step %d: footer is not at the bottom:\n%s", mode, size, i, view)
				}
				m = cleanupSend(t, m, keyRunes("j"))
			}
		}
	}
}

// TestTitleNamesOnlyTheScreen pins that the title line is the screen's name:
// the key legends moved to the footer, so the title carries none.
func TestTitleNamesOnlyTheScreen(t *testing.T) {
	for _, mode := range []listMode{modeManage, modeLaunch} {
		m := newRowList(footerKeysReport(), rowListOpts{Mode: mode, Theme: themes.Default, Now: time.Now()})
		m = cleanupSend(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		title := m.titleLine()
		for _, bad := range []string{"enter", "quit", "space", "filter"} {
			if strings.Contains(title, bad) {
				t.Errorf("mode %v: title names %q: %q", mode, bad, title)
			}
		}
		if !strings.Contains(title, "wt") {
			t.Errorf("mode %v: title lost the name: %q", mode, title)
		}
	}
}
