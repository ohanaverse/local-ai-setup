package removal

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// newMovedRemoteWorld is a repo whose origin has branch "topic" one commit
// ahead of main with main's exact tree (a PR merged from it, then more was
// merged into it, and the same work landed on main by another PR): the merged
// PR's head is not the tip, the content is in the base, the row is listed
// after a clean fetch.
func newMovedRemoteWorld(t *testing.T) remoteWorld {
	t.Helper()
	f := newFixture(t)
	mainTip := f.git("rev-parse", "main")
	moved := f.git("commit-tree", "-p", mainTip, "-m", "merged into the branch after its PR merged", "main^{tree}")
	f.git("branch", "topic", moved)
	f.Push("topic")
	// The PR merged an older head (mainTip stands for it); its merge commit is in the base.
	stubRemoteGh(t, mergedPR(mainTip, mainTip), []byte("[]"))
	in := f.inputs()
	in.Fetch = true
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	r := cleanup.Classify(s, cleanup.Options{Now: later()})
	row := f.row(r, cleanup.KindRemote, "topic")
	return remoteWorld{f, moved, s, r, row}
}

// TestRemoteMovedAfterMergeWithEqualTreeIsRemoved lists an origin branch that
// moved after its PR merged but whose tree equals the base's, then removes it
// with the origin approval alone. What: the row is proven on trees-match with
// the moved PR as its proof, the preview needs only Remote, and the push
// deletes origin's branch. Why: nothing is lost, and this is the stacked-PR
// leftover (#74/#75/#76) users could not clear before.
func TestRemoteMovedAfterMergeWithEqualTreeIsRemoved(t *testing.T) {
	w := newMovedRemoteWorld(t)
	if w.row.Status != cleanup.StatusProven || w.row.Evidence != cleanup.EvTreesMatch || w.row.PR == nil || w.row.PR.HeadRefOid == w.tip {
		t.Fatalf("row %s evidence %q pr %+v, want proven on trees-match with a PR whose head is not the tip", w.row.Status, w.row.Evidence, w.row.PR)
	}
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	if it.Approved != (Needs{Remote: true}) {
		t.Fatalf("approved %+v, want only Remote", it.Approved)
	}
	o := one(t, e, it)
	if o.Branch != BrDeleted || w.originHas("topic") != "" {
		t.Fatalf("outcome %+v; origin still has %q", o, w.originHas("topic"))
	}
}

// TestRemoteMovedGateRefusesWhenTheTreeNoLongerMatches rescans the item as a
// moved-PR row whose proof is no longer a whole-tree match. What: refused,
// nothing pushed. Why: the exact-head rule stands for every proof but
// trees-match; the gate must not trust the listing's evidence.
func TestRemoteMovedGateRefusesWhenTheTreeNoLongerMatches(t *testing.T) {
	w := newMovedRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	old := rescan
	rescan = func(g cleanup.Git, p cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		r, err := old(g, p, in, o, tg)
		r.Evidence = cleanup.EvChangesInBase
		return r, err
	}
	t.Cleanup(func() { rescan = old })
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch == BrDeleted || len(*calls) != 0 || o.Worktree != WtRefused || w.originHas("topic") != w.tip {
		t.Fatalf("outcome %+v pushes %v", o, *calls)
	}
}
