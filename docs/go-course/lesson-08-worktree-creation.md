# Lesson 8: Worktree creation

## Concept Intro

The picker isn't just a chooser — it can also *create* a worktree. When the
user picks a bare branch, we create a worktree for it and launch there. And
the `-w <name>` flag does the same non-interactively. This lesson ports
`ensure_worktree_for_name` and `handle_branch_selection` from `wt-core.sh`.

The logic, in order:

1. If a branch is already checked out in some worktree, use that path (idempotent).
2. If a worktree already exists at `.worktrees/<name>`, use it.
3. Otherwise create one with `git worktree add`.

Branch names can contain slashes (`feature/my-branch`, `origin/feature`). The
**last path component** becomes the worktree directory name
(`.worktrees/my-branch`). For a remote-tracking branch (`origin/feature`), we
create a *new local branch* `feature` tracking it, then check it out.

A safety guard rejects names that still contain path separators after
extraction (prevents traversal like `..`).

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `git worktree list --porcelain` | Used again to check for an existing worktree. |
| `git show-ref --verify --quiet refs/heads/<name>` | Returns success iff the local branch exists. |
| `git worktree add <path> <branch>` | Checks out an existing branch. |
| `git worktree add -b <branch> <path>` | Creates a new branch and checks it out. |
| `git worktree add -b <local> <path> <remote>` | Creates a local branch tracking a remote ref. |
| `path/filepath.Base` | Extracts the last path component safely. |
| `path/filepath.Join` | Joins the repo root and `.worktrees/<name>`. |

## Worked Walkthrough

Create `internal/worktree/create.go`:

> **Regrounding note:** Lesson 7 changed `runGit` to take a `dir` argument
> (so tests can run git inside a temp repo) and `listWorktrees` to take
> `(dir, cwdRoot)`. This lesson follows the same convention: every function
> takes `dir` (the repo root) as its first parameter. The original lesson had
> `EnsureForName` compute the root itself via a `RepoRoot()` helper, but that
> breaks the testability pattern — the caller (the CLI) already knows the
> root, so we pass it in instead.

```go
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
```

> **Regrounding note:** The original lesson used `exec.Command("git",
> "show-ref", ...)` directly for `branchExists` and a `bytes_Contains` helper
> that doesn't exist. Both are replaced with `runGit(dir, ...)` (which returns
> a non-nil error on a non-zero exit, so `err == nil` means "the ref exists")
> and the standard-library `bytes.Contains`.

### Creating from a selected branch

Port `handle_branch_selection`: given a picked branch, decide whether it's a
local branch, a remote-tracking branch, or a brand-new branch, and create the
worktree accordingly. This is the more complex path because remote branches
need special handling.

```go
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
```

Note: `EnsureForName` reuses an existing worktree path when the branch is
already checked out (returns `e.Path` directly), while `EnsureForBranch`
always creates under `.worktrees/`, matching the bash behavior where bare
branches and remotes get worktrees but a `-w name` that is already checked
out reuses the existing path.

## Run It

Wire the `-w` flag (already declared in `cmd/wt/main.go`) to `EnsureForName`.
In the root `RunE`, before the TUI placeholder:

```go
if w, _ := cmd.Flags().GetString("worktree"); w != "" {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("not in a git repo: %w", err)
	}
	path, err := worktree.EnsureForName(strings.TrimSpace(string(root)), w)
	if err != nil {
		return err
	}
	fmt.Println("worktree at:", path)
	return nil
}
```

```bash
go run ./cmd/wt -w my-feature
```

```
worktree at: /Users/keith/.../agent-worktree/.worktrees/my-feature
```

And for a remote branch:

```go
path, err := worktree.EnsureForBranch(dir, "origin/feature/x")
```

creates local branch `x` tracking `origin/feature/x` at `.worktrees/x`.

## Try It Yourself

Call `EnsureForBranch(dir, "origin/foo")` where `foo` already exists as a
local branch, and verify it returns the "already exists" error rather than
creating a conflicting worktree.

<details>
<summary>Solution</summary>

```go
func TestRemoteConflictsWithLocal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	// set up: local branch "foo" + remote "origin/foo" (e.g. via git remote add)
	_, err := EnsureForBranch(dir, "origin/foo")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got %v", err)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 08: worktree creation" && git tag lesson-08
```
