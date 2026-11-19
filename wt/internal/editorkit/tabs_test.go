package editorkit

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestTabBarBracketsTheCurrentTab verifies the tab bar draws the names it is
// given in order with the current one in brackets: the names are passed in,
// and the current tab must be bracketed for a terminal without colour.
func TestTabBarBracketsTheCurrentTab(t *testing.T) {
	if got, want := ansi.Strip(TabBar(themes.Theme{}, []string{"A", "B"}, 1)), " A   [B]"; got != want {
		t.Errorf("TabBar current 1 = %q, want %q", got, want)
	}
	if got, want := ansi.Strip(TabBar(themes.Theme{}, []string{"A", "B"}, 0)), "[A]   B "; got != want {
		t.Errorf("TabBar current 0 = %q, want %q", got, want)
	}
}
