package worktree

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
	"sort"
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

// GroupKind identifies a logical group of picker rows.
type GroupKind int

const (
	GroupWorktrees GroupKind = iota
	GroupLocalBranches
	GroupRemoteBranches
)

// EntryGroup is one ordered slice of entries in the picker. The picker
// renders groups in order (worktrees, locals, remotes) with a
// separator between locals and remotes.
type EntryGroup struct {
	Kind    GroupKind
	Entries []Entry
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

// RepoRoot returns the absolute path of the current git repository root.
func RepoRoot() (string, error) {
	out, err := runGit("", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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

// DefaultBranch returns the repo default branch (e.g. main) from origin/HEAD.
func DefaultBranch(dir string) (string, error) {
	out, err := runGit(dir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", nil //nolint:nilerr // No remote or origin/HEAD not set is non-fatal.
	}
	s := strings.TrimSpace(string(out))
	return strings.TrimPrefix(s, "refs/remotes/origin/"), nil
}

// Enumerate returns pickable targets grouped by kind: worktrees
// first, then local branches, then remote branches. The default
// branch is always emitted as a TypeBranch row (even when also
// checked out in a worktree) so the picker is never empty for that
// reason alone.
func Enumerate(dir, cwdRoot string) ([]EntryGroup, error) {
	worktreeEntries, err := listWorktrees(dir, cwdRoot)
	if err != nil {
		return nil, err
	}
	used := inUse(worktreeEntries)
	db, _ := DefaultBranch(dir)

	local, err := listLocalBranches(dir)
	if err != nil {
		return nil, err
	}
	localSet := make(map[string]bool, len(local))
	var localEntries []Entry
	for _, b := range local {
		localSet[b] = true
		// Always emit the default branch as a TypeBranch row.
		if b == db || !used[b] {
			localEntries = append(localEntries, Entry{Type: TypeBranch, Branch: b})
		}
	}

	remotes, err := listRemoteBranches(dir)
	if err != nil {
		return nil, err
	}
	var remoteEntries []Entry
	for _, r := range remotes {
		short := r[strings.IndexByte(r, '/')+1:]
		if localSet[short] {
			continue
		}
		if !used[r] {
			remoteEntries = append(remoteEntries, Entry{Type: TypeBranch, Branch: r})
		}
	}

	// Sort each group by branch name (with remote prefix stripped for remotes).
	sortByBranch := func(es []Entry, stripRemote bool) {
		sort.Slice(es, func(i, j int) bool {
			a, b := es[i].Branch, es[j].Branch
			if stripRemote {
				if i := strings.IndexByte(a, '/'); i >= 0 {
					a = a[i+1:]
				}
				if i := strings.IndexByte(b, '/'); i >= 0 {
					b = b[i+1:]
				}
			}
			return a < b
		})
	}
	sortByBranch(worktreeEntries, false)
	sortByBranch(localEntries, false)
	sortByBranch(remoteEntries, true)

	return []EntryGroup{
		{Kind: GroupWorktrees, Entries: worktreeEntries},
		{Kind: GroupLocalBranches, Entries: localEntries},
		{Kind: GroupRemoteBranches, Entries: remoteEntries},
	}, nil
}
