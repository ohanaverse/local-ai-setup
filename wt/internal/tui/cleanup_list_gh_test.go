package tui

import (
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
