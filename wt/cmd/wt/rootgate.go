package main

import (
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// gateRoot is the check on a launch wt makes in launchPath without the picker
// (--cwd): working in a repository's root needs the person's say-so. It does
// nothing when launchPath is not a root, when another live session holds the
// root (the shared-root menu that follows is the one question) and when a
// parent wt session already holds it (a nested launch under an agent at the
// root). Otherwise, with no terminal it refuses and names --root; on a
// terminal it asks, and a no is errCancelled. A claims directory that cannot
// be read counts as no other session, so the question is still asked.
func gateRoot(launchPath, agent string, in io.Reader, out io.Writer, isTTY bool) error {
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
		return fmt.Errorf("the repository root %s needs your say-so to launch in; pass --root, or -W <name> to use a worktree", root.Path)
	}
	if !rootclaim.ConfirmRoot(root, in, out) {
		return errCancelled
	}
	return nil
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
