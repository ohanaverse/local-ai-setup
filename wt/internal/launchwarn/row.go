package launchwarn

import (
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// FromRow maps a unified-list row (cleanup.Row) to the Target of a launch
// about to go where that row is. It is pure mapping: no git, no probes, no
// clock — the row was gathered already — so it is cheap and deterministically
// testable. The facts only the gathering knows come from the row: the PR
// state and number, the gone upstream, and the dirt exactly as the raw losses
// hold it (a rescue record covering the changes does not make them
// disappear). The facts a row cannot hold — live holders, the root's branch,
// the default, the hint — stay zero for the caller's live read (ForDir or
// ForNew) to fill, and the caller's plan keeps owning the creation: there is
// one creation path, and this only describes it.
func FromRow(r cleanup.Row) *Target {
	t := &Target{
		Dir:          r.Path,
		Branch:       r.Branch,
		IsRoot:       r.Primary,
		UpstreamGone: r.Upstream != nil && r.Upstream.Gone,
		Dirty:        r.Losses.Dirty, // losses: raw, the changes are physically there whatever a rescue record covers
	}
	switch {
	case r.PRUnknown:
		// "PR ?" — the lookup never answered (skipped, still to run, or no
		// usable answer): the state is unknown regardless of any tag, and
		// Collect warns no merge for it.
		t.PRState = "unknown"
	case r.PRTag != nil:
		t.PRState = strings.ToLower(r.PRTag.State) // GitHub's spelling ("MERGED") to Target's ("merged")
		t.PRNumber = r.PRTag.Number
	}
	if r.Path == "" {
		// A row no worktree holds yet owes its worktree at the commit point,
		// and this is the creation the pick path already plans for it
		// (worktree.Resolve → ForNew): a remote-only row's new local branch
		// tracks the remote ref, a bare branch is checked out as it is, and
		// NewBranch is false for both — a tracking plan's Creates() is false
		// too, so the creation line keeps its frozen "creating worktree …
		// tracking …" wording. Path stays empty: a row cannot know the
		// repository root the worktree goes under, and the plan fills it.
		c := &Creation{Branch: r.Branch, NewBranch: false}
		if r.Kind == cleanup.KindRemote {
			c.Tracks = r.Remote + "/" + r.Name
		}
		t.Creation = c
	}
	return t
}
