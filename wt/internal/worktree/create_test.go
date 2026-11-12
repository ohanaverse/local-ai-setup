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

	path, _, err := EnsureForBranch(dir, "feature/x")
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

	path, _, err := EnsureForBranch(dir, "origin/feature")
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

	path, _, err := EnsureForBranch(dir, "feature/x")
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

	path, _, err := EnsureForBranch(dir, "origin/feature/x")
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

	_, _, err := EnsureForBranch(dir, "origin/foo")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got %v", err)
	}
}

// EnsureForName must error when the derived worktree path collides with an
// existing worktree for a DIFFERENT branch. Full-name derivation flattens
// slashes, so "feature/x" and "feature-x" share .worktrees/feature-x: typing
// "feature/x" while feature-x's worktree sits there must error rather than
// silently return the wrong worktree — and the typed-name path surfaces that
// collision through Resolve/Plan.Ensure before any create, so the user
// cannot land on the wrong branch believing they created "feature/x".
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
			_, _, err := EnsureForBranch(dir, pick)
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
	if _, _, err := EnsureForBranch(dir, name); err == nil || !strings.Contains(err.Error(), "255") {
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
// Returns the default branch name (main or master). The default-branch tests
// name the default through DefaultBranch(), which reads origin/HEAD, so they
// need a real remote.
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

// EnsureForName must create a linked worktree for the default branch at
// .worktrees/<default> when the root checkout has moved to another branch —
// the default is an ordinary existing branch there, and the old refusal to
// ever link it is gone — and a second call must reuse that worktree without
// error and at the same path, so -W <default> is idempotent.
func TestEnsureForNameCreatesDefaultBranchWorktree(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	// Move the primary checkout off the default branch so nothing holds it.
	cmd := exec.Command("git", "checkout", "-b", "topic")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b topic: %v\n%s", err, out)
	}

	path, err := EnsureForName(dir, db)
	if err != nil {
		t.Fatalf("EnsureForName(%q) = %v, want the default branch's worktree", db, err)
	}
	if want := filepath.Join(dir, ".worktrees", db); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}
	second, err := EnsureForName(dir, db)
	if err != nil {
		t.Fatalf("second EnsureForName(%q) = %v, want a reuse", db, err)
	}
	resolvedFirst, _ := filepath.EvalSymlinks(path)
	resolvedSecond, _ := filepath.EvalSymlinks(second)
	if resolvedFirst != resolvedSecond {
		t.Fatalf("second call paths differ: %q vs %q", path, second)
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

// EnsureForName must treat every form of the default branch as an ordinary
// name: with the old refusal gone, <remote>/<default> — the default reachable
// only as a remote ref — creates its tracking worktree at
// .worktrees/<default> and a second call reuses it, while lookalikes (a local
// branch ending in "/<default>", a remote whose name ends in the default)
// resolve like the picker would. Each case builds a fresh repo because a
// wrong success creates .worktrees/<default>, and the path-collision guard
// would then mask later cases' failures.
func TestEnsureForNameDefaultBranchForms(t *testing.T) {
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
		// Move the primary checkout off the default branch. "topic" (not
		// "feature") so refs/heads/feature doesn't block creating the
		// feature/<db> branch in the precision case below (git forbids a ref
		// that is both a leaf and a directory).
		run("checkout", "-b", "topic")
		return dir, db, run
	}

	// createsAt asserts the name lands at .worktrees/<dirName> and that a
	// second EnsureForName for the same name reuses that worktree.
	createsAt := func(t *testing.T, dir, name, dirName string) {
		t.Helper()
		path, err := EnsureForName(dir, name)
		if err != nil {
			t.Fatalf("EnsureForName(%q) = %v, want the default branch's worktree", name, err)
		}
		if want := filepath.Join(dir, ".worktrees", dirName); path != want {
			t.Fatalf("path = %q, want %q", path, want)
		}
		second, err := EnsureForName(dir, name)
		if err != nil {
			t.Fatalf("second EnsureForName(%q) = %v, want a reuse", name, err)
		}
		resolvedFirst, _ := filepath.EvalSymlinks(path)
		resolvedSecond, _ := filepath.EvalSymlinks(second)
		if resolvedFirst != resolvedSecond {
			t.Fatalf("second call paths differ: %q vs %q", path, second)
		}
	}

	t.Run("origin form creates the tracking worktree", func(t *testing.T) {
		dir, db, run := newRepo(t)
		// The fork-workflow shape where the default branch is reachable only
		// as origin/<default>: no local branch of that name remains.
		run("branch", "-D", db)
		createsAt(t, dir, "origin/"+db, db)
	})

	t.Run("upstream form creates the tracking worktree", func(t *testing.T) {
		dir, db, run := newRepo(t)
		// A second remote so refs/remotes/upstream/<db> exists too — the
		// fork-workflow shape where the default branch is reachable via a
		// non-origin remote only.
		upstreamDir := filepath.Join(dir, "upstream.git")
		if err := exec.Command("git", "init", "--bare", upstreamDir).Run(); err != nil {
			t.Fatal(err)
		}
		run("remote", "add", "upstream", upstreamDir)
		run("push", "upstream", db)
		run("branch", "-D", db)
		createsAt(t, dir, "upstream/"+db, db)
	})

	t.Run("origin HEAD alias names no branch", func(t *testing.T) {
		dir, _, _ := newRepo(t)
		// origin/HEAD is a symbolic alias, not a branch of its own: the local
		// branch it would create is literally called "HEAD", and git refuses
		// that name with its own message. The default-branch refusal is gone;
		// resolving the alias to <remote>/<default> is not this change's job.
		_, err := EnsureForName(dir, "origin/HEAD")
		if err == nil || !strings.Contains(err.Error(), "not a valid branch name") {
			t.Fatalf("EnsureForName(origin/HEAD) error = %v, want git's 'not a valid branch name'", err)
		}
		if strings.Contains(err.Error(), "default branch") {
			t.Fatalf("error = %q, want no default-branch refusal", err)
		}
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

	t.Run("local branch named like a remote form resolves", func(t *testing.T) {
		dir, db, run := newRepo(t)
		run("branch", "origin/"+db+"-x")
		run("branch", "origin/"+db+"/y")
		if _, err := EnsureForName(dir, "origin/"+db+"/y"); err != nil {
			t.Fatalf("local branch origin/%s/y failed: %v", db, err)
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
			t.Fatalf("feature branch on remote rel/%s failed: %v", db, err)
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

// EnsureForBranch — the picker's bare-branch path — must create the default
// branch's worktree at .worktrees/<default> when the root checkout has moved
// to another branch, exactly like any other branch (the old refusal is gone),
// and a second call must reuse that worktree with no error and the same path.
func TestEnsureForBranchCreatesDefaultBranchWorktree(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	// Move the primary checkout off the default branch so nothing holds it.
	cmd := exec.Command("git", "checkout", "-b", "topic")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b topic: %v\n%s", err, out)
	}

	path, _, err := EnsureForBranch(dir, db)
	if err != nil {
		t.Fatalf("EnsureForBranch(%q) = %v, want the default branch's worktree", db, err)
	}
	if want := filepath.Join(dir, ".worktrees", db); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}
	second, _, err := EnsureForBranch(dir, db)
	if err != nil {
		t.Fatalf("second EnsureForBranch(%q) = %v, want a reuse", db, err)
	}
	resolvedFirst, _ := filepath.EvalSymlinks(path)
	resolvedSecond, _ := filepath.EvalSymlinks(second)
	if resolvedFirst != resolvedSecond {
		t.Fatalf("second call paths differ: %q vs %q", path, second)
	}
}

// EnsureForBranch must materialize the default branch from a remote form of
// ANY remote — the fork-workflow shape where it is reachable only as
// upstream/<default> — landing at .worktrees/<default> like any
// remote-tracking name (the old any-remote refusal is gone), and it must stay
// precise: a local branch whose name ends in "/<default>" (feature/main) is a
// feature branch, never the default.
func TestEnsureForBranchDefaultBranchAnyRemoteForm(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	// Add a second remote "upstream" and push the default branch to it so
	// refs/remotes/upstream/<db> exists.
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

	// Move the primary checkout off the default branch and drop the local
	// default, so it is reachable only as upstream/<db>. Use "topic" (not
	// "feature") so refs/heads/feature doesn't block creating the
	// feature/<db> branch below (git forbids a ref that is both a leaf and a
	// directory: refs/heads/feature vs feature/main).
	run("checkout", "-b", "topic")
	run("branch", "-D", db)

	path, _, err := EnsureForBranch(dir, "upstream/"+db)
	if err != nil {
		t.Fatalf("EnsureForBranch(upstream/%s) = %v, want the default branch's worktree", db, err)
	}
	if want := filepath.Join(dir, ".worktrees", db); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	second, _, err := EnsureForBranch(dir, "upstream/"+db)
	if err != nil {
		t.Fatalf("second EnsureForBranch(upstream/%s) = %v, want a reuse", db, err)
	}
	resolvedFirst, _ := filepath.EvalSymlinks(path)
	resolvedSecond, _ := filepath.EvalSymlinks(second)
	if resolvedFirst != resolvedSecond {
		t.Fatalf("second call paths differ: %q vs %q", path, second)
	}

	// A local branch ending in "/<db>" is a feature branch, not the default.
	run("branch", "feature/"+db)
	if _, _, err := EnsureForBranch(dir, "feature/"+db); err != nil {
		t.Errorf("EnsureForBranch(feature/%s) = %v, want nil (feature branch, not the default)", db, err)
	}
}

// EnsureForBranch must keep git's own error when the branch is already
// checked out elsewhere (the default branch in the primary checkout here):
// git alone refuses two worktrees on one branch, and its "already used by
// worktree" message — surfaced verbatim, never wrapped away or replaced by a
// friendlier refusal — is what tells the user where the branch lives. This is
// exactly the case the removed default-branch refusal used to shadow.
func TestEnsureForBranchAlreadyCheckedOutKeepsGitsError(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	db := setupDefaultRemote(t, dir)

	_, _, err := EnsureForBranch(dir, db)
	if err == nil {
		t.Fatalf("EnsureForBranch(%q) = nil error, want git's refusal", db)
	}
	msg := err.Error()
	// Git says "is already used by worktree at" since 2.35; older git said
	// "is already checked out at".
	if !strings.Contains(msg, "already used by worktree") && !strings.Contains(msg, "already checked out") {
		t.Fatalf("error = %q, want git's own 'already used by worktree' message", msg)
	}
	if strings.Contains(msg, "default branch") {
		t.Fatalf("error = %q, want no wt-style default-branch refusal in place of git's", msg)
	}
}

// EnsureForBranch must report exactly what it made (Created) in each of its
// cases — a brand-new branch (NewBranch), an existing local branch (checked
// out as is) and a remote-tracking branch (NewBranch plus Tracks) — and a
// reused worktree must report no creation at all. The picker's launch line
// states "creates branch/worktree" from this and must never claim a creation
// that did not happen.
func TestEnsureForBranchDetail(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		prep func(t *testing.T, dir string)
		want Created
	}{
		{name: "new branch", arg: "feat", want: Created{Branch: "feat", NewBranch: true}},
		{name: "existing local branch", arg: "topic", want: Created{Branch: "topic"},
			prep: func(t *testing.T, dir string) {
				if out, err := runGitCmd(dir, "branch", "topic"); err != nil {
					t.Fatalf("git branch: %v\n%s", err, out)
				}
			}},
		{name: "remote-tracking branch", arg: "origin/feat", want: Created{Branch: "feat", Tracks: "origin/feat", NewBranch: true},
			prep: func(t *testing.T, dir string) { setupRemote(t, dir, "seed", "feat") }},
		{name: "reused worktree", arg: "feat", want: Created{Branch: "feat"},
			prep: func(t *testing.T, dir string) {
				if _, _, err := EnsureForBranch(dir, "feat"); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			gitInit(t, dir)
			if c.prep != nil {
				c.prep(t, dir)
			}
			_, got, err := EnsureForBranch(dir, c.arg)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("made %+v, want %+v", got, c.want)
			}
		})
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
		// The default branch's remote form is an ordinary remote-tracking
		// name: no form-specific refusal at classification. Its local branch
		// is deleted here so the case tests that form alone — with a local
		// branch left unheld, the name is a local-branch conflict instead,
		// refused at the classification (the -W and typed-name
		// TrackingNameWithLocalBranch tests) and, on EnsureForBranch, at the
		// commit point (TestRemoteConflictsWithLocal).
		{name: "default branch remote form", arg: "origin/main",
			prepare: func(t *testing.T, dir string) {
				setupDefaultRemote(t, dir)
				git(t, dir, "checkout", "-b", "topic")
				git(t, dir, "branch", "-D", "main")
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
// -b Feat … origin/Feat` would check out origin/feat under the wrong name
// (origin/Main, the default branch form, included). The filesystem probe
// decides whether this test runs; assertions read for-each-ref, never
// refExists.
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

// TestPlanAccessors pins Plan's read accessors — Branch, Tracks, Path,
// Creates, Start — on every classification form, so the create question and
// the launch line describe exactly what Ensure will do. The remote-tracking
// form matters most: Branch is the local branch the plan creates (never the
// typed origin/… name) and Tracks is the ref it follows.
func TestPlanAccessors(t *testing.T) {
	t.Run("new branch", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		p, err := Resolve(dir, "feat")
		if err != nil {
			t.Fatal(err)
		}
		if !p.Creates() {
			t.Error("Creates() = false, want true for a brand-new branch")
		}
		if p.Branch() != "feat" || p.Tracks() != "" {
			t.Errorf("Branch/Tracks = %q/%q, want %q/\"\"", p.Branch(), p.Tracks(), "feat")
		}
		if want := filepath.Join(dir, ".worktrees", "feat"); p.Path() != want {
			t.Errorf("Path() = %q, want %q", p.Path(), want)
		}
		if p.Start() != "from main" {
			t.Errorf("Start() = %q, want %q", p.Start(), "from main")
		}
	})

	t.Run("existing local branch", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		if out, err := runGitCmd(dir, "branch", "topic"); err != nil {
			t.Fatalf("git branch: %v\n%s", err, out)
		}
		p, err := Resolve(dir, "topic")
		if err != nil {
			t.Fatal(err)
		}
		if p.Creates() {
			t.Error("Creates() = true, want false for an existing branch")
		}
		if p.Branch() != "topic" || p.Tracks() != "" {
			t.Errorf("Branch/Tracks = %q/%q, want %q/\"\"", p.Branch(), p.Tracks(), "topic")
		}
		if want := filepath.Join(dir, ".worktrees", "topic"); p.Path() != want {
			t.Errorf("Path() = %q, want %q", p.Path(), want)
		}
		if p.Start() != "" {
			t.Errorf("Start() = %q, want \"\" (no creation, no base)", p.Start())
		}
	})

	t.Run("worktree already holds the branch", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		held, err := EnsureForName(dir, "topic")
		if err != nil {
			t.Fatal(err)
		}
		p, err := Resolve(dir, "topic")
		if err != nil {
			t.Fatal(err)
		}
		if p.Creates() {
			t.Error("Creates() = true, want false for a held branch")
		}
		resolvedPath, _ := filepath.EvalSymlinks(p.Path())
		resolvedHeld, _ := filepath.EvalSymlinks(held)
		if resolvedPath != resolvedHeld {
			t.Errorf("Path() = %q, want the holding worktree %q", p.Path(), held)
		}
	})

	t.Run("the root holds the branch", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		p, err := Resolve(dir, "main")
		if err != nil {
			t.Fatal(err)
		}
		resolvedPath, _ := filepath.EvalSymlinks(p.Path())
		resolvedDir, _ := filepath.EvalSymlinks(dir)
		if resolvedPath != resolvedDir {
			t.Errorf("Path() = %q, want the primary checkout %q", p.Path(), dir)
		}
	})

	t.Run("remote-tracking name", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		setupRemote(t, dir, "seed", "feat")
		p, err := Resolve(dir, "origin/feat")
		if err != nil {
			t.Fatal(err)
		}
		if p.Creates() {
			t.Error("Creates() = true, want false (a tracking branch is not the create question)")
		}
		if p.Branch() != "feat" || p.Tracks() != "origin/feat" {
			t.Errorf("Branch/Tracks = %q/%q, want %q/%q", p.Branch(), p.Tracks(), "feat", "origin/feat")
		}
		if want := filepath.Join(dir, ".worktrees", "feat"); p.Path() != want {
			t.Errorf("Path() = %q, want %q", p.Path(), want)
		}
		if p.Start() != "" {
			t.Errorf("Start() = %q, want \"\" (no creation question)", p.Start())
		}
	})
}

// TestResolveEnsureCreatesWhatWasAsked pins that a Plan carries one
// classification from the question to the creation: Creates and Start match
// WouldCreate, and Ensure makes the worktree and branch the question described.
// It matters because -W and both picker routes now ask and create through the
// same object (#525).
func TestResolveEnsureCreatesWhatWasAsked(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	p, err := Resolve(dir, "brand-new")
	if err != nil {
		t.Fatal(err)
	}
	creates, start, _ := WouldCreate(dir, "brand-new")
	if !p.Creates() || p.Start() != start || creates != p.Creates() || p.Start() != "from main" {
		t.Fatalf("plan = creates %v start %q, WouldCreate = %v %q", p.Creates(), p.Start(), creates, start)
	}
	path, err := p.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, path, "rev-parse", "--abbrev-ref", "HEAD")); got != "brand-new" {
		t.Fatalf("worktree HEAD = %q, want brand-new", got)
	}
}

