package removal_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// These tests use only removal's exported API, as another package (the CLI,
// the TUI) would: Ruling N, a preview not produced by Preview, or edited,
// cannot be confirmed.

// TestForgedPreviewIsNotConfirmed (reviewer's TestZZExtForgedPreviewRedirty)
// lists a dirty worktree, cleans it, previews it, dirties it again, then
// builds an ItemPreview by hand from the LISTED row (dirty, needs --force).
// Confirmed must refuse it: otherwise it seals the listed losses and Run
// removes the new uncommitted file.
func TestForgedPreviewIsNotConfirmed(t *testing.T) {
	x := removal.NewExtFixture(t)
	a := x.Worktree("a")
	x.Dirty(a, "wip.txt")
	s, r := x.Report()
	listed := x.Row(r, cleanup.KindWorktree, "a")
	if err := os.Remove(filepath.Join(a, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	e := x.Engine(s)
	e.Preview([]removal.Item{removal.ItemFromRow(listed)})
	x.Dirty(a, "wip.txt")
	forged := removal.ItemPreview{Item: removal.ItemFromRow(listed), Fresh: listed,
		Approval: removal.ApprovalNeedsForce, Needs: removal.Needs{Confirm: true, Force: true}}
	if _, err := forged.Confirmed(removal.Needs{Confirm: true, Force: true}); err == nil || !strings.Contains(err.Error(), "not produced by Preview") {
		t.Fatalf("forged preview: err %v, want not produced by Preview", err)
	}
	if _, err := forged.ConfirmedDeletingBranch(removal.Needs{Confirm: true, Force: true, Delete: true}); err == nil {
		t.Error("forged preview accepted by ConfirmedDeletingBranch")
	}
	if _, err := os.Stat(filepath.Join(a, "wip.txt")); err != nil {
		t.Error("the uncommitted file is gone")
	}
}

// genuine previews kept (on an unpushed unmerged branch: needs Confirm,
// offers -D) and loose (a branch item needing -D) and returns them with the
// engine and kept's tip.
func genuine(t *testing.T) (removal.ExtFixture, removal.Engine, []removal.ItemPreview, string) {
	t.Helper()
	x := removal.NewExtFixture(t)
	p := x.Worktree("kept")
	x.Commit(p, "k.txt", "work", "unpushed")
	x.UnmergedBranch("loose")
	s, r := x.Report()
	e := x.Engine(s)
	ps := e.Preview([]removal.Item{removal.ItemFromRow(x.Row(r, cleanup.KindWorktree, "kept")), removal.ItemFromRow(x.Row(r, cleanup.KindBranch, "loose"))})
	tip, _ := x.RefTip("refs/heads/kept")
	return x, e, ps, tip
}

// TestEditedPreviewIsNotConfirmed (reviewer's TestZZExtEditedPreviewUnion
// and more) edits one field of a genuine preview of kept before confirming:
// the batch union written into Needs, CanDeleteBranch set on a preview that
// did not offer it, the summary Approval, and what Fresh says (losses,
// status, tip). Each is refused by both constructors, and kept's unpushed
// branch survives.
func TestEditedPreviewIsNotConfirmed(t *testing.T) {
	for name, c := range map[string]struct {
		edit     func(p *removal.ItemPreview)
		approved removal.Needs
	}{
		"needs union":   {func(p *removal.ItemPreview) { p.Needs = removal.Needs{Confirm: true, Delete: true} }, removal.Needs{Confirm: true, Delete: true}},
		"needs dropped": {func(p *removal.ItemPreview) { p.Needs = removal.Needs{} }, removal.Needs{}},
		"approval":      {func(p *removal.ItemPreview) { p.Approval = removal.ApprovalProven }, removal.Needs{Confirm: true}},
		"can delete":    {func(p *removal.ItemPreview) { p.CanDeleteBranch = false }, removal.Needs{Confirm: true}},
		"fresh losses":  {func(p *removal.ItemPreview) { p.Fresh.Losses.Unpushed = 0 }, removal.Needs{Confirm: true}},
		"fresh status":  {func(p *removal.ItemPreview) { p.Fresh.Status = cleanup.StatusProven }, removal.Needs{Confirm: true}},
		"fresh tip":     {func(p *removal.ItemPreview) { p.Fresh.Tip = strings.Repeat("0", 40) }, removal.Needs{Confirm: true}},
		"item path":     {func(p *removal.ItemPreview) { p.Item.Path += "x" }, removal.Needs{Confirm: true}},
	} {
		t.Run(name, func(t *testing.T) {
			x, e, ps, tip := genuine(t)
			p := ps[0]
			c.edit(&p)
			if _, err := p.Confirmed(c.approved); err == nil || !strings.Contains(err.Error(), "not produced by Preview") {
				t.Errorf("Confirmed: err %v, want not produced by Preview", err)
			}
			withD := c.approved
			withD.Delete = true
			if it, err := p.ConfirmedDeletingBranch(withD); err == nil {
				e.Run([]removal.Item{it}, nil)
				t.Errorf("ConfirmedDeletingBranch accepted an edited preview")
			}
			if got, ok := x.RefTip("refs/heads/kept"); !ok || got != tip {
				t.Error("refs/heads/kept deleted")
			}
		})
	}
}

// TestGenuinePreviewAndItsCopyConfirm confirms an untouched genuine preview
// and a value copy of one (a caller may store previews in a slice or pass
// them by value): both confirm, and the copy's item runs as Preview said.
func TestGenuinePreviewAndItsCopyConfirm(t *testing.T) {
	x, e, ps, _ := genuine(t)
	copied := ps[1]
	if _, err := ps[0].Confirmed(ps[0].Needs); err != nil {
		t.Errorf("genuine kept: %v", err)
	}
	if _, err := ps[0].ConfirmedDeletingBranch(removal.Needs{Confirm: true, Delete: true}); err != nil {
		t.Errorf("genuine kept opt-in: %v", err)
	}
	it, err := copied.Confirmed(copied.Needs)
	if err != nil {
		t.Fatalf("copy of loose: %v", err)
	}
	if o := e.Run([]removal.Item{it}, nil)[0]; o.Branch != removal.BrDeleted {
		t.Errorf("loose: %s/%s %q, want deleted", o.Worktree, o.Branch, o.Reason)
	}
	if _, ok := x.RefTip("refs/heads/loose"); ok {
		t.Error("refs/heads/loose still there")
	}
}

// TestRefusedPreviewIsNotConfirmed confirms a genuine refused preview (a
// locked worktree) with every approval: an error, never a sealed item.
func TestRefusedPreviewIsNotConfirmed(t *testing.T) {
	x := removal.NewExtFixture(t)
	p := x.MergedWorktree("topic")
	x.Lock(p)
	s, r := x.Report()
	it := removal.ItemFromRow(x.Row(r, cleanup.KindWorktree, "topic"))
	it.Saw = cleanup.StatusProven
	pv := x.Engine(s).Preview([]removal.Item{it})[0]
	if pv.Approval != removal.ApprovalRefused {
		t.Fatalf("precondition: approval %s", pv.Approval)
	}
	all := removal.Needs{Confirm: true, Force: true, Delete: true}
	if _, err := pv.Confirmed(all); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("Confirmed: err %v, want refused", err)
	}
	if _, err := pv.ConfirmedDeletingBranch(all); err == nil {
		t.Error("ConfirmedDeletingBranch accepted a refused preview")
	}
}

// TestRunRefusesUnsealedBranchItem hands Run a branch-only item built by
// hand from a listed row with -D: like a worktree item, it is refused as not
// confirmed and the branch stays.
func TestRunRefusesUnsealedBranchItem(t *testing.T) {
	x := removal.NewExtFixture(t)
	x.UnmergedBranch("loose")
	s, r := x.Report()
	tip, _ := x.RefTip("refs/heads/loose")
	it := removal.ItemFromRow(x.Row(r, cleanup.KindBranch, "loose"))
	it.Approved = removal.Needs{Confirm: true, Delete: true}
	o := x.Engine(s).Run([]removal.Item{it}, nil)[0]
	if o.Worktree != removal.WtRefused || o.Branch != removal.BrNotAttempted || o.Reason != "invalid target: not confirmed" {
		t.Errorf("%s/%s %q, want refused: not confirmed", o.Worktree, o.Branch, o.Reason)
	}
	if got, ok := x.RefTip("refs/heads/loose"); !ok || got != tip {
		t.Error("refs/heads/loose touched")
	}
}
