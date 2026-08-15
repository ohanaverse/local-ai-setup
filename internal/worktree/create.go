package worktree

import (
	"bytes"
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

	path := filepath.Join(dir, ".worktrees", safeName)
	// Idempotent: if the path is already a registered worktree, skip creation.
	if isWorktreePath(dir, path) {
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

// isWorktreePath reports whether path is already registered as a worktree.
func isWorktreePath(dir, path string) bool {
	out, err := runGit(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte("worktree "+path+"\n"))
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
