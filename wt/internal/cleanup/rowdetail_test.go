package cleanup

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// TestReadRowDetailListsLocalWork pins what the detail screen reads for a
// worktree: the commits no remote has (newest first, 8-hex hashes), the
// uncommitted files with their status letter, and the git-ignored paths.
// Why: these three lists are the whole point of the screen — they say what
// removing the row would destroy — and each comes from a different git call,
// so a swapped or misparsed listing would describe someone else's work.
func TestReadRowDetailListsLocalWork(t *testing.T) {
	f := newFixture(t)
	f.Commit("a.txt", "one", "local one")
	f.Commit("a.txt", "two", "local two")
	if err := os.WriteFile(filepath.Join(f.Dir, "a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.Dirty("new.txt")
	f.Ignored(".env.local")

	d := ReadRowDetail(Git{Dir: f.Dir}, Row{Kind: KindWorktree, Name: "repo", Path: f.Dir, Branch: "main"})
	if d.Err != "" {
		t.Fatalf("Err = %q, want none", d.Err)
	}
	if len(d.Commits) != 2 || d.CommitsMore != 0 {
		t.Fatalf("Commits = %+v, want the two unpushed commits", d.Commits)
	}
	for i, want := range []string{"local two", "local one"} {
		if d.Commits[i].Subject != want {
			t.Errorf("commit %d = %+v, want subject %q (newest first)", i, d.Commits[i], want)
		}
		if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(d.Commits[i].Hash) {
			t.Errorf("commit %d hash %q is not 8 hex characters", i, d.Commits[i].Hash)
		}
	}
	if want := []DetailFile{{Status: "M", Path: "a.txt"}, {Status: "??", Path: "new.txt"}}; !equalFiles(d.Uncommitted, want) {
		t.Errorf("Uncommitted = %+v, want %+v", d.Uncommitted, want)
	}
	if d.UncommittedMore != 0 {
		t.Errorf("UncommittedMore = %d, want 0", d.UncommittedMore)
	}
	if len(d.Ignored) != 1 || d.Ignored[0] != ".env.local" {
		t.Errorf("Ignored = %v, want [.env.local]", d.Ignored)
	}
}

// TestReadRowDetailCapsLists pins the cap: past DetailListCap entries a list
// keeps the first 20 and reports how many it left out, rather than growing
// without bound or silently dropping the rest. Why: a worktree with
// thousands of untracked files must not turn the detail screen into a
// multi-second frame or a screen with no end.
func TestReadRowDetailCapsLists(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 45; i++ {
		f.Dirty(fmt.Sprintf("untracked-%02d.txt", i))
	}
	d := ReadRowDetail(Git{Dir: f.Dir}, Row{Kind: KindWorktree, Name: "repo", Path: f.Dir})
	if d.Err != "" {
		t.Fatalf("Err = %q, want none", d.Err)
	}
	if len(d.Uncommitted) != DetailListCap || d.UncommittedMore != 45-DetailListCap {
		t.Errorf("Uncommitted = %d entries (+%d), want %d (+%d)",
			len(d.Uncommitted), d.UncommittedMore, DetailListCap, 45-DetailListCap)
	}
}

// TestReadRowDetailSanitisesNames pins that every string the listing returns
// is one clean line: a path or commit subject carrying a newline, a tab or a
// terminal escape never reaches the screen as a control character (it could
// retitle the terminal or forge a second line of a row). Why: paths are
// user-written data, and the detail screen prints them verbatim.
func TestReadRowDetailSanitisesNames(t *testing.T) {
	f := newFixture(t)
	f.Commit("tab.txt", "x", "subject\twith\ttabs")
	hostile := "new\nline\x1b[31mred"
	if err := os.WriteFile(filepath.Join(f.Dir, hostile), []byte("x"), 0o644); err != nil {
		t.Skipf("this filesystem refuses the hostile name: %v", err)
	}
	d := ReadRowDetail(Git{Dir: f.Dir}, Row{Kind: KindWorktree, Name: "repo", Path: f.Dir})
	if d.Err != "" {
		t.Fatalf("Err = %q, want none", d.Err)
	}
	var all []string
	for _, c := range d.Commits {
		all = append(all, c.Hash, c.Subject)
	}
	for _, u := range d.Uncommitted {
		all = append(all, u.Status, u.Path)
	}
	all = append(all, d.Ignored...)
	if len(all) == 0 {
		t.Fatal("nothing was listed")
	}
	for _, s := range all {
		if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) }) {
			t.Errorf("%q still carries a control character", s)
		}
	}
}

