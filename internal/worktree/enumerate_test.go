package worktree

import (
	"os/exec"
	"path/filepath"
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

	entries, err := Enumerate(dir, dir)
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
		t.Fatalf("expected feature branch entry, got %+v", entries)
	}
}

// The default branch (main/master) is always checked out in at least one
// worktree, so it must never appear as a bare TypeBranch. Showing it as a
// bare branch would encourage users to work directly on main, defeating the
// guard's purpose.
func TestEnumerateSkipsDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// main/master is checked out, so it shouldn't appear as a bare branch either.
	entries, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type == TypeBranch && (e.Branch == "main" || e.Branch == "master") {
			t.Fatalf("default branch should not appear as bare branch, got %+v", e)
		}
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

	entries, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	var foundWorktree, foundBranch bool
	for _, e := range entries {
		if e.Type == TypeWorktree && e.Branch == "feature" {
			foundWorktree = true
		}
		if e.Type == TypeBranch && e.Branch == "feature" {
			foundBranch = true
		}
	}
	if !foundWorktree {
		t.Fatalf("expected worktree entry for feature, got %+v", entries)
	}
	if foundBranch {
		t.Fatalf("feature should not appear as bare branch when checked out in worktree, got %+v", entries)
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

	entries, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.Branch == "origin/feature" {
			t.Fatalf("origin/feature should be shadowed by local feature, got %+v", entries)
		}
	}
}
