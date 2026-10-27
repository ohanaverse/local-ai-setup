package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// errCancelled is a quit (q or Esc) at the shared-root menu: nothing launched.
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
// returns the directory to launch in. A -W launch names its worktree and is
// never checked.
func holdSharedRoot(cmd *cobra.Command, agent, launchPath string) (string, error) {
	if mustGetString(cmd, "worktree") != "" {
		return launchPath, nil
	}
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
	root, ok := worktree.RootOf(dir)
	if !ok {
		return dir, noop, nil
	}
	note := func(err error) (string, func(), error) {
		fmt.Fprintf(out, "wt: note: could not check other sessions in %s (%v); launching anyway\n", root, err)
		return dir, noop, nil
	}
	commonDir, err := gitOutput(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return note(err)
	}
	branch, err := gitOutput(root, "branch", "--show-current")
	if err != nil || branch == "" {
		branch = "(detached)"
	}
	self, err := sessionclaim.Self(root, branch, agent)
	if err != nil {
		return note(err)
	}

	choice := byte('c')
	release, others, err := sessionclaim.Join(commonDir, self, func(others []sessionclaim.Claim) bool {
		if allow {
			return true
		}
		if !isTTY {
			choice = 0
			return false
		}
		fmt.Fprintf(out, "wt: the repository root %s is in use by another session:\n", root)
		for _, o := range others {
			fmt.Fprintf(out, "  %s\n", describeClaim(o))
		}
		fmt.Fprint(out, "  c  continue here    w  new worktree    q  quit (Esc)\nchoose [c/w/q]: ")
		choice = readChoice(in)
		fmt.Fprintln(out)
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
			names[i] = describeClaim(o)
		}
		return "", noop, fmt.Errorf("the repository root %s is in use by %s; pass --allow-shared-root to launch there anyway, or -W <name> to use a worktree", root, strings.Join(names, ", "))
	case choice == 'w':
		name := fmt.Sprintf("session-%d-%s", os.Getpid(), time.Now().Format("150405"))
		path, err := worktree.EnsureForName(root, name)
		if err != nil {
			return "", noop, fmt.Errorf("new worktree: %w", err)
		}
		fmt.Fprintf(out, "wt: launching in the new worktree %s\n", path)
		return path, noop, nil
	default:
		return "", noop, errCancelled
	}
}

// describeClaim names one other session for the menu and the refusal.
func describeClaim(c sessionclaim.Claim) string {
	running := time.Since(time.Unix(c.Started, 0)).Round(time.Second)
	return fmt.Sprintf("%s (pid %d, running %s, started on %s)", c.Agent, c.PID, running, c.Branch)
}

// readChoice reads keys from in until one is c, w or q (Esc and Ctrl-C are
// q), one key at a time on a terminal. End of input is q.
func readChoice(in io.Reader) byte {
	if f, ok := in.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if state, err := term.MakeRaw(f.Fd()); err == nil {
			defer term.Restore(f.Fd(), state)
		}
	}
	buf := make([]byte, 1)
	for {
		if n, err := in.Read(buf); n == 0 && err != nil {
			return 'q'
		} else if n == 0 {
			continue
		}
		switch b := buf[0]; b {
		case 'c', 'C':
			return 'c'
		case 'w', 'W':
			return 'w'
		case 'q', 'Q', 0x1b, 0x03:
			return 'q'
		}
	}
}

// gitOutput runs git in dir and returns its trimmed output.
func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
