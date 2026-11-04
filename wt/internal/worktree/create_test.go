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

// EnsureForName must reject traversal-shaped names like "../evil". The
// derived directory flattens separators ("..-evil", itself a harmless single
// component) and git rejects the branch name itself, so the call errors
// either way — the user-facing behavior is that traversal is rejected.
func TestEnsureForNameRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	_, err := EnsureForName(dir, "../evil")
	if err == nil {
		t.Fatal("expected error for path traversal name")
	}
}

// EnsureForBranch must create a worktree for a brand-new branch in a
// directory derived from the full name, flattened. This is the picker path
// for a branch that doesn't exist yet.
func TestEnsureForBranchNewBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	path, err := EnsureForBranch(dir, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "feature-x")
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
	setupRemote(t, dir, "temp", "feature")

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
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
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
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
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
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
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

// EnsureForName must derive the worktree directory from the FULL branch
// name with path-hostile characters flattened
// (.worktrees/feature-my-branch for feature/my-branch): two branches sharing
// a last component need distinct directories, which a last-component rule
// cannot give them.
func TestEnsureForNameSlashBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	path, err := EnsureForName(dir, "feature/my-branch")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "feature-my-branch")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// EnsureForBranch must create a worktree for an existing local branch that
// contains slashes in a directory derived from the full name, flattened —
// the same rule as every other creation path.
func TestEnsureForBranchLocalWithSlash(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature/x")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature/x: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
	}

	path, err := EnsureForBranch(dir, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "feature-x")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// EnsureForBranch must create a local branch tracking a remote-tracking
// branch whose name contains slashes in a directory derived from the full
// LOCAL name (origin/feature/x → local branch feature/x → .worktrees/feature-x).
func TestEnsureForBranchRemoteWithSlash(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	setupRemote(t, dir, "temp", "feature/x")

	path, err := EnsureForBranch(dir, "origin/feature/x")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "feature-x")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// refExists must report true for an existing local branch and false for a
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

	if !refExists(dir, "refs/heads/feature") {
		t.Error("refExists(refs/heads/feature) = false, want true")
	}
	if refExists(dir, "refs/heads/nope") {
		t.Error("refExists(refs/heads/nope) = true, want false")
	}
}

