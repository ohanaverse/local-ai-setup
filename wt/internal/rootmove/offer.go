package rootmove

import "github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"

// Offered says whether the cleanup list's R action offers `wt restore-root`
// for a row, and when it does not, the one line the menu shows instead. It
// reads the row alone — the list's own gathered truth — and never git: the
// verdict is a function of the row's Primary flag, its drift label
// (cleanup.ReasonRootDrift: a named drift or a detached HEAD) and its
// no-default label (cleanup.ReasonNoDefaultBranch).
//
// The shapes, in order: a non-root row names a thing restore-root never
// moves; a root in a repository with no resolvable default has nothing to
// restore to (the move's whole point is returning the root to the default);
// a drifting root is the row R exists for; and every other root already sits
// where the move would leave it — on the default branch, whose name the row
// does not carry, so the message quotes the branch the row does (on a
// default-branch root that IS the default's name).
func Offered(row cleanup.Row) (bool, string) {
	if !row.Primary {
		return false, "only the root can be restored"
	}
	switch {
	case hasReason(row.Reasons, cleanup.ReasonNoDefaultBranch):
		return false, "no default branch to restore to"
	case hasReason(row.Reasons, cleanup.ReasonRootDrift):
		return true, ""
	default:
		return false, "root is already on " + row.Branch
	}
}

// hasReason reports whether one of rs carries code.
func hasReason(rs []cleanup.Reason, code string) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}
