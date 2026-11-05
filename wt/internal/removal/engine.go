package removal

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// itemCtx is what the branch stage needs about one item once its worktree
// stage has run: the item as the user saw it, its fresh re-classification,
// and, for a worktree item, the path that was removed.
type itemCtx struct {
	item  Item
	fresh cleanup.Row
	// removedPath is the worktree path `git worktree remove` was given (the
	// repository's spelling); "" for a branch-only item.
	removedPath string
	// considerBranch: the branch stage may act (the worktree stage
	// succeeded, or there was no worktree). It never deletes anything when
	// false.
	considerBranch bool
	// dry: decide only (Plan / DryRun); the worktree at removedPath is
	// still there.
	dry bool
}

// Run acts on items one at a time, in the order given, re-classifying each
// immediately before it is touched (spec "Re-check in PR B"). Every item
// yields exactly one Outcome, in the same order; progress (nil-safe) is
// called after each. With Opts.DryRun it is Plan.
func (e Engine) Run(items []Item, progress func(Outcome)) []Outcome {
	return e.each(items, progress, e.Opts.DryRun)
}

// Plan makes every decision Run would make, re-checking each item the same
// way, and changes nothing: an item Run would remove is WtRemoved with the
// reason "dry run: would remove".
func (e Engine) Plan(items []Item) []Outcome { return e.each(items, nil, true) }

func (e Engine) each(items []Item, progress func(Outcome), dry bool) []Outcome {
	out := make([]Outcome, 0, len(items))
	for _, it := range items {
		o := e.safeItem(it, dry)
		out = append(out, o)
		if progress != nil {
			progress(o)
		}
	}
	return out
}

// safeItem is item with a panic turned into a failed outcome: one item's
// bug must not lose the outcomes of the others or leave the run unreported.
func (e Engine) safeItem(it Item, dry bool) (o Outcome) {
	defer func() {
		if r := recover(); r != nil {
			o = NewOutcome(it, WtFailed, BrNotAttempted, fmt.Sprintf("internal error: %v", r))
		}
	}()
	return e.item(it, dry)
}

func (e Engine) item(it Item, dry bool) Outcome {
	switch it.Kind {
	case cleanup.KindWorktree:
		return e.worktreeItem(it, dry)
	case cleanup.KindBranch:
		return e.branchItem(it, dry)
	}
	return NewOutcome(it, WtRefused, BrNotAttempted, fmt.Sprintf("invalid target: kind %q is not removable", it.Kind))
}

// refuse is a refusal of it's worktree: nothing was touched.
func refuse(it Item, reason string) Outcome {
	return NewOutcome(it, WtRefused, BrNotAttempted, reason)
}

// skipped is it skipped because it changed since the user saw it.
func skipped(it Item, reason string) Outcome {
	return NewOutcome(it, WtSkippedChanged, BrNotAttempted, reason)
}

