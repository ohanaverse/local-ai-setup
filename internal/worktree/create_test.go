package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// EnsureForName must create a worktree at .worktrees/<name> for a brand-new
// branch. The returned path must exist and the branch must be checked out
// there. A missing worktree means the git worktree add command is wrong.
func TestEnsureForNameCreatesWorktree(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	path, err := EnsureForName(dir, "my-feature")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "my-feature")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}
}

// EnsureForName must be idempotent: calling it twice for the same branch
// returns the same path without error. A second call that errors or returns
// a different path would break the -w flag on repeated invocations.
func TestEnsureForNameIdempotent(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	first, err := EnsureForName(dir, "my-feature")
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureForName(dir, "my-feature")
	if err != nil {
		t.Fatal(err)
	}
	// git returns resolved paths (macOS /var → /private/var), so compare
	// resolved paths rather than raw strings.
	resolvedFirst, _ := filepath.EvalSymlinks(first)
	resolvedSecond, _ := filepath.EvalSymlinks(second)
	if resolvedFirst != resolvedSecond {
		t.Fatalf("paths differ: %q vs %q", first, second)
	}
}

// EnsureForName must reject names that still contain path separators after
// extraction, preventing traversal like "..". Note: filepath.Base strips the
// separators, so the guard is defensive; in practice git itself rejects such
// branch names. Either way the user-facing behavior is that traversal is
// rejected.
func TestEnsureForNameRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	_, err := EnsureForName(dir, "../evil")
	if err == nil {
		t.Fatal("expected error for path traversal name")
	}
}

// EnsureForBranch must create a worktree for a brand-new branch, using the
// last path component as the directory name. This is the picker path for a
// branch that doesn't exist yet.
func TestEnsureForBranchNewBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	path, err := EnsureForBranch(dir, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "x")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// EnsureForBranch must create a local branch tracking a remote-tracking
// branch, using the short name as the worktree directory. This is the
// origin/feature path in the picker.
func TestEnsureForBranchRemote(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Set up a bare remote with a branch.
	remoteDir := filepath.Join(dir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Create a local branch and push it to origin as "feature", then delete
	// the local branch so origin/feature exists without a local feature.
	cmd = exec.Command("git", "checkout", "-b", "temp")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b temp: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "push", "origin", "temp:feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git push temp:feature: %v\n%s", err, out)
	}

	// Switch back to default and delete the local temp branch.
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
	cmd = exec.Command("git", "branch", "-D", "temp")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch -D temp: %v\n%s", err, out)
	}

	// Fetch so origin/feature is known.
	cmd = exec.Command("git", "fetch", "origin")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fetch: %v\n%s", err, out)
	}

	path, err := EnsureForBranch(dir, "origin/feature")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "feature")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// setupRemote creates a bare remote at dir/remote.git, adds it as "origin",
// and pushes a local branch to it as <remoteBranch>. The local branch is then
// deleted so only the remote-tracking ref remains. Returns the remote dir.
func setupRemote(t *testing.T, dir, localBranch, remoteBranch string) string {
	t.Helper()
	remoteDir := filepath.Join(dir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "checkout", "-b", localBranch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b %s: %v\n%s", localBranch, err, out)
	}
	cmd = exec.Command("git", "push", "origin", localBranch+":"+remoteBranch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git push %s:%s: %v\n%s", localBranch, remoteBranch, err, out)
	}

	// Switch back to default and delete the local branch.
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
	cmd = exec.Command("git", "branch", "-D", localBranch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch -D %s: %v\n%s", localBranch, err, out)
	}

	cmd = exec.Command("git", "fetch", "origin")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fetch: %v\n%s", err, out)
	}
	return remoteDir
}

// EnsureForName must create a worktree for a branch that already exists
// locally using `git worktree add <path> <branch>` (no -b). The branch must
// be checked out at the returned path.
func TestEnsureForNameExistingLocalBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "existing")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b existing: %v\n%s", err, out)
	}
	// Switch back to default so existing is not checked out here.
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

	path, err := EnsureForName(dir, "existing")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "existing")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	// The branch must be checked out in the new worktree.
	out, err := exec.Command("git", "-C", path, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "existing" {
		t.Fatalf("checked out branch = %q, want existing", strings.TrimSpace(string(out)))
	}
}

// EnsureForName must reuse the path of a branch that is already checked out
// in a worktree, rather than creating a second worktree. This is the
// idempotent reuse path (the first loop in EnsureForName).
func TestEnsureForNameReusesCheckedOutWorktree(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a worktree for a feature branch.
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}
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
	wtDir := filepath.Join(dir, ".worktrees", "feature")
	cmd = exec.Command("git", "worktree", "add", wtDir, "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	path, err := EnsureForName(dir, "feature")
	if err != nil {
		t.Fatal(err)
	}
	// git returns resolved paths, so compare resolved.
	resolvedPath, _ := filepath.EvalSymlinks(path)
	resolvedWt, _ := filepath.EvalSymlinks(wtDir)
	if resolvedPath != resolvedWt {
		t.Fatalf("path = %q, want existing worktree %q", path, wtDir)
	}
}

