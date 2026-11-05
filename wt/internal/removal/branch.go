package removal

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// runRefGit runs the branch stage's update-ref calls (the delete and a
// restore). It is a seam: tests record the argument vector or make it fail.
var runRefGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }

// beforeUpdateRef runs between the branch stage's last check and its
// update-ref; afterUpdateRef between a delete git confirmed and the re-check
// of the worktrees. They are test seams (a test moves the ref, or checks it
// out, there); they do nothing in wt.
var (
	beforeUpdateRef = func() {}
	afterUpdateRef  = func() {}
)

// listWorktrees and heldRefs are cleanup.ListWorktrees and cleanup.HeldRefs;
// seams so a test can make the branch stage's fresh reads fail.
var (
	listWorktrees = cleanup.ListWorktrees
	heldRefs      = cleanup.HeldRefs
)

// verdictOnlyUnknown are the Unknown reason codes that only answer "is the
// branch merged?" (Ruling H). -D deletes regardless of that answer, so with
// DeleteUnmerged they do not keep a branch. Every other Unknown code, about
// identity or about what would be lost (branch_unknown, gates_unavailable,
// no_default_branch, unpushed_unknown), still does.
var verdictOnlyUnknown = map[string]bool{
	cleanup.ReasonMergeProofUnavailable: true,
	cleanup.ReasonPRUnknown:             true,
	cleanup.ReasonPRMergeUnknown:        true,
	cleanup.ReasonCannotVerify:          true,
}

// knownNotUnknown are the reason codes that never make a row Unknown (unique
// work and soft findings). A row's code in neither this set nor
// verdictOnlyUnknown is treated as an Unknown that keeps the branch, so a
// code added to the classifier later fails closed.
var knownNotUnknown = map[string]bool{
	cleanup.ReasonUnpushed:          true,
	cleanup.ReasonNetZeroUnpushed:   true,
	cleanup.ReasonPRHeadMoved:       true,
	cleanup.ReasonOpenPR:            true,
	cleanup.ReasonDirty:             true,
	cleanup.ReasonDetachedUnique:    true,
	cleanup.ReasonNotMerged:         true,
	cleanup.ReasonIgnoredFiles:      true,
	cleanup.ReasonWeakSignal:        true,
	cleanup.ReasonFreshBranch:       true,
	cleanup.ReasonBaseIsLocal:       true,
	cleanup.ReasonNoMergedPR:        true,
	cleanup.ReasonClaimsUnavailable: true,
}

// deleteBranch is the branch stage for one item: it deletes the local branch
// fresh names, or says why not. It runs only after the worktree stage
// succeeded (ic.considerBranch) or for a branch-only item. The rules, first
// match deciding:
//
//  1. no branch (a detached worktree): not_attempted;
//  2. the default branch (any case, or a <remote>/<default> spelling), or
//     no default branch resolved: retained_default (fail closed);
//  3. the ref is not refs/heads/ or its tip is not a commit id; or the row
//     is Unknown, except (with -D) when every Unknown code only answers "is
//     it merged?" (verdictOnlyUnknown, Ruling H): retained_unknown;
//  4. the branch alone is not proven (Row.BranchProven, Ruling A; never the
//     combined worktree Status) and -D was not given: retained_unmerged;
//  5. a fresh worktree list (read now, after the removal) shows a worktree
//     holding the branch (cleanup.HeldRefs: checked out, or held by a
//     rebase or bisect; refnames compared case-folded): retained_checked_out;
//     a list or a holder that cannot be read: retained_unknown;
//  6. `git update-ref --no-deref -d <ref> <tip>`, a compare-and-swap on the
//     tip the re-check read: deleted (verified gone, and no worktree holds
//     it afterwards), retained_moved when the ref moved or vanished first,
//     retained_unknown on any other failure.
//
// update-ref -d has none of `git branch -d`'s checks (it deletes a
// checked-out branch, the default branch, unmerged work): every guard above
// is this function's own. A worktree that checks the branch out between
// rule 5 and the update-ref is caught by the re-check after it, which puts
// the branch back create-only. --no-deref: a ref that became a symbolic ref
// in the window is deleted itself, never its target.
//
// With ic.dry it runs rules 1 to 5 and reports what rule 6 would do,
// running nothing that writes.
func (e Engine) deleteBranch(ic itemCtx, fresh cleanup.Row) (BranchOutcome, string) {
	if !ic.considerBranch {
		return BrNotAttempted, "the worktree was not removed, so its branch was not considered"
	}
	ref := fresh.Ref
	// 1.
	if ref == "" {
		return BrNotAttempted, "detached HEAD: no branch to delete"
	}
	// 2.
	if e.Prior.NoDefault || e.Prior.Base.Default == "" {
		return BrRetainedDefault, fmt.Sprintf("branch %s kept: the default branch could not be resolved", ref)
	}
	if isDefaultForm(ref, e.Prior.Base.Default) {
		return BrRetainedDefault, fmt.Sprintf("branch %s kept: it is the default branch", ref)
	}
	// 3.
	if !strings.HasPrefix(ref, "refs/heads/") || !commitID.MatchString(fresh.Tip) {
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: its commit could not be read", ref)
	}
	if fresh.Unknown && !(e.Opts.DeleteUnmerged && onlyVerdictUnknown(fresh)) {
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: its state could not be read (%s)", ref, unknownCodes(fresh))
	}
	// 4.
	if !fresh.BranchProven && !e.Opts.DeleteUnmerged {
		return BrRetainedUnmerged, fmt.Sprintf("branch %s kept: not proven merged; -D deletes it", ref)
	}
	// 5.
	removed := ""
	if ic.dry {
		// A dry run has not removed the item's worktree; the run will have.
		removed = ic.removedPath
	}
	if out, reason := e.checkHolders(ref, removed); out != "" {
		return out, reason
	}
	// 6.
	if ic.dry {
		return BrDeleted, "dry run: would delete " + ref
	}
	return e.casDelete(ref, fresh.Tip)
}

