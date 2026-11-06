package removal

import (
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestDetailLinesKeepsRestoreIDAndOneLine pins the lines the command text and
// the tick list share: losses, warnings and the deleted branch's commit id,
// each one sanitised line. Why: the commit id is the only record of a deleted
// branch, and the two output paths must never format it differently.
func TestDetailLinesKeepsRestoreIDAndOneLine(t *testing.T) {
	tip := strings.Repeat("a", 40)
	o := Outcome{
		Item:       Item{Kind: cleanup.KindBranch, Name: "topic"},
		LossLines:  []string{"2 unpushed\ncommits"},
		Warnings:   []string{"gc may drop it"},
		DeletedTip: tip,
	}
	want := []string{"loss: 2 unpushed commits", "warning: gc may drop it", "branch tip was " + tip}
	if got := DetailLines(o); !slices.Equal(got, want) {
		t.Errorf("DetailLines = %q, want %q", got, want)
	}
	if got := DetailLines(Outcome{}); len(got) != 0 {
		t.Errorf("DetailLines of an empty outcome = %q, want none", got)
	}
	if h := Headline(o); !strings.HasPrefix(h, "branch topic: worktree ") {
		t.Errorf("Headline = %q", h)
	}
}
