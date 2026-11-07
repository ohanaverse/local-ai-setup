package worktree

import (
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
