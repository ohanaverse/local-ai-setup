package removal

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// ItemFromRow builds the PREVIEW INPUT for a listed row: what the user
// ticked (kind, path, ref, name) and what the list showed (status, commit,
// and the row's whole Losses, an unknown unpushed count and unreadable gates
// included; never a zero or recomputed value, Ruling G). The commit is the
// branch tip, or for a detached worktree its HEAD (the commit the engine
// compares for it). It carries no approvals.
//
// Run must not receive it: pass it to Engine.Preview, show the preview, and
// hand Run the Item ItemPreview.Confirmed (or ConfirmedDeletingBranch)
// builds from what the user confirmed (Ruling K; Tasks 9 and 10 depend on this).
//
// Every kind converts, a remote row included: the engine refuses a remote
// item ("not removable"), so a caller that lets one through gets a refusal,
// never a silently re-kinded item.
func ItemFromRow(r cleanup.Row) Item {
	tip := r.Tip
	if r.Kind == cleanup.KindWorktree && r.Ref == "" {
		tip = r.Head
	}
	return Item{
		Kind:      r.Kind,
		Path:      r.Path,
		Ref:       r.Ref,
		Name:      r.Name,
		Saw:       r.Status,
		SawTip:    tip,
		SawLosses: r.Losses,
	}
}

// Approval is the summary of what a caller must obtain from the user before
// confirming an item (Ruling B). The values are wire strings; never rename
// one.
type Approval string

const (
	// ApprovalProven: proven with nothing to lose; a plain yes (--yes).
	ApprovalProven Approval = "proven"
	// ApprovalNeedsConfirm: not proven, or loses something no flag covers
	// (ignored files only, fresh_branch, pr_unknown, a kept unproven
	// branch...); a typed confirmation in the TUI, --accept-loss on the CLI.
	ApprovalNeedsConfirm Approval = "needs_confirm"
	// ApprovalNeedsForce: uncommitted changes, or a detached HEAD's unique
	// commits; --force plus the typed confirmation.
	ApprovalNeedsForce Approval = "needs_force"
	// ApprovalNeedsDelete: a branch item whose branch is not proven merged;
	// -D plus the typed confirmation.
	ApprovalNeedsDelete Approval = "needs_delete"
	// ApprovalRefused: Run would refuse it, skip it, or do nothing to it;
	// ItemPreview.Reason is the engine's reason. No approval removes it.
	ApprovalRefused Approval = "refused"
)

// ItemPreview is what a confirmation shows for one item, decided by the
// engine's own gates (the ones Run uses) and changing nothing.
type ItemPreview struct {
	Item Item
	// Fresh is the item's re-classification now (zero when the item was
	// refused before it could be read, or is gone).
	Fresh cleanup.Row
	// LossLines say what removing it would lose (cleanup.LossLines(Fresh)).
	LossLines []string
	// Procs are the live processes in the worktree (a warning only).
	Procs []Proc
	// Warnings: the process check could not run, and the branch stage's
	// warnings. One sanitised line each.
	Warnings []string
	// Approval summarises Needs: needs_force over needs_delete over
	// needs_confirm over proven; or refused.
	Approval Approval
	// Needs are the approvals Confirmed requires (needsOf, the rule Run
	// enforces); every approval a refused item would need is false.
	Needs Needs
	// CanDeleteBranch: a worktree item whose branch is not proven merged and
	// would be deleted if the user individually opts in to it
	// (ConfirmedDeletingBranch; plain Confirmed refuses Delete); without it
	// the branch is kept (see Notes). An optional extra approval, not a need.
	CanDeleteBranch bool
	// Reason is the engine's reason for a refused item; "" otherwise.
	Reason string
	// Notes are lines for the confirmation that ask nothing, such as
	// "branch topic will be kept: not proven merged". One sanitised line each.
	Notes []string
	// Plan is what Run does with Approved == Needs (a dry-run outcome); for
	// a refused item, the refusal or skip.
	Plan Outcome
}

// Confirmed is the Item Run must receive once the user confirmed this
// preview with approved: Kind, Path, Ref and Name from the preview's item,
// and what the preview SHOWED (Saw, SawTip, SawLosses from Fresh, so a loss
// that appears after the confirmation is worse and skipped), with Approved
// = approved, sealed (Ruling L: Run and Plan refuse any Item that did not
// come unmodified from here or ConfirmedDeletingBranch). It is an error for
// a refused preview, when approved does not cover Needs, or when approved
// holds anything Needs does not (Ruling M: approved must equal what the
// item needs, so Delete only for a branch-only item that needs it). A
// union of approvals across items is therefore refused, and one item's -D
// or --force never reaches another. Deleting the unmerged branch a worktree
// item's preview merely offers (CanDeleteBranch) is not done here: see
// ConfirmedDeletingBranch.
func (p ItemPreview) Confirmed(approved Needs) (Item, error) {
	return p.confirm(approved, p.Needs)
}

