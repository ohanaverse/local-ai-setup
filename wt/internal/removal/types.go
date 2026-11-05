// Package removal carries out what `wt cleanup` lists: it removes worktrees
// and deletes local branches the user ticked, re-checking each one first. It
// is the only package in wt allowed to hold deletion code
// (TestDeletionStringsOnlyInRemovalPackage); internal/cleanup, which decides
// what is safe, stays read-only.
package removal

import (
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// DefaultRemoveTimeout bounds one `git worktree remove` when
// Options.RemoveTimeout is zero: a worktree with a large ignored tree
// (.venv, node_modules) takes a while to delete.
const DefaultRemoveTimeout = 10 * time.Minute

// Item is one row the user ticked, as they saw it, with the approvals they
// gave it. Run must only receive Items built by ItemPreview.Confirmed (or,
// for an item the user opted in to delete the unmerged branch of,
// ItemPreview.ConfirmedDeletingBranch) from the preview the user was shown
// (ItemFromRow builds a Preview input, not a Run input): Saw, SawTip and
// SawLosses are then what the confirmation showed, and Approved is what the
// user granted this item (Ruling K).
type Item struct {
	Kind cleanup.Kind
	Path string // worktree items: the worktree's path
	Ref  string // full refname (refs/heads/<b>); "" for a detached worktree
	Name string // the row's name
	// Saw is the status the user saw ticked; the item is skipped if a fresh
	// classification is worse.
	Saw cleanup.Status
	// SawTip is the commit the user saw: the branch tip, or a detached
	// worktree's HEAD. The item is skipped if it moved.
	SawTip string
	// SawLosses is what the user saw would be lost when they approved the
	// item (the row's Losses). The item is skipped if the fresh losses are
	// worse; the zero value means they saw nothing to lose, so any loss is
	// then a skip.
	SawLosses cleanup.Losses
	// Approved are the approvals the user gave THIS item (never a run-wide
	// flag): Confirm for an item that is not proven or loses something,
	// Force for uncommitted changes or a detached HEAD's unique commits,
	// Delete to delete a branch not proven merged (a branch item needs it
	// to do anything; on a worktree item it means "also delete its unmerged
	// branch", given only through ConfirmedDeletingBranch, Ruling M). No
	// approval overrides a hard blocker or an unknown.
	Approved Needs

	// seal is set only by ItemPreview.Confirmed and ConfirmedDeletingBranch
	// (Ruling L): a copy of every exported field, Approved included, as they
	// set it. Run and Plan refuse an item whose
	// seal is missing or no longer matches ("invalid target: not
	// confirmed"), so a hand-built Item, or a confirmed one edited
	// afterwards, never reaches a removal. Being unexported, it cannot be
	// set from another package; copying a whole Item value keeps it.
	seal itemSeal
}

// itemSeal is the exported fields of an Item, plus whether it was set.
type itemSeal struct {
	set       bool
	kind      cleanup.Kind
	path, ref string
	name      string
	saw       cleanup.Status
	sawTip    string
	sawLosses cleanup.Losses
	approved  Needs
}

// sealOf is it's exported fields as a set seal.
func sealOf(it Item) itemSeal {
	return itemSeal{set: true, kind: it.Kind, path: it.Path, ref: it.Ref, name: it.Name,
		saw: it.Saw, sawTip: it.SawTip, sawLosses: it.SawLosses, approved: it.Approved}
}

// confirmed reports whether it came unmodified from Confirmed or
// ConfirmedDeletingBranch.
func (it Item) confirmed() bool { return it.seal.set && it.seal == sealOf(it) }

// Needs is a set of approvals: what an item needs (ItemPreview.Needs) or
// what the user gave it (Item.Approved). Confirm is the typed confirmation
// (TUI) or --accept-loss (CLI); Force is --force; Delete is -D.
type Needs struct {
	Confirm bool
	Force   bool
	Delete  bool
}

// covers reports whether every approval need asks for is in n.
func (n Needs) covers(need Needs) bool {
	return (n.Confirm || !need.Confirm) && (n.Force || !need.Force) && (n.Delete || !need.Delete)
}

// WorktreeOutcome is what happened to an item's worktree. The values are wire
// strings of the removal JSON contract; never rename one.
type WorktreeOutcome string

const (
	WtRemoved        WorktreeOutcome = "removed"         // the worktree is gone
	WtSkippedChanged WorktreeOutcome = "skipped_changed" // it changed since the user saw it (status worse, tip moved)
	WtRefused        WorktreeOutcome = "refused"         // a hard blocker, or a loss the flags do not approve
	WtFailed         WorktreeOutcome = "failed"          // git failed or timed out, or the worktree is still there
	WtNotRequested   WorktreeOutcome = "not_requested"   // a branch-only item: there was no worktree to remove
)

// BranchOutcome is what happened to an item's local branch. The values are
// wire strings of the removal JSON contract; never rename one.
type BranchOutcome string

const (
	BrDeleted            BranchOutcome = "deleted"              // the ref is gone (compare-and-swap on the tip held)
	BrNotAttempted       BranchOutcome = "not_attempted"        // no branch (detached), or the worktree stage did not succeed
	BrRetainedUnmerged   BranchOutcome = "retained_unmerged"    // not proven merged and -D not given
	BrRetainedMoved      BranchOutcome = "retained_moved"       // the tip moved before the delete
	BrRetainedCheckedOut BranchOutcome = "retained_checked_out" // another worktree has it checked out
	BrRetainedDefault    BranchOutcome = "retained_default"     // the default branch, or the default is unresolved
	BrRetainedUnknown    BranchOutcome = "retained_unknown"     // its state could not be read, or git failed
)

// Outcome is the result for one item. Reason and every Warnings entry are one
// sanitised line (they carry git's stderr and OS errors): set them through
// NewOutcome, SetReason and AddWarning.
type Outcome struct {
	Item     Item
	Worktree WorktreeOutcome
	Branch   BranchOutcome
	Reason   string
	Warnings []string
	// LossLines are what removing the item would lose as the engine saw it
	// fresh when it decided (cleanup.LossLines of the re-check, one
	// sanitised line each); empty when the item was refused or skipped
	// before a fresh row existed.
	LossLines []string
	// DeletedTip is the full commit id of the branch when this run deleted
	// it, or may have (Ruling J): on every path where the branch stage
	// issued update-ref -d and cannot prove the ref still exists. That is:
	// deleted; deleted, then restored (a worktree took it, or the worktrees
	// could not be re-checked); deleted and not restored, or re-created by
	// someone else; git reported the delete but the ref could not be
	// re-read; git's update-ref failed and the ref was gone afterwards
	// (retained_moved) or could not be re-read (retained_unknown); and a
	// panic after the update-ref was issued (safeItem: failed /
	// retained_unknown). Never capped, unlike Reason: it is the one
	// reliable record of the commit. Empty for dry runs, for a delete git
	// refused with the ref still there, and for every branch this run did
	// not touch.
	DeletedTip string
}

// NewOutcome is an Outcome for it with reason sanitised to one line.
func NewOutcome(it Item, wt WorktreeOutcome, br BranchOutcome, reason string) Outcome {
	o := Outcome{Item: it, Worktree: wt, Branch: br}
	o.SetReason(reason)
	return o
}

// SetReason sets Reason to s as one sanitised, capped line.
func (o *Outcome) SetReason(s string) { o.Reason = cleanup.OneLine(s) }

// AddWarning appends s to Warnings as one sanitised, capped line.
func (o *Outcome) AddWarning(s string) { o.Warnings = append(o.Warnings, cleanup.OneLine(s)) }

// ProcFinder lists the live processes whose working directory is dir or below
// it (a warning before removing a worktree, never a refusal). Tests stub it;
// no test scans the machine.
type ProcFinder interface {
	InDir(dir string) ([]Proc, error)
}

// Proc is one live process found by a ProcFinder.
type Proc struct {
	PID     int
	Command string
}

// Options are the run's settings. Approvals are per item (Item.Approved),
// never run-wide: a flag one item needed must not reach another.
type Options struct {
	DryRun        bool             // decide every item, change nothing
	Now           func() time.Time // the clock for re-classification; nil means time.Now
	Procs         ProcFinder       // nil: no live-process check
	RemoveTimeout time.Duration    // one `git worktree remove`; zero means DefaultRemoveTimeout
}

// Engine removes items from one repository. Prior is the snapshot the user's
// list was built from; In and Classify are what built it, reused for each
// item's rescan.
type Engine struct {
	G        cleanup.Git
	Prior    cleanup.Snapshot
	In       cleanup.GatherInputs
	Classify cleanup.Options
	Opts     Options
}
