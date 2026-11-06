package removal

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// keptAndLoose is the reviewer's TestZZNaiveUnion repository: a worktree
// kept on an UNPUSHED unmerged branch (its preview keeps the branch and
// offers -D) and a branch-only item loose that needs -D. It returns the
// engine, both previews and kept's tip.
func keptAndLoose(t *testing.T) (*fixture, Engine, []ItemPreview, string) {
	t.Helper()
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("kept")
	f.Commit(p, "k.txt", "work", "unpushed work") // never pushed
	f.unmergedBranch("loose")
	s, r := f.report()
	e := f.engine(s, f.inputs(), Options{})
	ps := e.Preview([]Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "kept")), ItemFromRow(f.row(r, cleanup.KindBranch, "loose"))})
	if !ps[0].CanDeleteBranch || ps[0].Needs.Delete || !ps[1].Needs.Delete {
		t.Fatalf("precondition: kept can delete %v needs %+v; loose needs %+v", ps[0].CanDeleteBranch, ps[0].Needs, ps[1].Needs)
	}
	return f, e, ps, f.row(r, cleanup.KindWorktree, "kept").Tip
}

// TestConfirmedRefusesUnionForOfferedBranch (reviewer's TestZZNaiveUnion,
// Ruling M) confirms both previews with the union of their Needs, as a
// caller holding one batch-wide -D would: kept only OFFERS -D, so Confirmed
// refuses it (Delete is accepted only where the preview needs it), and
// kept's unpushed branch, the only home of its commit, survives.
func TestConfirmedRefusesUnionForOfferedBranch(t *testing.T) {
	f, _, ps, tip := keptAndLoose(t)
	union := Needs{Confirm: true, Delete: true}
	if _, err := ps[0].Confirmed(union); err == nil {
		t.Error("kept: Confirmed(union) accepted -D its preview only offers")
	}
	if _, err := ps[1].Confirmed(union); err != nil {
		t.Errorf("loose: Confirmed(union) = its own needs, refused: %v", err)
	}
	if got, ok := f.refTip("refs/heads/kept"); !ok || got != tip {
		t.Error("refs/heads/kept touched")
	}
}

// TestConfirmedDeletingBranchOptsInOneItem opts kept in to deleting its
// unmerged branch with the explicit per-item call and confirms loose with
// its own needs: the sealed kept item's Run deletes kept's branch, and the
// opt-in reaches no other item (a second worktree on an unmerged branch,
// confirmed plainly, keeps its branch).
func TestConfirmedDeletingBranchOptsInOneItem(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedPushed("kept")
	f.unmergedPushed("other")
	s, r := f.report()
	otherTip := f.row(r, cleanup.KindWorktree, "other").Tip
	e := f.engine(s, f.inputs(), Options{})
	ps := e.Preview([]Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "kept")), ItemFromRow(f.row(r, cleanup.KindWorktree, "other"))})
	kept, err := ps[0].ConfirmedDeletingBranch(Needs{Confirm: true, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := ps[1].Confirmed(ps[1].Needs)
	if err != nil {
		t.Fatal(err)
	}
	if !kept.confirmed() || !kept.Approved.Delete {
		t.Fatalf("kept item %+v: want sealed with Delete", kept)
	}
	out := e.Run([]Item{kept, other}, nil)
	if out[0].Worktree != WtRemoved || out[0].Branch != BrDeleted {
		t.Errorf("kept: %s/%s %q, want removed / deleted", out[0].Worktree, out[0].Branch, out[0].Reason)
	}
	if out[1].Worktree != WtRemoved || out[1].Branch != BrRetainedUnmerged {
		t.Errorf("other: %s/%s %q, want removed / retained_unmerged", out[1].Worktree, out[1].Branch, out[1].Reason)
	}
	if got, ok := f.refTip("refs/heads/other"); !ok || got != otherTip {
		t.Error("refs/heads/other touched")
	}
}

// TestConfirmedDeletingBranchNeedsAnOffer calls the opt-in where the preview
// offers no branch deletion (a proven worktree: its branch goes anyway; a
// worktree whose unmerged branch another worktree holds: -D could not
// delete it), or without Delete in approved: each is an error, so the
// call cannot become a back door for other approvals.
func TestConfirmedDeletingBranchNeedsAnOffer(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("proven")
	f.unmergedPushed("unm")
	s, r := f.report()
	ps := f.engine(s, f.inputs(), Options{}).Preview([]Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "proven")), ItemFromRow(f.row(r, cleanup.KindWorktree, "unm"))})
	if ps[0].CanDeleteBranch || !ps[1].CanDeleteBranch {
		t.Fatalf("precondition: can delete proven %v unm %v", ps[0].CanDeleteBranch, ps[1].CanDeleteBranch)
	}
	if _, err := ps[0].ConfirmedDeletingBranch(Needs{Delete: true}); err == nil {
		t.Error("proven: opt-in accepted with no offer")
	}
	if _, err := ps[1].ConfirmedDeletingBranch(Needs{Confirm: true}); err == nil {
		t.Error("unm: opt-in accepted without Delete")
	}
	if _, err := ps[1].ConfirmedDeletingBranch(Needs{Confirm: true, Force: true, Delete: true}); err == nil {
		t.Error("unm: opt-in accepted Force it does not need")
	}
	if _, err := ps[1].ConfirmedDeletingBranch(Needs{Delete: true}); err == nil {
		t.Error("unm: opt-in accepted without the Confirm it needs")
	}
	if _, err := ps[1].Confirmed(Needs{Confirm: true, Delete: true}); err == nil {
		t.Error("unm: plain Confirmed accepted an offered -D")
	}
}

// TestConfirmedBranchItemTakesDelete confirms a branch-only item with Delete
// through plain Confirmed: it NEEDS -D (it does nothing without it), so that
// is its own need, not an opt-in; the run deletes it.
func TestConfirmedBranchItemTakesDelete(t *testing.T) {
	f, e, ps, _ := keptAndLoose(t)
	it, err := ps[1].Confirmed(Needs{Confirm: true, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if o := one(t, e, it); o.Branch != BrDeleted {
		t.Errorf("loose: %s/%s %q, want deleted", o.Worktree, o.Branch, o.Reason)
	}
	if _, ok := f.refTip("refs/heads/loose"); ok {
		t.Error("refs/heads/loose still there")
	}
}
