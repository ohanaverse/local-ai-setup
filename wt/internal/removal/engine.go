package removal

import (
	"errors"
	"fmt"
	"path/filepath"
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
}

// deleteBranch is the branch stage; it is filled in by the branch-deletion
// task. Until then it deletes nothing.
func (e Engine) deleteBranch(ic itemCtx, fresh cleanup.Row) (BranchOutcome, string) {
	return BrNotAttempted, "branch handling arrives in the next task"
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
		return NewOutcome(it, WtNotRequested, BrNotAttempted, "branch items are handled in the branch stage")
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
func (e Engine) worktreeItem(it Item, dry bool) Outcome {
	// 1. A target no row could have produced.
	if why := e.invalidPath(it.Path); why != "" {
		return refuse(it, "invalid target: "+why)
	}

	// 2. Re-classify it now.
	fresh, err := cleanup.Rescan(e.G, e.Prior, e.In, e.classifyOpts(), cleanup.Target{Kind: cleanup.KindWorktree, Path: it.Path, Ref: it.Ref})
	if errors.Is(err, cleanup.ErrTargetGone) {
		return skipped(it, "no longer exists")
	}
	if err != nil {
		return refuse(it, "could not re-check: "+err.Error())
	}

	// 3. Exact identity: Rescan folds case, but refs/heads/Topic and
	// refs/heads/topic can be two branches.
	if it.Ref != "" && fresh.Ref != it.Ref {
		return refuse(it, fmt.Sprintf("branch spelling changed: %s is now %s", it.Ref, fresh.Ref))
	}
	if it.Ref == "" && fresh.Ref != "" {
		return refuse(it, "worktree is now on "+fresh.Ref+" (was detached)")
	}

	// 4. Hard blockers: no option overrides them.
	if len(fresh.Blockers) > 0 {
		return refuse(it, blockerReason(fresh.Blockers))
	}
	if fresh.Status == cleanup.StatusInUse {
		return refuse(it, "in use")
	}

	// 5. An unknown is never approved, Force or not.
	if fresh.Losses.GatesUnknown {
		return refuse(it, "could not read the worktree's state")
	}

	// 6. Changed since the user saw it.
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

	// 7. Losses only a flag approves.
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

	o := NewOutcome(it, WtRemoved, BrNotAttempted, "")

	// 8. Live processes: a warning, never a refusal.
	e.warnProcs(&o, fresh.Path)

	// 9. Dry run stops here.
	if dry {
		o.SetReason("dry run: would remove")
		return o
	}

	// 10. Remove and verify.
	if wt, reason := e.removeWorktree(fresh.Path, force); wt != WtRemoved {
		o.Worktree = wt
		o.SetReason(reason)
		return o
	}

	// 11. The branch stage.
	br, reason := e.deleteBranch(itemCtx{item: it, fresh: fresh, removedPath: fresh.Path, considerBranch: true}, fresh)
	o.Branch = br
	o.SetReason(reason)
	return o
}

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

// warnProcs adds a warning to o when live processes run in dir, or when the
// check could not run. It never refuses.
func (e Engine) warnProcs(o *Outcome, dir string) {
	if e.Opts.Procs == nil {
		return
	}
	ps, err := e.Opts.Procs.InDir(dir)
	if err != nil {
		o.AddWarning("could not check for running processes: " + err.Error())
		return
	}
	if len(ps) == 0 {
		return
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
	o.AddWarning(fmt.Sprintf("%d %s running in the worktree: %s%s", len(ps), word, strings.Join(names, ", "), more))
}