// TestReadRowDetailMissingDirectory pins the failure mode: a row whose
// directory is gone (the worktree moved or was deleted behind wt's back)
// returns an error line in Err with the sections empty, and does not panic.
// Why: the detail screen still opens for such a row — it must say it could
// not read rather than crash the list.
func TestReadRowDetailMissingDirectory(t *testing.T) {
	f := newFixture(t)
	gone := filepath.Join(f.Dir, "no-such-worktree")
	d := ReadRowDetail(Git{Dir: f.Dir}, Row{Kind: KindWorktree, Name: "gone", Path: gone})
	if d.Err == "" {
		t.Fatalf("Err is empty for a missing directory: %+v", d)
	}
	if len(d.Commits)+len(d.Uncommitted)+len(d.Ignored) != 0 {
		t.Errorf("sections are not empty for a missing directory: %+v", d)
	}
	if strings.ContainsFunc(d.Err, func(r rune) bool { return unicode.IsControl(r) }) {
		t.Errorf("Err %q carries a control character", d.Err)
	}
}

// TestReadRowDetailWritesNothing pins the read-only promise: the index bytes,
// every ref and the working tree are identical before and after the call.
// Why: this runs on every keystroke that opens the screen, inside a
// repository the user is working in; a listing that rewrote the index's stat
// cache would make concurrent git calls see a changed repository.
func TestReadRowDetailWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.Commit("a.txt", "one", "local one")
	f.Dirty("new.txt")
	f.Ignored(".env.local")
	gitDir := gitIn(t, f.Dir, "rev-parse", "--absolute-git-dir")

	before := fingerprintRepo(t, f.Dir, gitDir)
	ReadRowDetail(Git{Dir: f.Dir}, Row{Kind: KindWorktree, Name: "repo", Path: f.Dir})
	if after := fingerprintRepo(t, f.Dir, gitDir); after != before {
		t.Errorf("the call changed the repository:\n before %s\n after  %s", before, after)
	}
}

// TestReadRowDetailBranchRow pins a branch row's listing: the commits only it
// holds, and no file lists (a branch has no working tree, so there is nothing
// uncommitted or ignored to show). Why: a branch row that showed a file list
// would be describing the primary checkout's dirt as the branch's.
func TestReadRowDetailBranchRow(t *testing.T) {
	f := newFixture(t)
	f.git("switch", "-c", "topic")
	f.Commit("topic.txt", "work", "topic work")
	f.git("switch", "main")

	d := ReadRowDetail(Git{Dir: f.Dir}, Row{Kind: KindBranch, Name: "topic", Branch: "topic", Ref: "refs/heads/topic"})
	if d.Err != "" {
		t.Fatalf("Err = %q, want none", d.Err)
	}
	if len(d.Commits) != 1 || d.Commits[0].Subject != "topic work" {
		t.Errorf("Commits = %+v, want the one commit only topic holds", d.Commits)
	}
	if len(d.Uncommitted)+len(d.Ignored) != 0 {
		t.Errorf("a branch row lists files: %+v", d)
	}
}

// equalFiles compares two file listings entry by entry.
func equalFiles(a, b []DetailFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fingerprintRepo summarises what the call could change: the index's bytes,
// every ref git names, and every working file's name, size and content hash.
func fingerprintRepo(t *testing.T, dir, gitDir string) string {
	t.Helper()
	sum := sha256.New()
	idx := filepath.Join(gitDir, "index")
	if b, err := os.ReadFile(idx); err == nil {
		fmt.Fprintf(sum, "index %x\n", sha256.Sum256(b))
	}
	fmt.Fprintf(sum, "refs %s\n", gitIn(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"))
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == gitDir {
				return fs.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(sum, "%s %d %x\n", strings.TrimPrefix(p, dir), len(b), sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return fmt.Sprintf("%x", sum.Sum(nil))
}

// TestReadRowDetailUnbornHeadAndBareRow checks that a worktree whose HEAD has
// no commit yet lists its uncommitted files with a "no commits yet" note, and
// that a bare row reads empty with a note, neither as an error: both states
// are ordinary, and the screen should not report them as a failed git call.
func TestReadRowDetailUnbornHeadAndBareRow(t *testing.T) {
	dir := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, b)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := ReadRowDetail(Git{Dir: dir}, Row{Kind: KindWorktree, Path: dir})
	if d.Err != "" || d.Note != "no commits yet" {
		t.Fatalf("unborn: Err = %q, Note = %q; want no error and the note", d.Err, d.Note)
	}
	if len(d.Uncommitted) != 1 || d.Uncommitted[0].Path != "a.txt" {
		t.Errorf("unborn: Uncommitted = %+v, want a.txt", d.Uncommitted)
	}
	b := ReadRowDetail(Git{Dir: dir}, Row{Kind: KindWorktree, Path: dir, Bare: true})
	if b.Err != "" || b.Note == "" || len(b.Commits)+len(b.Uncommitted) != 0 {
		t.Errorf("bare: %+v, want empty with a note and no error", b)
	}
}