// worktreeItem runs the gates for one worktree item in their fixed order,
// then (unless dry) removes it and hands over to the branch stage.
//
// What the re-check cannot close. The last fresh read is the rescan; between
// it and `git worktree remove` (the compare window: Rescan to git) the tree
// can change. git itself still refuses a tree that became dirty (unless
// --force was passed, on the approved dirty path), a worktree locked or a
// worktree nested in it since, and the primary checkout. git does NOT
// protect against, in that window: ignored files added (they go with the
// directory), a merge or rebase started, a commit made on a detached HEAD
// (orphaned), or, on the dirty+Force path, any new file at all. The slow
// live-process check therefore runs before the rescan, to keep the window
// as short as the rescan's own gates allow.
func (e Engine) worktreeItem(it Item, dry bool) Outcome {
	// 1. A target no row could have produced, or a "saw" nothing could be
	// compared with.
	if why := e.invalidItem(it); why != "" {
		return refuse(it, "invalid target: "+why)
	}

	// 2. Live processes: a warning, never a refusal. Before the rescan, so
	// it stays out of the compare window.
	warnings := e.procWarnings(it.Path)

	// 3. Re-classify it now.
	fresh, err := rescan(e.G, e.Prior, e.In, e.classifyOpts(), cleanup.Target{Kind: cleanup.KindWorktree, Path: it.Path, Ref: it.Ref})
	if errors.Is(err, cleanup.ErrTargetGone) {
		return skipped(it, "no longer exists")
	}
	if err != nil {
		return refuse(it, "could not re-check: "+err.Error())
	}

	// 4. Exact identity: Rescan folds case, but refs/heads/Topic and
	// refs/heads/topic can be two branches.
	if it.Ref != "" && fresh.Ref != it.Ref {
		return refuse(it, fmt.Sprintf("branch spelling changed: %s is now %s", it.Ref, fresh.Ref))
	}
	if it.Ref == "" && fresh.Ref != "" {
		return refuse(it, "worktree is now on "+fresh.Ref+" (was detached)")
	}

	// 5. Hard blockers: no option overrides them.
	if len(fresh.Blockers) > 0 {
		return refuse(it, blockerReason(fresh.Blockers))
	}
	if fresh.Status == cleanup.StatusInUse {
		return refuse(it, "in use")
	}

	// 6. An unknown is never approved, Force or not.
	if fresh.Losses.GatesUnknown {
		return refuse(it, "could not read the worktree's state")
	}

	// 7. Changed since the user saw it: status, commit, what would be lost.
	if fresh.Status.WorseThan(it.Saw) {
		return skipped(it, fmt.Sprintf("status changed since it was listed (%s, was %s)", fresh.Status, it.Saw))
	}
	tip := fresh.Tip
	if fresh.Ref == "" {
		tip = fresh.Head
	}
	if tip == "" {
		return refuse(it, "could not read the worktree's commit")
	}
	if tip != it.SawTip {
		return skipped(it, "branch moved since it was listed")
	}
	if lossesWorse(fresh.Losses, it.SawLosses) {
		return skipped(it, "what would be lost changed since it was listed")
	}

	// 8. Losses only a flag approves.
	force := false
	if fresh.Losses.Dirty {
		if !e.Opts.Force {
			return refuse(it, "uncommitted changes; --force required")
		}
		force = true // git refuses a dirty worktree without it
	}
	if fresh.Losses.DetachedUnique && fresh.Ref == "" && !e.Opts.Force {
		return refuse(it, "commits that exist only in this worktree; --force required")
	}

	// 9. Defence in depth at the deletion site: git is handed only a valid
	// path that is the one the user ticked.
	if why := e.invalidPath(fresh.Path); why != "" {
		return refuse(it, "invalid target: "+why)
	}
	if cleanup.ResolvePath(fresh.Path) != cleanup.ResolvePath(it.Path) {
		return refuse(it, "path changed: the re-check found "+fresh.Path)
	}

	o := NewOutcome(it, WtRemoved, BrNotAttempted, "")
	for _, w := range warnings {
		o.AddWarning(w)
	}

	ic := itemCtx{item: it, fresh: fresh, removedPath: fresh.Path, considerBranch: true, dry: dry}

	// 10. A dry run removes nothing; it still decides the branch stage, as
	// the run would after a removal.
	if dry {
		r := e.deleteBranch(ic, fresh)
		r.Reason = "dry run: would remove; " + strings.TrimPrefix(r.Reason, "dry run: ")
		o.applyBranch(r)
		return o
	}

	// 11. Remove and verify.
	if wt, reason := e.removeWorktree(fresh.Path, force); wt != WtRemoved {
		o.Worktree = wt
		o.SetReason(reason)
		return o
	}

	// 12. The branch stage.
	o.applyBranch(e.deleteBranch(ic, fresh))
	return o
}

// applyBranch records the branch stage's result on o: its outcome, its
// reason and its warnings (each one sanitised line).
func (o *Outcome) applyBranch(r branchResult) {
	o.Branch = r.Out
	o.SetReason(r.Reason)
	for _, w := range r.Warnings {
		o.AddWarning(w)
	}
}

