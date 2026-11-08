package rescue

import "github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"

// Offered says, from a listed row alone (no git), whether a rescue is worth
// offering for it, and if not why not. It is the cheap screen the cleanup
// list uses to show its `r` key; Preflight re-checks everything fresh and
// remains the judge.
func Offered(row cleanup.Row) (bool, string) {
	switch row.Kind {
	case cleanup.KindWorktree:
	case cleanup.KindBranch:
		return false, "a branch with no worktree has no uncommitted work; wt -W " + cleanup.OneLine(row.Name) + " gives it one"
	default:
		return false, "only a worktree's work can be copied"
	}
	for _, b := range row.Blockers {
		switch b.Code {
		case cleanup.BlockPrimary:
			return false, "the primary checkout is not rescued here (#522)"
		case cleanup.BlockOperation:
			return false, "an operation is in progress there"
		}
	}
	// losses: raw, rescue is offered for what the worktree holds, record or not
	if row.Losses.GatesUnknown {
		return false, "its state could not be read"
	}
	// losses: raw, rescue is offered for what the worktree holds, record or not
	if !row.Losses.Dirty && row.Losses.Unpushed == 0 && !row.Losses.DetachedUnique {
		return false, "nothing to keep"
	}
	return true, ""
}
