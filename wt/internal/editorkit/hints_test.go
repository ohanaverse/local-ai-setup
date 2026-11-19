package editorkit

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// TestFitHintsPicksTheFullestThatFits verifies the hint line is the fullest
// that fits the width, the fullest when the width is not known yet, and the
// last one cut when none fits: it is the last line of every editor screen and
// must never be wider than the terminal.
func TestFitHintsPicksTheFullestThatFits(t *testing.T) {
	if got, want := FitHints(10, []string{"0123456789ab", "short"}), "short"; got != want {
		t.Errorf("FitHints at 10 = %q, want %q", got, want)
	}
	if got, want := FitHints(0, []string{"0123456789ab", "short"}), "0123456789ab"; got != want {
		t.Errorf("FitHints at 0 = %q, want %q", got, want)
	}
	if got, want := FitHints(3, []string{"abcdef"}), tuilayout.Clip("abcdef", 3); got != want {
		t.Errorf("FitHints at 3 = %q, want %q", got, want)
	}
}
