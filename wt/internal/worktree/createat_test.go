package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithTwoCommits is a fresh repo whose HEAD is the second commit; it
// returns the repo dir, the first commit's full id and the second's.
func repoWithTwoCommits(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = t.TempDir()
	gitInit(t, dir)
	first = strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	gitOut(t, dir, "commit", "--allow-empty", "-m", "two")
	second = strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
	return dir, first, second
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := runGitCmd(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return string(b)
}

// TestCreateAtStartsTheBranchAtTheGivenCommit checks that CreateAt makes the
// branch and its worktree at the commit it is handed, not at the HEAD of the
// directory it runs in. Why: a rescue must put the new branch on the source
// worktree's exact HEAD, which is generally not the primary checkout's.
func TestCreateAtStartsTheBranchAtTheGivenCommit(t *testing.T) {
	dir, first, _ := repoWithTwoCommits(t)

	path, err := CreateAt(dir, "rescued/x", first)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".worktrees", "rescued-x"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if got := strings.TrimSpace(gitOut(t, path, "rev-parse", "HEAD")); got != first {
		t.Errorf("new worktree HEAD = %s, want %s", got, first)
	}
	if got := strings.TrimSpace(gitOut(t, path, "symbolic-ref", "HEAD")); got != "refs/heads/rescued/x" {
		t.Errorf("HEAD = %s, want refs/heads/rescued/x", got)
	}
	// git config exits 1 when nothing matches, which is the answer wanted.
	if out, _ := runGitCmd(dir, "config", "--get-regexp", `^branch\.rescued/x\.(remote|merge)$`); len(out) != 0 {
		t.Errorf("the new branch tracks something: %q", out)
	}
}

// TestCreateAtRefusesAnExistingBranchOrPathAndCreatesNothing checks the
// three collisions (a branch of that name, a path another branch's name
// flattens onto, a directory already there) each error before git runs, and
// leave no stray branch. Why: git worktree add -b creates the branch first,
// so a late failure would leave one behind, and a rerun must never reuse the
// worktree of an earlier rescue.
func TestCreateAtRefusesAnExistingBranchOrPathAndCreatesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  string
	}{
		{"branch exists", func(t *testing.T, dir string) { gitOut(t, dir, "branch", "rescued/x") }, "already exists"},
		{"worktree holds it", func(t *testing.T, dir string) {
			if _, err := CreateAt(dir, "rescued/x", strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))); err != nil {
				t.Fatal(err)
			}
		}, "already exists"},
		{"path taken by another branch", func(t *testing.T, dir string) {
			gitOut(t, dir, "worktree", "add", "-b", "rescued-x", filepath.Join(dir, ".worktrees", "rescued-x"))
		}, "already in use"},
		{"directory exists", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".worktrees", "rescued-x"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "already exists"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, first, _ := repoWithTwoCommits(t)
			c.setup(t, dir)
			before := gitOut(t, dir, "for-each-ref")

			_, err := CreateAt(dir, "rescued/x", first)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
			if after := gitOut(t, dir, "for-each-ref"); after != before {
				t.Errorf("refs changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// TestCreateAtRefusesAStartThatIsNotACommit checks a start that is not a full
// commit id (a short id, a branch name, a tree id, junk) is refused with no
// branch made. Why: the start is the one fact the rescue promises, so it is
// never resolved from a name that could move.
func TestCreateAtRefusesAStartThatIsNotACommit(t *testing.T) {
	for _, start := range []string{"", "HEAD", "main", "abc123", strings.Repeat("0", 40), strings.Repeat("g", 40)} {
		t.Run(start, func(t *testing.T) {
			dir, _, _ := repoWithTwoCommits(t)
			if _, err := CreateAt(dir, "rescued/y", start); err == nil {
				t.Fatalf("CreateAt with start %q succeeded", start)
			}
			if out := gitOut(t, dir, "for-each-ref", "refs/heads/rescued"); out != "" {
				t.Errorf("a branch was made: %s", out)
			}
		})
	}
	t.Run("tree id", func(t *testing.T) {
		dir, _, _ := repoWithTwoCommits(t)
		tree := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD^{tree}"))
		if _, err := CreateAt(dir, "rescued/y", tree); err == nil {
			t.Fatal("CreateAt accepted a tree id")
		}
	})
}

// TestCheckNewMatchesCreateAt checks the read-only CheckNew answers with the
// same path CreateAt then makes and refuses the same names. Why: the rescue
// preflight must refuse before anything is written, with CreateAt's own rules.
func TestCheckNewMatchesCreateAt(t *testing.T) {
	dir, first, _ := repoWithTwoCommits(t)

	want, err := CheckNew(dir, "rescued/z")
	if err != nil {
		t.Fatal(err)
	}
	got, err := CreateAt(dir, "rescued/z", first)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("CreateAt path %q, CheckNew path %q", got, want)
	}
	if _, err := CheckNew(dir, "rescued/z"); err == nil {
		t.Error("CheckNew accepted a name CreateAt just used")
	}
	if _, err := CheckNew(dir, "bad name~"); err == nil {
		t.Error("CheckNew accepted an invalid branch name")
	}
}

// TestCheckNewRefusesNamesGitCannotUseAsABranch checks CheckNew and CreateAt
// refuse "@", a refs/ prefix and other invalid ref names, and leave no branch
// behind. Why: git worktree add -b creates the ref before it can fail, and
// "@" or refs/heads/z passed the looser name check, so a refusal said nothing
// was created while a stray ref existed.
func TestCheckNewRefusesNamesGitCannotUseAsABranch(t *testing.T) {
	dir, first, _ := repoWithTwoCommits(t)
	before := gitOut(t, dir, "for-each-ref")
	for _, name := range []string{"@", "refs/heads/z", "refs/z", "a..b", "-x", "a b"} {
		if _, err := CheckNew(dir, name); err == nil {
			t.Errorf("CheckNew(%q) accepted the name", name)
		}
		if _, err := CreateAt(dir, name, first); err == nil {
			t.Errorf("CreateAt(%q) accepted the name", name)
		}
	}
	if after := gitOut(t, dir, "for-each-ref"); after != before {
		t.Errorf("refs changed:\n%s\n--\n%s", before, after)
	}
}

// TestCreateAtTypesItsFailures checks a refused name is a *NotCreatedError
// with NameUnavailable (and no branch of ours), an unusable start a
// *NotCreatedError without it, and a `git worktree add` that fails after
// making the branch an *AddFailedError whose BranchLeft says so. Why: the
// rescue must tell "someone else holds the name" from "we left a branch
// behind" by type, not by looking for the ref afterwards.
func TestCreateAtTypesItsFailures(t *testing.T) {
	dir, first, _ := repoWithTwoCommits(t)
	gitOut(t, dir, "branch", "taken", first)

	var nc *NotCreatedError
	_, err := CreateAt(dir, "taken", first)
	if !errors.As(err, &nc) || !nc.NameUnavailable {
		t.Fatalf("taken name: %v, want NotCreatedError{NameUnavailable}", err)
	}
	_, err = CreateAt(dir, "fresh", "abc")
	if !errors.As(err, &nc) || nc.NameUnavailable {
		t.Fatalf("bad start: %v, want NotCreatedError without NameUnavailable", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(dir, ".worktrees")
	if err := os.Chmod(wt, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(wt, 0o755) })
	if os.WriteFile(filepath.Join(wt, "probe"), nil, 0o644) == nil {
		t.Skip("directory writable despite chmod (root?)")
	}
	var af *AddFailedError
	_, err = CreateAt(dir, "rescued/left", first)
	if !errors.As(err, &af) || !af.BranchLeft {
		t.Fatalf("failed add: %v, want AddFailedError{BranchLeft}", err)
	}
}

// TestCreateDetachedAtLeavesHeadDetachedAtStart checks the worktree is made at
// .worktrees/<branch> with a detached HEAD at start while the primary still
// holds the branch, and that the branch ref and the primary's HEAD do not
// move. Why: restore-root needs a copy of a branch's tip that git would
// refuse as a second checkout of the branch.
func TestCreateDetachedAtLeavesHeadDetachedAtStart(t *testing.T) {
	dir, _, second := repoWithTwoCommits(t)
	branch := strings.TrimSpace(gitOut(t, dir, "symbolic-ref", "--short", "HEAD"))
	refBefore := gitOut(t, dir, "for-each-ref")
	headBefore := gitOut(t, dir, "symbolic-ref", "HEAD")

	path, err := CreateDetachedAt(dir, branch, branch, second)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, ".worktrees", branch); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := runGitCmd(path, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Error("HEAD of the new worktree is not detached")
	}
	if got := strings.TrimSpace(gitOut(t, path, "rev-parse", "HEAD")); got != second {
		t.Errorf("HEAD = %s, want %s", got, second)
	}
	if after := gitOut(t, dir, "for-each-ref"); after != refBefore {
		t.Errorf("refs changed:\n%s\n--\n%s", refBefore, after)
	}
	if after := gitOut(t, dir, "symbolic-ref", "HEAD"); after != headBefore {
		t.Errorf("primary HEAD changed: %s -> %s", headBefore, after)
	}
}

// TestCreateDetachedAtRefusesAndCreatesNothing checks every bad input (a
// start that is not a full id or not a commit or not the branch tip, an
// absent branch, an existing path, a path another branch holds, a dirName
// that flattens to "..") is a *NotCreatedError, makes no directory or ref, and
// sets NameUnavailable only for the name and path rows. Why: the add must
// refuse before anything exists, and the caller tells a taken name from a
// bad start by type.
func TestCreateDetachedAtRefusesAndCreatesNothing(t *testing.T) {
	cases := []struct {
		name     string
		dirName  string
		branch   string
		start    func(first, second, tree string) string
		setup    func(t *testing.T, dir string)
		nameProb bool
	}{
		{"short start", "main", "main", func(_, s, _ string) string { return s[:12] }, nil, false},
		{"not a commit", "main", "main", func(_, _, tr string) string { return tr }, nil, false},
		{"tip differs", "main", "main", func(f, _, _ string) string { return f }, nil, false},
		{"branch absent", "nope", "nope", func(_, s, _ string) string { return s }, nil, false},
		{"path exists", "main", "main", func(_, s, _ string) string { return s }, func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, ".worktrees", "main"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"path used by another branch", "feature-x", "feature/x", func(_, s, _ string) string { return s }, func(t *testing.T, dir string) {
			gitOut(t, dir, "worktree", "add", "-b", "feature-x", filepath.Join(dir, ".worktrees", "feature-x"))
		}, true},
		{"dirName flattens to ..", "..", "main", func(_, s, _ string) string { return s }, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, first, second := repoWithTwoCommits(t)
			if c.branch == "feature/x" {
				// the branch must exist at the tip for the path row to be the failure
				gitOut(t, dir, "branch", "-f", "feature/x", second)
			}
			tree := strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD^{tree}"))
			if c.setup != nil {
				c.setup(t, dir)
			}
			refs := gitOut(t, dir, "for-each-ref")
			wts := gitOut(t, dir, "worktree", "list", "--porcelain")
			ents := dirEntries(t, filepath.Join(dir, ".worktrees"))

			_, err := CreateDetachedAt(dir, c.dirName, c.branch, c.start(first, second, tree))
			var nc *NotCreatedError
			if !errors.As(err, &nc) {
				t.Fatalf("err = %v, want *NotCreatedError", err)
			}
			if nc.NameUnavailable != c.nameProb {
				t.Errorf("NameUnavailable = %v, want %v", nc.NameUnavailable, c.nameProb)
			}
			if after := gitOut(t, dir, "for-each-ref"); after != refs {
				t.Errorf("refs changed")
			}
			if after := gitOut(t, dir, "worktree", "list", "--porcelain"); after != wts {
				t.Errorf("worktrees changed")
			}
			if after := dirEntries(t, filepath.Join(dir, ".worktrees")); after != ents {
				t.Errorf("directory changed: %q -> %q", ents, after)
			}
		})
	}
}

// dirEntries lists a directory's names joined, empty when it is absent.
func dirEntries(t *testing.T, d string) string {
	t.Helper()
	es, err := os.ReadDir(d)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// TestCheckNewDetachedMatchesCreateDetachedAt checks the read-only check gives
// the path the create then makes, refuses the same name afterwards, and writes
// nothing itself. Why: restore-root's preflight must refuse with the creator's
// own rules before anything is written.
func TestCheckNewDetachedMatchesCreateDetachedAt(t *testing.T) {
	dir, _, second := repoWithTwoCommits(t)
	branch := strings.TrimSpace(gitOut(t, dir, "symbolic-ref", "--short", "HEAD"))
	refs := gitOut(t, dir, "for-each-ref")

	want, err := CheckNewDetached(dir, branch, branch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".worktrees")); err == nil {
		t.Error("CheckNewDetached created .worktrees")
	}
	if after := gitOut(t, dir, "for-each-ref"); after != refs {
		t.Error("CheckNewDetached changed refs")
	}
	got, err := CreateDetachedAt(dir, branch, branch, second)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("create path %q, check path %q", got, want)
	}
	if _, err := CheckNewDetached(dir, branch, branch); err == nil {
		t.Error("CheckNewDetached accepted a path just used")
	}
	if _, err := CheckNewDetached(dir, "..", branch); err == nil {
		t.Error("CheckNewDetached accepted a dirName flattening to ..")
	}
}

