package cleanup

import (
	"slices"
	"strings"
	"testing"
)

// TestBranchesHolding pins the shared read: only refs/heads and refs/remotes
// count, a symref like origin/HEAD does not, and no holder is an empty
// answer, not an error. Why: the cleanup list and restore-root's Preflight
// both rely on it, so their answers cannot drift apart (#749).
func TestBranchesHolding(t *testing.T) {
	t.Run("a feature branch", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "-c", "feat")
		sha := f.Commit("a.txt", "a", "feat work")
		got, err := BranchesHolding(Git{Dir: f.Dir}, sha)
		if err != nil || !slices.Equal(got, []string{"refs/heads/feat"}) {
			t.Fatalf("BranchesHolding = %v, %v; want [refs/heads/feat]", got, err)
		}
	})
	t.Run("only a remote-tracking ref", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "-c", "feat")
		sha := f.Commit("a.txt", "a", "feat work")
		f.Push("feat")
		f.git("switch", "-q", "--detach", "HEAD")
		f.git("branch", "-D", "feat")
		got, err := BranchesHolding(Git{Dir: f.Dir}, sha)
		if err != nil || !slices.Equal(got, []string{"refs/remotes/origin/feat"}) {
			t.Fatalf("BranchesHolding = %v, %v; want [refs/remotes/origin/feat]", got, err)
		}
	})
	t.Run("a tag is not a branch", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "--detach", "HEAD")
		sha := f.Commit("a.txt", "a", "tagged")
		f.git("tag", "keep", sha)
		got, err := BranchesHolding(Git{Dir: f.Dir}, sha)
		if err != nil || len(got) != 0 {
			t.Fatalf("BranchesHolding = %v, %v; want none", got, err)
		}
	})
	t.Run("the origin/HEAD symref is skipped", func(t *testing.T) {
		f := newFixture(t)
		f.git("remote", "set-head", "origin", "main")
		sha := f.git("rev-parse", "HEAD")
		got, err := BranchesHolding(Git{Dir: f.Dir}, sha)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			if r == "refs/remotes/origin/HEAD" {
				t.Fatalf("symref listed: %v", got)
			}
		}
	})
	t.Run("an unknown commit is an error", func(t *testing.T) {
		f := newFixture(t)
		if got, err := BranchesHolding(Git{Dir: f.Dir}, "0123456789abcdef0123456789abcdef01234567"); err == nil {
			t.Fatalf("BranchesHolding = %v, nil; want an error", got)
		}
	})
}

// TestCheckGatesHeadHeld pins when Gates.HeadHeld is read and what it says.
// Why: the list offers R from it (#749), so it must follow the command's own
// reachability and be read only for a detached root.
func TestCheckGatesHeadHeld(t *testing.T) {
	gates := func(t *testing.T, f *fixture, dir string) Gates {
		t.Helper()
		w := factFor(t, f, t.TempDir(), dir)
		g, err := CheckGates(Git{Dir: f.Dir}, w, GateInputs{Base: baseRef})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	t.Run("held only by a feature branch", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "-c", "feat")
		f.Commit("a.txt", "a", "feat work")
		f.git("switch", "-q", "--detach", "feat")
		g := gates(t, f, f.Dir)
		if g.HeadHeld != TriYes || !g.DetachedUnique {
			t.Errorf("HeadHeld = %v, DetachedUnique = %v; want TriYes, true", g.HeadHeld, g.DetachedUnique)
		}
	})
	t.Run("held by the default", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "--detach", "HEAD")
		if g := gates(t, f, f.Dir); g.HeadHeld != TriYes {
			t.Errorf("HeadHeld = %v, want TriYes", g.HeadHeld)
		}
	})
	t.Run("held by nothing", func(t *testing.T) {
		f := newFixture(t)
		f.DetachOrphan(f.Dir)
		if g := gates(t, f, f.Dir); g.HeadHeld != TriNo {
			t.Errorf("HeadHeld = %v, want TriNo", g.HeadHeld)
		}
	})
	t.Run("a detached linked worktree is not read", func(t *testing.T) {
		f := newFixture(t)
		wt := f.Worktree("topic")
		f.DetachOrphan(wt)
		if g := gates(t, f, wt); g.HeadHeld != TriUnknown {
			t.Errorf("HeadHeld = %v, want TriUnknown", g.HeadHeld)
		}
	})
	t.Run("a root on a branch is not read", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "-c", "feat")
		if g := gates(t, f, f.Dir); g.HeadHeld != TriUnknown {
			t.Errorf("HeadHeld = %v, want TriUnknown", g.HeadHeld)
		}
	})
}

// TestClassifyCopiesHeadHeldOnlyWhenGatesRead pins that Row.HeadHeld is the
// gates' fact when they read cleanly, and stays unknown when they errored.
func TestClassifyCopiesHeadHeldOnlyWhenGatesRead(t *testing.T) {
	head := strings.Repeat("e", 40)
	root := func(ws WorktreeState) Row {
		ws.Fact = WorktreeFact{Path: "/r", Head: head, Detached: true, Primary: true}
		s := handSnapshot()
		s.Worktrees = []WorktreeState{ws}
		for _, r := range Classify(s, Options{Now: later()}).AllRows() {
			if r.Primary {
				return r
			}
		}
		t.Fatal("no root row")
		return Row{}
	}
	if r := root(WorktreeState{Gates: Gates{HeadHeld: TriYes, DetachedUnique: true}}); r.HeadHeld != TriYes || !r.Losses.DetachedUnique {
		t.Errorf("clean gates: HeadHeld = %v, DetachedUnique = %v", r.HeadHeld, r.Losses.DetachedUnique)
	}
	if r := root(WorktreeState{Gates: Gates{HeadHeld: TriNo}, GatesErr: "boom"}); r.HeadHeld != TriUnknown {
		t.Errorf("gates error: HeadHeld = %v, want TriUnknown", r.HeadHeld)
	}
}
