package tui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
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
// A branch it creates is recorded for the exit prompt (exitprompt.RecordNote),
// exactly as cmd/wt's -W path records one once its plan creates the branch; a
// branch or worktree that already existed is history this launch did not make
// and records nothing.
func (c *launchCreation) ensure() (string, error) {
	creates := c.plan.Creates()
	path, err := c.plan.Ensure()
	if err == nil && creates {
		exitprompt.RecordNote(os.Stderr, path, c.name)
	}
	return path, err
}

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
// branch, otherwise the directory's (ForDir, or ForDirKnownDirt when a list
// row holds the dirt already) — read when the run command runs, so a session
// that appeared after the pick is named in the warnings like any other — with
// the picked list row's own facts folded in (foldRowFacts).
// A read failure is the caller's to note (gateFor); the partial Target still
// carries what was learned.
//
// The picker's root row names the repository root explicitly and carries the
// root warning into the one confirmation like every other warning: choosing
// it is not consent. RootChosen is consent only where the person gave it —
// the --root flag on the flag paths (RunE), and ForDir's ancestor-held case.
// A root nobody else holds therefore still asks its one question naming the
// root; one another session holds names the holder in that same question.
func (m model) launchTarget() (launchwarn.Target, error) {
	var row *launchwarn.Target
	if m.pickedRow != nil {
		row = launchwarn.FromRow(*m.pickedRow)
	}
	if m.creation != nil {
		t, err := m.creation.target()
		return foldRowFacts(t, row), err
	}
	dir := m.selectedPath
	if dir == "" {
		dir = m.prePath
	}
	if row != nil {
		// A list pick's row already holds the dirt (raw losses): its answer
		// goes in place of ForDir's probe (ForDirKnownDirt), so the
		// full-tree `git status` scan is skipped outright — not run and its
		// answer thrown away — while the person waits at the commit point.
		t, err := launchwarn.ForDirKnownDirt(dir, row.Dirty)
		return foldRowFacts(t, row), err
	}
	return launchwarn.ForDir(dir)
}

// foldRowFacts folds the picked list row's mapped facts (launchwarn.FromRow;
// nil when the pick came from no row) into the live read t: row facts win for
// the PR state and number, the dirt and the gone upstream. The PR and
// upstream are what ForDir and ForNew leave zero "for a caller that has a
// cleanup row to set them"; the dirt is the row's raw answer, which the
// ForDir branch passes in as the gathered value so no probe runs to be
// overwritten (ForDir probes dirt for the callers that have no row — preset
// paths, typed names, cmd/wt's confirmLaunch — and keeps doing so).
// Everything else keeps its existing source — the holders, the root's branch
// and default, the hint, and the creation the pick's plan owns (one creation
// path). A typed-name or preset-path pick (row nil) is untouched.
func foldRowFacts(t launchwarn.Target, row *launchwarn.Target) launchwarn.Target {
	if row == nil {
		return t
	}
	t.PRState, t.PRNumber = row.PRState, row.PRNumber
	t.UpstreamGone, t.Dirty = row.UpstreamGone, row.Dirty
	return t
}

// gateFor binds this model's launchGate to the launch it is about to make, as
// the per-call value the run commands ask at the commit point. It reads the
// Target at the ask (launchTarget), never earlier, and probes stdin's
// TTY-ness the way cmd/wt's confirmLaunch does (its stdinTTY parity): without
// a terminal the warnings print and the launch proceeds, stdin never read.
func (m model) gateFor() func() (bool, error) {
	return func() (bool, error) {
		t, readErr := m.launchTarget()
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "wt: note: %v\n", readErr)
		}
		return m.launchGate(&t, stdinTTY())
	}
}

// stdinTTY is the interactive-invocation probe cmd/wt's confirmLaunch uses:
// stdin is a terminal. It is called at the ask, so a redirected stdin (a
// script, a test) is honestly no terminal and can never be read.
func stdinTTY() bool { return term.IsTerminal(os.Stdin.Fd()) }

// launchGate is the one question before a launch commits, asked at the
// commit point with the terminal the user's again (the run commands call it
// where holdRoot's prompt used to be). It mirrors cmd/wt's confirmLaunch and
// holdRoot: the collected warnings — or, with none, the one informational
// line "launching in <dir>" every pick with a directory gets (the creation
// line of a pick that owes a worktree is the one line in its place) — and one
// default-yes question when a warning remains and there is a terminal. With
// no terminal the warnings print and the launch proceeds, stdin never read.
//
// A "no" is a choice, not a failure: ok is false and err is nil, nothing is
// created, claimed, recorded or run, and the run ends as quitting the picker
// does. A creation that fails once the person continued is a failure, not a
// choice: err is that error and nothing is printed here, so it travels out as
// the run's error and main prints it once and exits non-zero, exactly as
// cmd/wt's -W path does through refuse. Either way the owed worktree is made
// only here, and the root is claimed for exactly the agent's run; every
// claims failure is one "wt: note:", never a refusal.
//
// A preset path (m.prePath: -W, --cwd, --root through the flag paths) was
// judged and answered in RunE's confirmLaunch already. This is its second
// arrival at the commit point, so the gate speaks and asks nothing there —
// a root launch is asked at most once — and only claims.
func (m model) launchGate(t *launchwarn.Target, isTTY bool) (ok bool, err error) {
	if m.prePath != "" {
		m.claimRoot(t.Dir)
		return true, nil
	}
	warnings, info := launchwarn.Collect(*t)
	if t.Dir == "" {
		// Nothing to name (a launch outside any repository): no
		// informational line either, as confirmLaunch decides it.
		info = ""
	}
	if !launchwarn.Check(os.Stdin, os.Stderr, isTTY, warnings, info, t.Creation) {
		return false, nil
	}
	if t.Hint != "" {
		fmt.Fprintf(os.Stderr, "wt: %s\n", t.Hint)
	}
	if m.creation != nil {
		if _, cerr := m.creation.ensure(); cerr != nil {
			return false, cerr
		}
	} else if m.typedPlan != nil {
		// A typed name that announced nothing still runs its plan's Ensure
		// here — cmd/wt's -W runs it unconditionally after the question
		// (main.go:551). A reused worktree is Ensure's idempotent no-op and
		// keeps this silent; a path that is only debris (a partial add, a
		// half-deleted worktree, a stray file) is git's own error instead of
		// a launch at a directory that is not a worktree, where every git
		// command resolves to the repository root.
		if _, cerr := m.typedPlan.Ensure(); cerr != nil {
			return false, cerr
		}
	}
	m.claimRoot(t.Dir)
	return true, nil
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
