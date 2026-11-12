package tui

import "github.com/ohanaverse/local-ai-setup/wt/internal/worktree"

// selectedEntryMsg is emitted when the user picks a worktree or branch: a
// picker row (entryOfRow), or the pre-resolved path of -W, --cwd or --root.
// Everything downstream (the bare-branch create, the shared-root check, the
// agent and model pickers) starts from it.
type selectedEntryMsg struct{ entry worktree.Entry }
