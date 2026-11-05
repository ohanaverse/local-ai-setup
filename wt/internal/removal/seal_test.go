package removal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestRunRefusesHandBuiltItem (reviewer's TestZZNaiveCallers) lists a dirty
// worktree, previews it clean, dirties it again, and hands Run an Item built
// by hand from the LISTED row with Confirm and Force set: its Saw and
// SawLosses say "dirty, approved", so without a seal it was removed and the
// new file lost. Only an Item from Confirmed reaches a removal (Ruling L):
// Run and Plan refuse it as not confirmed and the file stays.
func TestRunRefusesHandBuiltItem(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.Worktree("a")
	f.Dirty(a, "wip.txt")
	s, r := f.report()
	listed := ItemFromRow(f.row(r, cleanup.KindWorktree, "a"))
	if err := os.Remove(filepath.Join(a, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	e := f.engine(s, f.inputs(), Options{})
	previewOne(t, e, listed)
	f.Dirty(a, "wip.txt")
	naive := listed
	naive.Approved = Needs{Confirm: true, Force: true}
	for name, o := range map[string]Outcome{"plan": e.Plan([]Item{naive})[0], "run": one(t, e, naive)} {
		if o.Worktree != WtRefused || o.Branch != BrNotAttempted || o.Reason != "invalid target: not confirmed" {
			t.Errorf("%s: %s/%s %q, want refused: not confirmed", name, o.Worktree, o.Branch, o.Reason)
		}
	}
	if !exists(t, filepath.Join(a, "wip.txt")) {
		t.Error("the uncommitted file is gone")
	}
}

// TestRunRefusesEditedConfirmedItem (reviewer's TestZZTamperedConfirmed)
// confirms a worktree on an unpushed branch through the real Preview ->
// Confirmed path, then edits one field of the confirmed Item before Run: its
// status, its losses, its tip, or an approval (Delete, which would delete
// the unpushed branch). Every edit breaks the seal: refused, nothing
// touched. The unedited Item still runs (control).
func TestRunRefusesEditedConfirmedItem(t *testing.T) {
	for name, edit := range map[string]func(*Item){
		"saw":             func(it *Item) { it.Saw = cleanup.StatusProven },
		"saw losses":      func(it *Item) { it.SawLosses = cleanup.Losses{Dirty: true, Unpushed: 5} },
		"saw tip":         func(it *Item) { it.SawTip = strings.Repeat("0", 40) },
		"approved delete": func(it *Item) { it.Approved.Delete = true },
		"approved force":  func(it *Item) { it.Approved.Force = true },
		"none":            nil,
	} {
		t.Run(name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("kept")
			f.Commit(p, "k.txt", "work", "unpushed work")
			s, r := f.report()
			tip := f.row(r, cleanup.KindWorktree, "kept").Tip
			e := f.engine(s, f.inputs(), Options{})
			pv := previewOne(t, e, ItemFromRow(f.row(r, cleanup.KindWorktree, "kept")))
			it, err := pv.Confirmed(pv.Needs)
			if err != nil {
				t.Fatal(err)
			}
			if it.Saw == cleanup.StatusProven || it.Approved.Delete || it.Approved.Force {
				t.Fatalf("precondition: confirmed %+v; each edit must change a field", it)
			}
			if edit == nil {
				if o := one(t, e, it); o.Worktree != WtRemoved || o.Branch != BrRetainedUnmerged {
					t.Fatalf("unedited: %s/%s %q, want removed / retained_unmerged", o.Worktree, o.Branch, o.Reason)
				}
				return
			}
			edit(&it)
			o := one(t, e, it)
			if o.Worktree != WtRefused || o.Branch != BrNotAttempted || o.Reason != "invalid target: not confirmed" {
				t.Errorf("%s/%s %q, want refused: not confirmed", o.Worktree, o.Branch, o.Reason)
			}
			if !exists(t, p) {
				t.Error("worktree removed")
			}
			if got, ok := f.refTip("refs/heads/kept"); !ok || got != tip {
				t.Error("branch kept touched")
			}
		})
	}
}

// TestConfirmedRefusesApprovalsNotNeededOrOffered confirms each preview of a
// batch with the union of every item's Needs (the reviewer's
// TestZZNaiveUnion shape): an item is refused an approval its own preview
// neither needs nor offers, so one item's --force or -D cannot reach
// another. Here the proven worktree is offered nothing, and the dirty one on
// a proven branch is not offered -D.
func TestConfirmedRefusesApprovalsNotNeededOrOffered(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("proven")
	d := f.mergedWorktree("dirty")
	f.Dirty(d, "wip.txt")
	f.unmergedBranch("loose")
	s, r := f.report()
	ps := f.engine(s, f.inputs(), Options{}).Preview([]Item{
		ItemFromRow(f.row(r, cleanup.KindWorktree, "proven")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "dirty")),
		ItemFromRow(f.row(r, cleanup.KindBranch, "loose")),
	})
	var union Needs
	for _, p := range ps {
		if p.CanDeleteBranch {
			t.Fatalf("precondition: %s offers -D", p.Item.Name)
		}
		union = Needs{union.Confirm || p.Needs.Confirm, union.Force || p.Needs.Force, union.Delete || p.Needs.Delete}
	}
	if union != allNeeds {
		t.Fatalf("precondition: union %+v", union)
	}
	for i, wantErr := range []bool{true, true, true} {
		_, err := ps[i].Confirmed(union)
		if (err != nil) != wantErr || (err != nil && !strings.Contains(err.Error(), "approval not required")) {
			t.Errorf("%s: Confirmed(union) err %v", ps[i].Item.Name, err)
		}
		if _, err := ps[i].Confirmed(ps[i].Needs); err != nil {
			t.Errorf("%s: Confirmed(own needs) err %v", ps[i].Item.Name, err)
		}
	}
}

// TestConfirmedOffersDeleteOnlyWhereItWouldDelete checks CanDeleteBranch:
// true for a worktree on an unmerged branch (-D would delete it, so
// Confirmed accepts Delete there), false for a proven branch (it goes
// anyway) and for a branch another worktree holds (-D could not delete it),
// where Confirmed refuses Delete.
func TestConfirmedOffersDeleteOnlyWhereItWouldDelete(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedPushed("unm")
	f.mergedWorktree("proven")
	f.unmergedPushed("held")
	f.git("worktree", "add", "--force", filepath.Join(f.Dir, ".worktrees", "holder"), "held")
	s, r := f.report()
	// Two worktree rows are named held (the holder is on it too): take the
	// one at .worktrees/held.
	var held cleanup.Row
	for _, row := range r.Rows {
		if row.Kind == cleanup.KindWorktree && filepath.Base(row.Path) == "held" {
			held = row
		}
	}
	ps := f.engine(s, f.inputs(), Options{}).Preview([]Item{
		ItemFromRow(f.row(r, cleanup.KindWorktree, "unm")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "proven")),
		ItemFromRow(held),
	})
	for i, want := range []bool{true, false, false} {
		p := ps[i]
		if p.Approval == ApprovalRefused {
			t.Fatalf("%s refused: %s", p.Item.Name, p.Reason)
		}
		if p.CanDeleteBranch != want {
			t.Errorf("%s: CanDeleteBranch %v, want %v", p.Item.Name, p.CanDeleteBranch, want)
		}
		withD := p.Needs
		withD.Delete = true
		if _, err := p.Confirmed(withD); (err == nil) != want {
			t.Errorf("%s: Confirmed with Delete err %v, want accepted %v", p.Item.Name, err, want)
		}
	}
}

