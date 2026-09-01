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

// listBranches returns local and remote-tracking branch short names in one
// git for-each-ref call. Remote entries skip the synthetic */HEAD ref.
func listBranches(dir string) (local, remote []string, err error) {
	// NUL-separated: git ref names can legally contain "|" (and most other
	// punctuation), but never NUL, so %00 is the only delimiter guaranteed
	// not to collide with a branch name.
	out, err := runGit(dir, "for-each-ref", "--format=%(refname:short)%00%(refname)", "refs/heads", "refs/remotes/")
	if err != nil {
		return nil, nil, err
	}
	for _, line := range splitLines(out) {
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		short, full := parts[0], parts[1]
		switch {
		case strings.HasPrefix(full, "refs/heads/"):
			local = append(local, short)
		case strings.HasPrefix(full, "refs/remotes/"):
			// The synthetic */HEAD symbolic ref shortens to the bare remote
			// name (e.g. "origin"), so detect it via the full refname.
			if !strings.HasSuffix(full, "/HEAD") {
				remote = append(remote, short)
			}
		}
	}
	return local, remote, nil
}

// IsRepo reports whether dir is inside a git repository. It uses
// `rev-parse --git-dir` (not --show-toplevel) so bare repositories and
// directories inside .git still count, matching the previous behavior of
// cmd/wt/helpers.go:inGitRepoAt. Returns false for any git error.
func IsRepo(dir string) bool {
	_, err := runGit(dir, "rev-parse", "--git-dir")
	return err == nil
}

// RepoRootAt returns the absolute path of the git repository root that owns
// dir. It reuses runGit so working-directory handling and error behavior
// are consistent with the rest of the package.
func RepoRootAt(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RepoRoot is shorthand for RepoRootAt(".") — kept for existing callers.
func RepoRoot() (string, error) {
	return RepoRootAt(".")
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

// IsDefaultBranchForm reports whether branch names the repo default branch
// in any of its selectable forms — the bare local name (e.g. "main") or a
// remote-tracking ref under any remote (e.g. "origin/main", "upstream/main").
// Matching by short name across all remotes, rather than hardcoding "origin/",
// covers fork workflows where the default branch is only reachable as
// <other-remote>/<default>.
//
// The match is by name alone: a local branch whose name happens to end in
// "/<db>" (e.g. "feature/main") also matches. Callers that can distinguish a
// local branch from a real remote-tracking ref must add that context to avoid
// falsely refusing such branches. The hard-refusal paths do exactly that:
// EnsureForBranch confirms the ref under refs/remotes/, and buildList checks
// the entry's group kind.
func IsDefaultBranchForm(branch, db string) bool {
	if db == "" {
		return false
	}
	return branch == db || strings.HasSuffix(branch, "/"+db)
}

// SkipInPicker reports whether a bare default-branch entry should be omitted
// from the worktree picker. The default branch must never be offered as a
// create-target for a linked worktree, so we skip bare local default branches
// and bare remote-tracking refs that name the default branch across any
// remote. A checked-out worktree on the default branch (Path != "") is kept so
// it can be marked (current) or (default).
func SkipInPicker(groupKind GroupKind, e Entry, defaultBranch string) bool {
	if defaultBranch == "" {
		return false
	}
	if e.Path != "" {
		return false
	}
	if e.Branch == defaultBranch {
		return true
	}
	return groupKind == GroupRemoteBranches && IsDefaultBranchForm(e.Branch, defaultBranch)
}

// Enumerate returns pickable targets grouped by kind: worktrees
// first, then local branches, then remote branches. Branches already
// checked out in a worktree are omitted from the bare-branch lists
// so the picker never shows duplicates.
func Enumerate(dir, cwdRoot string) ([]EntryGroup, error) {
	worktreeEntries, err := listWorktrees(dir, cwdRoot)
	if err != nil {
		return nil, err
	}
	used := inUse(worktreeEntries)

	local, remotes, err := listBranches(dir)
	if err != nil {
		return nil, err
	}
	localSet := make(map[string]bool, len(local))
	var localEntries []Entry
	for _, b := range local {
		localSet[b] = true
		if !used[b] {
			localEntries = append(localEntries, Entry{Type: TypeBranch, Branch: b})
		}
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
