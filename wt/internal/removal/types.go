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

// Item is one row the user ticked, as they saw it.
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

// Options are the user's choices for one removal run.
type Options struct {
	Force          bool             // remove a dirty worktree (after the user approved the loss); never overrides a hard blocker
	DeleteUnmerged bool             // -D: delete a branch not proven merged; never overrides a hard blocker
	DryRun         bool             // decide every item, change nothing
	Now            func() time.Time // the clock for re-classification; nil means time.Now
	Procs          ProcFinder       // nil: no live-process check
	RemoveTimeout  time.Duration    // one `git worktree remove`; zero means DefaultRemoveTimeout
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