// TestCreateDetachedAtRunsNoHooks checks a post-checkout hook does not run
// for the detached add. Why: user code must not rewrite or fail a checkout
// that has to be a faithful copy of the branch tip.
func TestCreateDetachedAtRunsNoHooks(t *testing.T) {
	dir, _, second := repoWithTwoCommits(t)
	branch := strings.TrimSpace(gitOut(t, dir, "symbolic-ref", "--short", "HEAD"))
	marker := filepath.Join(t.TempDir(), "ran")
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ntouch '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateDetachedAt(dir, branch, branch, second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); err == nil {
		t.Error("the post-checkout hook ran")
	}
}

// TestCreateDetachedAtTypesItsFailure checks a `git worktree add` that fails
// is an *AddFailedError with BranchLeft false and DirLeft matching what
// exists. Why: the caller reports leftovers by type, and this creator never
// makes a branch.
func TestCreateDetachedAtTypesItsFailure(t *testing.T) {
	dir, _, second := repoWithTwoCommits(t)
	branch := strings.TrimSpace(gitOut(t, dir, "symbolic-ref", "--short", "HEAD"))
	wt := filepath.Join(dir, ".worktrees")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wt, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(wt, 0o755) })
	if os.WriteFile(filepath.Join(wt, "probe"), nil, 0o644) == nil {
		t.Skip("directory writable despite chmod (root?)")
	}
	_, err := CreateDetachedAt(dir, branch, branch, second)
	var af *AddFailedError
	if !errors.As(err, &af) {
		t.Fatalf("err = %v, want *AddFailedError", err)
	}
	if af.BranchLeft {
		t.Error("BranchLeft set")
	}
	_, statErr := os.Lstat(filepath.Join(wt, branch))
	if af.DirLeft != (statErr == nil) {
		t.Errorf("DirLeft = %v, but exists = %v", af.DirLeft, statErr == nil)
	}
}
