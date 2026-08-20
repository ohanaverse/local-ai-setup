package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// entryKind discriminates picker rows: real entries, the create-new
// sentinel, and the separator that visually divides locals from remotes.
type entryKind int

const (
	// kindEntry is a real worktree or branch row.
	kindEntry entryKind = iota
	// kindNewWorktree is the "+ New worktree…" sentinel that opens
	// the new-worktree prompt. The underlying entry field is unused.
	kindNewWorktree
	// kindSeparator is a non-selectable visual divider rendered
	// between the locals and remotes groups.
	kindSeparator
)

// entryItem adapts a worktree.Entry to a list.Item, or represents a
// sentinel/separator row. label carries the rendered text for
// separator rows and "(current)"/"(default)" markers for entry rows;
// the underlying entry worktree.Entry is never mutated, so it stays
// safe to forward into selectedEntryMsg at launch time.
type entryItem struct {
	kind  entryKind
	entry worktree.Entry
	label string
}

// FilterValue is used by the list's built-in filter. Non-entry rows
// (sentinel and separator) return "" so they are hidden when the user
// is filtering.
func (e entryItem) FilterValue() string {
	if e.kind != kindEntry {
		return ""
	}
	return e.entry.Branch
}

// Title renders the branch name (remote prefix stripped for display)
// for real entries, with any (current)/(default) marker appended;
// "+ New worktree…" for the sentinel; and the separator's label
// otherwise.
func (e entryItem) Title() string {
	switch e.kind {
	case kindNewWorktree:
		return "+ New worktree…"
	case kindSeparator:
		return e.label
	}
	b := e.entry.Branch
	if i := strings.IndexByte(b, '/'); i >= 0 {
		b = b[i+1:] // strip remote prefix for display
	}
	if e.label != "" {
		b += "  " + e.label
	}
	return b
}

// Description renders the metadata columns. The sentinel gets a static
// descriptor; separators get an empty descriptor.
func (e entryItem) Description() string {
	switch e.kind {
	case kindNewWorktree:
		return "create a new branch and worktree"
	case kindSeparator:
		return ""
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

// buildList constructs a list.Model from worktree groups in the order
// the picker should render them: sentinel, then the worktrees group,
// then the local-branches group, then — only when the remote-branches
// group is non-empty — a separator row, then the remote-branches group.
// Entries matching the repo default branch are rendered with
// "(default)"; the entry matching the launch directory (after resolving
// symlinks) is rendered with "(current)", which wins over "(default)"
// so the current worktree stays distinguishable from the bare
// default-branch row. The markers are tracked on entryItem.label so the
// underlying worktree.Entry is never mutated and remains safe to
// forward into selectedEntryMsg.
//
// repoRoot is resolved with filepath.EvalSymlinks so symlinked paths
// (e.g. ~/.worktrees/foo -> .worktrees/foo) compare correctly against
// the entry's Path.
func buildList(groups []worktree.EntryGroup, defaultBranch, repoRoot string, width, height int) list.Model {
	resolvedRepo, _ := filepath.EvalSymlinks(repoRoot)

	items := make([]list.Item, 0)
	items = append(items, entryItem{kind: kindNewWorktree})

	for _, g := range groups {
		// Render the locals→remotes divider only when there are remotes to
		// follow; otherwise a dangling "── remote branches ──" row with
		// nothing below it would show in repos with no remote-tracking
		// branches.
		if g.Kind == worktree.GroupRemoteBranches && len(g.Entries) > 0 {
			items = append(items, entryItem{kind: kindSeparator, label: "── remote branches ──"})
		}
		for _, e := range g.Entries {
			ei := entryItem{kind: kindEntry, entry: e}
			// Mark default-branch entries with (default). This applies to
			// the bare default-branch row and to non-current worktrees on
			// the default branch.
			if e.Branch == defaultBranch {
				ei.label = "(default)"
			}
			// The entry matching the launch directory is (current). This
			// wins over (default) so the current worktree is distinguishable
			// from the bare default-branch row: without this priority the
			// current worktree on main and the bare "main (default)" row
			// would both render "(default)" and be easy to confuse — and
			// the bare row bypasses the launch guard (see app.go).
			if e.Path != "" {
				resolved, _ := filepath.EvalSymlinks(e.Path)
				if resolved == resolvedRepo {
					ei.label = "(current)"
				}
			}
			items = append(items, ei)
		}
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
