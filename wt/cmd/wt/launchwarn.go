package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// errCancelled is a "no" at the one launch question: nothing launched. It is a
// choice, not a failure, so the launch paths end the run with status 0 and no
// message, as quitting the picker does.
var errCancelled = errors.New("launch cancelled")

// rootClaimRelease removes this session's claim on the repository root. It is
// set by holdRoot and called through releaseRootClaim.
var rootClaimRelease = func() {}

// releaseRootClaim removes this session's claim on the repository root, if it
// holds one. Safe to call more than once: runAgentCmd calls it before its
// os.Exit, and launchHere again when the launch returns.
func releaseRootClaim() {
	release := rootClaimRelease
	rootClaimRelease = func() {}
	release()
}

// confirmLaunch is the one confirmation before a launch. readErr is what
// launchwarn.ForDir/ForNew could not read (usually the session claims): one
// "wt: note:" line and then the launch goes on — a claim's absence is never a
// refusal. Otherwise it is launchwarn.Check: the collected warnings (or, with
// none, the one line of where the launch goes, or the creation line) and the
// one default-yes question when a terminal and a warning remain. With no
// terminal it prints and proceeds, reading nothing. Target.Hint follows as
// trailing advice (Collect ignores it). A "no" is errCancelled.
func confirmLaunch(t launchwarn.Target, readErr error, in io.Reader, out io.Writer, isTTY bool) error {
	if readErr != nil {
		fmt.Fprintf(out, "wt: note: %v\n", readErr)
	}
	warnings, info := launchwarn.Collect(t)
	if t.Dir == "" {
		// No directory, no line. A launch outside a checkout root (a plain
		// directory, a bare repository) printed nothing before the launch
		// warnings existed, and "launching in " with nothing after it is not
		// a line. Check prints no empty info.
		info = ""
	}
	proceed := launchwarn.Check(in, out, isTTY, warnings, info, t.Creation)
	if t.Hint != "" {
		fmt.Fprintf(out, "wt: %s\n", t.Hint)
	}
	if !proceed {
		return errCancelled
	}
	return nil
}

// holdRoot is the root-claim side of a launch in dir: when dir is a
// repository's primary checkout it writes this session's claim (path, branch,
// agent) and keeps the release for releaseRootClaim, so other launches warn
// about it (held) exactly while the agent runs. A linked worktree claims
// nothing. Another live holder is not a question here — launchwarn's held
// warning has already named them. A claim that cannot be read or written is
// one note and never a refusal.
func holdRoot(agent, dir string) {
	root, ok, err := rootclaim.Lookup(dir)
	if !ok {
		return
	}
	note := func(err error) {
		fmt.Fprintf(osStderr, "wt: note: %s\n", sessionclaim.CheckFailed(root.Path, err))
	}
	if err != nil {
		note(err)
		return
	}
	self, err := root.Self(agent)
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
