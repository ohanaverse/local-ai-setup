package tui

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// selectedEntryMsg is emitted when the user picks a worktree or branch: a
// picker row (entryOfRow), or the pre-resolved path of -W, --cwd or --root.
// Everything downstream (the bare-branch create, the shared-root check, the
// agent and model pickers) starts from it. row is the unified-list row the
// pick came from (nil for a preset path or a typed name): the launch gate's
// Target carries its PR, upstream and dirt facts — the ones only the list
// gathered (launchwarn.FromRow) — over the live read.
type selectedEntryMsg struct {
	entry worktree.Entry
	row   *cleanup.Row
}
