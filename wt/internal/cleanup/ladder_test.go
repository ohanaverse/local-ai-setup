package cleanup

import (
	"slices"
	"testing"
)

// TestProveLadder runs Prove over one fixture per merge style and checks the
// rung that proves it (or that nothing does). Why: each style is caught by a
// different rung; a style that slips to a later rung, or a branch with unique
// work that gets a proof, is a wrong verdict a user would act on.
func TestProveLadder(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		ev    Evidence
		v     Verdict
	}{
		{"true merge", func(f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.Merge("topic")
			f.Push("main")
		}, EvAncestor, Merged},
		{"fast-forward", func(f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Checkout("main")
			f.FastForward("topic")
			f.Commit("m", "m", "m") // main moves on, so the tip is not the base
			f.Push("main")
		}, EvAncestor, Merged},
		{"1:1 rebase-merge", func(f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			a := f.Commit("a", "a", "a")
			b := f.Commit("b", "b", "b")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.CherryPick(a, b)
			f.Push("main")
		}, EvMergeAddsNothing, Merged},
		{"multi-commit squash, main advanced", func(f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Commit("b", "b", "b")
			f.Squash("topic")
			f.Commit("m", "m", "m")
			f.Push("main")
		}, EvMergeAddsNothing, Merged},
		{"commit main lacks", func(f *fixture) {
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.Push("main")
		}, "", NotMerged},
		{"merge commit with unique resolution", func(f *fixture) {
			// topic's only own commit is cherry-picked to main, so git cherry
			// sees nothing unique; the merge resolution is the unique work.
			f.Commit("x", "base\n", "base x")
			f.Push("main")
			f.Branch("topic")
			f.Checkout("topic")
			x := f.Commit("x", "topic\n", "topic x")
			f.Checkout("main")
			// main moves first: a cherry-pick onto the same parent in the
			// same second would reproduce x's sha exactly.
			f.Commit("m", "m", "m")
			f.CherryPick(x)
			f.Commit("x", "main\n", "main x")
			f.Push("main")
			f.Checkout("topic")
			f.MergeResolving("main", "x", "resolved\n")
		}, "", NotMerged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.setup(f)
			ev, v := Prove(Git{Dir: f.Dir}, "origin/main", "topic")
			if ev != c.ev || v != c.v {
				t.Fatalf("Prove = (%q, %v), want (%q, %v)", ev, v, c.ev, c.v)
			}
		})
	}
}

// TestProveSquashThenRevertIsNotMerged squashes topic into main, then reverts
// it on main. Why: the content is gone from main, so deleting topic loses it;
// git cherry still calls every commit equivalent, so this proves cherry alone
// would have lied and must stay a weak signal. One commit, so the squash is
// patch-identical to it and cherry marks it "-".
func TestProveSquashThenRevertIsNotMerged(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Checkout("topic")
	f.Commit("a", "a", "a")
	f.Squash("topic")
	f.Revert("HEAD")
	f.Push("main")
	g := Git{Dir: f.Dir}
	if ev, v := Prove(g, "origin/main", "topic"); v != NotMerged {
		t.Fatalf("Prove = (%q, %v), want NotMerged", ev, v)
	}
	if w := WeakSignals(g, "origin/main", "topic"); !slices.Contains(w, EvCherryEquivalent) {
		t.Fatalf("WeakSignals = %v, want it to contain %q", w, EvCherryEquivalent)
	}
}

// TestProveGitErrorIsUndetermined asks about a tip that does not exist. Why: a
// git failure is not an answer; treating it as "no" and running on to later
// rungs could end in a proof built on nothing.
func TestProveGitErrorIsUndetermined(t *testing.T) {
	f := newFixture(t)
	if ev, v := Prove(Git{Dir: f.Dir}, "origin/main", "no-such-branch"); v != Undetermined || ev != "" {
		t.Fatalf("Prove = (%q, %v), want (\"\", Undetermined)", ev, v)
	}
}

// TestProveMergeTreeExitCodes gives merge-tree a branch that conflicts with
// main. Why: merge-tree exits 1 on a conflict, a clear "the branch changes
// something main changed differently" — NotMerged, not an error.
func TestProveMergeTreeExitCodes(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Checkout("topic")
	f.Commit("x", "topic", "topic x")
	f.Checkout("main")
	f.Commit("x", "main", "main x")
	f.Push("main")
	if ev, v := Prove(Git{Dir: f.Dir}, "origin/main", "topic"); v != NotMerged || ev != "" {
		t.Fatalf("Prove = (%q, %v), want (\"\", NotMerged)", ev, v)
	}
}

// TestWeakSignalsPatchID checks that a squash commit on main matching the
// branch's whole diff is reported as EvPatchID, and that unmerged work gets no
// weak signal. Why: the signal is shown as LESS SURE; a missing or a false one
// changes what the user is told.
func TestWeakSignalsPatchID(t *testing.T) {
	f := newFixture(t)
	f.Branch("topic")
	f.Checkout("topic")
	f.Commit("a", "a", "a")
	f.Commit("b", "b", "b")
	f.Squash("topic")
	g := Git{Dir: f.Dir}
	if w := WeakSignals(g, "origin/main", "topic"); !slices.Contains(w, EvPatchID) {
		t.Fatalf("WeakSignals = %v, want it to contain %q", w, EvPatchID)
	}

	f2 := newFixture(t)
	f2.Branch("topic")
	f2.Checkout("topic")
	f2.Commit("a", "a", "a")
	if w := WeakSignals(Git{Dir: f2.Dir}, "origin/main", "topic"); len(w) != 0 {
		t.Fatalf("WeakSignals on unmerged work = %v, want none", w)
	}
}
