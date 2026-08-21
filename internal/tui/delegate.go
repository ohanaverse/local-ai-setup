package tui

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// themedListDelegate returns a list.DefaultDelegate whose Normal/Selected
// styles are themed. This is the single styling point for every picker
// list in the TUI (worktree list, agent list, model list, resume/guard/
// ollama choice lists) — replacing the previous hardcoded list.NewDefaultDelegate
// so every list honors the active color theme. Tests continue to use
// list.NewDefaultDelegate() because they assert model state, not rendered
// colors.
func themedListDelegate(theme themes.Theme) list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.Styles.NormalTitle = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenUnselected))
	d.Styles.NormalDesc = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenDim))
	d.Styles.SelectedTitle = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenAccent)).
		Bold(true)
	d.Styles.SelectedDesc = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenDim))
	d.Styles.DimmedTitle = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenDim)).
		Italic(true)
	d.Styles.DimmedDesc = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenDim)).
		Italic(true)
	d.Styles.FilterMatch = lipgloss.NewStyle().
		Foreground(theme.Token(themes.TokenAccent)).
		Underline(true)
	return d
}
