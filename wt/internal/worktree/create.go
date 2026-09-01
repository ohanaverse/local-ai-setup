package worktree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// EnsureForName returns the worktree path for branch name, creating it if needed.
// dir is the repo root. If the branch is already checked out somewhere, that
// path is returned (idempotent).
func EnsureForName(dir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("worktree name cannot be empty")
	}

	// Already checked out in a worktree? Use it.
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Branch == name && e.Path != "" {
			return e.Path, nil
		}
	}

	// The default branch must never be a linked worktree. If it isn't
	// already checked out (the reuse loop above found nothing), refuse
	// rather than create .worktrees/<default>.
	if db, _ := DefaultBranch(dir); db != "" && name == db {
		return "", fmt.Errorf("refusing to create a worktree for the default branch %q; check it out in the primary checkout instead", name)
	}

	safeName := filepath.Base(name)
	if strings.ContainsAny(safeName, "/\\") {
		return "", fmt.Errorf("worktree name must not contain path separators: %s", name)
	}
	// "." and ".." survive filepath.Base and would resolve to the repo root
	// via filepath.Join (".worktrees/.." cleans to dir), silently creating
	// nothing. Reject them explicitly.
	if safeName == "." || safeName == ".." {
		return "", fmt.Errorf("worktree name must not be %q", safeName)
	}

	path := filepath.Join(dir, ".worktrees", safeName)
	// Idempotent: if the path is already a registered worktree for the same
	// branch, reuse it. If it's registered for a DIFFERENT branch (e.g. the
	// user typed "feature/x" but .worktrees/x already holds branch "x"),
	// error rather than silently returning the wrong worktree.
	if branch, ok := branchAtPath(entries, path); ok {
		if branch != "" && branch != name {
			return "", fmt.Errorf("worktree path %s is already in use by branch %q", path, branch)
		}
		return path, nil
	}

	if refExists(dir, "refs/heads/"+name) {
		if _, err := runGit(dir, "worktree", "add", path, name); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
	} else {
		if _, err := runGit(dir, "worktree", "add", "-b", name, path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
	}
	return path, nil
}

// refExists reports whether the fully-qualified ref (e.g. refs/heads/main)
// exists in dir.
func refExists(dir, ref string) bool {
	_, err := runGit(dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// branchAtPath returns the branch checked out at path among the already-listed
// worktree entries, if path is a registered worktree. The bool is true when
// path is registered (even when detached, in which case branch is ""). Paths
// are compared through symlinks so the lookup matches git's resolved porcelain
// output (macOS /var → /private/var); the old byte-compare failed on symlinked
// repos. Reusing the entries from listWorktrees avoids a second
// `git worktree list` subprocess on the EnsureForName path.
func branchAtPath(entries []Entry, path string) (string, bool) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	for _, e := range entries {
		ep, err := filepath.EvalSymlinks(e.Path)
		if err != nil {
			ep = e.Path
		}
		if ep == path {
			if e.Branch == "(detached)" {
				return "", true
			}
			return e.Branch, true
		}
	}
	return "", false
}

// EnsureForBranch creates (or reuses) a worktree for a picked branch.
// Handles local, remote-tracking, and new branches.
func EnsureForBranch(dir, branch string) (string, error) {
	// The default branch must never be a linked worktree, so refuse both
	// the local form and any remote-tracking form (origin/<default>,
	// upstream/<default>, ...). isDefaultBranchSelection confirms the ref
	// really lives under refs/remotes/ so a local branch that merely ends
	// in "/<default>" (e.g. feature/main) is not falsely refused.
	if db, _ := DefaultBranch(dir); db != "" && isDefaultBranchSelection(branch, db, dir) {
		return "", fmt.Errorf("refusing to create a worktree for the default branch %q; check it out in the primary checkout instead", db)
	}
	switch {
	case refExists(dir, "refs/heads/"+branch):
		// Local branch (possibly with slashes) — use last component.
		path := filepath.Join(dir, ".worktrees", filepath.Base(branch))
		if _, err := runGit(dir, "worktree", "add", path, branch); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
		return path, nil

	case refExists(dir, "refs/remotes/"+branch):
		// Remote-tracking branch — create a local branch tracking it.
		short := branch[strings.IndexByte(branch, '/')+1:]
		worktreeDir := filepath.Base(short)
		if strings.ContainsAny(worktreeDir, "/\\") {
			return "", fmt.Errorf("worktree name must not contain path separators: %s", short)
		}
		if refExists(dir, "refs/heads/"+short) {
			return "", fmt.Errorf("local branch %q already exists — cannot create from remote %q", short, branch)
		}
		path := filepath.Join(dir, ".worktrees", worktreeDir)
		if _, err := runGit(dir, "worktree", "add", "-b", short, path, branch); err != nil {
			return "", fmt.Errorf("git worktree add -b %s %s: %w", short, branch, err)
		}
		return path, nil

	default:
		// Brand-new branch.
		worktreeDir := filepath.Base(branch)
		path := filepath.Join(dir, ".worktrees", worktreeDir)
		if _, err := runGit(dir, "worktree", "add", "-b", branch, path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
		return path, nil
	}
}

// isDefaultBranchSelection reports whether picking branch would create (or
// reuse) a worktree on the repo default branch db — the situation the
// default-branch invariant forbids for linked worktrees. It matches the bare
// local default (db) or a real remote-tracking ref whose short name is db
// (origin/db, upstream/db, ...). A local branch that merely ends in "/db"
// (e.g. feature/main) is NOT a match: it is a feature branch, not the
// default, so IsDefaultBranchForm's name-only match is gated on the ref
// actually existing under refs/remotes/.
func isDefaultBranchSelection(branch, db, dir string) bool {
	if branch == db {
		return true
	}
	return IsDefaultBranchForm(branch, db) && refExists(dir, "refs/remotes/"+branch)
}
