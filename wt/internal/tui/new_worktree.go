package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// newWorktreePlaceholder is the textinput placeholder shown in the
// new-worktree prompt. It signals that the user can type either a
// branch name or a path-like name (e.g. feature/x).
const newWorktreePlaceholder = "branch-or-worktree-name"

// ErrorStyle returns the lipgloss style for user-facing errors in the
// active theme: m.newError under the new-worktree input and m.listError
// above the list. Exported so other packages building on the same theme
// (e.g. internal/configeditor) render errors identically.
func ErrorStyle(theme themes.Theme) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Token(themes.TokenError))
}

// newWorktreeCreatedMsg is emitted after a create attempt. On
// success, path is the worktree path and name is the branch name.
// On failure, err is set and the other fields are zero.
type newWorktreeCreatedMsg struct {
	path string
	name string
	err  error
}

// ensureNewWorktreeCmd returns a tea.Cmd that creates a worktree
// for name in the repo at root via EnsureForName and reports the
// result. Run it from the model via dispatching the returned
// command from Update.
func ensureNewWorktreeCmd(root, name string) tea.Cmd {
	return func() tea.Msg {
		plan, err := worktree.Resolve(root, name)
		if err != nil {
			return newWorktreeCreatedMsg{name: name, err: err}
		}
		path, err := plan.Ensure()
		if err == nil && plan.Creates() {
			exitprompt.RecordNote(os.Stderr, path, name)
		}
		return newWorktreeCreatedMsg{path: path, name: name, err: err}
	}
}

// newInputModel builds a focused textinput sized to the terminal
// width. width is the full terminal width; the input is sized to
// width-4 to leave room for padding in the View.
func newInputModel(width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = newWorktreePlaceholder
	ti.Focus()
	ti.CharLimit = 100
	ti.Width = width - 4
	return ti
}

// validateNewWorktreeName returns an error string if name is
// invalid (empty or whitespace-only), or "" if OK. Deeper
// validation is delegated to EnsureForName; this validator only
// catches the cheap client-side check before shelling out to git.
func validateNewWorktreeName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "name cannot be empty"
	}
	// "." and ".." would resolve to the repo root via filepath.Join
	// (".worktrees/.." cleans to the repo root), silently creating nothing.
	if name == "." || name == ".." {
		return "name cannot be \"" + name + "\""
	}
	return ""
}
