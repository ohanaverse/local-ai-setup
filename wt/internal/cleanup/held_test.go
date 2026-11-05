package cleanup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitPath is the absolute path of name in dir's own git dir.
func gitPath(t *testing.T, dir, name string) string {
	t.Helper()
	return gitIn(t, dir, "rev-parse", "--path-format=absolute", "--git-path", name)
}

// writeState writes content to name in dir's git dir (creating parents): a
// forged operation state, for shapes git will not leave on its own.
func writeState(t *testing.T, dir, name, content string) {
	t.Helper()
	p := gitPath(t, dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// factAt is the fresh worktree list's entry for path.
func factAt(t *testing.T, f *fixture, path string) WorktreeFact {
	t.Helper()
	facts, err := ListWorktrees(Git{Dir: f.Dir}, f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range facts {
		if resolve(w.Path) == resolve(path) {
			return w
		}
	}
	t.Fatalf("no worktree at %s", path)
	return WorktreeFact{}
}

// TestHeldRefs pins which refs a worktree holds the way git's own branch
// protection counts them: HEAD's branch, a rebase's head-name (merge and
// apply backends, but not `git am`), the refs the rebase will also move, and
// the branch a bisect started from. A rebase or bisect detaches HEAD, so
// without these a branch git refuses to delete would look free.
func TestHeldRefs(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(f *fixture, p string)
		want  []string
	}{
		{"on its branch", func(*fixture, string) {}, []string{"refs/heads/topic"}},
		{"detached", func(_ *fixture, p string) { gitIn(t, p, "checkout", "--detach") }, nil},
		{"stopped rebase (merge backend)", func(f *fixture, p string) { f.StoppedRebase(p) }, []string{"refs/heads/topic"}},
		{"stopped rebase (apply backend)", func(f *fixture, p string) {
			f.conflictPair(p)
			f.mustFail(p, "", "rebase", "--apply", "main")
		}, []string{"refs/heads/topic"}},
		{"stopped am holds only HEAD's branch", func(f *fixture, p string) { f.StoppedAm(p) }, []string{"refs/heads/topic"}},
		{"bisect from the branch, detached", func(f *fixture, p string) {
			for i := range 3 {
				commitIn(t, p, "b", strings.Repeat("x", i+1), "b")
			}
			gitIn(t, p, "bisect", "start", "HEAD", "main")
		}, []string{"refs/heads/topic"}},
		{"bisect from a detached HEAD", func(_ *fixture, p string) {
			gitIn(t, p, "checkout", "--detach")
			gitIn(t, p, "bisect", "start")
		}, nil},
		{"rebase of a detached HEAD", func(_ *fixture, p string) {
			gitIn(t, p, "checkout", "--detach")
			writeState(t, p, "rebase-merge/head-name", "detached HEAD\n")
		}, nil},
		{"rebase that also moves other refs", func(_ *fixture, p string) {
			gitIn(t, p, "checkout", "--detach")
			z := strings.Repeat("0", 40)
			writeState(t, p, "rebase-merge/head-name", "refs/heads/topic\n")
			writeState(t, p, rebaseMovesFile, "refs/heads/a\n"+z+"\n"+z+"\nrefs/heads/b\n"+z+"\n"+z+"\n")
		}, []string{"refs/heads/topic", "refs/heads/a", "refs/heads/b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			c.setup(f, p)
			got, err := HeldRefs(Git{Dir: f.Dir}, factAt(t, f, p))
			if err != nil {
				t.Fatalf("HeldRefs: %v", err)
			}
			slices.Sort(got)
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("HeldRefs = %q, want %q", got, want)
			}
		})
	}
}

// TestHeldRefsFailsClosed: an operation marker whose holder cannot be read
// or parsed is an error, never "holds nothing": the caller would delete a
// branch the operation will write back.
func TestHeldRefsFailsClosed(t *testing.T) {
	z := strings.Repeat("0", 40)
	for _, c := range []struct {
		name  string
		files map[string]string
	}{
		{"rebase-merge without head-name", map[string]string{"rebase-merge/msgnum": "1\n"}},
		{"head-name not a ref", map[string]string{"rebase-merge/head-name": "topic\n"}},
		{"rebasing apply without head-name", map[string]string{"rebase-apply/rebasing": ""}},
		{"moves file not in records of three", map[string]string{"rebase-merge/head-name": "refs/heads/topic\n", rebaseMovesFile: "refs/heads/a\n" + z + "\n"}},
		{"moves file naming a non-ref", map[string]string{"rebase-merge/head-name": "refs/heads/topic\n", rebaseMovesFile: "a\n" + z + "\n" + z + "\n"}},
		{"empty BISECT_START", map[string]string{"BISECT_START": "\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			gitIn(t, p, "checkout", "--detach")
			for name, content := range c.files {
				writeState(t, p, name, content)
			}
			if got, err := HeldRefs(Git{Dir: f.Dir}, factAt(t, f, p)); err == nil {
				t.Fatalf("HeldRefs = %q, nil error; want an error", got)
			}
		})
	}
}

// TestHeldRefsPrunableWorktree removes a worktree's directory behind git's
// back while a rebase there holds topic: the registration (prunable) still
// holds it, read from its administrative directory.
func TestHeldRefsPrunableWorktree(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	gitIn(t, p, "checkout", "--detach")
	writeState(t, p, "rebase-merge/head-name", "refs/heads/topic\n")
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	w := factAt(t, f, p)
	if !w.Prunable {
		t.Fatalf("precondition: %+v not prunable", w)
	}
	got, err := HeldRefs(Git{Dir: f.Dir}, w)
	if err != nil || !slices.Equal(got, []string{"refs/heads/topic"}) {
		t.Fatalf("HeldRefs = %q, %v; want the rebase's branch", got, err)
	}
}

// TestRescanBranchHeldByRebase: a branch a stopped rebase in a worktree will
// write back (HEAD detached, so the list shows no branch there) is refused by
// the rescan of a branch target, and an unreadable holder fails closed.
func TestRescanBranchHeldByRebase(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		f := newFixture(t)
		p := f.Worktree("topic")
		gitIn(t, p, "checkout", "--detach")
		if unreadable {
			writeState(t, p, "rebase-merge/msgnum", "1\n")
		} else {
			writeState(t, p, "rebase-merge/head-name", "refs/heads/topic\n")
		}
		noGh(t)
		s := gather(t, f)
		_, err := Rescan(Git{Dir: f.Dir}, s, GatherInputs{CWD: f.Dir, OwnRepo: "o/r"}, Options{Now: later()}, Target{Kind: KindBranch, Ref: "refs/heads/topic"})
		want := "rebased or bisected"
		if unreadable {
			want = "could not tell"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("unreadable=%v: Rescan error %v, want one naming %q", unreadable, err, want)
		}
	}
}
