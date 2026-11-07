package removal

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// previewOne previews the single item it with e.
func previewOne(t *testing.T, e Engine, it Item) ItemPreview {
	t.Helper()
	ps := e.Preview([]Item{it})
	if len(ps) != 1 {
		t.Fatalf("%d previews for one item", len(ps))
	}
	return ps[0]
}

// allNeeds is every approval.
var allNeeds = Needs{Confirm: true, Force: true, Delete: true}

// granted is it with Approved set to n, re-sealed (bypassing Confirmed, to
// test what Run does with an item whatever its approvals; production code
// can only seal through Confirmed).
func granted(it Item, n Needs) Item { it.Approved = n; return seal(it) }

// wantApproval fails unless p asks for approval a with needs n.
func wantApproval(t *testing.T, p ItemPreview, a Approval, n Needs) {
	t.Helper()
	if p.Approval != a || p.Needs != n {
		t.Fatalf("approval %s needs %+v (reason %q, notes %q), want %s %+v", p.Approval, p.Needs, p.Reason, p.Notes, a, n)
	}
}

// unmergedBranch makes a local branch with one commit main lacks, pushed,
// checked out nowhere: a branch-only row nothing proves merged.
func (f *fixture) unmergedBranch(name string) {
	f.t.Helper()
	f.git("switch", "-c", name)
	f.Commit(f.Dir, name+".txt", "work", name+" work")
	f.Push(name)
	f.git("switch", "main")
}

// TestApprovalWireStrings pins the approval kinds' strings: the CLI and the
// TUI branch on them, and a later JSON contract will carry them.
func TestApprovalWireStrings(t *testing.T) {
	want := map[Approval]string{
		ApprovalProven:       "proven",
		ApprovalNeedsConfirm: "needs_confirm",
		ApprovalNeedsForce:   "needs_force",
		ApprovalNeedsDelete:  "needs_delete",
		ApprovalRefused:      "refused",
	}
	for a, s := range want {
		if string(a) != s {
			t.Errorf("approval %q, want %q", a, s)
		}
	}
}

// TestItemFromRowCopiesWhatTheUserSaw builds an Item from a row by hand:
// every field the engine compares comes from the row exactly, and SawLosses
// is the WHOLE Losses, an unknown unpushed count (-1) and unreadable gates
// included (Task 5 carry: a zero or recomputed value would approve a loss
// the user never saw, or hide an unknown).
func TestItemFromRowCopiesWhatTheUserSaw(t *testing.T) {
	losses := cleanup.Losses{Dirty: true, IgnoredEntries: 3, IgnoredBytes: 1234, Unpushed: -1, DetachedUnique: true, GatesUnknown: true}
	row := cleanup.Row{
		Kind: cleanup.KindWorktree, Name: "topic", Path: "/r/.worktrees/topic", Branch: "topic",
		Status: cleanup.StatusUniqueWork, Ref: "refs/heads/topic",
		Tip: strings.Repeat("a", 40), Head: strings.Repeat("b", 40), Losses: losses,
	}
	got := ItemFromRow(row)
	want := Item{
		Kind: cleanup.KindWorktree, Path: "/r/.worktrees/topic", Ref: "refs/heads/topic", Name: "topic",
		Saw: cleanup.StatusUniqueWork, SawTip: strings.Repeat("a", 40), SawLosses: losses,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("item %+v\nwant %+v", got, want)
	}
}

// TestItemFromRowDetachedSawTipIsHead converts a detached worktree row: it
// has no branch tip, so what the user saw is its HEAD; the engine compares a
// detached worktree's HEAD, and an empty SawTip would always read as moved.
func TestItemFromRowDetachedSawTipIsHead(t *testing.T) {
	row := cleanup.Row{Kind: cleanup.KindWorktree, Name: "loose", Path: "/r/.worktrees/loose", Status: cleanup.StatusUniqueWork, Head: strings.Repeat("c", 40)}
	if got := ItemFromRow(row); got.SawTip != row.Head || got.Ref != "" {
		t.Errorf("item %+v, want SawTip = Head and no ref", got)
	}
}