// branchItem runs the gates for one branch-only item (a local branch with
// no worktree) in the worktree item's order, then hands it to the branch
// stage. A refused or skipped item's branch is not_attempted.
func (e Engine) branchItem(it Item, dry bool) Outcome {
	refuseB := func(reason string) Outcome { return NewOutcome(it, WtRefused, BrNotAttempted, reason) }
	skipB := func(reason string) Outcome { return NewOutcome(it, WtSkippedChanged, BrNotAttempted, reason) }

	// 1. A target no row could have produced.
	if why := invalidBranchItem(it); why != "" {
		return refuseB("invalid target: " + why)
	}

	// 2. Re-classify it now. A branch a worktree now holds is an error here
	// (Rescan gives no branch row for it): refused.
	fresh, err := rescan(e.G, e.Prior, e.In, e.classifyOpts(), cleanup.Target{Kind: cleanup.KindBranch, Ref: it.Ref})
	if errors.Is(err, cleanup.ErrTargetGone) {
		return skipB("no longer exists")
	}
	if err != nil {
		return refuseB("could not re-check: " + err.Error())
	}

	// 3. Exact identity: refs/heads/Topic and refs/heads/topic can be two
	// branches.
	if fresh.Ref != it.Ref {
		return refuseB(fmt.Sprintf("branch spelling changed: %s is now %s", it.Ref, fresh.Ref))
	}

	// 4. Hard blockers.
	if len(fresh.Blockers) > 0 {
		return refuseB(blockerReason(fresh.Blockers))
	}
	if fresh.Status == cleanup.StatusInUse {
		return refuseB("in use")
	}

	// 5. Changed since the user saw it.
	if fresh.Status.WorseThan(it.Saw) {
		return skipB(fmt.Sprintf("status changed since it was listed (%s, was %s)", fresh.Status, it.Saw))
	}
	if fresh.Tip != it.SawTip {
		return skipB("branch moved since it was listed")
	}
	if lossesWorse(fresh.Losses, it.SawLosses) {
		return skipB("what would be lost changed since it was listed")
	}

	// 6. (No live-process check: a branch has no directory.)
	// 7, 8. The branch stage; a dry run decides without writing.
	o := NewOutcome(it, WtNotRequested, BrNotAttempted, "")
	o.applyBranch(e.deleteBranch(itemCtx{item: it, fresh: fresh, considerBranch: true, dry: dry}, fresh))
	return o
}

// invalidBranchItem says why a branch item cannot be acted on before
// anything is read, or "": the ref must be a well-formed refs/heads/ name,
// there is no path, Saw is a tickable status and SawTip a full commit id.
func invalidBranchItem(it Item) string {
	switch {
	case !strings.HasPrefix(it.Ref, "refs/heads/") || len(it.Ref) == len("refs/heads/"):
		return fmt.Sprintf("branch ref %q is not a refs/heads/ name", it.Ref)
	case it.Path != "":
		return "a branch item has no path"
	case !slices.Contains(tickable, it.Saw):
		return fmt.Sprintf("unknown status seen %q", it.Saw)
	case !commitID.MatchString(it.SawTip):
		return fmt.Sprintf("commit seen %q is not a full commit id", it.SawTip)
	}
	return ""
}

// tickable are the statuses a user can tick; Saw must be one of them, or
// "worse than what the user saw" cannot be judged (an unknown status ranks
// worst, so nothing would ever be worse than it).
var tickable = []cleanup.Status{cleanup.StatusProven, cleanup.StatusLessSure, cleanup.StatusUniqueWork}

// commitID is a full commit id as git prints it (cleanup's fullCommitID).
var commitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// invalidItem says why a worktree item cannot be acted on before anything
// is read, or "": its path (invalidPath), its Saw (a tickable status,
// exactly) and its SawTip (a full commit id; empty only for a detached
// worktree, whose tip check then fails as moved).
func (e Engine) invalidItem(it Item) string {
	if why := e.invalidPath(it.Path); why != "" {
		return why
	}
	if !slices.Contains(tickable, it.Saw) {
		return fmt.Sprintf("unknown status seen %q", it.Saw)
	}
	switch {
	case it.SawTip == "" && it.Ref != "":
		return "no commit seen for " + it.Ref
	case it.SawTip != "" && !commitID.MatchString(it.SawTip):
		return fmt.Sprintf("commit seen %q is not a full commit id", it.SawTip)
	}
	return ""
}

