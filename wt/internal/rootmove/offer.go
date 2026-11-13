package rootmove

import "github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"

// Offered says whether the cleanup list's R action offers `wt restore-root`
// for a row, and when it does not, the one line the menu shows instead. It
// reads the row alone — the list's own gathered truth — and never git.
//
// The decision shape (R501a-d), in order. A non-root row names a thing
// restore-root never moves. A bare repository has no checkout to move. A
// detached root has no branch to move (restore-root's detached refusal), so
// the menu explains instead of offering. A root in a repository with no
// resolvable default has nothing to restore to (the move's whole point is
// returning the root to the default). A root demonstrably on the default —
// its row's cleanup.BlockDefaultBranch blocker, which the classifier adds
// exactly when the checked-out branch is the resolved default — already sits
// where the move would leave it; the message quotes the branch the row
// carries (the row has no default-branch name, so on a default-branch root
// that IS the default's name). Anything else is a named branch off the
// default — the row R exists for — with or without the drift label.
func Offered(row cleanup.Row) (bool, string) {
	switch {
	case !row.Primary:
		return false, "only the root can be restored"
	case row.Bare:
		return false, "a bare repository has no checkout to restore"
	case row.Branch == "":
		return false, "the root is detached — there is no branch to move"
	case hasReason(row.Reasons, cleanup.ReasonNoDefaultBranch):
		return false, "no default branch to restore to"
	case hasReason(row.Blockers, cleanup.BlockDefaultBranch):
		return false, "root is already on " + row.Branch
	default:
		return true, ""
	}
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