// EnsureForName must skip creation when a worktree already exists at
// .worktrees/<name> (the worktreeBranchAt idempotent path).
func TestEnsureForNameReusesExistingWorktreePath(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a worktree at .worktrees/feature directly.
	wtDir := filepath.Join(dir, ".worktrees", "feature")
	cmd := exec.Command("git", "worktree", "add", "-b", "feature", wtDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add -b: %v\n%s", err, out)
	}

	path, err := EnsureForName(dir, "feature")
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, _ := filepath.EvalSymlinks(path)
	resolvedWt, _ := filepath.EvalSymlinks(wtDir)
	if resolvedPath != resolvedWt {
		t.Fatalf("path = %q, want existing worktree %q", path, wtDir)
	}
}

// EnsureForName must reject an empty name.
func TestEnsureForNameEmptyName(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	_, err := EnsureForName(dir, "")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// EnsureForName must use the last path component of a slash-containing branch
// name as the worktree directory (.worktrees/my-branch for feature/my-branch).
func TestEnsureForNameSlashBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	path, err := EnsureForName(dir, "feature/my-branch")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "my-branch")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// EnsureForBranch must create a worktree for an existing local branch that
// contains slashes, using the last component as the directory name.
func TestEnsureForBranchLocalWithSlash(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature/x")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature/x: %v\n%s", err, out)
	}
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

	path, err := EnsureForBranch(dir, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "x")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// EnsureForBranch must create a local branch tracking a remote-tracking
// branch whose name contains slashes, using the last component as the
// worktree directory (origin/feature/x → .worktrees/x).
func TestEnsureForBranchRemoteWithSlash(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	setupRemote(t, dir, "temp", "feature/x")

	path, err := EnsureForBranch(dir, "origin/feature/x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "x")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// branchExists must report true for an existing local branch and false for a
// missing one. This drives the local-vs-new decision in EnsureForName and
// EnsureForBranch.
func TestBranchExists(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	if !branchExists(dir, "feature") {
		t.Error("branchExists(feature) = false, want true")
	}
	if branchExists(dir, "nope") {
		t.Error("branchExists(nope) = true, want false")
	}
}

// remoteExists must report true for an existing remote-tracking branch and
// false for a missing one. This drives the remote case in EnsureForBranch.
func TestRemoteExists(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	setupRemote(t, dir, "temp", "feature")

	if !remoteExists(dir, "origin/feature") {
		t.Error("remoteExists(origin/feature) = false, want true")
	}
	if remoteExists(dir, "origin/nope") {
		t.Error("remoteExists(origin/nope) = true, want false")
	}
}

// EnsureForBranch must return an error when a remote-tracking branch's short
// name already exists as a local branch. Creating a worktree would otherwise
// conflict with the existing local branch.
func TestRemoteConflictsWithLocal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Set up a bare remote with a branch.
	remoteDir := filepath.Join(dir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "remote", "add", "origin", remoteDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Create local branch "foo" and push it so origin/foo exists.
	cmd = exec.Command("git", "checkout", "-b", "foo")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b foo: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "push", "-u", "origin", "foo")
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

	cmd = exec.Command("git", "fetch", "origin")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fetch: %v\n%s", err, out)
	}

	_, err := EnsureForBranch(dir, "origin/foo")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got %v", err)
	}
}

// EnsureForName must error when the typed name's base path collides with an
// existing worktree for a DIFFERENT branch. Typing "feature/x" when
// .worktrees/x already holds branch "x" previously returned x's worktree
// silently, so the TUI's pendingHighlight found no match and the user landed
// on the wrong branch believing they created "feature/x".
func TestEnsureForNameCollisionErrors(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a worktree for branch "x" at .worktrees/x.
	wtDir := filepath.Join(dir, ".worktrees", "x")
	cmd := exec.Command("git", "worktree", "add", "-b", "x", wtDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add -b x: %v\n%s", err, out)
	}

	_, err := EnsureForName(dir, "feature/x")
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error = %q, want it to mention 'already in use'", err)
	}
}

// EnsureForName must still reuse a worktree whose path base matches a
// slash-containing branch (".worktrees/x" for branch "feature/x"). This is
// the legitimate idempotent-reuse case the collision guard must not break.
func TestEnsureForNameReusesSlashedBranchWorktree(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a worktree for branch "feature/x" at .worktrees/x.
	wtDir := filepath.Join(dir, ".worktrees", "x")
	cmd := exec.Command("git", "worktree", "add", "-b", "feature/x", wtDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add -b feature/x: %v\n%s", err, out)
	}

	path, err := EnsureForName(dir, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, _ := filepath.EvalSymlinks(path)
	resolvedWt, _ := filepath.EvalSymlinks(wtDir)
	if resolvedPath != resolvedWt {
		t.Fatalf("path = %q, want existing worktree %q", path, wtDir)
	}
}

// EnsureForName must reject "." and "..". filepath.Base leaves them intact
// and filepath.Join(".worktrees", "..") cleans to the repo root, so without
// this guard the call would return the repo root with no error and create
// nothing.
func TestEnsureForNameRejectsDot(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for _, name := range []string{".", ".."} {
		if _, err := EnsureForName(dir, name); err == nil {
			t.Errorf("EnsureForName(%q) = nil error, want non-nil", name)
		}
	}
}
