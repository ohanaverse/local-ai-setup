package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The no-terminal refusal ends with what the caller can actually do about it.
// A wt launch has --root and -W to say yes to the root without a prompt, but
// wt smoke has neither way in (--root is not its flag, and -W does not move
// its rows), so its remedies are dropping --cwd or standing below the root.
const (
	launchRootRemedy = "pass --root, or -W <name> to use a worktree"
	smokeRootRemedy  = "wt smoke has no --root: run it without --cwd (rows use a temporary repository), or from a directory below the root"
)

// gateRoot is the check on a launch wt makes in launchPath without the picker
// (--cwd): working in a repository's root needs the person's say-so. It does
// nothing when launchPath is not a root, when another live session holds the
// root (the shared-root menu that follows is the one question) and when a
// parent wt session live-holds it (a nested launch under an agent at the
// root; a stale claim whose pid was recycled does not count —
// docs/contracts/session-claims.md). Otherwise, with no terminal it refuses with remedy — what this
// command can name as the way in — marked as a rootGateError so the caller
// can silence cobra's usage block after it; on a terminal it asks, and a no
// is errCancelled. A claims directory that cannot be read counts as no other
// session, so the question is still asked.
func gateRoot(launchPath, agent string, in io.Reader, out io.Writer, isTTY bool, remedy string) error {
	root, ok, err := rootclaim.Lookup(launchPath)
	if !ok {
		return nil
	}
	if err == nil {
		if self, serr := root.Self(agent); serr == nil {
			if others, oerr := sessionclaim.Others(root.CommonDir, self); oerr == nil && len(others) > 0 {
				return nil
			}
			if held, herr := sessionclaim.AncestorHolds(root.CommonDir, self); herr == nil && held {
				return nil
			}
		}
	}
	if !isTTY {
		return asRootGateError(fmt.Errorf("the repository root %s needs your say-so to launch in; %s", root.Path, remedy))
	}
	if !rootclaim.ConfirmRoot(root, in, out) {
		return errCancelled
	}
	return nil
}

// rootGateError marks the gate's no-terminal refusal — text and chain
// unchanged — so a command's error path can tell this final refusal from a
// usage mistake and leave cobra's usage block silent after it, the way
// startError does for a failed start. The root launch needs no mark: its
// refusal path sets the silences itself (refuse).
type rootGateError struct {
	msg string
	err error
}

func (e *rootGateError) Error() string { return e.msg }
func (e *rootGateError) Unwrap() error { return e.err }

func asRootGateError(err error) error {
	return &rootGateError{msg: err.Error(), err: err}
}

// isRootGateError reports whether err is, or wraps, the gate's refusal.
func isRootGateError(err error) bool {
	var ge *rootGateError
	return errors.As(err, &ge)
}

// refuseRootBranch is the refusal for -W <name> when the worktree it resolved
// to (path) is the repository root: a branch the root has checked out names
// the root, and the root is reached with --root, not by a branch name.
func refuseRootBranch(name, path string) error {
	if root, ok := worktree.RootOf(path); ok {
		return fmt.Errorf("-W %s is the branch the repository root %s has checked out; pass --root to launch there, or name another worktree", name, root)
	}
	return nil
}
