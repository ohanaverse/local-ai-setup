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

// The picker paths (EnsureForBranch local and new-branch cases, and the
// remote-tracking case) must give the same "already in use by branch" error as
// -W when the derived path belongs to another branch. Flattening makes the
// collision easy to hit (feature/x vs feature-x), and git's own "already
// exists" never names the branch holding the path.
func TestEnsureForBranchCollisionErrors(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string) string{
		"new branch": func(t *testing.T, dir string) string { return "feature-x" },
		"local branch": func(t *testing.T, dir string) string {
			if out, err := runGitCmd(dir, "branch", "feature-x"); err != nil {
				t.Fatalf("git branch: %v\n%s", err, out)
			}
			return "feature-x"
		},
		"remote-tracking": func(t *testing.T, dir string) string {
			setupRemote(t, dir, "seed", "feature-x")
			return "origin/feature-x"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			// feature/x occupies .worktrees/feature-x.
			if _, err := EnsureForName(dir, "feature/x"); err != nil {
				t.Fatal(err)
			}
			pick := setup(t, dir)
			_, err := EnsureForBranch(dir, pick)
			if err == nil || !strings.Contains(err.Error(), "already in use by branch \"feature/x\"") {
				t.Fatalf("EnsureForBranch(%q) error = %v, want 'already in use by branch \"feature/x\"'", pick, err)
			}
		})
	}
}

