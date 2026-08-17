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
	if branch, ok := worktreeBranchAt(dir, path); ok {
		if branch != "" && branch != name {
			return "", fmt.Errorf("worktree path %s is already in use by branch %q", path, branch)
		}
		return path, nil
	}

	if branchExists(dir, name) {
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

func branchExists(dir, branch string) bool {
	_, err := runGit(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// worktreeBranchAt returns the branch checked out at path, if path is a
// registered worktree. The bool is true when path is registered (even when
// detached, in which case branch is ""). Paths are resolved through symlinks
// so the lookup matches git's porcelain output, which emits resolved paths
// (macOS /var → /private/var); the old byte-compare failed on symlinked repos.
func worktreeBranchAt(dir, path string) (string, bool) {
	out, err := runGit(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	var curPath string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			if curPath == path {
				return strings.TrimPrefix(line, "branch refs/heads/"), true
			}
		case strings.HasPrefix(line, "detached"):
			if curPath == path {
				return "", true
			}
		}
	}
	return "", false
}

// EnsureForBranch creates (or reuses) a worktree for a picked branch.
// Handles local, remote-tracking, and new branches.
func EnsureForBranch(dir, branch string) (string, error) {
	switch {
	case branchExists(dir, branch):
		// Local branch (possibly with slashes) — use last component.
		path := filepath.Join(dir, ".worktrees", filepath.Base(branch))
		if _, err := runGit(dir, "worktree", "add", path, branch); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
		return path, nil

	case remoteExists(dir, branch):
		// Remote-tracking branch — create a local branch tracking it.
		short := branch[strings.IndexByte(branch, '/')+1:]
		worktreeDir := filepath.Base(short)
		if strings.ContainsAny(worktreeDir, "/\\") {
			return "", fmt.Errorf("worktree name must not contain path separators: %s", short)
		}
		if branchExists(dir, short) {
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

func remoteExists(dir, ref string) bool {
	_, err := runGit(dir, "show-ref", "--verify", "--quiet", "refs/remotes/"+ref)
	return err == nil
}
