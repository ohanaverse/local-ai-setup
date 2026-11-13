package rootclaim

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// Site is where a directory sits: the worktree it is inside and which branch
// that worktree holds, the repository's common git directory (where the
// session claims live). It is the launch claims' shared vocabulary: a claim is keyed by
// Site.Path and lives in Site.CommonDir/wt-sessions.
type Site struct {
	// Path is the worktree top level in git's own absolute text
	// (`rev-parse --path-format=absolute --show-toplevel`): the cross-contract
	// claim path (docs/contracts/session-claims.md), so two spellings of one
	// checkout (a /var and a /private/var path on macOS) name one claim.
	Path string
	// CommonDir is `rev-parse --path-format=absolute --git-common-dir`'s text:
	// the directory holding wt-sessions, shared by every linked worktree.
	CommonDir string
	// Branch is the branch the worktree's HEAD names —
	// worktree.CurrentBranchAt's answer, `symbolic-ref --short -q HEAD`'s
	// semantics — and "" on a detached HEAD; the drift readers treat empty as
	// detached.
	Branch string
}

// LocateSite reports where dir sits. err is git's refusal when there is no
// worktree to name — outside any repository, or a bare one (worktree's
// ErrNotACheckout class) — or a genuine read failure; either way the zero
// Site is returned.
func LocateSite(dir string) (Site, error) {
	top, err := worktree.ToplevelOf(dir)
	if err != nil {
		return Site{}, err
	}
	s := Site{Path: top, Branch: worktree.CurrentBranchAt(top)}
	if s.CommonDir, err = worktree.CommonDirAt(top); err != nil {
		return Site{}, err
	}
	return s, nil
}
