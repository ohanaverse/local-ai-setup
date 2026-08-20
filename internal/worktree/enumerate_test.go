package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit creates a minimal git repo with one commit on main/master so
// subsequent git commands work. Used by every test that needs a repo.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}

// listWorktrees must return the current worktree with TypeCurrent and the
// correct branch (main or master). The path comparison must handle macOS
// /var → /private/var symlinks so tests are portable.
func TestListWorktrees(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	entries, err := listWorktrees(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 worktree, got %+v", entries)
	}
	if entries[0].Type != TypeCurrent {
		t.Errorf("type = %q, want current", entries[0].Type)
	}
	if entries[0].Branch != "main" && entries[0].Branch != "master" {
		t.Errorf("branch = %q, want main or master", entries[0].Branch)
	}
	if entries[0].Path != dir {
		// macOS may symlink /var to /private/var; git returns the resolved path.
		resolvedDir, _ := filepath.EvalSymlinks(dir)
		resolvedPath, _ := filepath.EvalSymlinks(entries[0].Path)
		if resolvedPath != resolvedDir {
			t.Errorf("path = %q, want %q", entries[0].Path, dir)
		}
	}
}

// listLocalBranches must return every branch in refs/heads, including
// branches created after init. A missing branch means the for-each-ref
// parsing or the git command is broken.
func TestListLocalBranches(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	branches, err := listLocalBranches(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range branches {
		if b == "feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected feature branch, got %v", branches)
	}
}

// Enumerate must list bare branches that are not checked out in any
// worktree. A branch that was created and then switched away from should
// appear as TypeBranch so the user can select it in the TUI.
func TestEnumerateFindsBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	// Switch back to main/master so feature is not in use.
	defaultBranch := "main"
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		defaultBranch = "master"
		cmd = exec.Command("git", "checkout", defaultBranch)
		cmd.Dir = dir
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout main/master: %v", err)
		}
	}

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Type == TypeBranch && e.Branch == "feature" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected feature branch entry, got %+v", groups)
	}
}

// TestEnumerateAlwaysEmitsDefaultBranchAsBare is the contract lock for PR
// #56's "always-full view": Enumerate must emit the default branch as a bare
// TypeBranch row even when that branch is already checked out in the current
// worktree, so the picker is never empty of alternatives just because the
// user is on main. This replaces the old TestEnumerateSkipsDefaultBranch
// invariant (that the default branch never appears as a bare branch). That
// invariant only ever held when origin/HEAD was unset, so the old test gave
// false confidence: in any real repo with origin/HEAD set, Enumerate emits
// the bare row — the opposite of what the old test asserted.
func TestEnumerateAlwaysEmitsDefaultBranchAsBare(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Detect the actual default branch (main or master, depending on the
	// git version's init.defaultBranch) instead of assuming "main".
	out, err := runGitCmd(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		t.Fatalf("detect default branch: %v\n%s", err, out)
	}
	defaultBranch := strings.TrimSpace(string(out))

	// Point origin/HEAD at the default branch so DefaultBranch resolves.
	// DefaultBranch trims the "refs/remotes/origin/" prefix, so the symref
	// must target a remote-tracking ref (refs/remotes/origin/<branch>), not
	// refs/heads/<branch>. Create that tracking ref via update-ref (no real
	// remote/fetch needed), then set origin/HEAD — mirroring setupTestRepo's
	// push + symbolic-ref pattern.
	if out, err := runGitCmd(dir, "update-ref", "refs/remotes/origin/"+defaultBranch, "refs/heads/"+defaultBranch); err != nil {
		t.Fatalf("git update-ref origin/%s: %v\n%s", defaultBranch, err, out)
	}
	if out, err := runGitCmd(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+defaultBranch); err != nil {
		t.Fatalf("git symbolic-ref origin/HEAD: %v\n%s", err, out)
	}

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	// The default branch is checked out in the current worktree, yet it must
	// ALSO appear as a bare TypeBranch row (the always-full view contract).
	foundCurrent := false
	foundBare := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Branch != defaultBranch {
				continue
			}
			if e.Type == TypeCurrent {
				foundCurrent = true
			}
			if e.Type == TypeBranch {
				foundBare = true
			}
		}
	}
	if !foundCurrent {
		t.Fatalf("expected current worktree on %q, got %+v", defaultBranch, groups)
	}
	if !foundBare {
		t.Fatalf("expected bare TypeBranch row for default branch %q (always-full view), got %+v", defaultBranch, groups)
	}
}