// ConfirmedDeletingBranch is Confirmed for an item whose unmerged branch the
// USER individually opted in to delete as well: approved must hold Delete,
// and the preview must offer it (CanDeleteBranch) or need it (a branch-only
// item); otherwise it has Confirmed's checks and seal. A caller makes this
// call per item, only for an item the user opted in for, and never derives
// it from a batch-wide flag: a CLI -D may apply only to the items whose
// preview offers it, by calling this method for each of them.
func (p ItemPreview) ConfirmedDeletingBranch(approved Needs) (Item, error) {
	name := cleanup.OneLine(p.Item.Name)
	if !approved.Delete {
		return Item{}, fmt.Errorf("%s: deleting the branch needs the Delete approval", name)
	}
	if !p.CanDeleteBranch && !p.Needs.Delete {
		return Item{}, fmt.Errorf("%s: its preview offers no branch to delete", name)
	}
	allowed := p.Needs
	allowed.Delete = true
	return p.confirm(approved, allowed)
}

// confirm builds the sealed Item for p when approved covers p.Needs and
// holds nothing outside allowed.
func (p ItemPreview) confirm(approved, allowed Needs) (Item, error) {
	name := cleanup.OneLine(p.Item.Name)
	if p.Approval == ApprovalRefused {
		return Item{}, fmt.Errorf("%s: refused: %s", name, p.Reason)
	}
	if p.Fresh.Status == "" {
		return Item{}, fmt.Errorf("%s: the preview has no re-check to confirm", name)
	}
	if !approved.covers(p.Needs) {
		return Item{}, fmt.Errorf("%s: approvals %+v do not cover what it needs %+v", name, approved, p.Needs)
	}
	if !allowed.covers(approved) {
		return Item{}, fmt.Errorf("%s: approval not required for this item: %+v given, %+v allowed", name, approved, allowed)
	}
	shown := ItemFromRow(p.Fresh)
	it := Item{
		Kind:      p.Item.Kind,
		Path:      p.Item.Path,
		Ref:       p.Item.Ref,
		Name:      p.Item.Name,
		Saw:       shown.Saw,
		SawTip:    shown.SawTip,
		SawLosses: shown.SawLosses,
		Approved:  approved,
	}
	it.seal = sealOf(it)
	return it, nil
}

// Preview decides every item as Run would, through the same gates, with
// every approval granted (so it reports what is needed and never masks a
// refusal), and says what each needs approved; it changes nothing. A
// worktree item's unproven branch is judged without Delete (kept; a note,
// and CanDeleteBranch when Delete would delete it). One live-process
// listing serves every item when the finder supports it (LsofFinder).
func (e Engine) Preview(items []Item) []ItemPreview {
	if b, ok := e.Opts.Procs.(batchFinder); ok {
		e.Opts.Procs = b.snapshot()
	}
	out := make([]ItemPreview, 0, len(items))
	for _, it := range items {
		out = append(out, e.safePreview(it))
	}
	return out
}

// safePreview is previewItem with a panic turned into a refusal.
func (e Engine) safePreview(it Item) (p ItemPreview) {
	defer func() {
		if r := recover(); r != nil {
			o := NewOutcome(it, WtFailed, BrNotAttempted, fmt.Sprintf("internal error: %v", r))
			p = ItemPreview{Item: it, Approval: ApprovalRefused, Reason: o.Reason, Plan: o}
		}
	}()
	return e.previewItem(it)
}

func (e Engine) previewItem(it Item) ItemPreview {
	full := it
	full.Approved = Needs{Confirm: true, Force: true, Delete: it.Kind == cleanup.KindBranch}
	g := e.gate(full)
	p := ItemPreview{Item: it, Procs: g.procs, Fresh: g.fresh}
	p.Warnings = oneLines(procWarnings(nil, g.procErr))
	if g.hasFresh {
		p.LossLines = cleanup.LossLines(g.fresh)
	}
	if g.stop != nil {
		p.Approval, p.Reason, p.Plan = ApprovalRefused, g.stop.Reason, *g.stop
		p.Plan.Item = it
		return p
	}

	plan, branchWarnings := e.act(full, g, true)
	plan.Item = it
	p.Plan = plan
	p.Warnings = append(p.Warnings, branchWarnings...)
	if it.Kind == cleanup.KindBranch && plan.Branch != BrDeleted {
		// A branch item whose branch would stay does nothing at all.
		p.Approval, p.Reason = ApprovalRefused, plan.Reason
		return p
	}

	p.Needs = g.need
	switch {
	case p.Needs.Force:
		p.Approval = ApprovalNeedsForce
	case p.Needs.Delete:
		p.Approval = ApprovalNeedsDelete
	case p.Needs.Confirm:
		p.Approval = ApprovalNeedsConfirm
	default:
		p.Approval = ApprovalProven
	}

	fresh := g.fresh
	if it.Kind == cleanup.KindWorktree && fresh.Ref != "" && plan.Branch != BrDeleted {
		if !fresh.BranchProven {
			withDelete := full
			withDelete.Approved.Delete = true
			alt, _ := e.act(withDelete, g, true)
			p.CanDeleteBranch = alt.Branch == BrDeleted
		}
		p.Notes = append(p.Notes, keptNote(fresh.Ref, plan))
	}
	return p
}

// keptNote is the preview line, one sanitised line, for a worktree item's
// branch that Run would keep.
func keptNote(ref string, plan Outcome) string {
	name := strings.TrimPrefix(ref, "refs/heads/")
	if plan.Branch == BrRetainedUnmerged {
		return cleanup.OneLine("branch " + name + " will be kept: not proven merged")
	}
	return cleanup.OneLine("branch " + name + " will be kept: " + strings.TrimPrefix(plan.Reason, "dry run: would remove; "))
}
