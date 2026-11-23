package removal

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// originWorld lists origin's "topic" (made by prep, which must leave it
// pushed) with gh answering no PR at all, after a clean fetch or not. Unlike
// newRemoteWorld it asserts nothing about the row's status.
func originWorld(t *testing.T, fetch bool, prep func(f *fixture) string) remoteWorld {
	t.Helper()
	f := newFixture(t)
	tip := prep(f)
	stubRemoteGh(t, []byte("[]"), []byte("[]"))
	in := f.inputs()
	in.Fetch = fetch
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	r := cleanup.Classify(s, cleanup.Options{Now: later()})
	return remoteWorld{f, tip, s, r, f.row(r, cleanup.KindRemote, "topic")}
}

// inBase pushes "topic" at main's tip: its content is in the base but no
// merged PR backs it, so after a clean fetch the row is less_sure and known.
func inBase(f *fixture) string {
	f.Branch("topic")
	f.Push("topic")
	return f.git("rev-parse", "main")
}

// ahead pushes "topic" one commit ahead of main with a different tree: work
// that exists nowhere else.
func ahead(f *fixture) string {
	main := f.git("rev-parse", "main")
	empty := f.git("hash-object", "-t", "tree", "/dev/null")
	tip := f.git("commit-tree", "-p", main, "-m", "unique work", empty)
	f.git("branch", "topic", tip)
	f.Push("topic")
	return tip
}

// lessSureItem is the sealed item a caller would hold for the listed row, with
// the given approvals.
func lessSureItem(w remoteWorld, approved Needs) Item {
	return seal(Item{Kind: cleanup.KindRemote, Ref: w.row.Ref, Name: "topic", Saw: cleanup.StatusLessSure, SawTip: w.tip, Approved: approved})
}

// TestRemoteLessSurePreviewNeedsRemoteAndConfirm previews a less_sure, known
// origin row and confirms it with each partial approval. What: it needs
// exactly Remote and Confirm, and neither alone confirms it. Why: B lets the
// user delete an origin branch wt could not prove merged, so both the loss
// approval (--accept-loss) and the public-deletion approval (--remote) are
// required, each per item.
func TestRemoteLessSurePreviewNeedsRemoteAndConfirm(t *testing.T) {
	w := originWorld(t, true, inBase)
	if w.row.Status != cleanup.StatusLessSure || w.row.Unknown {
		t.Fatalf("fixture row %s unknown=%v, want known less_sure (%v)", w.row.Status, w.row.Unknown, w.row.Reasons)
	}
	p := previewOne(t, w.engine(Options{}), ItemFromRow(w.row))
	if p.Approval == ApprovalRefused || p.Needs != (Needs{Remote: true, Confirm: true}) {
		t.Fatalf("preview %s needs %+v reason %q, want Remote+Confirm", p.Approval, p.Needs, p.Reason)
	}
	for name, n := range map[string]Needs{"remote only": {Remote: true}, "confirm only": {Confirm: true}} {
		if _, err := p.Confirmed(n); err == nil {
			t.Errorf("%s confirmed a less-sure origin item", name)
		}
	}
}

