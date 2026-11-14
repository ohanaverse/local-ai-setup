package cleanup

import (
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/dirtsnap"
)

// Prove and WeakSignals take only fully-qualified refs or full commit ids.
const (
	baseRef  = "refs/remotes/origin/main"
	topicRef = "refs/heads/topic"
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
		{"multi-commit squash, main not advanced", func(f *fixture) {
			// main is exactly the squash: the tip's tree is the base's tree.
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("a", "a", "a")
			f.Commit("b", "b", "b")
			f.Squash("topic")
		}, EvTreesMatch, Merged},
		{"commit then revert on the branch, main advanced", func(f *fixture) {
			// The tip's tree is the merge-base's tree: the branch adds nothing.
			// main moves on so the trees-match rung cannot answer first.
			f.Branch("topic")
			f.Checkout("topic")
			f.Commit("secret.txt", "s", "add secret")
			f.Revert("HEAD")
			f.Checkout("main")
			f.Commit("m", "m", "m")
			f.Push("main")
		}, EvNoAddedChanges, Merged},
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
			ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef)
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
	if ev, v := Prove(g, baseRef, topicRef); v != NotMerged {
		t.Fatalf("Prove = (%q, %v), want NotMerged", ev, v)
	}
	if w := WeakSignals(g, baseRef, topicRef); !slices.Contains(w, EvCherryEquivalent) {
		t.Fatalf("WeakSignals = %v, want it to contain %q", w, EvCherryEquivalent)
	}
}

// TestProveGitErrorIsUndetermined asks about a tip that does not exist. Why: a
// git failure is not an answer; treating it as "no" and running on to later
// rungs could end in a proof built on nothing.
func TestProveGitErrorIsUndetermined(t *testing.T) {
	f := newFixture(t)
	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, "refs/heads/no-such-branch"); v != Undetermined || ev != "" {
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
	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); v != NotMerged || ev != "" {
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
	if w := WeakSignals(g, baseRef, topicRef); !slices.Contains(w, EvPatchID) {
		t.Fatalf("WeakSignals = %v, want it to contain %q", w, EvPatchID)
	}

	f2 := newFixture(t)
	f2.Branch("topic")
	f2.Checkout("topic")
	f2.Commit("a", "a", "a")
	if w := WeakSignals(Git{Dir: f2.Dir}, baseRef, topicRef); len(w) != 0 {
		t.Fatalf("WeakSignals on unmerged work = %v, want none", w)
	}
}

// lockConflict builds the shape a merge driver can hide: .gitattributes routes
// `lock` to the `keep` driver, and topic and main both change `lock`.
func lockConflict(t *testing.T) *fixture {
	f := newFixture(t)
	f.Commit(".gitattributes", "lock merge=keep\n", "attributes")
	f.Commit("lock", "base\n", "base lock")
	f.Push("main")
	f.Branch("topic")
	f.Checkout("topic")
	f.Commit("lock", "topic\n", "topic lock")
	f.Checkout("main")
	f.Commit("lock", "main\n", "main lock")
	f.Push("main")
	return f
}

// TestProveMergeDriverIsUndetermined configures a merge driver (`true`, which
// keeps ours) for a file both sides changed. Why: merge-tree honours the
// driver, exits 0 and prints main's tree, so rung 5 would call topic Merged
// while topic's change is nowhere on main; a configured driver must stop the
// ladder at Undetermined.
func TestProveMergeDriverIsUndetermined(t *testing.T) {
	f := lockConflict(t)
	f.git("config", "merge.keep.driver", "true")
	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); v != Undetermined || ev != "" {
		t.Fatalf("Prove = (%q, %v), want (\"\", Undetermined)", ev, v)
	}
}

// TestProveNoMergeDriverProvesNormally runs the same .gitattributes shape
// with no driver configured, and a squash-merged branch beside it. Why: the
// driver guard must fire only on a configured driver; an attribute naming an
// undefined driver merges as text, so rung 5 still answers.
func TestProveNoMergeDriverProvesNormally(t *testing.T) {
	f := lockConflict(t)
	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); v != NotMerged || ev != "" {
		t.Fatalf("conflicting topic: Prove = (%q, %v), want (\"\", NotMerged)", ev, v)
	}

	f2 := newFixture(t)
	f2.Commit(".gitattributes", "lock merge=keep\n", "attributes")
	f2.Push("main")
	f2.Branch("topic")
	f2.Checkout("topic")
	f2.Commit("lock", "topic\n", "topic lock")
	f2.Commit("b", "b", "b")
	f2.Squash("topic")
	f2.Commit("m", "m", "m")
	f2.Push("main")
	if ev, v := Prove(Git{Dir: f2.Dir}, baseRef, topicRef); v != Merged || ev != EvMergeAddsNothing {
		t.Fatalf("squashed topic: Prove = (%q, %v), want (%q, Merged)", ev, v, EvMergeAddsNothing)
	}
}

