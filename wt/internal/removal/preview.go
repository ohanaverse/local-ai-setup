package removal

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// ItemFromRow is the one way a caller builds an Item from the row the user
// saw: what they ticked (kind, path, ref, name) and what they saw (status,
// commit, and the row's whole Losses, an unknown unpushed count and
// unreadable gates included; never a zero or recomputed value, Ruling G).
// The commit is the branch tip, or for a detached worktree its HEAD (the
// commit the engine compares for it).
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

// Approval is what a caller must obtain from the user before passing an item
// to Run (Ruling B). The values are wire strings; never rename one.
type Approval string

const (
	// ApprovalProven: proven with nothing to lose; a plain yes (--yes).
	ApprovalProven Approval = "proven"
	// ApprovalNeedsConfirm: not proven, but nothing it loses needs a flag
	// (ignored files only, fresh_branch, pr_unknown, a kept unproven
	// branch...); a typed confirmation in the TUI, --accept-loss on the CLI.
	ApprovalNeedsConfirm Approval = "needs_confirm"
	// ApprovalNeedsForce: uncommitted changes, or a detached HEAD's unique
	// commits; --force plus the typed confirmation.
	ApprovalNeedsForce Approval = "needs_force"
	// ApprovalNeedsDelete: the branch would be deleted although not proven
	// merged; -D plus the typed confirmation.
	ApprovalNeedsDelete Approval = "needs_delete"
	// ApprovalRefused: Run would refuse it, skip it, or do nothing to it;
	// ItemPreview.Reason is the engine's reason. No approval removes it.
	ApprovalRefused Approval = "refused"
)

// Needs is every approval an item needs (it can need more than one: a dirty
// worktree whose unmerged branch -D would delete needs Force and Delete).
// Confirm is the typed confirmation (TUI) or --accept-loss (CLI), needed by
// every item that is not plain proven; Force is --force; Delete is -D. A
// refused item needs nothing (nothing approves it). The caller must obtain
// all of them; ItemPreview.Approval only summarises.
type Needs struct {
	Confirm bool
	Force   bool
	Delete  bool
}

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
	// Approval is the strongest of Needs (needs_force over needs_delete over
	// needs_confirm over proven), or refused.
	Approval Approval
	Needs    Needs
	// Reason is the engine's reason for a refused item; "" otherwise.
	Reason string
	// Notes are lines for the confirmation that ask nothing, such as
	// "branch topic will be kept: not proven merged".
	Notes []string
	// Plan is what Run would do given the approvals in Needs (Plan's dry-run
	// outcome); for a refused item, the refusal or skip.
	Plan Outcome
}

// Preview decides every item as Run would, through the same gates, and
// says what each needs approved; it changes nothing. A worktree item is
// judged with Force (Force never overrides a refusal, so this only shows
// whether it needs --force); a branch item with DeleteUnmerged (it can only
// be removed by deleting its branch). A worktree item's branch is judged
// with Opts.DeleteUnmerged as given: without it an unproven branch is kept,
// a note and not an approval question.
func (e Engine) Preview(items []Item) []ItemPreview {
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
	pe := e
	pe.Opts.Force = true
	pe.Opts.DryRun = true
	if it.Kind == cleanup.KindBranch {
		pe.Opts.DeleteUnmerged = true
	}
	g := pe.gate(it)
	p := ItemPreview{Item: it, Procs: g.procs, Fresh: g.fresh}
	procLines := procWarnings(nil, g.procErr)
	p.Warnings = append(p.Warnings, procLines...)
	if g.hasFresh {
		p.LossLines = cleanup.LossLines(g.fresh)
	}
	if g.stop != nil {
		p.Approval, p.Reason, p.Plan = ApprovalRefused, g.stop.Reason, *g.stop
		return p
	}

	plan := pe.act(it, g, true)
	p.Plan = plan
	// The branch stage's warnings follow the process lines act put first.
	if n := len(procWarnings(g.procs, g.procErr)); n <= len(plan.Warnings) {
		p.Warnings = append(p.Warnings, plan.Warnings[n:]...)
	}
	fresh := g.fresh
	if it.Kind == cleanup.KindBranch && plan.Branch != BrDeleted {
		// A branch item whose branch would stay does nothing at all.
		p.Approval, p.Reason = ApprovalRefused, plan.Reason
		return p
	}

	p.Needs.Force = g.needForce
	p.Needs.Delete = plan.Branch == BrDeleted && !fresh.BranchProven
	plain := fresh.Status == cleanup.StatusProven && fresh.Losses == (cleanup.Losses{})
	p.Needs.Confirm = !plain || p.Needs.Force || p.Needs.Delete
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

	if it.Kind == cleanup.KindWorktree && plan.Branch != BrDeleted && plan.Branch != BrNotAttempted {
		p.Notes = append(p.Notes, keptNote(fresh.Ref, plan))
	}
	return p
}

// keptNote is the preview line for a worktree item's branch that Run would
// keep.
func keptNote(ref string, plan Outcome) string {
	name := strings.TrimPrefix(ref, "refs/heads/")
	if plan.Branch == BrRetainedUnmerged {
		return "branch " + name + " will be kept: not proven merged"
	}
	return "branch " + name + " will be kept: " + strings.TrimPrefix(plan.Reason, "dry run: would remove; ")
}