// TestRemoteLessSureIsRemovedWithBothApprovals removes a less_sure origin
// branch through preview, confirm and run. What: origin's branch is deleted.
// Why: this is the user's explicit, approved escape for a branch wt cannot
// prove merged.
func TestRemoteLessSureIsRemovedWithBothApprovals(t *testing.T) {
	w := originWorld(t, true, inBase)
	e := w.engine(Options{})
	p := previewOne(t, e, ItemFromRow(w.row))
	it, err := p.Confirmed(Needs{Remote: true, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	o := one(t, e, it)
	if o.Branch != BrDeleted || w.originHas("topic") != "" {
		t.Fatalf("outcome %+v; origin still has %q", o, w.originHas("topic"))
	}
}

// TestRemoteLessSureGateNeedsEachApproval hands the gate hand-sealed items
// with one approval missing. What: refused naming the missing flag, nothing
// pushed. Why: the gate, not the preview, is what stops a caller that skipped
// the preview.
func TestRemoteLessSureGateNeedsEachApproval(t *testing.T) {
	w := originWorld(t, true, inBase)
	e := w.engine(Options{})
	calls := recordPush(t)
	cases := []struct {
		name     string
		approved Needs
		want     string
	}{
		{"no loss approval", Needs{Remote: true}, "--accept-loss"},
		{"no remote approval", Needs{Confirm: true}, "--remote"},
	}
	for _, c := range cases {
		o := one(t, e, lessSureItem(w, c.approved))
		if o.Branch == BrDeleted || o.Worktree != WtRefused || !strings.Contains(o.Reason, c.want) {
			t.Errorf("%s: outcome %+v, want refused naming %s", c.name, o, c.want)
		}
	}
	if len(*calls) != 0 || w.originHas("topic") != w.tip {
		t.Fatalf("a push was issued: %v", *calls)
	}
}

// TestRemoteUniqueWorkIsNeverRemovable gives an origin branch with commits
// that exist nowhere else every approval. What: refused, nothing pushed. Why:
// B widens only less_sure; unique work is what the approvals do not cover.
func TestRemoteUniqueWorkIsNeverRemovable(t *testing.T) {
	w := originWorld(t, true, ahead)
	if w.row.Status != cleanup.StatusUniqueWork {
		t.Fatalf("fixture row is %s, want unique_work", w.row.Status)
	}
	e := w.engine(Options{})
	calls := recordPush(t)
	it := seal(Item{Kind: cleanup.KindRemote, Ref: w.row.Ref, Name: "topic", Saw: cleanup.StatusUniqueWork, SawTip: w.tip, Approved: Needs{Remote: true, Confirm: true, Force: true, Delete: true}})
	if o := one(t, e, it); o.Branch == BrDeleted || o.Worktree != WtRefused {
		t.Fatalf("outcome %+v, want refused", o)
	}
	if p := previewOne(t, e, ItemFromRow(w.row)); p.Approval != ApprovalRefused {
		t.Fatalf("preview %s, want refused", p.Approval)
	}
	if len(*calls) != 0 || w.originHas("topic") != w.tip {
		t.Fatalf("a push was issued: %v", *calls)
	}
}

// TestRemoteLessSureThatIsUnknownIsNeverRemovable lists the row without this
// run's fetch, so wt cannot rule out an open PR that targets the branch.
// What: the row is less_sure and Unknown; every approval is refused. Why:
// fail closed; an approval covers a loss wt can name, not one it cannot see.
func TestRemoteLessSureThatIsUnknownIsNeverRemovable(t *testing.T) {
	w := originWorld(t, false, inBase)
	if w.row.Status != cleanup.StatusLessSure || !w.row.Unknown {
		t.Fatalf("fixture row %s unknown=%v, want unknown less_sure", w.row.Status, w.row.Unknown)
	}
	e := w.engine(Options{})
	calls := recordPush(t)
	o := one(t, e, lessSureItem(w, Needs{Remote: true, Confirm: true, Force: true, Delete: true}))
	if o.Branch == BrDeleted || o.Worktree != WtRefused || !strings.Contains(o.Reason, "could not verify") {
		t.Fatalf("outcome %+v, want refused: could not verify", o)
	}
	if len(*calls) != 0 || w.originHas("topic") != w.tip {
		t.Fatalf("a push was issued: %v", *calls)
	}
}

// TestRemoteLessSureTipMovedIsRetained moves origin's branch after the list.
// What: nothing is pushed and origin keeps the new commit. Why: the live
// read and the lease protect work pushed since the listing, whatever the
// verdict was.
func TestRemoteLessSureTipMovedIsRetained(t *testing.T) {
	w := originWorld(t, true, inBase)
	e := w.engine(Options{})
	p := previewOne(t, e, ItemFromRow(w.row))
	it, err := p.Confirmed(Needs{Remote: true, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	moved := w.moveOrigin(t)
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch != BrRetainedMoved || len(*calls) != 0 || w.originHas("topic") != moved {
		t.Fatalf("outcome %+v pushes %v origin %s", o, *calls, w.originHas("topic"))
	}
}
