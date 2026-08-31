package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// newWorktreePlaceholder is the textinput placeholder shown in the
// new-worktree prompt. It signals that the user can type either a
// branch name or a path-like name (e.g. feature/x).
const newWorktreePlaceholder = "branch-or-worktree-name"

// ErrorStyle returns the lipgloss style for user-facing errors in the
// active theme: m.newError under the new-worktree input and m.listError
// above the list. Exported so other packages building on the same theme
// (e.g. internal/ollamaconfig) render errors identically.
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
		path, err := worktree.EnsureForName(root, name)
		return newWorktreeCreatedMsg{path: path, name: name, err: err}
	}
}

// branchWorktreeCreatedMsg is emitted after EnsureForBranch creates (or
// reuses) a worktree for a picked bare branch. On success, path is the
// worktree path; on failure, err is set and path is empty.
type branchWorktreeCreatedMsg struct {
	path   string
	branch string
	err    error
}

// ensureBranchWorktreeCmd returns a tea.Cmd that creates a worktree for a
// bare branch via EnsureForBranch and reports the resolved path. Bare
// branches (TypeBranch, Path="") have no worktree yet; selecting one must
// materialize a worktree so the agent launches there rather than in wt's
// CWD (cmd.Dir="").
func ensureBranchWorktreeCmd(root, branch string) tea.Cmd {
	return func() tea.Msg {
		path, err := worktree.EnsureForBranch(root, branch)
		return branchWorktreeCreatedMsg{path: path, branch: branch, err: err}
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
