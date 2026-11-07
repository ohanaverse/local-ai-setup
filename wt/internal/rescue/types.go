// Package rescue copies a worktree's local-only work into a new branch and a
// new linked worktree, leaving the source exactly as it found it. It only
// creates: the source is read (git with optional locks off, never an index
// refresh), the destination is new, and nothing here deletes, switches or
// moves a ref. A failure after the destination exists keeps it, so the half
// built result is visible and recoverable (`wt remove` has its own proofs).
// Deletion belongs to internal/removal alone.
//
// The engine takes any worktree as its source (the primary checkout too) and
// a destination name; policy about which sources a command offers lives in
// the command layer and in Preflight's refusals.
package rescue

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// Limits on what Preflight accepts to copy: an untracked build directory
// that was not ignored would otherwise be copied silently. Fixed, no flag.
const (
	MaxUntrackedBytes = 512 << 20
	MaxUntrackedFiles = 20000
)

// BranchPrefix starts the default name of the new branch.
const BranchPrefix = "rescued/"

// Refusal codes: stable strings, part of the JSON contract
// (docs/contracts/rescue-result.schema.json).
const (
	CodeNotWorktree      = "not_worktree"
	CodePrimary          = "primary"
	CodeNothingToKeep    = "nothing_to_keep"
	CodeGatesUnknown     = "gates_unknown"
	CodeOperation        = "operation_in_progress"
	CodeUnmerged         = "unmerged_paths"
	CodeHiddenIndex      = "hidden_index_flags"
	CodeSubmodule        = "submodule_change"
	CodeStagedAndEdited  = "staged_and_modified"
	CodeIntentToAdd      = "intent_to_add"
	CodeNestedRepo       = "nested_repo"
	CodeSpecialFile      = "special_file"
	CodeTooLarge         = "too_large"
	CodeLineEndings      = "line_endings"
	CodeNameUnavailable  = "name_unavailable"
	CodeSourceChanged    = "source_changed"
	CodeUnreadable       = "unreadable"
	CodeVerifyFailed     = "verify_failed"
	CodeCarryFailed      = "carry_failed"
	CodeCreateFailed     = "create_failed"
	CodeSourceChangedMid = "source_changed_while_copying"
)

// Refusal is a reason nothing was created, with its stable code.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(code, msg string) *Refusal { return &Refusal{Code: code, Message: msg} }

// Source is the worktree whose work is copied: its path, the full refname of
// the branch it has checked out ("" when detached) and its HEAD commit.
type Source struct {
	Path string
	Ref  string
	Head string
}

// Counts is what a rescue carries and what it leaves behind.
type Counts struct {
	Commits        int   // commits reachable from no remote-tracking ref (0 when unknown)
	Staged         int   // paths with a staged change
	Unstaged       int   // paths with an unstaged change
	Untracked      int   // untracked, non-ignored paths copied
	UntrackedBytes int64 // their size
	IgnoredLeft    int   // ignored entries not carried
}

// Plan is a checked rescue: everything Execute will do, decided by Preflight
// and reading only.
type Plan struct {
	Repo     string // the primary checkout, where the new worktree is registered
	Source   Source
	Branch   string // the new branch, short name
	DestPath string // the new worktree
	Start    string // the commit the new branch starts at: the source's HEAD
	Counts   Counts
	Warnings []string

	untracked []string    // paths to copy, relative to the source
	fp        fingerprint // the source as Preflight found it
}

// Status is the frozen outcome string.
type Status string

const (
	StatusMoved     Status = "moved"
	StatusWouldMove Status = "would_move"
	StatusRefused   Status = "refused"
	StatusFailed    Status = "failed"
)

// Outcome is what a rescue did. Created says whether the destination exists
// (so a failed outcome with Created set names a half-built destination to
// keep). Completed lists the steps that finished.
type Outcome struct {
	Status    Status
	Plan      Plan
	Code      string // the Refusal or failure code; "" for moved and would_move
	Reason    string
	Created   bool
	Completed []string
}

// Engine runs a rescue in one repository. G runs git in the repository (any
// of its worktrees); Prior, In and Classify are what cleanup.Rescan needs to
// re-check the source row, as internal/removal's engine takes them.
type Engine struct {
	G        cleanup.Git
	Prior    cleanup.Snapshot
	In       cleanup.GatherInputs
	Classify cleanup.Options
}
