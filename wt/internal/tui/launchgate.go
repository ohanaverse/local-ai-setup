package tui

import (
	"fmt"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// launchCreation is a pick that owes its worktree at the commit point: a bare
// branch (a branch no worktree checks out yet) or a remote-only branch (a
// local branch tracking it). The pick resolves the plan read-only — nothing
// exists yet — so the gate can state the creation in launchwarn's frozen
// wording and a declined gate creates nothing. plan.Ensure makes the worktree
// only once the person has continued, at exactly plan.Path(), which is the
// path the launch's command was built for.
type launchCreation struct {
	repoRoot string
	name     string // what the row named: a branch, or <remote>/<branch>
	plan     *worktree.Plan
}

// target is the launch's already-read Target: the creation's facts (the
// branch, its tracking ref and the path it goes to), so Check prints the
// creation line as the one informational line before anything exists.
func (c *launchCreation) target() (launchwarn.Target, error) {
	return launchwarn.ForNew(c.repoRoot, c.name, c.plan)
}

// ensure creates (or reuses) the worktree the pick owes and returns its path.
func (c *launchCreation) ensure() (string, error) { return c.plan.Ensure() }

// rootClaimRelease removes this session's claim on the repository root. It is
// set by claimRoot and called through releaseRootClaim, mirroring cmd/wt.
var rootClaimRelease = func() {}

// releaseRootClaim removes this session's claim on the repository root, if it
// holds one. Safe to call more than once: every run command calls it after
// the agent exits, and a declined gate before any claim is a no-op.
func releaseRootClaim() {
	release := rootClaimRelease
	rootClaimRelease = func() {}
	release()
}

// launchTarget is the already-read Target of the launch this model is bound
// to: the owed creation's (ForNew) when the pick was a bare or remote-only
// branch, otherwise the directory's (ForDir) — read when the run command
// runs, so a session that appeared after the pick is named in the warnings
// like any other. A read failure is the caller's to note (gateFor); the
// partial Target still carries what was learned.
//
// The picker's root row names the repository root explicitly, so choosing it
// is the consent for the root warning — what --root is on the flag paths (the
// warning's own text says so). A root nobody else holds therefore proceeds
// with the one informational line; one another session holds asks about the
// holder.
func (m model) launchTarget() (launchwarn.Target, error) {
	if m.creation != nil {
		return m.creation.target()
	}
	dir := m.selectedPath
	if dir == "" {
		dir = m.prePath
	}
	t, err := launchwarn.ForDir(dir)
	t.RootChosen = t.IsRoot
	return t, err
}

// gateFor binds this model's launchGate to the launch it is about to make, as
// the per-call value the run commands ask at the commit point. It reads the
// Target at the ask (launchTarget), never earlier.
func (m model) gateFor() func() bool {
	return func() bool {
		t, readErr := m.launchTarget()
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "wt: note: %v\n", readErr)
		}
		return m.launchGate(&t)
	}
}

// launchGate is the one question before a launch commits, asked at the
// commit point with the terminal the user's again (the run commands call it
// where holdRoot's prompt used to be). It mirrors cmd/wt's confirmLaunch and
// holdRoot: the collected warnings — or, with none, the one informational
// line of a root launch, or the creation line of a pick that owes a
// worktree — and one default-yes question when a warning remains. A "no" is
// a choice, not a failure: ok is false, nothing is created, claimed,
// recorded or run, and the run ends as quitting the picker does. Then the
// owed worktree is made and the root claimed for exactly the agent's run;
// every claims failure is one "wt: note:", never a refusal.
//
// A preset path (m.prePath: -W, --cwd, --root through the flag paths) was
// judged and answered in RunE's confirmLaunch already. This is its second
// arrival at the commit point, so the gate speaks and asks nothing there —
// a root launch is asked at most once — and only claims.
func (m model) launchGate(t *launchwarn.Target) (ok bool) {
	if m.prePath != "" {
		m.claimRoot(t.Dir)
		return true
	}
	warnings, info := launchwarn.Collect(*t)
	if !t.IsRoot {
		// The one informational line is the shared place's — the root, the
		// screen this gate replaced — and a creation's (Check prints the
		// creation line in its place). An ordinary linked-worktree launch
		// says nothing.
		info = ""
	}
	if !launchwarn.Check(os.Stdin, os.Stderr, true, warnings, info, t.Creation) {
		return false
	}
	if t.Hint != "" {
		fmt.Fprintf(os.Stderr, "wt: %s\n", t.Hint)
	}
	if m.creation != nil {
		if _, err := m.creation.ensure(); err != nil {
			fmt.Fprintf(os.Stderr, "wt: %v\n", err)
			return false
		}
	}
	m.claimRoot(t.Dir)
	return true
}

// claimRoot writes this session's claim when dir is a repository's primary
// checkout — claims are root-only until a later task extends them — and keeps
// the release for releaseRootClaim, so other launches warn about it (held)
// exactly while the agent runs. A linked worktree claims nothing. Another
// live holder is not a question here: launchwarn's held warning has named
// them, and the person has answered it. A claim that cannot be read or
// written is one note and never a refusal.
func (m model) claimRoot(dir string) {
	root, ok, err := rootclaim.Lookup(dir)
	if !ok {
		return
	}
	note := func(err error) {
		fmt.Fprintf(os.Stderr, "wt: note: %s\n", sessionclaim.CheckFailed(root.Path, err))
	}
	if err != nil {
		note(err)
		return
	}
	self, err := root.Self(m.agent)
	if err != nil {
		note(err)
		return
	}
	release, _, err := sessionclaim.Join(root.CommonDir, self, func([]sessionclaim.Claim) bool { return true })
	if err != nil {
		note(err)
		return
	}
	rootClaimRelease = release
}
