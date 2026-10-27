package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// errCancelled is a quit (q or Esc) at the shared-root menu: nothing launched.
// It is a choice, not a failure, so launchHere ends the run with status 0 and
// no message, as quitting the picker does.
var errCancelled = errors.New("launch cancelled")

// rootClaimRelease removes this session's claim on the repository root. It is
// set by holdSharedRoot and called through releaseRootClaim.
var rootClaimRelease = func() {}

// releaseRootClaim removes this session's claim on the repository root, if it
// holds one. Safe to call more than once: runAgentCmd calls it before its
// os.Exit, and runLaunchPath again when the launch returns.
func releaseRootClaim() {
	release := rootClaimRelease
	rootClaimRelease = func() {}
	release()
}

// holdSharedRoot runs confirmSharedRoot for a launch wt makes in launchPath
// without the picker, keeps the claim's release for releaseRootClaim, and
// returns the directory to launch in. It decides on launchPath, not the flag
// that named it: a -W that resolves to the root is checked like --cwd.
func holdSharedRoot(cmd *cobra.Command, agent, launchPath string) (string, error) {
	allow, _ := cmd.Flags().GetBool("allow-shared-root")
	dir, release, err := confirmSharedRoot(launchPath, agent, allow, os.Stdin, osStderr, stdinTTY())
	if err != nil {
		// main prints the one "wt:" line; cobra's copy and usage block would
		// only repeat it.
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return "", err
	}
	rootClaimRelease = release
	return dir, nil
}

// confirmSharedRoot is the check before a launch in dir. When dir is not a
// repository's root it does nothing. Otherwise it claims the root for this
// session (sessionclaim.Join); when other live sessions hold it, it names them
// and asks on in: c continues in the root, w moves the launch to a new
// worktree (newDir, no claim), q or Esc returns errCancelled. With no
// terminal it refuses unless allow (--allow-shared-root) says to continue. A
// claim that cannot be read or written is a one-line note on out, never a
// refusal. release removes the claim and is never nil.
func confirmSharedRoot(dir, agent string, allow bool, in io.Reader, out io.Writer, isTTY bool) (newDir string, release func(), err error) {
	noop := func() {}
	root, ok, err := rootclaim.Lookup(dir)
	if !ok {
		return dir, noop, nil
	}
	note := func(err error) (string, func(), error) {
		fmt.Fprintf(out, "wt: note: %s\n", sessionclaim.CheckFailed(root.Path, err))
		return dir, noop, nil
	}
	if err != nil {
		return note(err)
	}
	self, err := root.Self(agent)
	if err != nil {
		return note(err)
	}

	choice := byte('c')
	release, others, err := sessionclaim.Join(root.CommonDir, self, func(others []sessionclaim.Claim) bool {
		if allow {
			return true
		}
		if !isTTY {
			choice = 0
			return false
		}
		choice = rootclaim.Choose(root, others, in, out)
		return choice == 'c'
	})
	if err != nil {
		return note(err)
	}
	switch {
	case len(others) == 0 || choice == 'c':
		return dir, release, nil
	case choice == 0:
		names := make([]string, len(others))
		for i, o := range others {
			names[i] = sessionclaim.Describe(o, time.Now())
		}
		return "", noop, fmt.Errorf("the repository root %s is in use by %s; pass --allow-shared-root to launch there anyway, or -W <name> to use a worktree", root.Path, strings.Join(names, ", "))
	case choice == 'w':
		name := sessionclaim.NewWorktreeName(os.Getpid(), time.Now())
		path, err := worktree.EnsureForName(root.Path, name)
		if err != nil {
			return "", noop, fmt.Errorf("new worktree: %w", err)
		}
		fmt.Fprintf(out, "wt: launching in the new worktree %s\n", path)
		return path, noop, nil
	default:
		return "", noop, errCancelled
	}
}
