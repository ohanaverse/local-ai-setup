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

	// The default branch must never be a linked worktree, whether typed as
	// the bare name or as a remote-tracking form (origin/<default>,
	// <remote>/HEAD, ...).
	if err := refuseDefaultBranch(dir, name); err != nil {
		return "", err
	}

	// A remote-tracking name (origin/feature) resolves like the picker does:
	// reuse a worktree already on the local branch, else create one tracking
	// the remote.
	if _, short, ok := remoteRefParts(dir, name); ok {
		for _, e := range entries {
			if e.Branch == short && e.Path != "" {
				return e.Path, nil
			}
		}
		return addTrackingWorktree(dir, name, short)
	}

	path, err := worktreePath(dir, name)
	if err != nil {
		return "", err
	}
	// Idempotent: if the path is already a registered worktree for the same
	// branch, reuse it. If it's registered for a DIFFERENT branch — a name
	// that flattens to another's (feature/x and feature-x share
	// .worktrees/feature-x), or a last-component worktree left by an older
	// wt — error rather than silently returning the wrong worktree.
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

// maxDirNameBytes is the filesystem's per-entry name limit (NAME_MAX on macOS
// and Linux). Git's .git/worktrees/<basename> entry shares it.
const maxDirNameBytes = 255

// worktreeDirName derives a worktree directory name from a full branch name.
// Every path-hostile character — path separators on any platform, colon, and
// ASCII control characters — becomes "-", so feature/x and bug/x land in
// distinct directories (.worktrees/feature-x, .worktrees/bug-x) instead of
// racing for the last path component. Everything else is kept (invalid
// UTF-8 bytes re-encode as U+FFFD). A derived "." or ".." is an error:
// filepath.Join cleans ".." up to the repo root and "." to the .worktrees
// container — the wrong path either way. A derived name over maxDirNameBytes
// is an error too: git nests a ref's components as directories, so a long
// branch can be valid while its flattened name, one directory entry, is not.
func worktreeDirName(name string) (string, error) {
	dirName := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		}
		if r < 0x20 || r == 0x7f {
			return '-'
		}
		return r
	}, name)
	if dirName == "." || dirName == ".." {
		return "", fmt.Errorf("worktree name must not be %q", dirName)
	}
	if len(dirName) > maxDirNameBytes {
		return "", fmt.Errorf("branch name %q is too long for a worktree directory (%d bytes after flattening, limit %d): shorten the branch name",
			name, len(dirName), maxDirNameBytes)
	}
	return dirName, nil
}

// worktreePath is where branch name's linked worktree lives: the derived
// directory name under <dir>/.worktrees. Every creation path goes through it.
func worktreePath(dir, name string) (string, error) {
	dirName, err := worktreeDirName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".worktrees", dirName), nil
}

// pathCollision errors when path is a registered worktree of a DIFFERENT
// branch than name — the one "already in use" message for -W and the picker.
// Flattening makes it easy to hit (feature/x and feature-x share
// .worktrees/feature-x), and git's own "already exists" never names the
// branch holding the path. A detached worktree or the same branch is not a
// collision here; the caller decides what reusing it means.
func pathCollision(entries []Entry, path, name string) error {
	if branch, ok := branchAtPath(entries, path); ok && branch != "" && branch != name {
		return fmt.Errorf("worktree path %s is already in use by branch %q", path, branch)
	}
	return nil
}

// newWorktreePath is worktreePath for the picker paths, which create a
// worktree without the -W reuse loop: it also lists the registered worktrees
// and refuses a path another branch holds.
func newWorktreePath(dir, name string) (string, error) {
	path, err := worktreePath(dir, name)
	if err != nil {
		return "", err
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return "", err
	}
	if err := pathCollision(entries, path, name); err != nil {
		return "", err
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
	if err := refuseDefaultBranch(dir, branch); err != nil {
		return "", err
	}
	switch {
	case refExists(dir, "refs/heads/"+branch):
		// Local branch (possibly with slashes) — the directory derives from
		// the full name.
		path, err := newWorktreePath(dir, branch)
		if err != nil {
			return "", err
		}
		if _, err := runGit(dir, "worktree", "add", path, branch); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
		return path, nil

	case refExists(dir, "refs/remotes/"+branch):
		// Remote-tracking branch — create a local branch tracking it.
		short := branch[strings.IndexByte(branch, '/')+1:]
		return addTrackingWorktree(dir, branch, short)

	default:
		// Brand-new branch.
		path, err := newWorktreePath(dir, branch)
		if err != nil {
			return "", err
		}
		if _, err := runGit(dir, "worktree", "add", "-b", branch, path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
		return path, nil
	}
}

// addTrackingWorktree creates a worktree on a new local branch short that
// tracks the remote-tracking ref remoteRef.
func addTrackingWorktree(dir, remoteRef, short string) (string, error) {
	path, err := newWorktreePath(dir, short)
	if err != nil {
		return "", err
	}
	if refExists(dir, "refs/heads/"+short) {
		return "", fmt.Errorf("local branch %q already exists — cannot create from remote %q", short, remoteRef)
	}
	if _, err := runGit(dir, "worktree", "add", "-b", short, path, remoteRef); err != nil {
		return "", fmt.Errorf("git worktree add -b %s %s: %w", short, remoteRef, err)
	}
	return path, nil
}

// refuseDefaultBranch is the one default-branch guard for both creation
// paths. It errors when name selects the repo default branch, naming it as the
// user typed it.
func refuseDefaultBranch(dir, name string) error {
	if db := DefaultBranch(dir); db != "" && isDefaultBranchSelection(name, db, dir) {
		return fmt.Errorf("refusing to create a worktree for the default branch %q; check it out in the primary checkout instead", name)
	}
	return nil
}

// remoteRefParts splits name into (remote, branch) when it is a real
// remote-tracking ref: refs/remotes/<name> exists, no local branch of that
// exact name exists, and the prefix is a configured remote (longest wins, so
// remotes with slashes work). A local branch such as feature/main is never
// treated as remote.
func remoteRefParts(dir, name string) (remote, short string, ok bool) {
	if refExists(dir, "refs/heads/"+name) || !refExists(dir, "refs/remotes/"+name) {
		return "", "", false
	}
	out, err := runGit(dir, "remote")
	if err != nil {
		return "", "", false
	}
	for _, r := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, r+"/") && len(r) > len(remote) {
			remote = r
		}
	}
	if remote == "" {
		return "", "", false
	}
	return remote, name[len(remote)+1:], true
}

// isDefaultBranchSelection reports whether picking branch would create (or
// reuse) a worktree on the repo default branch db: the bare local default, or
// a real remote-tracking ref (see remoteRefParts) whose branch is db or HEAD
// (<remote>/HEAD is a symbolic alias for the default).
func isDefaultBranchSelection(branch, db, dir string) bool {
	if branch == db {
		return true
	}
	_, short, ok := remoteRefParts(dir, branch)
	return ok && (short == db || short == "HEAD")
}
