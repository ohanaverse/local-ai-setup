package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// ghDownCause is a long, realistic one-line gh failure.
const ghDownCause = "gh pr list: exit status 4: To get started with GitHub CLI, please run: gh auth login"

// ghDownFixture is longRowsFixture with gh unavailable for the reason above.
func ghDownFixture() cleanup.Report {
	r := longRowsFixture()
	r.GH = cleanup.GHUnavailable
	for i := range r.Rows {
		for j := range r.Rows[i].Reasons {
			if r.Rows[i].Reasons[j].Code == cleanup.ReasonPRUnknown {
				r.Rows[i].Reasons[j].Detail = ghDownCause
			}
		}
	}
	return r
}

// ghDownCause2 is a second distinct cause, as long as a real one.
const ghDownCause2 = "own repository unknown; cannot filter fork PRs (origin names no GitHub repository here)"

// ghDownTwoCauses is ghDownFixture with every other PR-lookup row failing
// for ghDownCause2 instead.
func ghDownTwoCauses() cleanup.Report {
	r := ghDownFixture()
	n := 0
	for i := range r.Rows {
		for j := range r.Rows[i].Reasons {
			if r.Rows[i].Reasons[j].Code == cleanup.ReasonPRUnknown {
				if n%2 == 1 {
					r.Rows[i].Reasons[j].Detail = ghDownCause2
				}
				n++
			}
		}
	}
	return r
}

// assertHeaderWholeOrGone fails when the view shows any part of the gh line
// without all of it: the "gh: unavailable" start without every cause, or a
// cause's first words without the whole cause.
func assertHeaderWholeOrGone(t *testing.T, label, view string, causes ...string) {
	t.Helper()
	flat := squash(view)
	hasHeader := strings.Contains(flat, squash("gh:unavailable"))
	for _, c := range causes {
		whole := strings.Contains(flat, squash(c))
		start := strings.Contains(flat, squash(c[:20]))
		if hasHeader && !whole {
			t.Errorf("%s: header shows gh but cuts %q:\n%s", label, c, view)
		}
		if start && !whole {
			t.Errorf("%s: cause %q shown in part:\n%s", label, c, view)
		}
	}
}

// TestCleanupListHeaderTwoCausesAt80x24 pins that with two long distinct gh
// causes the 80x24 TUI shows the gh line with both causes whole, or drops
// the header, never a mid-sentence cut, within 24 rows. Why: two causes
// make the header longest, the case most likely to be clipped.
func TestCleanupListHeaderTwoCausesAt80x24(t *testing.T) {
	r := ghDownTwoCauses()
	if !strings.Contains(strings.Join(cleanup.HeaderLines(r, r.Fetch.At), "\n"), ghDownCause2) {
		t.Fatal("fixture does not carry the second cause")
	}
	for _, name := range []string{"", "many-reasons"} {
		m := newSizedCleanupList(t, r, 80, 24)
		if name != "" {
			m = moveTo(t, m, name)
		}
		view := m.View()
		if h := lipgloss.Height(view); h > 24 {
			t.Errorf("row %q: view is %d rows, want <= 24", name, h)
		}
		assertHeaderWholeOrGone(t, "row "+name, view, ghDownCause, ghDownCause2)
		if name == "" && !strings.Contains(squash(view), squash(ghDownCause+" | "+ghDownCause2)) {
			t.Errorf("first row: header lacks both causes:\n%s", view)
		}
	}
}

// TestCleanupListGHHeaderNarrowIsWholeOrGone pins the gh-down header below
// 80 columns: at 40 and 24 columns, at a full and a short height, the header
// shows whole (wrapped) or not at all, never clipped, and the view fits.
// Why: earlier review found this correct but unpinned below 80 columns.
func TestCleanupListGHHeaderNarrowIsWholeOrGone(t *testing.T) {
	for _, w := range []int{40, 24} {
		for _, h := range []int{24, 10} {
			for _, name := range []string{"", "many-reasons"} {
				m := newSizedCleanupList(t, ghDownFixture(), w, h)
				if name != "" {
					m = moveTo(t, m, name)
				}
				view := m.View()
				label := fmt.Sprintf("%dx%d row %q", w, h, name)
				if got := lipgloss.Height(view); got > h {
					t.Errorf("%s: view is %d rows, want <= %d", label, got, h)
				}
				for _, line := range strings.Split(view, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Errorf("%s: line %d wide, want <= %d: %q", label, lw, w, line)
					}
				}
				assertHeaderWholeOrGone(t, label, view, ghDownCause)
			}
		}
	}
}

// TestCleanupListHeaderNamesWhyGHIsDown pins that, at 80 columns, the TUI
// header shows the whole gh cause (wrapped between words) or none of it when
// the frame drops the header for a tall row, never a cut one, and that the
// view stays within 24 rows. Why: a silent mid-sentence cut would hide the
// fix (gh auth login) the cause names.
func TestCleanupListHeaderNamesWhyGHIsDown(t *testing.T) {
	for _, name := range []string{"", "many-reasons"} {
		m := newSizedCleanupList(t, ghDownFixture(), 80, 24)
		if name != "" {
			m = moveTo(t, m, name)
		}
		view := m.View()
		if h := lipgloss.Height(view); h > 24 {
			t.Errorf("row %q: view is %d rows, want <= 24", name, h)
		}
		flat := squash(view)
		hasHeader := strings.Contains(flat, squash("gh:unavailable"))
		hasCause := strings.Contains(flat, squash(ghDownCause))
		if hasHeader && !hasCause {
			t.Errorf("row %q: header shows gh but cuts the cause:\n%s", name, view)
		}
		if name == "" && !hasCause {
			t.Errorf("row %q: header lacks the cause:\n%s", name, view)
		}
	}
}
