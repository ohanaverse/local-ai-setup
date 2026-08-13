# Lesson 7: Worktree & branch enumeration

## Concept Intro

Before the TUI can present a picker, it needs the list of choices. This is the
port of `gather_entries()` from `wt-core.sh`. It enumerates three kinds of
targets:

1. **Worktrees** — every registered worktree (from `git worktree list
   --porcelain`), tagging the current directory's one as `current`.
2. **Local branches** not checked out in any worktree.
3. **Remote-tracking branches** (e.g. `origin/feature`) not shadowed by a
   local branch of the same short name.

We shell out to `git` and parse its output. The porcelain format for worktrees
is block-oriented:

```
worktree /abs/path
branch refs/heads/main
```

A blank line separates blocks. We parse that with a small state machine and
emit rows. Branch enumeration uses `git for-each-ref` which is scriptable and
stable (better than parsing `git branch -a`).

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `git worktree list --porcelain` | Machine-readable list of worktrees. |
| `git for-each-ref --format=%(refname:short)` | Lists refs with a custom format, one per line. |
| `bufio.Scanner` | Reads lines from stdout without loading everything into memory. |
| `cmd.StdoutPipe()` | Streams a command's stdout line-by-line. |
| `bufio.Scanner.Split(bufio.ScanLines)` | Split default; handles long lines and trailing newlines. |
| `Entry` struct | `{ Type, Branch, Path string }` — one pickable target. |

## Worked Walkthrough

Create `internal/worktree/enumerate.go`:

```go
package worktree

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
)

// Type of a pickable target.
type Type string

const (
	TypeCurrent  Type = "current"
	TypeWorktree Type = "worktree"
	TypeBranch   Type = "branch"
)

// Entry is one pickable target.
type Entry struct {
	Type   Type
	Branch string // branch name; "(detached)" for detached worktrees
	Path   string // worktree path; empty for bare branches
}

// runGit runs a git command and returns stdout bytes.
func runGit(args ...string) ([]byte, error) {
	return exec.Command("git", args...).Output()
}

// listWorktrees parses `git worktree list --porcelain` into entries.
func listWorktrees(cwdRoot string) ([]Entry, error) {
	out, err := runGit("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	var path, branch string
	isBare := false

	flush := func() {
		if path != "" && !isBare {
			t := TypeWorktree
			if path == cwdRoot {
				t = TypeCurrent
			}
			b := branch
			if b == "" {
				b = "(detached)"
			}
			entries = append(entries, Entry{Type: t, Branch: b, Path: path})
		}
		path, branch = "", ""
		isBare = false
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			branch = strings.TrimPrefix(line, "branch refs/heads/")
		case line == "bare":
			isBare = true
		}
	}
	flush()
	return entries, nil
}
```

Now branch enumeration. Remote branches are included only when no local branch
has the same short name:

```go
// listLocalBranches returns short names of all local branches.
func listLocalBranches() ([]string, error) {
	out, err := runGit("for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// listRemoteBranches returns remote-tracking branches, skipping */HEAD.
func listRemoteBranches() ([]string, error) {
	out, err := runGit("for-each-ref", "--format=%(refname:short)", "refs/remotes/")
	if err != nil {
		return nil, err
	}
	var remotes []string
	for _, b := range splitLines(out) {
		if strings.Contains(b, "/") && !strings.HasSuffix(b, "/HEAD") {
			remotes = append(remotes, b)
		}
	}
	return remotes, nil
}

func splitLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(bytes.TrimSpace(b)), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
```

### Assembling the full list

The `Enumerate` function ties it together, mirroring `gather_entries`:

```go
// inUse returns the set of branch names checked out in any worktree.
func inUse(entries []Entry) map[string]bool {
	m := make(map[string]bool)
	for _, e := range entries {
		if e.Branch != "" && e.Branch != "(detached)" {
			m[e.Branch] = true
		}
	}
	return m
}

// Enumerate returns all pickable targets in the current repo.
func Enumerate(cwdRoot string) ([]Entry, error) {
	entries, err := listWorktrees(cwdRoot)
	if err != nil {
		return nil, err
	}

	used := inUse(entries)
	// Prevent the default branch from appearing as a bare branch so users are
	// nudged toward feature branches.
	if db, _ := defaultBranch(); db != "" {
		used[db] = true
	}

	local, err := listLocalBranches()
	if err != nil {
		return nil, err
	}
	localSet := make(map[string]bool, len(local))
	for _, b := range local {
		localSet[b] = true
		if !used[b] {
			entries = append(entries, Entry{Type: TypeBranch, Branch: b})
		}
	}

	remotes, err := listRemoteBranches()
	if err != nil {
		return nil, err
	}
	for _, r := range remotes {
		short := r[strings.IndexByte(r, '/')+1:]
		if localSet[short] {
			continue // shadowed by a local branch
		}
		if !used[r] {
			entries = append(entries, Entry{Type: TypeBranch, Branch: r})
		}
	}
	return entries, nil
}

// defaultBranch returns the repo default branch (e.g. main) from origin/HEAD.
func defaultBranch() (string, error) {
	out, err := runGit("symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	return strings.TrimPrefix(s, "refs/remotes/origin/"), nil
}
```

## Run It

Add a temporary debug flag or test in `main`:

```go
root, _ := exec.Command("git", "rev-parse", "--show-toplevel").Output()
entries, _ := worktree.Enumerate(strings.TrimSpace(string(root)))
for _, e := range entries {
	fmt.Printf("%-9s %-30s %s\n", e.Type, e.Branch, e.Path)
}
```

```bash
go run ./cmd/wt
```

```
current    main                              /Users/keith/.../agent-worktree
branch     feature/my-feature
branch     origin/feature/x
```

## Try It Yourself

Write a unit test that runs `git init` in a temp dir (via `os.MkdirTemp` +
`exec.Command("git","init")`), creates a branch, and asserts `Enumerate`
contains a `branch` entry for it.

<details>
<summary>Solution</summary>

```go
func TestEnumerateFindsBranch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "--allow-empty", "-m", "init")
	run("checkout", "-b", "feature")

	entries, err := Enumerate(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Type == TypeBranch && e.Branch == "feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected feature branch, got %+v", entries)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 07: worktree & branch enumeration" && git tag lesson-07
```
