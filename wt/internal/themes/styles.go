package themes

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// ListDelegate returns a list.DefaultDelegate whose Normal/Selected styles
// are themed. This is the single styling point for every list in wt's
// terminal programs: internal/tui's pickers (worktree, agent, model, ollama
// and replace-choice lists) and internal/configeditor's tabs. It lives here,
// beside the theme tokens it reads, so the two programs share it without one
// importing the other. Tests continue to use list.NewDefaultDelegate()
// because they assert model state, not rendered colors.
func ListDelegate(theme Theme) list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.Styles.NormalTitle = lipgloss.NewStyle().
		Foreground(theme.Token(TokenUnselected))
	d.Styles.NormalDesc = lipgloss.NewStyle().
		Foreground(theme.Token(TokenDim))
	d.Styles.SelectedTitle = lipgloss.NewStyle().
		Foreground(theme.Token(TokenAccent)).
		Bold(true)
	d.Styles.SelectedDesc = lipgloss.NewStyle().
		Foreground(theme.Token(TokenDim))
	d.Styles.DimmedTitle = lipgloss.NewStyle().
		Foreground(theme.Token(TokenDim)).
		Italic(true)
	d.Styles.DimmedDesc = lipgloss.NewStyle().
		Foreground(theme.Token(TokenDim)).
		Italic(true)
	d.Styles.FilterMatch = lipgloss.NewStyle().
		Foreground(theme.Token(TokenAccent)).
		Underline(true)
	return d
}

// ErrorStyle returns the lipgloss style for user-facing errors in the active
// theme: internal/tui's new-worktree error under the input and its list error
// above the list, and internal/configeditor's form errors, so every error
// renders identically.
func ErrorStyle(theme Theme) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Token(TokenError))
}
