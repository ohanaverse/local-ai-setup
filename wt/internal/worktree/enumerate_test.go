package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitInit creates a minimal git repo with one commit on main/master so
// subsequent git commands work. Used by every test that needs a repo.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-b", "main"},
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

// listBranches must return every local branch in refs/heads, including
// branches created after init. A missing branch means the for-each-ref
// parsing or the git command is broken.
func TestListBranches(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	local, _, _, err := listBranches(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(local, "feature") {
		t.Fatalf("expected feature branch, got %v", local)
	}
}

// listBranches must not drop a branch whose name contains "|". A "|"-joined
// for-each-ref format collides with such names (git ref names may legally
// contain "|"), silently vanishing the branch from both local and remote
// lists; the NUL-separated format must survive this without special-casing.
func TestListBranchesPipeInName(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "weird|name")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b weird|name: %v\n%s", err, out)
	}

	local, _, _, err := listBranches(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(local, "weird|name") {
		t.Fatalf("expected weird|name branch, got %v", local)
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
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
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

// TestEnumerateListsACheckedOutDefaultBranchToo pins the end of the picker's
// `!used[b]` fold: the default branch checked out in the root is listed as
// the current worktree and as a local branch both. Enumerate shows what git
// has; it no longer hides a branch because a worktree holds it (the list's
// one-row-per-branch shape now comes from internal/cleanup).
func TestEnumerateListsACheckedOutDefaultBranchToo(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	foundCurrent := false
	foundBare := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Branch != "main" {
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
	if !foundCurrent || !foundBare {
		t.Fatalf("current %v, bare %v: want main listed as both, got %+v", foundCurrent, foundBare, groups)
	}
}

// TestEnumerateIncludesDefaultBranchWhenNotCheckedOut asserts that when
// the default branch exists locally but is not checked out in any worktree,
// it appears as a bare TypeBranch row so the user can select it.
func TestEnumerateIncludesDefaultBranchWhenNotCheckedOut(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a feature branch and switch to it so main is not checked out.
	if out, err := runGitCmd(dir, "checkout", "-b", "feature"); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	// Set up origin/HEAD so DefaultBranch resolves to "main".
	if out, err := runGitCmd(dir, "update-ref", "refs/remotes/origin/main", "refs/heads/main"); err != nil {
		t.Fatalf("git update-ref origin/main: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("git symbolic-ref origin/HEAD: %v\n%s", err, out)
	}

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	foundBare := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if e.Type == TypeBranch && e.Branch == "main" {
				foundBare = true
			}
		}
	}
	if !foundBare {
		t.Fatalf("expected bare TypeBranch row for main when not checked out, got %+v", groups)
	}
}

// TestEnumerateWorktreeAndBranch pins that a branch checked out in a linked
// worktree is listed as that worktree and, since the `!used[b]` fold is gone,
// as a local branch too: Enumerate lists every ref git has.
func TestEnumerateWorktreeAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	if out, err := runGitCmd(dir, "branch", "feature"); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}
	wtDir := filepath.Join(dir, ".worktrees", "feature")
	if out, err := runGitCmd(dir, "worktree", "add", wtDir, "feature"); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
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
	if !foundWorktree || !foundBranch {
		t.Fatalf("worktree %v, branch %v: want feature listed as both, got %+v", foundWorktree, foundBranch, groups)
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
	t.Chdir(sub)

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
	t.Chdir(dir)

	if _, err := RepoRoot(); err == nil {
		t.Fatal("expected error outside repo, got nil")
	}
}

// IsRepo must report true for a repo root and false for an arbitrary
// directory. This replaces the cmd/wt-level inGitRepoAt test and keeps the
// contract in the package that actually owns git interaction.
func TestIsRepo(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	if !IsRepo(repo) {
		t.Errorf("IsRepo(repo root) = false, want true")
	}
	if IsRepo(t.TempDir()) {
		t.Errorf("IsRepo(non-repo) = true, want false")
	}

	// Bare repos and directories inside .git are still "inside a git repo"
	// (`git rev-parse --git-dir` succeeds there). IsRepo must not regress to
	// --show-toplevel semantics, which fail in both — the pre-refactor
	// inGitRepoAt routed these to the TUI, and losing that would silently
	// launch agents in the current directory instead of showing the picker.
	bare := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if !IsRepo(bare) {
		t.Errorf("IsRepo(bare repo) = false, want true")
	}
	if !IsRepo(filepath.Join(repo, ".git")) {
		t.Errorf("IsRepo(.git dir) = false, want true")
	}
}

// RepoRootAt must return the repo root for a subdirectory, matching
// RepoRoot when run from inside the repo.
func TestRepoRootAt(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	sub := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := RepoRootAt(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotResolved != want {
		t.Errorf("RepoRootAt(subdir) = %q, want %q", got, want)
	}
}

// TestEnumerateListsARemoteBranchBesideItsLocal pins the end of the
// `localSet` fold: origin/feature is listed even though a local feature
// exists. Enumerate shows every ref; folding a remote into its local row is
// internal/cleanup's job (Row.Upstream).
func TestEnumerateListsARemoteBranchBesideItsLocal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	remoteDir := filepath.Join(dir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", remoteDir},
		{"checkout", "-b", "feature"},
		{"push", "-u", "origin", "feature"},
		{"checkout", "main"},
	} {
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range groups {
		for _, e := range g.Entries {
			found = found || e.Branch == "origin/feature"
		}
	}
	if !found {
		t.Fatalf("origin/feature missing beside local feature: %+v", groups)
	}
}

// TestFetchRemotesFetchesNewRemoteBranches pins FetchRemotes, the one home
// of the picker's fetch: a branch a teammate pushed since the last manual
// fetch is a remote-tracking ref after it. Enumerate itself no longer
// fetches (the picker calls FetchRemotes on open; Part 3 moves that to a
// refresh key), so the branch is absent before the call.
func TestFetchRemotesFetchesNewRemoteBranches(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	remoteDir := filepath.Join(dir, "remote.git")
	// -b main pins the bare repo's HEAD whatever init.defaultBranch says, so
	// the clone below checks out main rather than an unborn HEAD.
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", remoteDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare remote: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "remote", "add", "origin", remoteDir); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "push", "-u", "origin", "main"); err != nil {
		t.Fatalf("git push -u origin main: %v\n%s", err, out)
	}

	cloneDir := t.TempDir()
	if out, err := exec.Command("git", "clone", remoteDir, cloneDir).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	if out, err := runGitCmd(cloneDir, "checkout", "-b", "teammate-feature"); err != nil {
		t.Fatalf("git checkout -b teammate-feature: %v\n%s", err, out)
	}
	if out, err := runGitCmd(cloneDir, "push", "origin", "teammate-feature"); err != nil {
		t.Fatalf("git push teammate-feature: %v\n%s", err, out)
	}

	has := func() bool {
		groups, err := Enumerate(dir, dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			for _, e := range g.Entries {
				if e.Branch == "origin/teammate-feature" {
					return true
				}
			}
		}
		return false
	}
	if has() {
		t.Fatal("Enumerate fetched: origin/teammate-feature listed before FetchRemotes")
	}
	if err := FetchRemotes(dir); err != nil {
		t.Fatalf("FetchRemotes: %v", err)
	}
	if !has() {
		t.Fatal("origin/teammate-feature missing after FetchRemotes")
	}
}

// TestFetchRemotesPrunesDeletedRemoteBranches pins --prune: a
// remote-tracking ref for a branch deleted upstream is gone after
// FetchRemotes, instead of lingering as a stale row.
func TestFetchRemotesPrunesDeletedRemoteBranches(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	remoteDir := filepath.Join(dir, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remoteDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare remote: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", remoteDir},
		{"push", "-u", "origin", "main"},
		{"checkout", "-b", "gone-soon"},
		{"push", "origin", "gone-soon"},
		{"checkout", "main"},
		{"fetch", "origin"},
		{"branch", "-d", "gone-soon"},
	} {
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "-C", remoteDir, "branch", "-D", "gone-soon").CombinedOutput(); err != nil {
		t.Fatalf("git -C remote branch -D gone-soon: %v\n%s", err, out)
	}

	if err := FetchRemotes(dir); err != nil {
		t.Fatalf("FetchRemotes: %v", err)
	}
	if out, _ := runGitCmd(dir, "for-each-ref", "refs/remotes/origin/gone-soon"); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("origin/gone-soon should have been pruned: %s", out)
	}
}

