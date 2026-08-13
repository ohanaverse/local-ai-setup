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

```go
package worktree

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureForName returns the worktree path for branch name, creating it if needed.
// If the branch is already checked out somewhere, that path is returned.
func EnsureForName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("worktree name cannot be empty")
	}
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}

	// Already checked out in a worktree? Use it.
	entries, err := listWorktrees(root)
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

	path := filepath.Join(root, ".worktrees", safeName)
	// Idempotent: if the path is already a registered worktree, skip creation.
	if isWorktreePath(path) {
		return path, nil
	}

	if branchExists(name) {
		if _, err := runGit("worktree", "add", path, name); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
	} else {
		if _, err := runGit("worktree", "add", "-b", name, path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
	}
	return path, nil
}

// RepoRoot returns the absolute path of the current git repository root.
func RepoRoot() (string, error) {
	out, err := runGit("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func branchExists(branch string) bool {
	return exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// isWorktreePath reports whether path is already registered as a worktree.
func isWorktreePath(path string) bool {
	out, err := runGit("worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	return bytes_Contains(out, []byte("worktree "+path+"\n"))
}
```

`bytes_Contains` isn't a function — use the `bytes` package. Replace it:

```go
import "bytes"

func isWorktreePath(path string) bool {
	out, err := runGit("worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	return bytes.Contains(out, []byte("worktree "+path+"\n"))
}
```

### Creating from a selected branch

Port `handle_branch_selection`: given a picked branch, decide whether it's a
local branch, a remote-tracking branch, or a brand-new branch, and create the
worktree accordingly. This is the more complex path because remote branches
need special handling.

```go
// EnsureForBranch creates (or reuses) a worktree for a picked branch.
// Handles local, remote-tracking, and new branches.
func EnsureForBranch(branch string) (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}

	switch {
	case branchExists(branch):
		// Local branch (possibly with slashes) — use last component.
		path := filepath.Join(root, ".worktrees", filepath.Base(branch))
		if _, err := runGit("worktree", "add", path, branch); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
		return path, nil

	case remoteExists(branch):
		// Remote-tracking branch — create a local branch tracking it.
		short := branch[strings.IndexByte(branch, '/')+1:]
		worktreeDir := filepath.Base(short)
		if strings.ContainsAny(worktreeDir, "/\\") {
			return "", fmt.Errorf("worktree name must not contain path separators: %s", short)
		}
		if branchExists(short) {
			return "", fmt.Errorf("local branch %q already exists — cannot create from remote %q", short, branch)
		}
		path := filepath.Join(root, ".worktrees", worktreeDir)
		if _, err := runGit("worktree", "add", "-b", short, path, branch); err != nil {
			return "", fmt.Errorf("git worktree add -b %s %s: %w", short, branch, err)
		}
		return path, nil

	default:
		// Brand-new branch.
		worktreeDir := filepath.Base(branch)
		path := filepath.Join(root, ".worktrees", worktreeDir)
		if _, err := runGit("worktree", "add", "-b", branch, path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
		return path, nil
	}
}

func remoteExists(ref string) bool {
	return exec.Command("git", "show-ref", "--verify", "--quiet", "refs/remotes/"+ref).Run() == nil
}
```

Note: `EnsureForName` uses the plain path (no `.worktrees` nesting change),
and `EnsureForBranch` always goes under `.worktrees/`, matching the bash
behavior where bare branches and remotes get worktrees but a `-w name` that
is already checked out reuses the existing path.

## Run It

```go
path, err := worktree.EnsureForName("my-feature")
fmt.Println("worktree at:", path)
```

```bash
go run ./cmd/wt
```

```
worktree at: /Users/keith/.../agent-worktree/.worktrees/my-feature
```

And for a remote branch:

```go
path, err := worktree.EnsureForBranch("origin/feature/x")
```

creates local branch `x` tracking `origin/feature/x` at `.worktrees/x`.

## Try It Yourself

Call `EnsureForBranch("origin/foo")` where `foo` already exists as a local
branch, and verify it returns the "already exists" error rather than creating
a conflicting worktree.

<details>
<summary>Solution</summary>

```go
func TestRemoteConflictsWithLocal(t *testing.T) {
	// set up: local branch "foo" + remote "origin/foo" (e.g. via git remote add)
	_, err := EnsureForBranch("origin/foo")
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
