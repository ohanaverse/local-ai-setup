package removal

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// confirmAll previews items and confirms each with exactly what its preview
// needs, failing the test on a refused one.
func confirmAll(t *testing.T, e Engine, items []Item) ([]ItemPreview, []Item) {
	t.Helper()
	ps := e.Preview(items)
	var out []Item
	for _, p := range ps {
		it, err := p.Confirmed(p.Needs)
		if err != nil {
			t.Fatalf("%s: %v", p.Item.Name, err)
		}
		out = append(out, it)
	}
	return ps, out
}

// TestBatchApprovalsArePerItem (reviewer's TestAdvBatchUnionFlags) confirms
// a batch: a worktree on an unpushed, unmerged branch (its preview says the
// branch is kept) and a branch-only item that needs -D. Run as ONE batch,
// the -D one item was given must not reach the other: the kept branch, the
// only home of its commit, stays at its tip; the other is deleted. With a
// run-wide -D (Ruling K's Critical 1) the kept branch was deleted.
func TestBatchApprovalsArePerItem(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("kept")
	f.Commit(p, "k.txt", "work", "unpushed work") // never pushed
	f.unmergedBranch("loose")
	s, r := f.report()
	keptTip := f.row(r, cleanup.KindWorktree, "kept").Tip
	e := f.engine(s, f.inputs(), Options{})
	ps, items := confirmAll(t, e, []Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "kept")), ItemFromRow(f.row(r, cleanup.KindBranch, "loose"))})
	if !slices.Contains(ps[0].Notes, "branch kept will be kept: not proven merged") || !ps[1].Needs.Delete {
		t.Fatalf("previews: kept notes %q, loose needs %+v", ps[0].Notes, ps[1].Needs)
	}
	out := e.Run(items, nil)
	if out[0].Worktree != WtRemoved || out[0].Branch != BrRetainedUnmerged {
		t.Errorf("kept: run %s/%s %q, want removed / retained_unmerged", out[0].Worktree, out[0].Branch, out[0].Reason)
	}
	if tip, ok := f.refTip("refs/heads/kept"); !ok || tip != keptTip {
		t.Errorf("refs/heads/kept = %q (exists %v), want intact at %s", tip, ok, keptTip)
	}
	if out[1].Branch != BrDeleted {
		t.Errorf("loose: run %s/%s %q, want deleted", out[1].Worktree, out[1].Branch, out[1].Reason)
	}
}