// TestFetchRemotesReportsFailureInOneLine pins FetchRemotes' error: a dead
// remote is an error the caller may ignore (the picker does, silently), its
// text one line, and Enumerate is unaffected by it.
func TestFetchRemotesReportsFailureInOneLine(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	if out, err := runGitCmd(dir, "remote", "add", "origin", "/nonexistent/does-not-exist.git"); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	err := FetchRemotes(dir)
	if err == nil {
		t.Fatal("FetchRemotes with a dead remote returned nil")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error is not one line: %q", err)
	}
	groups, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}
}

// setupTestRepo creates a minimal git repo with a default branch (main)
// and a couple of local feature branches, so Enumerate has something to
// group. The repo points at a fake "origin" so origin/HEAD resolves and
// DefaultBranch() finds the default branch.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitInit(t, dir)

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
// origin` will collide with the parent repo's existing origin. gitInit
// sets cmd.Dir on its own commands, but setupTestRepo's later commands
// once omitted it and silently ran in the wrong repo, which only
// surfaced when the test ran inside the worktree where `origin` is
// already configured.
func runGitCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// gitRun is runGitCmd that fails the test on an error and returns the
// trimmed output.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGitCmd(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupTestRepoWithManyBranches mirrors setupTestRepo but seeds enough
// local and remote branches to verify alphabetical ordering within each
// group: main, alpha, beta (local) plus origin/beta, origin/gamma,
// origin/zzz (remote-only).
func setupTestRepoWithManyBranches(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitInit(t, dir)

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
	// Bring remote-tracking refs into the local repo so listBranches sees them.
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

// TestEnumerateAlwaysIncludesDefaultBranch pins that the default branch is
// always listed: checked out in the root it appears as the worktree and, with
// the folds gone, as a local branch too.
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
	if !foundAsBranch || !foundAsWorktree {
		t.Errorf("main as branch %v, as worktree %v: want both", foundAsBranch, foundAsWorktree)
	}
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

// TestRootOfPrimaryYes checks the primary checkout is the root, since that is
// the one directory whose launches wt records and warns about.
func TestRootOfPrimaryYes(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	want, _ := filepath.EvalSymlinks(dir)
	root, ok := RootOf(dir)
	if !ok || root != want {
		t.Fatalf("RootOf(%s) = %q, %v; want %q, true", dir, root, ok, want)
	}
}

// TestRootOfLinkedWorktreeNo checks a linked worktree is not the root, so a
// launch there never shows the shared-root warning.
func TestRootOfLinkedWorktreeNo(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	linked := filepath.Join(dir, ".worktrees", "feat")
	if out, err := runGitCmd(dir, "worktree", "add", "-b", "feat", linked); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if root, ok := RootOf(linked); ok {
		t.Fatalf("RootOf(linked) = %q, true; want false", root)
	}
	if root, ok := RootOf(filepath.Join(dir, ".worktrees")); ok {
		t.Fatalf("RootOf(subdirectory) = %q, true; want false", root)
	}
}

// TestRootOfBareNo checks a bare repository and a plain directory have no
// root, so the check is skipped rather than failing the launch.
func TestRootOfBareNo(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "r.git")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if root, ok := RootOf(bare); ok {
		t.Fatalf("RootOf(bare) = %q, true; want false", root)
	}
	if root, ok := RootOf(t.TempDir()); ok {
		t.Fatalf("RootOf(non-repo) = %q, true; want false", root)
	}
}

