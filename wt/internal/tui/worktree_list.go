package tui

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// selectedEntryMsg is emitted when the user picks a worktree or branch: a
// picker row (entryOfRow), or the pre-resolved path of -W, --cwd or --root.
// Everything downstream (the owed worktree creation, the launch gate's
// question, the agent and model pickers) starts from it. row is the
// unified-list row the pick came from (nil for a preset path or a typed
// name): the launch gate's
// Target carries its PR, upstream and dirt facts — the ones only the list
// gathered (launchwarn.FromRow) — over the live read.
type selectedEntryMsg struct {
	entry worktree.Entry
	row   *cleanup.Row
}

// refreshRowsChosenMsg is launch mode's `F`: the list asks for a refresh —
// the remotes fetched, the rows re-gathered (the picker's only fetch, since
// opening fetches nothing). The app runs it (app.go's refreshRowsChosenMsg
// handler): it holds the rows' gather inputs and the in-flight guard that
// drops a second `F` while one refresh is running.
type refreshRowsChosenMsg struct{}
