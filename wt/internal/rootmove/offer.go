package rootmove

import (
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// Offered says whether the cleanup list's R action offers `wt restore-root`
// for a row, and when it does not, the one line the menu shows instead. It
// reads the row alone — the list's own gathered truth — and never git.
//
// The decision shape (R501a-e) mirrors Preflight's own refusal order, so the
// menu never offers a move the command would refuse. A non-root row names a
// thing restore-root never moves. A bare repository has no checkout to move.
// A root with no commit has nothing to move. A detached root has no branch to
// move, so the menu explains instead of offering. A root in a repository
// with no resolvable default has nothing to restore to (the move's whole
// point is returning the root to the default). A root demonstrably on the
// default — its row's cleanup.BlockDefaultBranch blocker, which the
// classifier adds exactly when the checked-out branch is the resolved
// default — already sits where the move would leave it; the message quotes
// the branch the row carries (the row has no default-branch name, so on a
// default-branch root that IS the default's name). Anything else is a named
// branch off the default — the row R exists for — with or without the drift
// label. Last come the two refusals Preflight takes from the rescue engine it
// runs in root mode, in its order: an operation in progress (the row's
// cleanup.BlockOperation blocker) and state that could not be read
// (Losses.GatesUnknown), each worded as rescue.Offered words them.
func Offered(row cleanup.Row) (bool, string) {
	switch {
	case !row.Primary:
		return false, "only the root can be restored"
	case row.Bare:
		return false, "a bare repository has no checkout to restore"
	case noCommit(row.Head):
		return false, "the root's branch has no commits yet"
	case row.Branch == "":
		return false, "the root is detached — there is no branch to move"
	case hasReason(row.Reasons, cleanup.ReasonNoDefaultBranch):
		return false, "no default branch to restore to"
	case hasReason(row.Blockers, cleanup.BlockDefaultBranch):
		return false, "root is already on " + row.Branch
	case hasReason(row.Blockers, cleanup.BlockOperation):
		// A merge, cherry-pick or revert keeps HEAD on the branch, so the row
		// looks like any drifted root; rescue's Preflight (run in root mode)
		// refuses it.
		return false, "an operation is in progress there"
	// losses: raw, the move is refused whenever the root's state could not be read
	case row.Losses.GatesUnknown:
		return false, "its state could not be read"
	default:
		return true, ""
	}
}

// noCommit is Preflight's own no_commit check: a worktree's Head is "" — or
// the 40-zero null sha, what `git worktree list` prints for an unborn branch
// — when there is no commit to move.
func noCommit(head string) bool {
	return head == "" || strings.Trim(head, "0") == ""
}

// hasReason reports whether one of rs — a row's Reasons or Blockers — carries
// code.
func hasReason(rs []cleanup.Reason, code string) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}