// TestRootOfSymlinkedRootYes checks a root reached through a symlink is still
// the root and is reported by its physical path, so two launches through
// different spellings of the path see each other's claims.
func TestRootOfSymlinkedRootYes(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	root, ok := RootOf(link)
	if !ok || root != want {
		t.Fatalf("RootOf(%s) = %q, %v; want %q, true", link, root, ok, want)
	}
}

// TestRootOfMiscasedPathYes checks a root reached by a path that differs only
// in letter case (the same directory on a case-insensitive filesystem) is
// still the root, because the root gate must not be skipped by typing the
// path in another case. It skips where the filesystem is case-sensitive.
func TestRootOfMiscasedPathYes(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	resolved, _ := filepath.EvalSymlinks(dir)
	miscased := filepath.Join(filepath.Dir(resolved), strings.ToUpper(filepath.Base(resolved)))
	if _, err := os.Stat(miscased); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	root, ok := RootOf(miscased)
	if !ok || root != resolved {
		t.Fatalf("RootOf(%s) = %q, %v; want %q, true", miscased, root, ok, resolved)
	}
}

// TestPrimaryOfFromRootLinkedAndSubdir checks PrimaryOf finds the primary
// checkout from the root, a linked worktree and a subdirectory, because
// `wt --root` must launch in the root from wherever it is run.
func TestPrimaryOfFromRootLinkedAndSubdir(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	want, _ := filepath.EvalSymlinks(dir)
	linked := filepath.Join(dir, ".worktrees", "feat")
	if out, err := runGitCmd(dir, "worktree", "add", "-b", "feat", linked); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, from := range map[string]string{"root": dir, "linked": linked, "subdir": sub} {
		got, err := PrimaryOf(from)
		if err != nil || got != want {
			t.Errorf("PrimaryOf(%s) = %q, %v; want %q", name, got, err, want)
		}
	}
}

