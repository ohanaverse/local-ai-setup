package editorkit

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TabBar is the editor's first line: every tab, the current one in brackets
// (and in the accent colour, where there is colour) so that it reads as
// current on a terminal without any.
func TabBar(theme themes.Theme, names []string, current int) string {
	active := lipgloss.NewStyle().Foreground(theme.Token(themes.TokenAccent)).Bold(true)
	dim := lipgloss.NewStyle().Foreground(theme.Token(themes.TokenDim))
	bar := ""
	for i, name := range names {
		if i > 0 {
			bar += "  "
		}
		if i == current {
			bar += active.Render("[" + name + "]")
		} else {
			bar += dim.Render(" " + name + " ")
		}
	}
	return bar
}
