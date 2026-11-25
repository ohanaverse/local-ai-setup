package rootclaim

import (
	"errors"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// HoldLaunch claims the worktree dir sits in for this process and returns the
// live claims other sessions hold on that same worktree path — what every
// launch calls once the worktree exists. The claim feeds the labels and the
// launch warnings only and never blocks or fails a launch: a directory with
// no worktree to claim (outside any repository, a bare one) is a clean no-op,
// and a genuinely unexpected failure is returned only for the caller to print
// as a note.
//
// The self claim is written first (sessionclaim.Hold prunes the stale claims
// under the lock before its write), so others is read from a directory that
// already holds this session — Holders leaves out self and self's ancestors.
// release removes exactly the claim Hold wrote and is never nil.
func HoldLaunch(dir, agent string) (release func(), others []sessionclaim.Claim, err error) {
	release = func() {}
	site, err := LocateSite(dir)
	if err != nil {
		if errors.Is(err, worktree.ErrNotACheckout) {
			return release, nil, nil // no worktree to claim: nothing to hold
		}
		return release, nil, err
	}
	self, err := sessionclaim.Self(site.Path, site.Branch, agent)
	if err != nil {
		return release, nil, err
	}
	if release, err = sessionclaim.Hold(site.CommonDir, self); err != nil {
		return release, nil, err
	}
	holders, err := sessionclaim.Holders(site.CommonDir, self)
	if err != nil {
		return release, nil, err
	}
	// Holders keys claims by the path each recorded. wt's own writers record
	// worktree.ToplevelOf, but a foreign or hand-written claim may name the
	// worktree through a symlink alias (/var against /private/var), so every
	// key is folded through worktree.ResolvePath, the resolver cleanup's
	// HeldOn and rootmove's heldBy use, and compared with the resolved site
	// path; the claims of all matching keys are joined and sorted by pid.
	want := worktree.ResolvePath(site.Path)
	for path, claims := range holders {
		if worktree.ResolvePath(path) == want {
			others = append(others, claims...)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].PID < others[j].PID })
	return release, others, nil
}
