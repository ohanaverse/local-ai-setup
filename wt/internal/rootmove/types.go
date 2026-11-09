// Package rootmove is `wt restore-root`'s engine: it moves the branch the
// repository's primary checkout (the root) has checked out into a linked
// worktree under .worktrees, carrying the root's uncommitted work with it,
// and puts the root back on the default branch. Preflight decides whether the
// root can move and reads only; the copy is internal/rescue's (root mode),
// and the one step that resets and switches the root is internal/removal's
// ReleaseRoot, behind its own proofs. Nothing here deletes, resets or moves a
// ref.
package rootmove

import (
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// Refusal and failure codes: stable strings, part of the JSON contract
// (docs/contracts/restore-root-result.*). rescue's codes reach the caller
// unchanged beside these; CodeHiddenIndex is rescue's own string, which
// Preflight also answers for a clean root.
const (
	CodeDetachedRoot        = "detached_root"
	CodeAlreadyDefault      = "already_default"
	CodeDefaultInWorktree   = "default_in_worktree"
	CodeRootHeld            = "root_held"
	CodeNoCommit            = "no_commit"
	CodeReleaseRefused      = "release_refused"
	CodeNoDefaultBranch     = "no_default_branch"
	CodeIgnoredCollision    = "ignored_collision"
	CodeRecordNotWritten    = "record_not_written"
	CodeWorktreesNotIgnored = "worktrees_not_ignored"
	CodeHiddenIndex         = rescue.CodeHiddenIndex
	// CodeReleaseFailed: removal.ReleaseRoot stopped after a write (a
	// *removal.ReleaseFailed); CodeSwitchFailed: the new worktree could not
	// be switched onto the branch (step 5). A read-back mismatch (step 7) is
	// rescue's CodeVerifyFailed.
	CodeReleaseFailed = "release_failed"
	CodeSwitchFailed  = "switch_failed"
)

// Engine moves one repository's root. Rescue is the root-mode rescue engine
// (its G, Prior, In and Classify as `wt rescue` builds them); Rescue.Prior.Repo
// is the root. Rescue.G.Dir is the caller's directory, which may be a linked
// worktree, so rootmove never runs git there: see gitAt.
type Engine struct {
	Rescue rescue.Engine
}

// gitAt is the one way rootmove builds a Git: one that runs in dir with
// Rescue.G's timeout, -c settings and environment, and core.fsmonitor=false
// first (as rescue's own worktree-level git does). Never Rescue.G.Dir.
func (e Engine) gitAt(dir string) cleanup.Git {
	return cleanup.Git{
		Dir:     dir,
		Timeout: e.Rescue.G.Timeout,
		Config:  append([]string{"core.fsmonitor=false"}, e.Rescue.G.Config...),
		Env:     slices.Clone(e.Rescue.G.Env),
	}
}

// Plan is a checked move: everything Execute will do, decided by Preflight,
// which reads only.
type Plan struct {
	Repo     string // the root (the primary checkout)
	Branch   string // the root's branch, short name: the branch that moves
	Default  string // the default branch the root is switched to
	Head     string // the root's HEAD commit, the destination's start
	DirName  string // the destination's directory name under .worktrees
	DestPath string // the destination worktree
	// DefaultFF says origin/<Default> is a strict descendant of the local
	// Default, so a `merge --ff-only` after the switch would move it forward;
	// FFNote says why not when it is false.
	DefaultFF bool
	FFNote    string
	// FFBenign says the note is not a problem (the default is already at
	// origin), so Execute logs it as a step, not a warning.
	FFBenign bool
	Counts   rescue.Counts
	Warnings []string

	rp        *rescue.Plan // the root-mode rescue plan Execute copies with
	skipStart bool         // see SkipStartRecheck
	// common is the absolute git common dir Preflight read once; "" (a Plan
	// not made by Preflight) is read again where used. core.ignorecase is
	// deliberately not kept: Execute's checks read it fresh.
	common string
}

// SkipStartRecheck tells Execute that nothing happened between Preflight and
// it that gave the root time to change (no prompt was shown); see
// rescue.Plan.SkipStartRecheck.
// Execute then skips its start-of-run fingerprint and claim re-checks; the
// checks after the copy and before the release still run.
func (p *Plan) SkipStartRecheck() { p.skipStart = true; p.rp.SkipStartRecheck() }
