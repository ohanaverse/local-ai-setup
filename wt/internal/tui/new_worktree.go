package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
)

// newWorktreePlaceholder is the textinput placeholder shown in the
// new-worktree prompt. It signals that the user can type either a
// branch name or a path-like name (e.g. feature/x).
const newWorktreePlaceholder = "branch-or-worktree-name"

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