// TestDirtyAgainAfterConfirmationIsSkipped (reviewer's TestAdvStaleRowForce)
// lists a dirty worktree, cleans it before the preview (which then shows
// nothing to lose, no --force), confirms that, and dirties it again before
// Run, in a batch where ANOTHER item was given --force. What the user
// confirmed is the preview, so the new file is a worse loss: skipped, the
// file kept. With SawLosses from the listed row and a run-wide --force
// (Critical 2) it was removed.
func TestDirtyAgainAfterConfirmationIsSkipped(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.Worktree("a")
	f.Dirty(a, "wip.txt")
	b := f.Worktree("b")
	f.Dirty(b, "wip.txt")
	s, r := f.report()
	listed := []Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "a")), ItemFromRow(f.row(r, cleanup.KindWorktree, "b"))}
	if err := os.Remove(filepath.Join(a, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	e := f.engine(s, f.inputs(), Options{})
	ps, items := confirmAll(t, e, listed)
	if ps[0].Needs.Force || !ps[1].Needs.Force {
		t.Fatalf("needs: a %+v, b %+v; want only b to need --force", ps[0].Needs, ps[1].Needs)
	}
	f.Dirty(a, "wip.txt")
	out := e.Run(items, nil)
	if out[0].Worktree != WtSkippedChanged || !exists(t, filepath.Join(a, "wip.txt")) {
		t.Errorf("a: run %s %q, file there %v; want skipped_changed and the file kept", out[0].Worktree, out[0].Reason, exists(t, filepath.Join(a, "wip.txt")))
	}
	if out[1].Worktree != WtRemoved {
		t.Errorf("b: run %s %q, want removed", out[1].Worktree, out[1].Reason)
	}
}

// TestConfirmedCopiesWhatThePreviewShowed lists a dirty worktree and cleans
// it before the preview: the confirmed Item carries the preview's fresh
// status, tip and losses (proven, nothing to lose), not the listed row's
// (unique_work, dirty), and the approvals given.
func TestConfirmedCopiesWhatThePreviewShowed(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.Worktree("a")
	f.Dirty(a, "wip.txt")
	s, r := f.report()
	listed := ItemFromRow(f.row(r, cleanup.KindWorktree, "a"))
	if err := os.Remove(filepath.Join(a, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), listed)
	if p.Needs != (Needs{}) {
		t.Fatalf("precondition: needs %+v, want none (clean now)", p.Needs)
	}
	it, err := p.Confirmed(Needs{})
	if err != nil {
		t.Fatal(err)
	}
	want := seal(Item{Kind: listed.Kind, Path: listed.Path, Ref: listed.Ref, Name: listed.Name,
		Saw: p.Fresh.Status, SawTip: p.Fresh.Tip, SawLosses: p.Fresh.Losses})
	if !reflect.DeepEqual(it, want) || it.Saw == listed.Saw || it.SawLosses == listed.SawLosses {
		t.Errorf("confirmed %+v\nwant %+v (listed saw %s %+v)", it, want, listed.Saw, listed.SawLosses)
	}
}

// TestConfirmedRejectsApprovalsThatDoNotCoverNeeds confirms previews with a
// needed approval missing: Confirmed refuses to build an Item Run would have
// to refuse, so a caller cannot drop an approval by accident.
func TestConfirmedRejectsApprovalsThatDoNotCoverNeeds(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.Worktree("topic")
	f.Dirty(a, "wip.txt")
	f.unmergedBranch("loose")
	s, r := f.report()
	ps := f.engine(s, f.inputs(), Options{}).Preview([]Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")), ItemFromRow(f.row(r, cleanup.KindBranch, "loose"))})
	for _, c := range []struct {
		p    ItemPreview
		gave Needs
	}{
		{ps[0], Needs{Confirm: true}},
		{ps[0], Needs{Force: true}},
		{ps[0], Needs{}},
		{ps[1], Needs{Confirm: true}},
		{ps[1], Needs{Delete: true}},
	} {
		if _, err := c.p.Confirmed(c.gave); err == nil {
			t.Errorf("%s: Confirmed(%+v) accepted; needs %+v", c.p.Item.Name, c.gave, c.p.Needs)
		}
	}
	if _, err := ps[0].Confirmed(ps[0].Needs); err != nil {
		t.Errorf("exact needs refused: %v", err)
	}
}

// TestRunRequiresConfirmationForUnprovenItems hands Run items with no
// approvals at all: a not-proven worktree (ignored files) is refused as
// needing confirmation, a proven one (the plain yes) is removed.
func TestRunRequiresConfirmationForUnprovenItems(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	ign := f.Worktree("ign")
	f.Ignored(ign, ".env")
	proven := f.mergedWorktree("proven")
	s, r := f.report()
	e := f.engine(s, f.inputs(), Options{})
	out := e.Run([]Item{
		granted(itemOf(f.row(r, cleanup.KindWorktree, "ign")), Needs{}),
		granted(itemOf(f.row(r, cleanup.KindWorktree, "proven")), Needs{}),
	}, nil)
	if out[0].Worktree != WtRefused || !strings.Contains(out[0].Reason, "needs confirmation") || !exists(t, ign) {
		t.Errorf("ign: %s %q, want refused: needs confirmation", out[0].Worktree, out[0].Reason)
	}
	if out[1].Worktree != WtRemoved || exists(t, proven) {
		t.Errorf("proven: %s %q, want removed", out[1].Worktree, out[1].Reason)
	}
}

// TestProvenWithLossLinesNeedsConfirm feeds the engine a synthetic fresh
// row that is proven yet would lose an ignored entry: any loss line needs a
// confirmation, in Preview and in Run alike, whatever the status says.
func TestProvenWithLossLinesNeedsConfirm(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Status != cleanup.StatusProven {
		t.Fatalf("precondition: %s", row.Status)
	}
	loss := cleanup.Losses{IgnoredEntries: 1, IgnoredBytes: 10}
	stubRescan(t, func(g cleanup.Git, prior cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
		fresh, err := cleanup.Rescan(g, prior, in, o, tg)
		fresh.Losses = loss
		return fresh, err
	})
	it := ItemFromRow(row)
	it.SawLosses = loss
	e := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, e, it)
	wantApproval(t, p, ApprovalNeedsConfirm, Needs{Confirm: true})
	if o := one(t, e, granted(it, Needs{})); o.Worktree != WtRefused || !strings.Contains(o.Reason, "needs confirmation") {
		t.Errorf("run with no approval: %s %q, want refused: needs confirmation", o.Worktree, o.Reason)
	}
}

// TestPreviewRunsLsofOncePerCall previews three worktrees with an lsof
// finder: one machine-wide listing serves all of them (lsof per item would
// cost up to 5 s each), and each gets the processes in its own directory.
func TestPreviewRunsLsofOncePerCall(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.Worktree("a")
	b := f.Worktree("b")
	f.Worktree("c")
	s, r := f.report()
	calls := 0
	out := lsofRecord(41, 1, "vim", mustResolve(t, a)) + lsofRecord(42, 1, "node", mustResolve(t, b)+"/sub")
	finder := lsofFinder{
		run:     func(context.Context, ...string) ([]byte, error) { calls++; return []byte(out), nil },
		timeout: time.Second,
		self:    999999,
	}
	var items []Item
	for _, n := range []string{"a", "b", "c"} {
		items = append(items, ItemFromRow(f.row(r, cleanup.KindWorktree, n)))
	}
	ps := f.engine(s, f.inputs(), Options{Procs: finder}).Preview(items)
	if calls != 1 {
		t.Errorf("lsof ran %d times for one Preview, want 1", calls)
	}
	want := [][]Proc{{{41, "vim"}}, {{42, "node"}}, nil}
	for i, p := range ps {
		if !reflect.DeepEqual(p.Procs, want[i]) {
			t.Errorf("%s: procs %+v, want %+v", p.Item.Name, p.Procs, want[i])
		}
	}
}

// TestPreviewNeedsAreExactlyWhatRunRequires previews items covering every
// outcome kind, then for each: Run with any one needed approval withheld
// refuses it (or, for a branch item without -D, keeps the branch) with the
// right reason and changes nothing; Run with Approved == Needs (through
// Confirmed) does what the preview planned; a refused preview's item is
// refused by Run with every approval, for the same reason.
func TestPreviewNeedsAreExactlyWhatRunRequires(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("proven")
	ign := f.Worktree("ign")
	f.Ignored(ign, ".env")
	dirty := f.Worktree("dirty")
	f.Dirty(dirty, "wip.txt")
	f.unmergedPushed("kept")
	f.unmergedBranch("loose")
	det := filepath.Join(f.Dir, ".worktrees", "det")
	f.git("worktree", "add", "--detach", det)
	f.Commit(det, "x.txt", "x", "only here")
	locked := f.Worktree("locked")
	f.Lock(locked)
	f.Worktree("bogus")
	gone := f.Worktree("gone")
	s, r := f.report()
	f.git("worktree", "remove", gone)

	items := []Item{
		ItemFromRow(f.row(r, cleanup.KindWorktree, "proven")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "ign")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "dirty")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "kept")),
		ItemFromRow(f.row(r, cleanup.KindBranch, "loose")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "det")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "locked")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "bogus")),
		ItemFromRow(f.row(r, cleanup.KindWorktree, "gone")),
		ItemFromRow(f.row(r, cleanup.KindRemote, "loose")),
	}
	items[6].Saw = cleanup.StatusProven
	items[7].Saw = "bogus"
	want := []Approval{ApprovalProven, ApprovalNeedsConfirm, ApprovalNeedsForce, ApprovalNeedsConfirm, ApprovalNeedsDelete,
		ApprovalNeedsForce, ApprovalRefused, ApprovalRefused, ApprovalRefused, ApprovalRefused}

	e := f.engine(s, f.inputs(), Options{})
	ps := e.Preview(items)
	for i, p := range ps {
		name := items[i].Name
		if p.Approval != want[i] {
			t.Errorf("%s: approval %s (%q), want %s", name, p.Approval, p.Reason, want[i])
			continue
		}
		if p.Approval == ApprovalRefused {
			o := one(t, e, granted(items[i], allNeeds))
			if o.Worktree == WtRemoved || o.Branch == BrDeleted || o.Reason != p.Reason {
				t.Errorf("%s: preview refused %q, run %s/%s %q", name, p.Reason, o.Worktree, o.Branch, o.Reason)
			}
			continue
		}
		it, err := p.Confirmed(p.Needs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, w := range []struct {
			on     bool
			drop   func(*Needs)
			reason string
		}{
			{p.Needs.Confirm, func(n *Needs) { n.Confirm = false }, "needs confirmation"},
			{p.Needs.Force, func(n *Needs) { n.Force = false }, "--force required"},
			{p.Needs.Delete, func(n *Needs) { n.Delete = false }, "-D deletes it"},
		} {
			if !w.on {
				continue
			}
			short := it
			w.drop(&short.Approved)
			// Confirmed would not build this item (it does not cover
			// Needs); sealed by hand to test the engine's own rule behind
			// Confirmed. Production code can only seal through Confirmed.
			short = seal(short)
			before := f.state()
			o := one(t, e, short)
			if o.Worktree == WtRemoved || o.Branch == BrDeleted || !strings.Contains(o.Reason, w.reason) {
				t.Errorf("%s with %+v: run %s/%s %q, want nothing done naming %q", name, short.Approved, o.Worktree, o.Branch, o.Reason, w.reason)
			}
			if f.state() != before {
				t.Errorf("%s with %+v: the repository changed", name, short.Approved)
			}
		}
		o := one(t, e, it)
		if o.Worktree != p.Plan.Worktree || o.Branch != p.Plan.Branch {
			t.Errorf("%s: preview plans %s/%s, run did %s/%s (%q)", name, p.Plan.Worktree, p.Plan.Branch, o.Worktree, o.Branch, o.Reason)
		}
	}
}