// TestPlanEnsureRefusesAChangedBase pins that a creation whose base moved
// after the plan was read is refused rather than made from a base other than
// the one the plan read. The stale-prompt race is #525's whole point.
func TestPlanEnsureRefusesAChangedBase(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	p, err := Resolve(dir, "brand-new")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "checkout", "-b", "elsewhere")
	_, err = p.Ensure()
	if err == nil || !strings.Contains(err.Error(), "the repository changed since the plan was read") || !strings.Contains(err.Error(), "from main") || !strings.Contains(err.Error(), "from elsewhere") {
		t.Fatalf("error = %v, want one naming both bases and the plan it read", err)
	}
	if out := gitRun(t, dir, "for-each-ref", "refs/heads/brand-new"); strings.TrimSpace(out) != "" {
		t.Fatalf("branch was created anyway: %q", out)
	}
}

// TestPlanEnsureDoesNotReuseABranchMadeMeanwhile pins that a name approved for
// creation which another session made first fails instead of silently
// becoming a reuse of that branch (git's own refusal at `worktree add -b`).
func TestPlanEnsureDoesNotReuseABranchMadeMeanwhile(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	p, err := Resolve(dir, "brand-new")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "branch", "brand-new")
	if _, err := p.Ensure(); err == nil {
		t.Fatal("Ensure succeeded over a branch created after the plan")
	}
}
