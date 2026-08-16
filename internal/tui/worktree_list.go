package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// entryItem adapts a worktree.Entry to a list.Item.
type entryItem struct {
	entry worktree.Entry
}

// FilterValue is used by the list's built-in filter.
func (e entryItem) FilterValue() string { return e.entry.Branch }

// Title renders the branch name (remote prefix stripped for display).
func (e entryItem) Title() string {
	b := e.entry.Branch
	if i := strings.IndexByte(b, '/'); i >= 0 {
		b = b[i+1:] // strip remote prefix
	}
	return b
}

// Description renders the metadata columns.
func (e entryItem) Description() string {
	path := e.entry.Path
	if path == "" {
		path = "(no worktree)"
	}
	return strings.TrimSpace(strings.Join([]string{
		pad("["+string(e.entry.Type)+"]", 9),
		path,
	}, " "))
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// selectedEntryMsg is emitted when the user picks a worktree or branch.
type selectedEntryMsg struct{ entry worktree.Entry }

// buildList constructs a list.Model from worktree entries.
func buildList(entries []worktree.Entry, width, height int) list.Model {
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, entryItem{entry: e})
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = "Pick a worktree or branch"
	l.SetShowStatusBar(true)
	return l
}
