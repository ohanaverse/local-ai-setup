package removal

import (
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// runRefGit runs the branch stage's update-ref. It is a seam: tests record
// the argument vector or make it fail.
var runRefGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }

// beforeUpdateRef runs between the branch stage's last check and its
// update-ref. It is a test seam (a test moves or deletes the ref there to
// prove the compare-and-swap holds); it does nothing in wt.
var beforeUpdateRef = func() {}

// listWorktrees is cleanup.ListWorktrees; a seam so a test can make the
// branch stage's fresh listing fail.
var listWorktrees = cleanup.ListWorktrees

// deleteBranch is the branch stage for one item: it deletes the local branch
// fresh names, or says why not. It runs only after the worktree stage
// succeeded (ic.considerBranch) or for a branch-only item. The rules, first
// match deciding:
//
//  1. no branch (a detached worktree): not_attempted;
//  2. the default branch (any case, or a <remote>/<default> spelling), or
//     no default branch resolved: retained_default (fail closed);
//  3. the row is Unknown or its tip is not a commit id: retained_unknown;
//  4. the branch alone is not proven (Row.BranchProven, Ruling A; never the
//     combined worktree Status) and -D was not given: retained_unmerged;
//  5. a fresh worktree list (read now, after the removal) shows a worktree
//     with the branch checked out, refnames compared case-folded:
//     retained_checked_out; a list that fails: retained_unknown;
//  6. `git update-ref -d <ref> <tip>`, a compare-and-swap on the tip the
//     re-check read: deleted (verified gone), retained_moved when the ref
//     moved or vanished first, retained_unknown on any other failure.
//
// update-ref -d has none of `git branch -d`'s checks (it deletes a
// checked-out branch, the default branch, unmerged work): every guard above
// is this function's own. What it cannot close: a worktree checking the
// branch out between rule 5 and the update-ref (a window of one git call).
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
	if fresh.Unknown {
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: its state could not be read (%s)", ref, unknownCodes(fresh))
	}
	if !strings.HasPrefix(ref, "refs/heads/") || !commitID.MatchString(fresh.Tip) {
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: its commit could not be read", ref)
	}
	// 4.
	if !fresh.BranchProven && !e.Opts.DeleteUnmerged {
		return BrRetainedUnmerged, fmt.Sprintf("branch %s kept: not proven merged; -D deletes it", ref)
	}
	// 5.
	facts, err := listWorktrees(e.G, e.In.CWD)
	if err != nil {
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: could not list worktrees: %v", ref, err)
	}
	removed := ""
	if ic.dry {
		// A dry run has not removed the item's worktree; the run will have.
		removed = ic.removedPath
	}
	if at := checkedOutIn(facts, ref, removed); at != "" {
		return BrRetainedCheckedOut, fmt.Sprintf("branch %s kept: checked out in worktree %s", ref, at)
	}
	// 6.
	if ic.dry {
		return BrDeleted, "dry run: would delete " + ref
	}
	return e.casDelete(ref, fresh.Tip)
}

// casDelete runs `git update-ref -d ref tip` and judges the result by
// re-reading the ref: git's exit status alone is never taken for it.
func (e Engine) casDelete(ref, tip string) (BranchOutcome, string) {
	short := tip[:7]
	beforeUpdateRef()
	args := []string{"update-ref", "-d", ref, tip}
	out, err := runRefGit(e.G, args...)
	if err == nil && out.Code == 0 {
		now, exists, rerr := e.readRef(ref)
		switch {
		case rerr != nil:
			return BrRetainedUnknown, fmt.Sprintf("branch %s: git reported it deleted but it could not be re-read: %v", ref, rerr)
		case exists:
			return BrRetainedUnknown, fmt.Sprintf("branch %s still exists (at %s) although git reported it deleted", ref, abbrev(now))
		}
		name := strings.TrimPrefix(ref, "refs/heads/")
		return BrDeleted, fmt.Sprintf("deleted %s (was %s; restore with: git branch %s %s)", ref, short, name, short)
	}
	gitErr := ""
	if err != nil {
		gitErr = err.Error()
	} else {
		gitErr = fmt.Sprintf("exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}
	now, exists, rerr := e.readRef(ref)
	switch {
	case rerr != nil:
		return BrRetainedUnknown, fmt.Sprintf("branch %s kept: git update-ref failed (%s) and it could not be re-read: %v", ref, gitErr, rerr)
	case !exists:
		return BrRetainedMoved, fmt.Sprintf("branch %s moved since it was re-checked (it no longer exists)", ref)
	case now != tip:
		return BrRetainedMoved, fmt.Sprintf("branch %s moved since it was re-checked (now %s, was %s)", ref, abbrev(now), short)
	}
	return BrRetainedUnknown, fmt.Sprintf("branch %s kept: git update-ref failed: %s", ref, gitErr)
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

// checkedOutIn is the path of a listed worktree that has ref checked out, or
// "": full refnames compared case-folded (on a case-insensitive volume
// Feature/x and feature/x are one ref file; on a case-sensitive one the fold
// over-matches, which only keeps more). The worktree at removed (resolved)
// is ignored: a dry run's own, not yet removed. Prunable and locked entries
// count: git still treats their branch as checked out.
func checkedOutIn(facts []cleanup.WorktreeFact, ref, removed string) string {
	self := ""
	if removed != "" {
		self = cleanup.ResolvePath(removed)
	}
	for _, w := range facts {
		if !strings.EqualFold(w.Branch, ref) {
			continue
		}
		if self != "" && cleanup.ResolvePath(w.Path) == self {
			continue
		}
		return w.Path
	}
	return ""
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