// onlyVerdictUnknown reports whether every reason code of r that can make a
// row Unknown is in verdictOnlyUnknown.
func onlyVerdictUnknown(r cleanup.Row) bool {
	for _, x := range r.Reasons {
		if !verdictOnlyUnknown[x.Code] && !knownNotUnknown[x.Code] {
			return false
		}
	}
	return true
}

// checkHolders lists the worktrees afresh and returns retained_checked_out
// when one holds ref, retained_unknown when that cannot be read, or "" when
// none holds it. The worktree at removed (a dry run's own) is skipped.
func (e Engine) checkHolders(ref, removed string) (BranchOutcome, string) {
	facts, err := listWorktrees(e.G, e.In.CWD)
	if err != nil {
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: could not list worktrees: %v", ref, err)
	}
	at, err := holderOf(e.G, facts, ref, removed)
	switch {
	case err != nil:
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: %v", ref, err)
	case at != "":
		// The path before the explanation: the line cap must not cut it.
		return BrRetainedCheckedOut, fmt.Sprintf("branch %s kept: held by worktree %s (checked out, or a rebase or bisect there)", ref, at)
	}
	return "", ""
}

// holderOf is the path of a listed worktree that holds ref (heldRefs), or
// "". The worktree at removed (resolved) is skipped. Prunable and locked
// entries count: git still treats what they hold as in use.
func holderOf(g cleanup.Git, facts []cleanup.WorktreeFact, ref, removed string) (string, error) {
	self := ""
	if removed != "" {
		self = cleanup.ResolvePath(removed)
	}
	for _, w := range facts {
		if self != "" && cleanup.ResolvePath(w.Path) == self {
			continue
		}
		if refHeld([]string{w.Branch}, ref) {
			return w.Path, nil
		}
		held, err := heldRefs(g, w)
		if err != nil {
			return "", fmt.Errorf("could not tell which branches worktree %s holds: %v", w.Path, err)
		}
		if refHeld(held, ref) {
			return w.Path, nil
		}
	}
	return "", nil
}

// refHeld reports whether ref is among held, full refnames compared
// case-folded: on a case-insensitive volume Feature/x and feature/x are one
// ref file; on a case-sensitive one the fold over-matches, which only keeps
// more.
func refHeld(held []string, ref string) bool {
	for _, h := range held {
		if h != "" && strings.EqualFold(h, ref) {
			return true
		}
	}
	return false
}