// TestProveRequiresQualifiedRefs gives a branch `t2` with a unique commit and
// a tag `t2` at main. Why: a short name resolves to the tag, so the branch
// looked like main's own commit (same-commit, Merged); only a fully-qualified
// ref or a full commit id says which object is meant.
func TestProveRequiresQualifiedRefs(t *testing.T) {
	f := newFixture(t)
	f.Branch("t2")
	f.Checkout("t2")
	tip := f.Commit("a", "a", "a")
	f.Checkout("main")
	f.git("tag", "t2")
	g := Git{Dir: f.Dir}

	if ev, v := Prove(g, baseRef, "t2"); v != Undetermined || ev != "" {
		t.Fatalf("bare t2: Prove = (%q, %v), want (\"\", Undetermined)", ev, v)
	}
	if ev, v := Prove(g, "origin/main", "refs/heads/t2"); v != Undetermined || ev != "" {
		t.Fatalf("bare base: Prove = (%q, %v), want (\"\", Undetermined)", ev, v)
	}
	if ev, v := Prove(g, baseRef, "refs/heads/t2"); v != NotMerged || ev != "" {
		t.Fatalf("refs/heads/t2: Prove = (%q, %v), want (\"\", NotMerged)", ev, v)
	}
	if ev, v := Prove(g, baseRef, tip); v != NotMerged || ev != "" {
		t.Fatalf("full sha: Prove = (%q, %v), want (\"\", NotMerged)", ev, v)
	}
	for _, bad := range []string{"refs/heads/t2~1", "refs/heads/t2^", "refs/tags/t2", tip[:12]} {
		if _, v := Prove(g, baseRef, bad); v != Undetermined {
			t.Errorf("Prove(%q) = %v, want Undetermined", bad, v)
		}
	}
	if w := WeakSignals(g, baseRef, "t2"); len(w) != 0 {
		t.Fatalf("bare t2: WeakSignals = %v, want none", w)
	}
}

// TestProveChangesInBaseAfterMainEditedSameFile pins rung 6: when main
// squash-merges the branch and then edits a line adjacent to the branch's
// change, merge-tree conflicts (rung 5 declines) yet the branch's change-set
// reverse-applies onto main's tree, so the ladder must answer
// (changes-in-base, Merged) instead of NotMerged. Why: without the rung a
// squash-merged row whose file main kept editing reads as unique work — the
// exact false negative containment exists to close.
func TestProveChangesInBaseAfterMainEditedSameFile(t *testing.T) {
	f := newFixture(t)
	adjacentSquash(f)

	// Sanity: rung 5 really declines this layout (the folded-region conflict
	// an adjacent later edit causes), so the proof can only come from rung 6.
	f.mustFail(f.Dir, "", "merge-tree", "--write-tree", "main", "topic")

	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); ev != EvChangesInBase || v != Merged {
		t.Fatalf("Prove = (%q, %v), want (%q, Merged)", ev, v, EvChangesInBase)
	}
}

// TestProveRevertedChangeStaysNotMerged pins rung 6 failing closed on a
// revert: after main reverts the squashed change the text is gone from the
// base, containment says no, and the ladder must stay NotMerged. Why: a
// reverted change must never prove — that is the exact defeat of the weak
// cherry/patch-id signals rung 6 must not repeat.
func TestProveRevertedChangeStaysNotMerged(t *testing.T) {
	f := newFixture(t)
	base := containedLines()
	f.Commit("a.txt", containedRender(base), "add a.txt")
	f.Branch("topic")
	f.Checkout("topic")
	ls := containedCopy(base)
	ls[0] = "CHANGED1"
	f.Commit("a.txt", containedRender(ls), "topic changes line 1")
	f.Squash("topic")
	f.Revert(f.git("rev-parse", "HEAD"))
	f.Push("main")

	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); v != NotMerged || ev != "" {
		t.Fatalf("Prove = (%q, %v), want (\"\", NotMerged): main reverted the change, it is not in the base", ev, v)
	}
}

// TestProveMergeDriverStillUndetermined pins that a configured merge driver
// stops the ladder before containment: rung 6 runs only after rung 5 answered
// NotMerged, and a driver makes rung 5 unanswerable, so an answerable
// containment check must never overwrite Undetermined with a proof (the
// driver could be hiding a real conflict).
func TestProveMergeDriverStillUndetermined(t *testing.T) {
	f := newFixture(t)
	adjacentSquash(f)
	f.git("config", "merge.keep.driver", "true")

	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); v != Undetermined || ev != "" {
		t.Fatalf("Prove = (%q, %v), want (\"\", Undetermined): a configured merge driver stops the ladder", ev, v)
	}
}

// TestProveContainmentUnknownStaysNotMerged pins that an unknown containment
// answer is NotMerged, never Merged: over the caps (or any git failure) the
// ladder must not promote "could not tell" to proof. Why: every new signal
// fails closed — a false Merged loses work.
func TestProveContainmentUnknownStaysNotMerged(t *testing.T) {
	f := newFixture(t)
	adjacentSquash(f)
	old := dirtLimits
	dirtLimits = dirtsnap.Limits{MaxFiles: DirtMaxFiles, MaxBytes: 4}
	t.Cleanup(func() { dirtLimits = old })

	if ev, v := Prove(Git{Dir: f.Dir}, baseRef, topicRef); v != NotMerged || ev != "" {
		t.Fatalf("Prove = (%q, %v), want (\"\", NotMerged): containment unknown over the caps must not prove", ev, v)
	}
}