// TestPrimaryOfBareAndOutside checks PrimaryOf errors for a bare repository
// and a directory outside any repository, because neither has a checkout a
// launch could land in.
func TestPrimaryOfBareAndOutside(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "r.git")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if got, err := PrimaryOf(bare); err == nil {
		t.Errorf("PrimaryOf(bare) = %q, nil; want error", got)
	}
	if got, err := PrimaryOf(t.TempDir()); err == nil {
		t.Errorf("PrimaryOf(non-repo) = %q, nil; want error", got)
	}
}

// TestListWorktreesMarksOnlyThePrimary checks exactly the primary checkout's
// entry carries Root, and none does in a bare repository, because the picker
// drops the Root row and must not drop a linked worktree.
func TestListWorktreesMarksOnlyThePrimary(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	linked := filepath.Join(dir, ".worktrees", "feat")
	if out, err := runGitCmd(dir, "worktree", "add", "-b", "feat", linked); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	roots := 0
	for _, e := range entries {
		if !e.Root {
			continue
		}
		roots++
		if got, _ := filepath.EvalSymlinks(e.Path); got != want {
			t.Errorf("Root entry path = %q; want %q", e.Path, want)
		}
	}
	if len(entries) != 2 || roots != 1 {
		t.Fatalf("entries = %+v; want 2 entries with exactly 1 Root", entries)
	}

	bare := filepath.Join(t.TempDir(), "r.git")
	if out, err := exec.Command("git", "clone", "--bare", dir, bare).CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	bl := filepath.Join(t.TempDir(), "bl")
	if out, err := runGitCmd(bare, "worktree", "add", bl, "main"); err != nil {
		t.Fatalf("bare worktree add: %v\n%s", err, out)
	}
	entries, err = listWorktrees(bare, bare)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("bare entries = %+v; want 1", entries)
	}
	for _, e := range entries {
		if e.Root {
			t.Errorf("bare repo entry %+v marked Root", e)
		}
	}
}