// casDelete runs `git update-ref --no-deref -d ref tip` and judges the
// result by re-reading the ref: git's exit status alone is never taken for
// it. After a confirmed delete it re-checks the worktrees and puts the
// branch back if one now holds it.
func (e Engine) casDelete(ref, tip string) (BranchOutcome, string) {
	beforeUpdateRef()
	out, err := runRefGit(e.G, "update-ref", "--no-deref", "-d", ref, tip)
	if err != nil || out.Code != 0 {
		return e.deleteFailed(ref, tip, err, out)
	}
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		return BrRetainedUnknown, fmt.Sprintf("branch %s: git reported it deleted but it could not be re-read (was %s): %v", ref, tip, rerr)
	case exists:
		return BrRetainedUnknown, fmt.Sprintf("branch %s still exists (at %s) although git reported it deleted", ref, abbrev(now))
	}

	// A worktree that checked the branch out (or started a rebase or bisect
	// on it) in the window: put the branch back, create-only.
	afterUpdateRef()
	if br, why := e.checkHolders(ref, ""); br != "" {
		if rerr := e.restoreRef(ref, tip); rerr != "" {
			return BrRetainedUnknown, fmt.Sprintf("branch %s deleted, then a worktree held it or that could not be checked (%s), and restoring it failed: %s; restore with: git branch %s %s",
				ref, why, rerr, strings.TrimPrefix(ref, "refs/heads/"), tip)
		}
		if br == BrRetainedCheckedOut {
			return BrRetainedCheckedOut, fmt.Sprintf("branch %s: a worktree checked it out while it was being deleted; restored at %s", ref, abbrev(tip))
		}
		return BrRetainedUnknown, fmt.Sprintf("branch %s restored at %s: after deleting it, the worktrees could not be re-checked (%s)", ref, abbrev(tip), why)
	}
	name := strings.TrimPrefix(ref, "refs/heads/")
	return BrDeleted, fmt.Sprintf("deleted %s (was %s; restore with: git branch %s %s until git gc prunes it, possibly the next gc)", ref, tip, name, tip)
}

// deleteFailed judges a delete git did not confirm by re-reading the ref.
func (e Engine) deleteFailed(ref, tip string, err error, out cleanup.Out) (BranchOutcome, string) {
	gitErr := ""
	if err != nil {
		gitErr = err.Error()
	} else {
		gitErr = fmt.Sprintf("exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: git update-ref failed (%s) and it could not be re-read (was %s): %v", ref, gitErr, tip, rerr)
	case !exists:
		// Someone else deleted it, or git did before it failed (a timeout
		// after the write): fail closed, but say so and give the tip.
		return BrRetainedMoved, fmt.Sprintf("branch %s gone after git error; it may have been deleted by this run (was %s; %s)", ref, tip, gitErr)
	case now != tip:
		return BrRetainedMoved, fmt.Sprintf("branch %s moved since it was re-checked (now %s, was %s)", ref, abbrev(now), abbrev(tip))
	}
	return BrRetainedUnknown, fmt.Sprintf("branch %s kept: git update-ref failed: %s", ref, gitErr)
}

// restoreRef recreates ref at tip only if it does not exist (the all-zero
// old value, as long as the tip's object format), and confirms it by
// re-reading. It returns "" or why it failed.
func (e Engine) restoreRef(ref, tip string) string {
	out, err := runRefGit(e.G, "update-ref", ref, tip, strings.Repeat("0", len(tip)))
	switch {
	case err != nil:
		return err.Error()
	case out.Code != 0:
		return fmt.Sprintf("exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		return rerr.Error()
	case !exists || now != tip:
		return "git reported it restored but it is not at " + tip
	}
	return ""
}

// readRef reads ref's tip with one for-each-ref naming it, matching the
// exact spelling (a pattern also matches refs below it, and rev-parse would
// resolve another case's ref file on a case-insensitive volume).
func (e Engine) readRef(ref string) (tip string, exists bool, err error) {
	args := []string{"for-each-ref", "--format=%(refname)%00%(objectname)", ref}
	out, err := e.G.Run(args...)
	if err != nil {
		return "", false, err
	}
	if out.Code != 0 {
		return "", false, fmt.Errorf("git for-each-ref: exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}
	for _, line := range strings.Split(out.Stdout, "\n") {
		name, sha, ok := strings.Cut(line, "\x00")
		if ok && name == ref {
			return strings.TrimSpace(sha), true, nil
		}
	}
	return "", false, nil
}

// isDefaultForm reports whether the local ref names the default branch db:
// the name itself, or any <x>/<db> (a local branch spelled like a remote's
// default), case-folded. It over-matches a branch such as fix/main, which is
// then kept: fail closed.
func isDefaultForm(ref, db string) bool {
	name := strings.ToLower(strings.TrimPrefix(ref, "refs/heads/"))
	db = strings.ToLower(db)
	return name == db || strings.HasSuffix(name, "/"+db)
}

// unknownCodes names the row's reasons, for a retained_unknown reason.
func unknownCodes(r cleanup.Row) string {
	var codes []string
	for _, x := range r.Reasons {
		codes = append(codes, x.Code)
	}
	if len(codes) == 0 {
		return "no detail"
	}
	return strings.Join(codes, ", ")
}

// abbrev is the first seven characters of a commit id.
func abbrev(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