// TestConfirmedTakesTheTipFromFresh checks Confirmed reads the commit from
// the preview's fresh row, not from its input item. Through the real path
// the two are equal (the gate refuses a moved tip), so the preview's input
// is edited here to tell them apart: an Item with the input's tip would be
// skipped as moved; with Fresh's, the confirmed item runs.
func TestConfirmedTakesTheTipFromFresh(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("topic")
	s, r := f.report()
	e := f.engine(s, f.inputs(), Options{})
	pv := previewOne(t, e, ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	pv.Item.SawTip = strings.Repeat("1", 40)
	it, err := pv.Confirmed(pv.Needs)
	if err != nil {
		t.Fatal(err)
	}
	if it.SawTip != pv.Fresh.Tip {
		t.Fatalf("confirmed tip %s, want fresh %s", it.SawTip, pv.Fresh.Tip)
	}
	if o := one(t, e, it); o.Worktree != WtRemoved {
		t.Errorf("run %s/%s %q, want removed", o.Worktree, o.Branch, o.Reason)
	}
}

// TestRefusedItemCarriesNoProcessWarnings finds live processes in a locked
// worktree: the refusal is the whole answer; a process warning on it would
// read as if the removal had gone ahead.
func TestRefusedItemCarriesNoProcessWarnings(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Lock(p)
	s, r := f.report()
	it := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
	it.Saw = cleanup.StatusProven
	it = seal(it) // edited after building; only Confirmed seals in production
	pf := &stubProcs{procs: []Proc{{1, "vim"}}}
	o := one(t, f.engine(s, f.inputs(), Options{Procs: pf}), it)
	if o.Worktree != WtRefused || len(o.Warnings) != 0 {
		t.Errorf("outcome %s %q warnings %q, want refused with no warnings", o.Worktree, o.Reason, o.Warnings)
	}
	if len(pf.dirs) != 1 {
		t.Errorf("finder asked %v, want once (the check ran)", pf.dirs)
	}
}