// TestItemFromRowRemoteIsConvertedAndRefused converts a remote row: the
// conversion keeps its kind (callers never get a silently re-kinded item),
// and the engine refuses it (an unproven origin row is not removable), in
// Preview and in Run alike.
func TestItemFromRowRemoteIsConvertedAndRefused(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedBranch("topic")
	s, r := f.report()
	it := ItemFromRow(f.row(r, cleanup.KindRemote, "topic"))
	if it.Kind != cleanup.KindRemote {
		t.Fatalf("kind %s, want remote", it.Kind)
	}
	e := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, e, it)
	o := one(t, e, granted(it, allNeeds))
	if p.Approval != ApprovalRefused || o.Worktree != WtRefused || p.Reason != o.Reason || !strings.Contains(o.Reason, "only a proven origin branch") {
		t.Fatalf("preview %s %q, run %s %q; want both refused: only a proven origin branch", p.Approval, p.Reason, o.Worktree, o.Reason)
	}
}

// TestPreviewProvenNeedsPlainYes previews a squash-merged clean worktree:
// proven with nothing to lose, so a plain yes (--yes) approves it, and its
// branch would go with it.
func TestPreviewProvenNeedsPlainYes(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(row))
	wantApproval(t, p, ApprovalProven, Needs{})
	if p.Fresh.Path != row.Path || p.Fresh.Status != cleanup.StatusProven || len(p.LossLines) != 0 {
		t.Errorf("fresh %+v loss lines %q", p.Fresh, p.LossLines)
	}
	if p.Plan.Worktree != WtRemoved || p.Plan.Branch != BrDeleted {
		t.Errorf("plan %+v, want removed / deleted", p.Plan)
	}
}

// TestPreviewIgnoredFilesNeedsConfirm previews a worktree whose only loss is
// an ignored .env: no flag covers it (Ruling B), but it is not proven, so it
// needs a typed confirmation (--accept-loss), and the loss line says what
// goes.
func TestPreviewIgnoredFilesNeedsConfirm(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p0 := f.Worktree("topic")
	f.Ignored(p0, ".env")
	s, r := f.report()
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	wantApproval(t, p, ApprovalNeedsConfirm, Needs{Confirm: true})
	if len(p.LossLines) != 1 || !strings.Contains(p.LossLines[0], "1 ignored entry") {
		t.Errorf("loss lines %q, want the ignored entry", p.LossLines)
	}
}

// TestPreviewFreshBranchNeedsConfirm previews a worktree on a branch made a
// moment ago (fresh_branch: too young for its ancestor proof): nothing is
// lost that a flag covers, so a typed confirmation; its branch is kept
// without -D, and the preview says so.
func TestPreviewFreshBranchNeedsConfirm(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("topic")
	now := time.Now()
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, f.inputs())
	if err != nil {
		t.Fatal(err)
	}
	r := cleanup.Classify(s, cleanup.Options{Now: now})
	row := f.row(r, cleanup.KindWorktree, "topic")
	if !hasReason(row, cleanup.ReasonFreshBranch) {
		t.Fatalf("precondition: reasons %+v lack fresh_branch", row.Reasons)
	}
	e := Engine{G: cleanup.Git{Dir: f.Dir}, Prior: s, In: f.inputs(), Classify: cleanup.Options{Now: now}, Opts: Options{Now: func() time.Time { return now }}}
	p := previewOne(t, e, ItemFromRow(row))
	wantApproval(t, p, ApprovalNeedsConfirm, Needs{Confirm: true})
	if !slices.Contains(p.Notes, "branch topic will be kept: not proven merged") {
		t.Errorf("notes %q, want the kept branch", p.Notes)
	}
}

// TestPreviewDirtyNeedsForce previews a worktree with an uncommitted file:
// that loss is gone for good, so it needs --force (and a typed confirm);
// the engine's own Force check says so, Preview does not re-decide it.
func TestPreviewDirtyNeedsForce(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p0 := f.Worktree("topic")
	f.Dirty(p0, "wip.txt")
	s, r := f.report()
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	wantApproval(t, p, ApprovalNeedsForce, Needs{Confirm: true, Force: true})
	if !slices.Contains(p.LossLines, "uncommitted changes will be lost") {
		t.Errorf("loss lines %q", p.LossLines)
	}
}

// TestPreviewUnmergedBranchItemNeedsDelete previews a branch-only item
// nothing proves merged: a branch item can only be removed by deleting its
// branch, which takes -D (a need, not an option).
func TestPreviewUnmergedBranchItemNeedsDelete(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedBranch("topic")
	s, r := f.report()
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(f.row(r, cleanup.KindBranch, "topic")))
	wantApproval(t, p, ApprovalNeedsDelete, Needs{Confirm: true, Delete: true})
}