// A branch checked out in a worktree must appear as TypeWorktree, not as a
// bare TypeBranch. Duplicating it would clutter the picker and could lead
// to the user trying to create a second worktree for the same branch.
func TestEnumerateWorktreeAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a worktree for a feature branch.
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	// Switch back to default before adding worktree (can't add a branch
	// that's already checked out in the current worktree).
	defaultBranch := "main"
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		defaultBranch = "master"
		cmd = exec.Command("git", "checkout", defaultBranch)
		cmd.Dir = dir
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout main/master: %v", err)
		}
	}

	wtDir := filepath.Join(dir, ".worktrees", "feature")
	cmd = exec.Command("git", "worktree", "add", wtDir, "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	// Switch back to default so feature is checked out in the worktree.
	defaultBranch = "main"
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		defaultBranch = "master"
		cmd = exec.Command("git", "checkout", defaultBranch)
		cmd.Dir = dir
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout main/master: %v", err)
		}
	}

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	var foundWorktree, foundBranch bool
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Type == TypeWorktree && e.Branch == "feature" {
				foundWorktree = true
			}
			if e.Type == TypeBranch && e.Branch == "feature" {
				foundBranch = true
			}
		}
	}
	if !foundWorktree {
		t.Fatalf("expected worktree entry for feature, got %+v", groups)
	}
	if foundBranch {
		t.Fatalf("feature should not appear as bare branch when checked out in worktree, got %+v", groups)
	}
}

// RepoRoot must report the root of the repo containing the current working
// directory. The TUI relies on this to know which repo to enumerate without
// being passed an explicit directory.
func TestRepoRoot(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Change into a subdirectory and verify RepoRoot still returns dir.
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = sub
	want, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	wantStr := strings.TrimSpace(string(want))

	// RepoRoot uses the current working directory, so run from sub.
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}

	got, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != wantStr {
		t.Errorf("RepoRoot = %q, want %q", got, wantStr)
	}
}

// RepoRoot must fail when not inside a git repo so the TUI can report the
// error instead of showing an empty list.
func TestRepoRootNotARepo(t *testing.T) {
	dir := t.TempDir()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := RepoRoot(); err == nil {
		t.Fatal("expected error outside repo, got nil")
	}
}

// Remote branches whose short name matches a local branch must be excluded.
// This prevents duplicate entries like "feature" and "origin/feature" when
// both resolve to the same worktree target. The local branch takes priority.
func TestEnumerateRemoteBranchShadowedByLocal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Set up a fake remote with a branch that also exists locally.
	remoteDir := filepath.Join(dir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Create local branch "feature" and push it.
	cmd = exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "push", "-u", "origin", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git push: %v\n%s", err, out)
	}

	// Switch back to default.
	defaultBranch := "main"
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		defaultBranch = "master"
		cmd = exec.Command("git", "checkout", defaultBranch)
		cmd.Dir = dir
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout: %v", err)
		}
	}

	// Fetch so origin/feature is known.
	cmd = exec.Command("git", "fetch", "origin")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fetch: %v\n%s", err, out)
	}

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Branch == "origin/feature" {
				t.Fatalf("origin/feature should be shadowed by local feature, got %+v", groups)
			}
		}
	}
}

// setupTestRepo creates a minimal git repo with a default branch (main)
// and a couple of local feature branches, so Enumerate has something to
// group. The repo points at a fake "origin" so origin/HEAD resolves and
// DefaultBranch() finds the default branch.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	// Set up a bare remote so origin/HEAD can resolve and DefaultBranch returns "main".
	remoteDir := filepath.Join(dir, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remoteDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare remote: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "remote", "add", "origin", remoteDir); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "push", "-u", "origin", "main"); err != nil {
		t.Fatalf("git push -u origin main: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("git symbolic-ref origin/HEAD: %v\n%s", err, out)
	}

	// Create a couple of local feature branches so the local group has more than one entry.
	for _, name := range []string{"feature-a", "feature-b"} {
		if out, err := runGitCmd(dir, "checkout", "-b", name); err != nil {
			t.Fatalf("git checkout -b %s: %v\n%s", name, err, out)
		}
	}
	// Switch back to main so the feature branches are bare.
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	return dir
}

