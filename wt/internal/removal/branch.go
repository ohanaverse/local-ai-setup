package removal

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// runRefGit runs the branch stage's update-ref calls (the delete and a
// restore). It is a seam: tests record the argument vector or make it fail.
var runRefGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }

// probeGit runs the read-only probes the case-sibling and submodule checks
// make. It is a seam: tests make one fail to pin that it fails closed.
var probeGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }

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
// Approved.Delete they do not keep a branch. Every other Unknown code, about
// identity or about what would be lost (branch_unknown, gates_unavailable,
// no_default_branch, unpushed_unknown), still does.
var verdictOnlyUnknown = map[string]bool{
	cleanup.ReasonMergeProofUnavailable: true,
	cleanup.ReasonPRUnknown:             true,
	cleanup.ReasonPRMergeUnknown:        true,
	cleanup.ReasonCannotVerify:          true,
	cleanup.ReasonNotChecked:            true,
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

// gcCaveat is the warning on every deletion: the restore command works only
// while git keeps the unreachable commit.
const gcCaveat = "the commit survives only until git gc prunes it, possibly the next gc"

// branchResult is the branch stage's answer: the outcome, one reason line,
// and warnings (paths, git's errors, caveats) kept out of the reason.
//
// A reason is capped at 200 runes (Outcome.SetReason). Every reason for a
// branch this stage deleted, or deleted and could not restore, therefore
// names the full commit id immediately after the ref, "<ref> (was <sha>)",
// keeps the rest short, and puts a restore command last: a cut loses only
// prose, never the one record of the commit. Paths and git's text go to
// Warnings.
type branchResult struct {
	Out      BranchOutcome
	Reason   string
	Warnings []string
	// DeletedTip is the full commit id when this stage deleted the branch,
	// or may have (Outcome.DeletedTip, Ruling J): set through deletedTip.
	DeletedTip string
}

// deletedTip is r recording tip as the deleted commit.
func (r branchResult) deletedTip(tip string) branchResult {
	r.DeletedTip = tip
	return r
}

func result(out BranchOutcome, reason string, warnings ...string) branchResult {
	return branchResult{Out: out, Reason: reason, Warnings: warnings}
}

// deleteBranch is the branch stage for one item: it deletes the local branch
// fresh names, or says why not. It runs only after the worktree stage
// succeeded (ic.considerBranch) or for a branch-only item. The rules, first
// match deciding:
//
//  1. no branch (a detached worktree): not_attempted;
//  2. the default branch (any case, or a <remote>/<default> spelling), or
//     no default branch resolved: retained_default (fail closed);
//  3. the ref is not refs/heads/ or its tip is not a 40-hex commit id
//     (sha256 repositories are not supported for branch deletion in this
//     release: "object format not supported"); or the row is Unknown,
//     except (with -D) when its Unknown codes are verdict-only (at least one
//     in verdictOnlyUnknown, none outside it and knownNotUnknown; Ruling H):
//     retained_unknown;
//  4. the branch alone is not proven (Row.BranchProven, Ruling A; never the
//     combined worktree Status) and -D was not given: retained_unmerged;
//  5. a fresh worktree list (read now, after the removal) shows a worktree
//     holding the branch (cleanup.HeldRefs: checked out, or held by a
//     rebase or bisect; refnames compared case-folded): retained_checked_out;
//     a list or a holder that cannot be read: retained_unknown;
//     5b. another local branch whose refname differs only in case (checkCaseSibling,
//     Ruling X), or branches that cannot be listed: retained_unknown;
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
func (e Engine) deleteBranch(ic itemCtx, fresh cleanup.Row) branchResult {
	if !ic.considerBranch {
		return result(BrNotAttempted, "the worktree was not removed, so its branch was not considered")
	}
	ref := fresh.Ref
	// 1.
	if ref == "" {
		return result(BrNotAttempted, "detached HEAD: no branch to delete")
	}
	// 2.
	if e.Prior.NoDefault || e.Prior.Base.Default == "" {
		return result(BrRetainedDefault, fmt.Sprintf("branch %s kept: the default branch could not be resolved", ref))
	}
	if isDefaultForm(ref, e.Prior.Base.Default) {
		return result(BrRetainedDefault, fmt.Sprintf("branch %s kept: it is the default branch", ref))
	}
	// 3.
	if !strings.HasPrefix(ref, "refs/heads/") {
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: not a local branch ref", ref))
	}
	if !cleanup.IsFullCommitID(fresh.Tip) {
		if sha256ID(fresh.Tip) {
			return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: object format not supported (sha256 repositories are not supported for branch deletion in this release)", ref))
		}
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: its commit could not be read", ref))
	}
	if fresh.Unknown && !(ic.item.Approved.Delete && onlyVerdictUnknown(fresh)) {
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: its state could not be read (%s)", ref, unknownCodes(fresh)))
	}
	// 4.
	if !fresh.BranchProven && !ic.item.Approved.Delete {
		return result(BrRetainedUnmerged, fmt.Sprintf("branch %s kept: not proven merged; -D deletes it", ref))
	}
	// 5.
	removed := ""
	if ic.dry {
		// A dry run has not removed the item's worktree; the run will have.
		removed = ic.removedPath
	}
	if h := e.checkHolders(ref, removed); h.Out != "" {
		return h
	}
	// 5b.
	if r := e.checkCaseSibling(ref); r.Out != "" {
		return r
	}
	// 6.
	if ic.dry {
		return result(BrDeleted, "dry run: would delete "+ref)
	}
	r := e.casDelete(ref, fresh.Tip, ic.pending)
	if ic.pending != nil {
		// What the result says, now that it is known: cleared when the
		// compare-and-swap provably did not delete.
		*ic.pending = r.DeletedTip
	}
	return r
}

// onlyVerdictUnknown reports whether r's Unknown is only about the merge
// verdict: at least one code in verdictOnlyUnknown and every code in it or
// in knownNotUnknown. An Unknown row with no codes is not.
func onlyVerdictUnknown(r cleanup.Row) bool {
	verdict := false
	for _, x := range r.Reasons {
		switch {
		case verdictOnlyUnknown[x.Code]:
			verdict = true
		case !knownNotUnknown[x.Code]:
			return false
		}
	}
	return verdict
}

// sha256ID reports a 64-hex object id.
func sha256ID(s string) bool {
	if len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// checkHolders lists the worktrees afresh and returns retained_checked_out
// when one holds ref, retained_unknown when that cannot be read, or a zero
// result when none holds it. The worktree at removed (a dry run's own) is
// skipped. Paths and errors go to warnings.
func (e Engine) checkHolders(ref, removed string) branchResult {
	facts, err := listWorktrees(e.G, e.In.CWD)
	if err != nil {
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: could not list worktrees", ref), "could not list worktrees: "+err.Error())
	}
	at, err := holderOf(e.G, facts, ref, removed)
	switch {
	case err != nil:
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: could not tell which branches a worktree holds", ref), err.Error())
	case at != "":
		return result(BrRetainedCheckedOut, fmt.Sprintf("branch %s kept: held by another worktree (checked out, or a rebase or bisect there)", ref), "held by worktree "+at)
	}
	return branchResult{}
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

// checkCaseSibling returns retained_unknown when another local branch's
// refname differs from ref only in case (or the branches cannot be listed),
// else a zero result. On a case-insensitive volume one loose ref file
// answers to both spellings, so `update-ref -d` on one (with the other
// packed) deletes both and the re-read sees only the one it was asked
// about. It is refused on every filesystem (Ruling X): over-retaining on a
// case-sensitive one is the fail-closed direction.
func (e Engine) checkCaseSibling(ref string) branchResult {
	out, err := probeGit(e.G, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err == nil && out.Code != 0 {
		err = fmt.Errorf("git for-each-ref: exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}
	if err != nil {
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: could not list branches", ref), "could not list branches: "+err.Error())
	}
	for _, name := range strings.Split(out.Stdout, "\n") {
		name = strings.TrimSpace(name)
		if name != "" && name != ref && strings.EqualFold(name, ref) {
			return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: a branch differing only in case exists (%s); git cannot delete one without the other on this volume; delete by hand", ref, name))
		}
	}
	return branchResult{}
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
// branch back if one now holds it (or that cannot be read).
//
// pending (nil-safe) is set to tip immediately before the update-ref is
// issued: if anything after it panics, safeItem still reports the tip.
func (e Engine) casDelete(ref, tip string, pending *string) branchResult {
	was := fmt.Sprintf("%s (was %s)", ref, tip)

	beforeUpdateRef()
	if pending != nil {
		*pending = tip
	}
	out, err := runRefGit(e.G, "update-ref", "--no-deref", "-d", ref, tip)
	if err != nil || out.Code != 0 {
		return e.deleteFailed(ref, tip, gitErrText(err, out))
	}
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		// git reported the delete: it may have happened, so keep the tip.
		return result(BrRetainedUnknown, "branch "+was+": git reported it deleted but it could not be re-read", rerr.Error()).deletedTip(tip)
	case exists && now != tip:
		// The compare-and-swap exited 0: this run deleted tip, and someone
		// re-created the ref at another commit before the re-read.
		return result(BrRetainedUnknown, fmt.Sprintf("git deleted %s (was %s) but it exists again at %s", ref, tip, abbrev(now))).deletedTip(tip)
	case exists:
		// Anomalous: git reported the delete and the ref reads back at the
		// same tip. The run issued a successful delete, so keep the tip.
		return result(BrRetainedUnknown, fmt.Sprintf("branch %s still exists (at %s) although git reported it deleted", was, abbrev(now))).deletedTip(tip)
	}

	// A worktree that checked the branch out (or started a rebase or bisect
	// on it) in the window: put the branch back, create-only. From here on
	// the ref is known gone after this run's update-ref, so every result
	// records the tip (Ruling J).
	afterUpdateRef()
	return e.afterDelete(ref, tip).deletedTip(tip)
}

// afterDelete is casDelete's result once the delete is confirmed: deleted,
// or the branch put back (or not) because a worktree now holds it or that
// cannot be read.
func (e Engine) afterDelete(ref, tip string) branchResult {
	was := fmt.Sprintf("%s (was %s)", ref, tip)
	name := strings.TrimPrefix(ref, "refs/heads/")
	restoreCmd := "restore: " + restoreCommand(ref, tip)
	h := e.checkHolders(ref, "")
	if h.Out == "" {
		// `git branch -D` drops the branch's [branch "<name>"] section
		// (remote, merge); update-ref does not, and a later branch of the
		// same name (the restore command makes one) would inherit that
		// upstream. A missing section is exit 128 and nothing to do.
		warnings := []string{gcCaveat}
		if out, err := e.G.Run("config", "--remove-section", "branch."+name); err != nil || (out.Code != 0 && out.Code != 128) {
			warnings = append(warnings, "the branch's upstream config (branch."+name+".*) could not be removed: "+gitErrText(err, out))
		}
		return result(BrDeleted, fmt.Sprintf("deleted %s (was %s; %s)", ref, tip, restoreCmd), warnings...)
	}
	restored, recreated, why := e.restoreRef(ref, tip)
	switch {
	case restored && h.Out == BrRetainedCheckedOut:
		return result(BrRetainedCheckedOut, "branch "+was+" restored: a worktree took it while it was being deleted", h.Warnings...)
	case restored:
		return result(BrRetainedUnknown, "branch "+was+" restored: the worktrees could not be re-checked after deleting it", h.Warnings...)
	case recreated != "":
		// A create-only restore, or `git branch`, would fail: say what is
		// there now and which commit was deleted.
		return result(BrRetainedUnknown,
			fmt.Sprintf("branch %s deleted; %s was re-created at %s; the deleted commit was %s", was, name, abbrev(recreated), tip),
			append(h.Warnings, gcCaveat)...)
	}
	return result(BrRetainedUnknown, fmt.Sprintf("branch %s deleted and not restored; %s", was, restoreCmd),
		append(h.Warnings, "restoring it failed: "+why, gcCaveat)...)
}

// restoreCommand is the command that puts ref back at tip: `git branch
// <name> <tip>`, or, for a name that starts with '-' (git branch refuses
// it, and `git branch -D <tip>` reads as a delete), `git update-ref <ref>
// <tip> ""`: the empty old value makes it create-only, like `git branch`
// (it fails if the ref was re-created meanwhile, instead of moving it).
func restoreCommand(ref, tip string) string {
	name := strings.TrimPrefix(ref, "refs/heads/")
	if strings.HasPrefix(name, "-") {
		return fmt.Sprintf(`git update-ref %s %s ""`, ref, tip)
	}
	return fmt.Sprintf("git branch %s %s", name, tip)
}

// gitErrText is a failed git call's error as one line.
func gitErrText(err error, out cleanup.Out) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
}

// deleteFailed judges a delete git did not confirm by re-reading the ref.
// Every result that cannot prove the ref still exists records the tip
// (Ruling J): git may have deleted it before failing (a timeout after the
// write).
func (e Engine) deleteFailed(ref, tip, gitErr string) branchResult {
	was := fmt.Sprintf("%s (was %s)", ref, tip)
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		return result(BrRetainedUnknown, "branch "+was+": git update-ref failed and it could not be re-read",
			"git update-ref: "+gitErr, "re-read: "+rerr.Error()).deletedTip(tip)
	case !exists:
		// Someone else deleted it, or git did before it failed (a timeout
		// after the write): fail closed, but say so and give the tip.
		return result(BrRetainedMoved, "branch "+was+" gone after git error; it may have been deleted by this run",
			"git update-ref: "+gitErr).deletedTip(tip)
	case now != tip:
		return result(BrRetainedMoved, fmt.Sprintf("branch %s moved since it was re-checked (now %s, was %s)", ref, abbrev(now), abbrev(tip)))
	}
	return result(BrRetainedUnknown, fmt.Sprintf("branch %s kept: git update-ref failed", ref), "git update-ref: "+gitErr)
}

// restoreRef recreates ref at tip only if it does not exist (the all-zero
// old value, as long as the tip's object format) and judges the result by
// re-reading the ref, never by git's exit alone: restored when it is at tip;
// recreated (its commit) when it exists at another commit; otherwise why
// it is not back.
func (e Engine) restoreRef(ref, tip string) (restored bool, recreated, why string) {
	out, err := runRefGit(e.G, "update-ref", ref, tip, strings.Repeat("0", len(tip)))
	gitErr := ""
	if err != nil || out.Code != 0 {
		gitErr = gitErrText(err, out)
	}
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		return false, "", "could not re-read it: " + rerr.Error()
	case exists && now == tip:
		return true, "", ""
	case exists:
		return false, now, ""
	case gitErr != "":
		return false, "", gitErr
	}
	return false, "", "git reported it restored but it is not there"
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