// TestPreviewWorktreeUnprovenBranchCanBeDeleted previews a worktree on an
// unmerged branch: removing the worktree keeps the branch, so -D is not a
// need; it is an optional extra approval (CanDeleteBranch). Opted in with
// ConfirmedDeletingBranch, Run deletes that branch, and only because THIS
// item has it.
func TestPreviewWorktreeUnprovenBranchCanBeDeleted(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedPushed("topic")
	s, r := f.report()
	e := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, e, ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	wantApproval(t, p, ApprovalNeedsConfirm, Needs{Confirm: true})
	if !p.CanDeleteBranch || p.Plan.Branch != BrRetainedUnmerged {
		t.Fatalf("can delete %v plan %+v, want deletable and kept without Delete", p.CanDeleteBranch, p.Plan)
	}
	it, err := p.ConfirmedDeletingBranch(Needs{Confirm: true, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if o := one(t, e, it); o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Errorf("run %s/%s %q, want removed / deleted", o.Worktree, o.Branch, o.Reason)
	}
}

// TestPreviewWorktreeKeepsUnprovenBranchWithoutDelete previews the same
// worktree: without Delete its branch is kept (its commits stay), so -D is
// not asked for; the preview says the branch is kept.
func TestPreviewWorktreeKeepsUnprovenBranchWithoutDelete(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedPushed("topic")
	s, r := f.report()
	p := previewOne(t, f.engine(s, f.inputs(), Options{}), ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	wantApproval(t, p, ApprovalNeedsConfirm, Needs{Confirm: true})
	if !slices.Contains(p.Notes, "branch topic will be kept: not proven merged") || p.Plan.Branch != BrRetainedUnmerged {
		t.Errorf("notes %q plan %+v, want the branch kept", p.Notes, p.Plan)
	}
}

// TestPreviewDirtyWorktreeOnUnprovenBranch previews a dirty worktree on an
// unmerged branch: it needs Force (summary needs_force) and may also be
// opted in to Delete; with both (ConfirmedDeletingBranch), Run removes it
// and deletes the branch.
func TestPreviewDirtyWorktreeOnUnprovenBranch(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p0 := f.unmergedPushed("topic")
	f.Dirty(p0, "wip.txt")
	s, r := f.report()
	e := f.engine(s, f.inputs(), Options{})
	p := previewOne(t, e, ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	wantApproval(t, p, ApprovalNeedsForce, Needs{Confirm: true, Force: true})
	if !p.CanDeleteBranch {
		t.Fatal("CanDeleteBranch false for an unmerged branch")
	}
	it, err := p.ConfirmedDeletingBranch(Needs{Confirm: true, Force: true, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if o := one(t, e, it); o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Errorf("run %s/%s %q, want removed / deleted", o.Worktree, o.Branch, o.Reason)
	}
}

// TestPreviewRefusedShowsEngineReason previews items the engine refuses: a
// hard blocker (locked, ticked proven), unreadable gates and an invalid Saw.
// Each is refused with the engine's own reason, and Run (every approval)
// refuses the same item for the same reason: Preview never re-decides.
func TestPreviewRefusedShowsEngineReason(t *testing.T) {
	cases := map[string]func(f *fixture) Item{
		"blocker": func(f *fixture) Item {
			p := f.Worktree("topic")
			f.Lock(p)
			_, r := f.report()
			it := ItemFromRow(f.row(r, cleanup.KindWorktree, "topic"))
			it.Saw = cleanup.StatusProven
			return it
		},
		"unknown gates": func(f *fixture) Item {
			p := f.Worktree("topic")
			f.exclude("cache/")
			sub := filepath.Join(p, "cache", "sub")
			f.write(sub, "f", "x")
			if err := os.Chmod(sub, 0); err != nil {
				f.t.Fatal(err)
			}
			f.t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
			_, r := f.report()
			return ItemFromRow(f.row(r, cleanup.KindWorktree, "topic"))
		},
		"invalid saw": func(f *fixture) Item {
			f.Worktree("topic")
			_, r := f.report()
			it := ItemFromRow(f.row(r, cleanup.KindWorktree, "topic"))
			it.Saw = "bogus"
			return it
		},
	}
	want := map[string]string{"blocker": cleanup.BlockLocked, "unknown gates": "could not read", "invalid saw": "invalid target"}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			it := setup(f)
			s, _ := f.report()
			e := f.engine(s, f.inputs(), Options{})
			p := previewOne(t, e, it)
			if p.Approval != ApprovalRefused || !strings.Contains(p.Reason, want[name]) {
				t.Fatalf("preview %s %q, want refused naming %q", p.Approval, p.Reason, want[name])
			}
			if _, err := p.Confirmed(allNeeds); err == nil {
				t.Error("Confirmed accepted a refused preview")
			}
			o := one(t, e, granted(it, allNeeds))
			if o.Worktree != WtRefused || o.Reason != p.Reason {
				t.Errorf("run %s %q, preview reason %q: want the same refusal", o.Worktree, o.Reason, p.Reason)
			}
			if !exists(t, it.Path) {
				t.Error("worktree removed")
			}
		})
	}
}

// TestPreviewListsProcesses previews a worktree with live processes: the
// preview lists them (pid and name) so the confirmation can show them, and
// they change no approval (a warning, never a refusal).
func TestPreviewListsProcesses(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p0 := f.mergedWorktree("topic")
	s, r := f.report()
	pf := &stubProcs{procs: []Proc{{11, "vim"}, {12, "node"}}}
	p := previewOne(t, f.engine(s, f.inputs(), Options{Procs: pf}), ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")))
	if !reflect.DeepEqual(p.Procs, pf.procs) {
		t.Errorf("procs %+v, want %+v", p.Procs, pf.procs)
	}
	wantApproval(t, p, ApprovalProven, Needs{})
	if len(pf.dirs) != 1 || cleanup.ResolvePath(pf.dirs[0]) != cleanup.ResolvePath(p0) {
		t.Errorf("finder asked %v, want once for %s", pf.dirs, p0)
	}
}

// TestPreviewProcFinderErrorBecomesWarning makes the finder fail: the
// preview carries a warning that the check could not run, never a refusal,
// and keeps it even for a refused item (the user still sees it was not
// checked).
func TestPreviewProcFinderErrorBecomesWarning(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	locked := f.Worktree("topic2")
	f.Lock(locked)
	s, r := f.report()
	pf := &stubProcs{err: errors.New("lsof timed out after 5s")}
	e := f.engine(s, f.inputs(), Options{Procs: pf})
	lockedItem := ItemFromRow(f.row(r, cleanup.KindWorktree, "topic2"))
	lockedItem.Saw = cleanup.StatusProven
	ps := e.Preview([]Item{ItemFromRow(f.row(r, cleanup.KindWorktree, "topic")), lockedItem})
	if ps[0].Approval != ApprovalProven || ps[1].Approval != ApprovalRefused {
		t.Fatalf("approvals %s, %s; want proven, refused", ps[0].Approval, ps[1].Approval)
	}
	for _, p := range ps {
		if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "could not check for running processes: lsof timed out") {
			t.Errorf("%s: warnings %q", p.Item.Name, p.Warnings)
		}
	}
}

// TestPreviewMutatesNothing previews a mix (proven, dirty, unmerged,
// a branch item, a locked one) and requires the repository byte-for-byte
// unchanged: Preview feeds a confirmation screen, which must remove nothing
// before the user says yes.
func TestPreviewMutatesNothing(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.mergedWorktree("topic")
	b := f.Worktree("topic2")
	f.Dirty(b, "wip.txt")
	c := f.unmergedPushed("topic3")
	d := f.Worktree("topic4")
	f.Lock(d)
	f.unmergedBranch("loose")
	s, r := f.report()
	var items []Item
	for _, n := range []string{"topic", "topic2", "topic3", "topic4"} {
		items = append(items, ItemFromRow(f.row(r, cleanup.KindWorktree, n)))
	}
	items = append(items, ItemFromRow(f.row(r, cleanup.KindBranch, "loose")))
	before := f.state()
	ps := f.engine(s, f.inputs(), Options{}).Preview(items)
	if after := f.state(); after != before {
		t.Fatalf("Preview changed the repository:\nbefore %s\nafter %s", before, after)
	}
	for _, p := range []string{a, b, c, d} {
		if !exists(t, p) {
			t.Errorf("Preview removed %s", p)
		}
	}
	if len(ps) != len(items) {
		t.Fatalf("%d previews for %d items", len(ps), len(items))
	}
}