// On a case-insensitive volume (macOS default) feature/x and Feature/x
// flatten to .worktrees/feature-x and .worktrees/Feature-x — one directory.
// branchAtPath's EvalSymlinks compare keeps the typed case and misses it, so
// the filesystem must be asked whether the two paths are the same file. The
// test probes the volume first and skips on a case-sensitive one, where the
// two names are different directories and no collision exists.
func TestEnsureForNameCaseInsensitiveCollision(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	probe := filepath.Join(dir, "CaseProbe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err != nil {
		t.Skip("case-sensitive filesystem: feature-x and Feature-x are different directories")
	}

	if _, err := EnsureForName(dir, "feature/x"); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureForName(dir, "Feature/x")
	if err == nil || !strings.Contains(err.Error(), "already in use by branch \"feature/x\"") {
		t.Fatalf("EnsureForName(Feature/x) error = %v, want 'already in use by branch \"feature/x\"'", err)
	}
	// refExists would say yes here (the ref file resolves case-insensitively),
	// so read the branch names git actually lists.
	out, gerr := runGitCmd(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if strings.Contains(string(out), "Feature/x") {
		t.Errorf("branch Feature/x was created despite the collision; branches:\n%s", out)
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

// worktreeDirName must refuse a flattened name over 255 bytes. Git stores
// refs/heads/a/b/c as nested directories, so a branch of many short components
// is a valid ref, but the flattened name is ONE directory entry (and git's
// .git/worktrees/<basename> entry), where the filesystem allows 255 bytes. Left
// alone, git fails with a raw "File name too long" that never says to shorten
// the branch. 255 bytes passes; 256 is refused, counting bytes not runes.
func TestWorktreeDirNameRejectsOverlongName(t *testing.T) {
	if _, err := worktreeDirName(strings.Repeat("a", 255)); err != nil {
		t.Errorf("255-byte name: %v, want nil", err)
	}
	for _, name := range []string{
		strings.Repeat("a", 256),
		strings.Repeat("ab/", 100),    // 300 bytes, each component short
		strings.Repeat("\u00e9", 128), // 128 runes = 256 bytes
	} {
		_, err := worktreeDirName(name)
		if err == nil {
			t.Errorf("worktreeDirName(%d bytes) = nil error, want too-long error", len(name))
			continue
		}
		if !strings.Contains(err.Error(), "255") {
			t.Errorf("error = %q, want it to name the 255-byte limit", err)
		}
	}
}

// EnsureForName and EnsureForBranch must surface the too-long error before
// running git, so the user is told to shorten the branch name and no
// half-created branch is left behind.
func TestEnsureRefusesOverlongBranchName(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	name := strings.Repeat("ab/", 100) + "x"
	if _, err := EnsureForName(dir, name); err == nil || !strings.Contains(err.Error(), "255") {
		t.Errorf("EnsureForName error = %v, want the 255-byte limit", err)
	}
	if _, err := EnsureForBranch(dir, name); err == nil || !strings.Contains(err.Error(), "255") {
		t.Errorf("EnsureForBranch error = %v, want the 255-byte limit", err)
	}
	if refExists(dir, "refs/heads/"+name) {
		t.Error("a branch was created despite the refusal")
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

// A brand-new -W name must start its branch at dir's HEAD, not at the default
// branch. L2's prompt names that base to the user, so the refactor onto
// classifyName must keep creation branching from where the prompt says.
func TestEnsureForNameNewBranchStartsAtHead(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	if out, err := runGitCmd(dir, "checkout", "-b", "topic"); err != nil {
		t.Fatalf("checkout: %v\n%s", err, out)
	}
	if out, err := runGitCmd(dir, "commit", "--allow-empty", "-m", "topic work"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	head, _ := runGitCmd(dir, "rev-parse", "HEAD")
	if _, err := EnsureForName(dir, "fresh"); err != nil {
		t.Fatal(err)
	}
	got, _ := runGitCmd(dir, "rev-parse", "fresh")
	if string(got) != string(head) {
		t.Fatalf("fresh = %s, want dir's HEAD %s", got, head)
	}
}

// WouldCreate must say true only for the case where EnsureForName would run
// `git worktree add -b` for a branch that does not exist, name how that case
// would start (the phrase the question joins after "create it": "from
// <branch>" or "from detached HEAD <sha>"), and return the very error
// EnsureForName returns for invalid names, so the question is never asked
// about a name that would be rejected after "y". Every case builds a fresh
// repo because a wrong creation would leave a branch that masks the next
// case.
func TestWouldCreate(t *testing.T) {
	type tc struct {
		name     string
		prepare  func(t *testing.T, dir string)
		arg      string
		creates  bool
		base     string // exact base, or prefix when basePrefix
		basePref bool
		wantErr  bool
	}
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		if out, err := runGitCmd(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	cases := []tc{
		{name: "unknown name on a branch", arg: "brand-new", creates: true, base: "from main"},
		{name: "unknown name on a non-default branch", arg: "brand-new", creates: true, base: "from topic",
			prepare: func(t *testing.T, dir string) { git(t, dir, "checkout", "-b", "topic") }},
		{name: "unknown name on a detached HEAD", arg: "brand-new", creates: true, base: "from detached HEAD ", basePref: true,
			prepare: func(t *testing.T, dir string) { git(t, dir, "checkout", "--detach") }},
		{name: "existing worktree", arg: "wt-branch",
			prepare: func(t *testing.T, dir string) {
				git(t, dir, "worktree", "add", "-b", "wt-branch", filepath.Join(dir, ".worktrees", "wt-branch"))
			}},
		{name: "existing local branch without a worktree", arg: "lonely",
			prepare: func(t *testing.T, dir string) { git(t, dir, "branch", "lonely") }},
		{name: "remote-tracking name", arg: "origin/feat",
			prepare: func(t *testing.T, dir string) {
				setupRemote(t, dir, "feat", "feat")
			}},
		{name: "default branch checked out in the root", arg: "main"},
		{name: "empty name", arg: "", wantErr: true},
		{name: "default branch remote form", arg: "origin/main", wantErr: true,
			prepare: func(t *testing.T, dir string) {
				setupDefaultRemote(t, dir)
				git(t, dir, "checkout", "-b", "topic")
			}},
		{name: "path collision", arg: "feature/x", wantErr: true,
			prepare: func(t *testing.T, dir string) {
				git(t, dir, "worktree", "add", "-b", "feature-x", filepath.Join(dir, ".worktrees", "feature-x"))
			}},
		{name: "overlong name", arg: strings.Repeat("ab/", 100) + "x", wantErr: true},
		{name: "double dot is not a ref name", arg: "a..b", wantErr: true},
		{name: ".lock suffix is not a ref name", arg: "foo.lock", wantErr: true},
		{name: "tilde is not a ref name", arg: "x~1", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			if c.prepare != nil {
				c.prepare(t, dir)
			}
			creates, base, err := WouldCreate(dir, c.arg)
			if c.wantErr {
				if err == nil {
					t.Fatal("WouldCreate error = nil, want one")
				}
				if creates {
					t.Error("creates = true alongside an error")
				}
				// Same input, same error text from EnsureForName.
				_, ensureErr := EnsureForName(dir, c.arg)
				if ensureErr == nil || ensureErr.Error() != err.Error() {
					t.Errorf("WouldCreate error = %q, EnsureForName error = %v; want equal", err, ensureErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if creates != c.creates {
				t.Fatalf("creates = %v, want %v", creates, c.creates)
			}
			if !c.creates {
				return
			}
			if c.basePref && !strings.HasPrefix(base, c.base) || !c.basePref && base != c.base {
				t.Errorf("base = %q, want %q", base, c.base)
			}
			if c.basePref {
				sha, err := runGitCmd(dir, "rev-parse", "--short", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				if want := c.base + strings.TrimSpace(string(sha)); base != want {
					t.Errorf("base = %q, want %q", base, want)
				}
			}
			out, err := runGitCmd(dir, "for-each-ref", "--format=%(refname)", "refs/heads/"+c.arg)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range strings.Split(string(out), "\n") {
				if strings.TrimSpace(l) == "refs/heads/"+c.arg {
					t.Error("WouldCreate created the branch")
				}
			}
		})
	}
}

// A name git cannot use as a branch must be refused by WouldCreate, with git's
// own reason in the error, so the caller never asks "create it?" about a name
// that is bound to fail.
func TestWouldCreateInvalidRefNameQuotesGit(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	_, _, err := WouldCreate(dir, "a..b")
	if err == nil || !strings.Contains(err.Error(), "a..b") || !strings.Contains(err.Error(), "not a valid branch name") {
		t.Fatalf("error = %v, want git's 'not a valid branch name' naming a..b", err)
	}
}

// On a case-insensitive volume refs/heads/Main resolves to main's ref file. A
// case-only variant of an existing branch must be an error, not a silent
// second checkout of that branch and not a create question.
func TestClassifyNameCaseVariantOfBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	// The prescribed case-sensitivity probe: the filesystem, not show-ref's
	// case-folded ref-file resolution, decides whether this test runs.
	if err := os.WriteFile(filepath.Join(dir, "CaseProbe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err != nil {
		t.Skip("case-sensitive volume: Main and main are different branches")
	}
	_, _, err := WouldCreate(dir, "Main")
	if err == nil || !strings.Contains(err.Error(), "differs only in case") {
		t.Fatalf("WouldCreate(Main) error = %v, want a 'differs only in case' error", err)
	}
	if _, err := EnsureForName(dir, "Main"); err == nil {
		t.Fatal("EnsureForName(Main) succeeded, want the same refusal")
	}
	if _, serr := os.Stat(filepath.Join(dir, ".worktrees", "Main")); serr == nil {
		t.Error("a worktree was created for the case variant")
	}
}

// On a case-insensitive volume show-ref resolves refs/remotes/origin/Feat
// through origin/feat's ref file. A case-only variant of a remote-tracking
// name must be refused exactly like a local one, never tracked: `worktree add
// -b Feat … origin/Feat` would check out origin/feat under the wrong name, and
// origin/Main (the default branch form) would put the default branch in a
// linked worktree — the invariant refuseDefaultBranch exists for. The
// filesystem probe decides whether this test runs; assertions read
// for-each-ref, never refExists.
func TestClassifyNameCaseVariantOfRemoteRef(t *testing.T) {
	cases := []struct {
		name  string
		arg   string
		setup func(t *testing.T, dir string)
	}{
		{name: "any remote branch", arg: "origin/Feat", setup: func(t *testing.T, dir string) {
			setupRemote(t, dir, "feat", "feat")
		}},
		{name: "the default branch form", arg: "origin/Main", setup: func(t *testing.T, dir string) {
			setupDefaultRemote(t, dir)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			// The prescribed case-sensitivity probe: the filesystem, not
			// show-ref's case-folded ref-file resolution, decides whether
			// this case can happen at all.
			if err := os.WriteFile(filepath.Join(dir, "CaseProbe"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err != nil {
				t.Skip("case-sensitive volume: the typed case names a different ref")
			}
			c.setup(t, dir)
			if _, _, err := WouldCreate(dir, c.arg); err == nil || !strings.Contains(err.Error(), "differs only in case") {
				t.Fatalf("WouldCreate(%s) error = %v; want a 'differs only in case' error", c.arg, err)
			}
			if _, err := EnsureForName(dir, c.arg); err == nil || !strings.Contains(err.Error(), "differs only in case") {
				t.Fatalf("EnsureForName(%s) error = %v; want the same refusal", c.arg, err)
			}
			short := c.arg[strings.IndexByte(c.arg, '/')+1:]
			out, err := runGitCmd(dir, "for-each-ref", "--format=%(refname)", "refs/heads")
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range strings.Split(string(out), "\n") {
				if l == "refs/heads/"+short {
					t.Errorf("local branch %s was created under the wrong case: %s", short, out)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".worktrees", short)); err == nil {
				t.Errorf(".worktrees/%s was created", short)
			}
		})
	}
}

// A name git cannot create because a local branch sits on its ref path — a
// leaf branch above it (feature blocks feature/x) or a branch nested under it
// (feature/x blocks feature) — must be refused before the create question,
// naming the branch in the way. Git nests refs as directories and would fail
// at the ref lock after "y"; the question and git's own failure must agree.
func TestWouldCreateRefNamespaceConflictNamesTheBlocker(t *testing.T) {
	for _, c := range []struct {
		name, arg, blocker string
		prep               []string
	}{
		{name: "leaf blocks its slashed name", arg: "feature/x", blocker: "feature", prep: []string{"branch", "feature"}},
		{name: "nested branch blocks the leaf name", arg: "feature", blocker: "feature/x", prep: []string{"branch", "feature/x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			if out, err := runGitCmd(dir, c.prep...); err != nil {
				t.Fatalf("git %v: %v\n%s", c.prep, err, out)
			}
			_, _, err := WouldCreate(dir, c.arg)
			if err == nil || !strings.Contains(err.Error(), c.arg) || !strings.Contains(err.Error(), c.blocker) {
				t.Fatalf("WouldCreate(%s) error = %v; want it to name %q and %q", c.arg, err, c.arg, c.blocker)
			}
			_, ensureErr := EnsureForName(dir, c.arg)
			if ensureErr == nil || ensureErr.Error() != err.Error() {
				t.Errorf("WouldCreate error = %q, EnsureForName error = %v; want equal", err, ensureErr)
			}
		})
	}
}

// On an unborn HEAD (a fresh `git init`, no commits) `git worktree add -b`
// prints "No possible source branch, inferring --orphan" and creates a branch
// sharing no history — not one off the unborn current branch. The start
// phrase the create question shows must say that, never "from <branch>", or
// the question promises a base git would not use.
func TestWouldCreateUnbornHeadDescribesAnOrphan(t *testing.T) {
	dir := t.TempDir()
	// gitInit's commit is deliberately absent: HEAD must be unborn.
	if out, err := runGitCmd(dir, "init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	creates, start, err := WouldCreate(dir, "topic")
	if err != nil {
		t.Fatal(err)
	}
	if !creates {
		t.Fatal("creates = false; want true")
	}
	const want = "as an empty orphan branch with no shared history (this repository has no commits yet)"
	if start != want {
		t.Errorf("start = %q; want %q", start, want)
	}
	out, err := runGitCmd(dir, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branches exist after the question: %s", out)
	}
}

// On a case-insensitive volume a case-folded ref-namespace conflict must be
// refused before the create question exactly like the exact-case one: with
// `feature` existing, `git branch Feature/x` fails at the ref lock
// ("'refs/heads/Feature' exists") because git nests refs as directories, so
// the question would be asked about a creation that can never happen. The
// filesystem probe decides whether this case can happen at all; assertions
// read for-each-ref, never refExists.
func TestWouldCreateCaseFoldedRefNamespaceConflict(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "CaseProbe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "caseprobe")); err != nil {
		t.Skip("case-sensitive volume: Feature/x and feature coexist")
	}
	if out, err := runGitCmd(dir, "branch", "feature"); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}
	_, _, err := WouldCreate(dir, "Feature/x")
	if err == nil || !strings.Contains(err.Error(), `"Feature/x"`) || !strings.Contains(err.Error(), `"feature"`) {
		t.Fatalf("WouldCreate(Feature/x) error = %v; want it to name %q and %q", err, "Feature/x", "feature")
	}
	_, ensureErr := EnsureForName(dir, "Feature/x")
	if ensureErr == nil || ensureErr.Error() != err.Error() {
		t.Errorf("WouldCreate error = %q, EnsureForName error = %v; want equal", err, ensureErr)
	}
}
