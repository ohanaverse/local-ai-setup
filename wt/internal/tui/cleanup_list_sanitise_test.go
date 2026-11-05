package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// hostileGitDetail is git stderr as it reaches a row detail: a terminal
// title escape (ESC ] ... BEL), a right-to-left override, a carriage return
// and a second line.
const hostileGitDetail = "cleanup: git status: exit 128: fatal: x\x1b]0;pwn\x07 ‮evil\rmore\nline2"

// TestCleanupListSanitisesToolDetails builds a report whose unpushed, gates
// and claims unavailable details hold git's raw stderr, and checks each
// row's text and its detail pane. Why: the TUI draws cleanup.RowText, and
// those details carried ESC/BEL, bidi controls, CR and LF to the terminal,
// which could retitle it or split a row over several lines.
func TestCleanupListSanitisesToolDetails(t *testing.T) {
	r := cleanup.Report{Repo: "/repo", Base: cleanup.Base{Default: "main", Ref: "origin/main"},
		Fetch: cleanup.FetchResult{OK: true}, GH: cleanup.GHOK}
	for _, code := range []string{cleanup.ReasonUnpushedUnknown, cleanup.ReasonGatesUnavailable, cleanup.ReasonClaimsUnavailable} {
		r.Rows = append(r.Rows, cleanup.Row{Kind: cleanup.KindBranch, Name: "topic-" + code, Branch: "topic-" + code,
			Status: cleanup.StatusLessSure, Unknown: true, Reasons: []cleanup.Reason{{Code: code, Detail: hostileGitDetail}}})
	}
	m := newSizedCleanupList(t, r, 120, 40)
	bad := []string{"\x1b]", "\x07", "‮", "\r"}
	seen := 0
	for i, it := range m.items {
		if it.heading != "" {
			continue
		}
		seen++
		for _, b := range append(bad, "\x1b", "\n") {
			if strings.Contains(it.text, b) {
				t.Errorf("row %q text holds %q: %q", it.row.Name, b, it.text)
			}
		}
		m.cursor = i
		// The pane is styled (CSI "\x1b[" may appear), so only an OSC
		// introducer counts as an escape here.
		pane := m.detail()
		for _, b := range bad {
			if strings.Contains(pane, b) {
				t.Errorf("row %q detail holds %q: %q", it.row.Name, b, pane)
			}
		}
	}
	if seen != 3 {
		t.Fatalf("saw %d rows, want 3", seen)
	}
}
