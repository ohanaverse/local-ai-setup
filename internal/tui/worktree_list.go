package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// entryKind discriminates sentinel rows from real entries in the picker.
type entryKind int

const (
	// kindEntry is a real worktree or branch row.
	kindEntry entryKind = iota
	// kindNewWorktree is the "+ New worktree…" sentinel that opens
	// the new-worktree prompt. The underlying entry field is unused.
	kindNewWorktree
)

// entryItem adapts a worktree.Entry to a list.Item. With kind =
// kindNewWorktree, it represents the create-new sentinel row.
type entryItem struct {
	kind  entryKind
	entry worktree.Entry
}

// FilterValue is used by the list's built-in filter. The sentinel
// returns "" so it is hidden when the user is filtering.
func (e entryItem) FilterValue() string {
	if e.kind == kindNewWorktree {
		return ""
	}
	return e.entry.Branch
}

// Title renders the branch name (remote prefix stripped for display)
// for real entries, and a "+ New worktree…" label for the sentinel.
func (e entryItem) Title() string {
	if e.kind == kindNewWorktree {
		return "+ New worktree…"
	}
	b := e.entry.Branch
	if i := strings.IndexByte(b, '/'); i >= 0 {
		b = b[i+1:] // strip remote prefix
	}
	return b
}

// Description renders the metadata columns. The sentinel gets a
// static descriptor so users understand what the row does.
func (e entryItem) Description() string {
	if e.kind == kindNewWorktree {
		return "create a new branch and worktree"
	}
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

// buildList constructs a list.Model from worktree entries, prepending
// the "+ New worktree…" sentinel so it's always reachable.
func buildList(entries []worktree.Entry, width, height int) list.Model {
	items := make([]list.Item, 0, len(entries)+1)
	items = append(items, entryItem{kind: kindNewWorktree})
	for _, e := range entries {
		items = append(items, entryItem{kind: kindEntry, entry: e})
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = "Pick a worktree or branch"
	l.SetShowStatusBar(true)
	// Advertise the 'n' shortcut in the footer help line so users know they
	// can open the new-worktree prompt directly instead of scrolling to the
	// "+ New worktree…" sentinel row. bubbles/list renders these bindings in
	// both the short help line and the full help view, and hides them while
	// the user is actively typing a filter query.
	newBinding := key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new worktree"))
	l.AdditionalShortHelpKeys = func() []key.Binding { return []key.Binding{newBinding} }
	l.AdditionalFullHelpKeys = func() []key.Binding { return []key.Binding{newBinding} }
	return l
}
