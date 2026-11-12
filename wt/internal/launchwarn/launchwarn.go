// Package launchwarn decides which warnings a launch shows before it
// proceeds. Collect is the pure rule set; it reads no git and prompts nothing.
package launchwarn

import (
	"fmt"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// Warning codes.
const (
	CodeRoot          = "root"
	CodeRootDrift     = "root_drift"
	CodeHeld          = "held"
	CodeDirtyHeld     = "dirty_held"
	CodeDefaultBranch = "default_branch"
	CodePRMerged      = "pr_merged"
	CodeUpstreamGone  = "upstream_gone"
)

// Creation is what a launch is about to create before it runs the agent.
type Creation struct {
	Branch string // the branch the new worktree checks out
	Tracks string // the ref it tracks, "" for none
	// NewBranch selects the `creating branch <b> and worktree <p>` line
	// (creationLine). It is not "the branch does not exist yet": a tracking
	// creation — a new local branch on a remote-tracking ref — is false even
	// though its local branch is new, because its line is the `creating
	// worktree <p> for branch <b> tracking <r>` shape (worktree.Plan.Creates
	// answers the same way, and ForNew copies it).
	NewBranch bool
	Path      string // where the worktree goes
}

// Target is the place a launch is about to start, as already read.
type Target struct {
	Dir, Branch, Default, RootBranch string
	IsRoot, RootChosen               bool // the target is the root; the user passed --root
	UpstreamGone, Dirty              bool
	PRState                          string // "merged", "open", "closed", "" or "unknown"
	PRNumber                         int
	Holders                          []sessionclaim.Claim // live sessions on Dir
	Creation                         *Creation            // non-nil when the launch creates the worktree; Check's concern, Collect ignores it
	Hint                             string               // optional trailing advice; the caller's to append, Collect ignores it
}

// Warning is one thing worth saying before the launch. Text is plain, with no
// "wt: " prefix.
type Warning struct{ Code, Text string }

// now is the clock Describe reads; tests replace it.
var now = time.Now

// Collect returns every warning that applies to t, in a fixed order, and the
// one-line note of where the launch goes. info is returned even when there
// are warnings; Check prints it only when there are none.
func Collect(t Target) (warnings []Warning, info string) {
	add := func(code, text string) { warnings = append(warnings, Warning{code, text}) }
	if t.IsRoot && !t.RootChosen {
		add(CodeRoot, fmt.Sprintf("%s is the repository root; a launch here shares it with every other session (--root accepts this)", t.Dir))
	}
	if t.RootBranch != "" && t.Default != "" && t.RootBranch != t.Default {
		add(CodeRootDrift, fmt.Sprintf("root is on %s, not %s", t.RootBranch, t.Default))
	}
	if len(t.Holders) > 0 {
		n := now()
		text := "in use by another session:"
		for i, h := range t.Holders {
			if i > 0 {
				text += ";"
			}
			text += " " + sessionclaim.Describe(h, n)
		}
		add(CodeHeld, text)
		if t.Dirty {
			add(CodeDirtyHeld, "it has uncommitted changes and another session is using it")
		}
	}
	if t.Branch != "" && t.Branch == t.Default && !t.IsRoot {
		add(CodeDefaultBranch, fmt.Sprintf("a worktree on %s stops the root from being on it; wt restore-root undoes this", t.Default))
	}
	if t.PRState == "merged" {
		add(CodePRMerged, fmt.Sprintf("PR #%d is merged", t.PRNumber))
	}
	if t.UpstreamGone {
		add(CodeUpstreamGone, "its upstream branch is gone")
	}
	return warnings, "launching in " + t.Dir
}
