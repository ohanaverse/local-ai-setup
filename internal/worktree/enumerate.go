package worktree

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
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

// runGit runs a git command in dir (or the current directory if dir is empty).
func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Output()
}

// listWorktrees parses `git worktree list --porcelain` into entries.
func listWorktrees(dir, cwdRoot string) ([]Entry, error) {
	out, err := runGit(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}

	// Resolve cwdRoot for comparison since git returns resolved paths.
	resolvedCwd, _ := filepath.EvalSymlinks(cwdRoot)

	var entries []Entry
	var path, branch string
	isBare := false

	flush := func() {
		if path != "" && !isBare {
			t := TypeWorktree
			resolvedPath, _ := filepath.EvalSymlinks(path)
			if resolvedPath == resolvedCwd {
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

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// listLocalBranches returns short names of all local branches.
func listLocalBranches(dir string) ([]string, error) {
	out, err := runGit(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// listRemoteBranches returns remote-tracking branches, skipping */HEAD.
func listRemoteBranches(dir string) ([]string, error) {
	out, err := runGit(dir, "for-each-ref", "--format=%(refname:short)", "refs/remotes/")
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

// defaultBranch returns the repo default branch (e.g. main) from origin/HEAD.
func defaultBranch(dir string) (string, error) {
	out, err := runGit(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", nil //nolint:nilerr // No remote or origin/HEAD not set is non-fatal.
	}
	s := strings.TrimSpace(string(out))
	return strings.TrimPrefix(s, "refs/remotes/origin/"), nil
}

// Enumerate returns all pickable targets in the repo at dir.
// cwdRoot is the path of the current worktree; its entry is tagged TypeCurrent.
func Enumerate(dir, cwdRoot string) ([]Entry, error) {
	entries, err := listWorktrees(dir, cwdRoot)
	if err != nil {
		return nil, err
	}

	used := inUse(entries)
	// Prevent the default branch from appearing as a bare branch so users are
	// nudged toward feature branches.
	if db, _ := defaultBranch(dir); db != "" {
		used[db] = true
	}

	local, err := listLocalBranches(dir)
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

	remotes, err := listRemoteBranches(dir)
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