// runGitCmd is a tiny wrapper around exec.Command("git", ...) that
// pins cmd.Dir to dir. Without this, every git invocation defaults to
// the test binary's working directory — which is the repo root when
// `go test` is run from a worktree, and any subsequent `git remote add
// origin` will collide with the parent repo's existing origin. The
// first setupTestRepo loop did set cmd.Dir directly, but the later
// commands omitted it and silently ran in the wrong repo, which only
// surfaced when the test ran inside the worktree where `origin` is
// already configured.
func runGitCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// setupTestRepoWithManyBranches mirrors setupTestRepo but seeds enough
// local and remote branches to verify alphabetical ordering within each
// group: main, alpha, beta (local) plus origin/beta, origin/gamma,
// origin/zzz (remote-only).
func setupTestRepoWithManyBranches(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	// Set up the bare remote and point origin/HEAD at main so the default
	// branch resolves to "main" (matching the brief's expectation).
	remoteDir := filepath.Join(dir, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remoteDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare remote: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "remote", "add", "origin", remoteDir); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "push", "-u", "origin", "main"); err != nil {
		t.Fatalf("git push -u origin main: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("git symbolic-ref origin/HEAD: %v\n%s", err, out)
	}

	// Create enough local branches (out of alphabetical order) to exercise the sort.
	for _, name := range []string{"gamma", "alpha", "beta"} {
		if out, err := runGitCmd(dir, "checkout", "-b", name); err != nil {
			t.Fatalf("git checkout -b %s: %v\n%s", name, err, out)
		}
	}
	// Switch back to main so the other branches are bare.
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	// Make beta and gamma remote-only: push them, then delete the local copies.
	if out, err := runGitCmd(dir, "push", "-u", "origin", "beta"); err != nil {
		t.Fatalf("git push origin beta: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "branch", "-D", "beta"); err != nil {
		t.Fatalf("git branch -D beta: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "push", "-u", "origin", "gamma"); err != nil {
		t.Fatalf("git push origin gamma: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "branch", "-D", "gamma"); err != nil {
		t.Fatalf("git branch -D gamma: %v\n%s", err, out)
	}
	// Push zzz to origin and drop the local copy so it appears only in the remote group.
	if out, err := runGitCmd(dir, "checkout", "-b", "zzz"); err != nil {
		t.Fatalf("git checkout -b zzz: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "push", "-u", "origin", "zzz"); err != nil {
		t.Fatalf("git push origin zzz: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "branch", "-D", "zzz"); err != nil {
		t.Fatalf("git branch -D zzz: %v\n%s", err, out)
	}
	// Bring remote-tracking refs into the local repo so listRemoteBranches sees them.
	if out, err := runGitCmd(dir, "fetch", "origin"); err != nil {
		t.Fatalf("git fetch origin: %v\n%s", err, out)
	}

	return dir
}

// TestEnumerateReturnsGroups verifies the new three-group return
// shape. The picker relies on this ordering to render rows without
// re-sorting.
func TestEnumerateReturnsGroups(t *testing.T) {
	dir := setupTestRepo(t)
	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3 (worktrees, local branches, remote branches)", len(groups))
	}
	if groups[0].Kind != GroupWorktrees {
		t.Errorf("group[0] = %v, want GroupWorktrees", groups[0].Kind)
	}
	if groups[1].Kind != GroupLocalBranches {
		t.Errorf("group[1] = %v, want GroupLocalBranches", groups[1].Kind)
	}
	if groups[2].Kind != GroupRemoteBranches {
		t.Errorf("group[2] = %v, want GroupRemoteBranches", groups[2].Kind)
	}
}

// TestEnumerateAlwaysIncludesDefaultBranch verifies the picker-content
// invariant: the default branch is always listed as a branch row, even
// when checked out in a worktree or when it's also the current branch.
func TestEnumerateAlwaysIncludesDefaultBranch(t *testing.T) {
	dir := setupTestRepo(t)
	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	var foundAsBranch, foundAsWorktree bool
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Branch == "main" {
				switch g.Kind {
				case GroupLocalBranches:
					foundAsBranch = true
				case GroupWorktrees:
					foundAsWorktree = true
				}
			}
		}
	}
	if !foundAsBranch {
		t.Error("default branch 'main' missing from local branches group")
	}
	_ = foundAsWorktree // may or may not be true depending on test repo layout
}

// TestEnumerateOrdering verifies that within each group, entries are
// sorted alphabetically by branch name (with remote prefix stripped
// for the remote group).
func TestEnumerateOrdering(t *testing.T) {
	dir := setupTestRepoWithManyBranches(t)
	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		for i := 1; i < len(g.Entries); i++ {
			if g.Entries[i-1].Branch > g.Entries[i].Branch {
				t.Errorf("group %v not sorted: %q > %q", g.Kind, g.Entries[i-1].Branch, g.Entries[i].Branch)
				break
			}
		}
	}
}