// refExists must report true for an existing remote-tracking branch and
// false for a missing one. This drives the remote case in EnsureForBranch.
func TestRemoteExists(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	setupRemote(t, dir, "temp", "feature")

	if !refExists(dir, "refs/remotes/origin/feature") {
		t.Error("refExists(refs/remotes/origin/feature) = false, want true")
	}
	if refExists(dir, "refs/remotes/origin/nope") {
		t.Error("refExists(refs/remotes/origin/nope) = true, want false")
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
	if out, err := runGitCmd(dir, "checkout", "main"); err != nil {
		t.Fatalf("git checkout main: %v\n%s", err, out)
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

// EnsureForName must error when the derived worktree path collides with an
// existing worktree for a DIFFERENT branch. Full-name derivation flattens
// slashes, so "feature/x" and "feature-x" share .worktrees/feature-x: typing
// "feature/x" while feature-x's worktree sits there must error rather than
// silently return the wrong worktree, so the TUI's pendingHighlight finds no
// stale match and the user cannot land on the wrong branch believing they
// created "feature/x".
func TestEnsureForNameCollisionErrors(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	// Create a worktree for branch "feature-x" at .worktrees/feature-x.
	wtDir := filepath.Join(dir, ".worktrees", "feature-x")
	cmd := exec.Command("git", "worktree", "add", "-b", "feature-x", wtDir)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add -b feature-x: %v\n%s", err, out)
	}

	_, err := EnsureForName(dir, "feature/x")
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("error = %q, want it to mention 'already in use'", err)
	}
}

// EnsureForName must give two branches sharing only their last component
// distinct worktrees (feature/x and bug/x). Under the old last-component
// derivation the second create errored "already in use" — the structural
// limitation full-name derivation removes.
func TestEnsureForNameBasenameCoexist(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	p1, err := EnsureForName(dir, "feature/x")
	if err != nil {
		t.Fatalf("EnsureForName(feature/x) = %v, want nil", err)
	}
	p2, err := EnsureForName(dir, "bug/x")
	if err != nil {
		t.Fatalf("EnsureForName(bug/x) = %v, want nil (shares only the last component with feature/x)", err)
	}
	if want := filepath.Join(dir, ".worktrees", "feature-x"); p1 != want {
		t.Errorf("path 1 = %q, want %q", p1, want)
	}
	if want := filepath.Join(dir, ".worktrees", "bug-x"); p2 != want {
		t.Errorf("path 2 = %q, want %q", p2, want)
	}
}

// worktreeDirName is the one path-derivation rule behind every creation
// path: path-hostile characters (separators on any platform, colon, ASCII
// control characters) flatten to "-", everything else is kept (invalid
// UTF-8 bytes re-encode as U+FFFD), and a derived "." or ".." is an error
// because filepath.Join cleans those out of .worktrees (".." reaches the
// repo root). The literal want values pin the mapping itself.
func TestWorktreeDirName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "plain", want: "plain"},
		{in: "feature/x", want: "feature-x"},
		{in: "a/b/c", want: "a-b-c"},
		{in: `back\slash`, want: "back-slash"},
		{in: "colon:name", want: "colon-name"},
		{in: "ctrl\x01char", want: "ctrl-char"},
		{in: "del\x7fchar", want: "del-char"},
		{in: "keep.these", want: "keep.these"},
		{in: ".", wantErr: true},
		{in: "..", wantErr: true},
	}
	for _, tc := range cases {
		got, err := worktreeDirName(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("worktreeDirName(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("worktreeDirName(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("worktreeDirName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// EnsureForName must reuse a worktree that carries its branch under the OLD
// last-component layout (".worktrees/x" holding "feature/x"): the reuse loop
// matches by branch name, never by derived path, so pre-change worktrees keep
// resolving with no migration.
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

// EnsureForName must reject "." and "..": worktreeDirName keeps them intact
// and filepath.Join then cleans ".." up to the repo root and "." to the
// .worktrees container — the wrong path with no error. TestWorktreeDirName
// pins the guard itself; this pins the entry point end to end.
func TestEnsureForNameRejectsDot(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	for _, name := range []string{".", ".."} {
		if _, err := EnsureForName(dir, name); err == nil {
			t.Errorf("EnsureForName(%q) = nil error, want non-nil", name)
		}
	}
}

// setupDefaultRemote creates a bare remote, adds it as origin, pushes the
// current branch, and points origin/HEAD at it so DefaultBranch() resolves.
// Returns the default branch name (main or master). The default-branch
// refusal in EnsureForName/EnsureForBranch is gated on DefaultBranch(), which
// reads origin/HEAD, so these tests need a real remote.
func setupDefaultRemote(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	db := strings.TrimSpace(string(out))

	remoteDir := filepath.Join(dir, "remote.git")
	if err := exec.Command("git", "init", "--bare", remoteDir).Run(); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("remote", "add", "origin", remoteDir)
	run("push", "origin", db)
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+db)
	return db
}

// EnsureForName must refuse to create a linked worktree for the default
// branch when it isn't already checked out. Creating .worktrees/main would
// put the default branch in a linked worktree — a dead end the guard makes
// read-only and the tool's model forbids.
func TestEnsureForNameRefusesDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	// Move the primary checkout off the default branch so it is "free".
	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	_, err := EnsureForName(dir, db)
	if err == nil {
		t.Fatalf("EnsureForName(%q) = nil error, want refusal", db)
	}
	if !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("error = %q, want it to mention 'default branch'", err)
	}
}

// EnsureForName must still open the primary checkout when the default branch
// is already checked out there. -W main should launch in the repo root, not
// create a linked worktree.
func TestEnsureForNameReusesDefaultBranchPrimary(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	path, err := EnsureForName(dir, db)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, _ := filepath.EvalSymlinks(path)
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	if resolvedPath != resolvedDir {
		t.Fatalf("path = %q, want primary checkout %q", path, dir)
	}
}

// EnsureForName must refuse every selectable form of the default branch (bare,
// <remote>/<default>, <remote>/HEAD) and resolve other remote-tracking names
// like the picker does. A local branch ending in "/<default>" is a feature
// branch and must not be refused. Each case builds a fresh repo because a
// wrong success creates .worktrees/<default>, and the path-collision guard
// would then mask later cases' failures.
func TestEnsureForNameRefusesDefaultBranchRemoteForm(t *testing.T) {
	newRepo := func(t *testing.T) (string, string, func(...string)) {
		t.Helper()
		dir := t.TempDir()
		gitInit(t, dir)
		db := setupDefaultRemote(t, dir)
		run := func(args ...string) {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		// Move the primary checkout off the default branch so it is
		// "free" — the state where creating .worktrees/<db> would become
		// the default's only checkout. "topic" (not "feature") so
		// refs/heads/feature doesn't block creating the feature/<db>
		// branch in the precision case below (git forbids a ref that is
		// both a leaf and a directory).
		run("checkout", "-b", "topic")
		return dir, db, run
	}

	refusal := func(t *testing.T, dir, name string) {
		t.Helper()
		_, err := EnsureForName(dir, name)
		if err == nil {
			t.Fatalf("EnsureForName(%q) = nil error, want refusal", name)
		}
		if !strings.Contains(err.Error(), "default branch") {
			t.Fatalf("EnsureForName(%q) error = %q, want it to mention 'default branch'", name, err)
		}
	}

	t.Run("origin form", func(t *testing.T) {
		dir, db, _ := newRepo(t)
		refusal(t, dir, "origin/"+db)
	})

	t.Run("upstream form", func(t *testing.T) {
		dir, db, run := newRepo(t)
		// A second remote so refs/remotes/upstream/<db> exists too — the
		// fork-workflow shape where the default branch is reachable via a
		// non-origin remote.
		upstreamDir := filepath.Join(dir, "upstream.git")
		if err := exec.Command("git", "init", "--bare", upstreamDir).Run(); err != nil {
			t.Fatal(err)
		}
		run("remote", "add", "upstream", upstreamDir)
		run("push", "upstream", db)
		refusal(t, dir, "upstream/"+db)
	})

	t.Run("origin HEAD alias", func(t *testing.T) {
		dir, _, _ := newRepo(t)
		refusal(t, dir, "origin/HEAD")
	})

	t.Run("remote-tracking feature gets a tracking branch", func(t *testing.T) {
		dir, _, run := newRepo(t)
		run("branch", "feat")
		run("push", "origin", "feat")
		run("branch", "-D", "feat")
		run("fetch", "origin")
		path, err := EnsureForName(dir, "origin/feat")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, ".worktrees", "feat"); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
		if !refExists(dir, "refs/heads/feat") || refExists(dir, "refs/heads/origin/feat") {
			t.Error("want local branch feat tracking origin/feat, not a literal origin/feat branch")
		}
	})

	t.Run("local branch named like a remote form is not refused", func(t *testing.T) {
		dir, db, run := newRepo(t)
		run("branch", "origin/"+db+"-x")
		run("branch", "origin/"+db+"/y")
		if _, err := EnsureForName(dir, "origin/"+db+"/y"); err != nil {
			t.Fatalf("local branch origin/%s/y refused: %v", db, err)
		}
	})

	t.Run("remote whose name ends in the default is matched exactly", func(t *testing.T) {
		dir, db, run := newRepo(t)
		other := filepath.Join(dir, "other.git")
		if err := exec.Command("git", "init", "--bare", other).Run(); err != nil {
			t.Fatal(err)
		}
		run("remote", "add", "rel/"+db, other)
		run("push", "rel/"+db, "topic")
		run("fetch", "rel/"+db)
		// refs/remotes/rel/<db>/topic belongs to remote "rel/<db>" branch
		// "topic": a feature branch, not the default.
		if _, err := EnsureForName(dir, "rel/"+db+"/topic"); err != nil {
			t.Fatalf("feature branch on remote rel/%s refused: %v", db, err)
		}
	})

	t.Run("feature branch is not the default", func(t *testing.T) {
		dir, db, run := newRepo(t)
		run("branch", "feature/"+db)
		path, err := EnsureForName(dir, "feature/"+db)
		if err != nil {
			t.Fatalf("EnsureForName(feature/%s) = %v, want nil (feature branch, not the default)", db, err)
		}
		if want := filepath.Join(dir, ".worktrees", "feature-"+db); path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
	})
}

// EnsureForBranch must refuse the default branch and its remote-tracking
// form. The picker path must not create a linked worktree for the default
// branch, even when it is reachable only as origin/<default>.
func TestEnsureForBranchRefusesDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	for _, branch := range []string{db, "origin/" + db, "origin/HEAD"} {
		if _, err := EnsureForBranch(dir, branch); err == nil {
			t.Errorf("EnsureForBranch(%q) = nil error, want refusal", branch)
		}
	}
}

// EnsureForBranch must refuse the default branch reachable via ANY remote,
// not just origin. In a fork workflow the default branch may only be
// reachable as upstream/<default>; selecting it would create a local
// <default> in a linked worktree — exactly the state the invariant forbids.
// The refusal must also be precise: a local branch whose name ends in
// "/<default>" (e.g. feature/main) is a feature branch, not the default, and
// must NOT be refused — the name-only suffix match is gated on the ref
// actually existing under refs/remotes/.
func TestEnsureForBranchRefusesDefaultBranchAnyRemote(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	// Add a second remote "upstream" and push the default branch to it so
	// refs/remotes/upstream/<db> exists — the fork-workflow shape where the
	// default branch is only reachable via a non-origin remote.
	upstreamDir := filepath.Join(dir, "upstream.git")
	if err := exec.Command("git", "init", "--bare", upstreamDir).Run(); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("remote", "add", "upstream", upstreamDir)
	run("push", "upstream", db)

	// Move the primary checkout off the default branch so it is "free".
	// Use "topic" (not "feature") so refs/heads/feature doesn't block
	// creating the feature/<db> branch below (git forbids a ref that is
	// both a leaf and a directory: refs/heads/feature vs feature/main).
	run("checkout", "-b", "topic")

	// upstream/<db> must be refused — the invariant holds across remotes.
	if _, err := EnsureForBranch(dir, "upstream/"+db); err == nil {
		t.Errorf("EnsureForBranch(upstream/%s) = nil error, want refusal", db)
	}

	// A local branch ending in "/<db>" is a feature branch, not the default.
	// It must NOT be refused: without the refs/remotes/ existence check, the
	// name-only suffix match would wrongly refuse it.
	run("branch", "feature/"+db)
	if _, err := EnsureForBranch(dir, "feature/"+db); err != nil {
		t.Errorf("EnsureForBranch(feature/%s) = %v, want nil (feature branch, not the default)", db, err)
	}
}
