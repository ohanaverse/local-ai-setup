package rootmove

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// Hooks are Execute's test seams, carried per Engine so a test can make one
// step fail or watch it without reaching into another run; production code
// and zero-value engines never set them. Each nil field resolves to the real
// behavior through its own method below.
type Hooks struct {
	// ExecuteInto is steps 2-3: carry, verify and record into the new
	// worktree (recovery rows 2 and 3).
	ExecuteInto func(rescue.Engine, *rescue.Plan, string) rescue.Outcome
	// BeforeRelease fires after the work was carried and recorded, just
	// before the claim re-check and the release (row 4 before the reset).
	BeforeRelease func()
	// ReleaseRoot is step 4, the one step that resets and switches the root;
	// rootmove only names it (row 4 after the reset).
	ReleaseRoot func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error)
	// StepGit runs the git of steps 5-6 (rows 5 and 6). Not runGit, which
	// is internal/removal's unrelated seam.
	StepGit func(cleanup.Git, ...string) (cleanup.Out, error)
	// AfterRelease fires once the release succeeded, before step 5 (row 7).
	AfterRelease func()
}

// into is ExecuteInto's nil default: rescue's own carry, verify and record.
func (h Hooks) into(e rescue.Engine, p *rescue.Plan, dest string) rescue.Outcome {
	if h.ExecuteInto != nil {
		return h.ExecuteInto(e, p, dest)
	}
	return e.ExecuteInto(p, dest)
}

// before is BeforeRelease's nil default: nothing.
func (h Hooks) before() {
	if h.BeforeRelease != nil {
		h.BeforeRelease()
	}
}

// release is ReleaseRoot's nil default: removal's ReleaseRoot, the real
// release behind its proofs.
func (h Hooks) release(g cleanup.Git, rel removal.RootRelease) (removal.ReleaseResult, error) {
	if h.ReleaseRoot != nil {
		return h.ReleaseRoot(g, rel)
	}
	return removal.ReleaseRoot(g, rel)
}

// step is StepGit's nil default: the Git's own Run.
func (h Hooks) step(g cleanup.Git, args ...string) (cleanup.Out, error) {
	if h.StepGit != nil {
		return h.StepGit(g, args...)
	}
	return g.Run(args...)
}

// after is AfterRelease's nil default: nothing.
func (h Hooks) after() {
	if h.AfterRelease != nil {
		h.AfterRelease()
	}
}