// lossesWorse reports whether fresh would lose something saw did not show
// (Ruling G: the user approved a specific loss): newly dirty, newly a unique
// detached HEAD, newly unreadable gates, more ignored entries or bytes, or
// more unpushed commits (a count that became unknown is more; an unknown
// that became a positive count is more too, failing closed).
func lossesWorse(fresh, saw cleanup.Losses) bool {
	switch {
	case fresh.Dirty && !saw.Dirty,
		fresh.DetachedUnique && !saw.DetachedUnique,
		fresh.GatesUnknown && !saw.GatesUnknown,
		fresh.IgnoredEntries > saw.IgnoredEntries,
		fresh.IgnoredBytes > saw.IgnoredBytes:
		return true
	}
	if fresh.Unpushed < 0 {
		return saw.Unpushed >= 0
	}
	if saw.Unpushed < 0 {
		return fresh.Unpushed > 0
	}
	return fresh.Unpushed > saw.Unpushed
}

// rescan is cleanup.Rescan; a seam so tests can hand the engine a row
// Rescan would not.
var rescan = cleanup.Rescan

// classifyOpts are the Classify options for a rescan: the run's clock
// (Opts.Now, else the wall clock) with the list's other options.
func (e Engine) classifyOpts() cleanup.Options {
	o := e.Classify
	if e.Opts.Now != nil {
		o.Now = e.Opts.Now()
	} else {
		o.Now = time.Now()
	}
	return o
}

// invalidPath says why p cannot be a linked worktree of this repository, or
// "" when it may be one: it must be absolute and not the filesystem root,
// not an ancestor of the primary checkout or of the git common dir, and not
// inside the common dir. The primary checkout itself passes here and is
// refused by its blocker.
func (e Engine) invalidPath(p string) string {
	if p == "" {
		return "no path"
	}
	if !filepath.IsAbs(p) {
		return "path is not absolute"
	}
	rp := cleanup.ResolvePath(p)
	if filepath.Dir(rp) == rp {
		return "path is the filesystem root"
	}
	if e.Prior.Repo == "" || e.Prior.CommonDir == "" {
		return "the list's repository is unknown"
	}
	repo, common := cleanup.ResolvePath(e.Prior.Repo), cleanup.ResolvePath(e.Prior.CommonDir)
	primary := strings.EqualFold(rp, repo)
	switch {
	case !primary && cleanup.Within(repo, rp):
		return "path contains the repository"
	case !primary && cleanup.Within(common, rp):
		return "path contains the git directory"
	case cleanup.Within(rp, common):
		return "path is inside the git directory"
	}
	return ""
}

// blockerReason names every hard blocker's code, the first one's detail.
func blockerReason(bs []cleanup.Reason) string {
	codes := make([]string, len(bs))
	for i, b := range bs {
		codes[i] = b.Code
	}
	s := "hard blocker: " + strings.Join(codes, ", ")
	if bs[0].Detail != "" {
		s += " (" + bs[0].Detail + ")"
	}
	return s
}

// maxProcNames is how many command names a live-process warning names.
const maxProcNames = 3

// procWarnings are the warnings for live processes running in dir, or for a
// check that could not run (none without Opts.Procs). It never refuses.
func (e Engine) procWarnings(dir string) []string {
	if e.Opts.Procs == nil {
		return nil
	}
	ps, err := e.Opts.Procs.InDir(dir)
	if err != nil {
		return []string{"could not check for running processes: " + err.Error()}
	}
	if len(ps) == 0 {
		return nil
	}
	var names []string
	for _, p := range ps {
		if len(names) == maxProcNames {
			break
		}
		names = append(names, p.Command)
	}
	more := ""
	if len(ps) > maxProcNames {
		more = ", …"
	}
	word := "processes"
	if len(ps) == 1 {
		word = "process"
	}
	return []string{fmt.Sprintf("%d %s running in the worktree: %s%s", len(ps), word, strings.Join(names, ", "), more)}
}
