package removal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	// pending (nil for previews) is safeItem's record of a commit whose
	// branch may be deleted: set just before update-ref -d is issued, then
	// to the result's DeletedTip. A panic after it keeps the restore id.
	pending *string
}

// Run acts on items one at a time, in the order given, re-classifying each
// immediately before it is touched (spec "Re-check in PR B"). Every item
// yields exactly one Outcome, in the same order; progress (nil-safe) is
// called after each; a panic in it becomes a warning on that item's Outcome
// and the run goes on. With Opts.DryRun it is Plan.
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
		if progress != nil {
			if w := safeProgress(progress, o); w != "" {
				o.AddWarning(w)
			}
		}
		out = append(out, o)
	}
	return out
}

// safeProgress calls progress with o and returns a warning when it panics,
// "" otherwise: a caller's display bug must not lose the outcomes (and the
// restore ids) of a run that already changed the repository.
func safeProgress(progress func(Outcome), o Outcome) (warning string) {
	defer func() {
		if r := recover(); r != nil {
			warning = fmt.Sprintf("progress callback panicked: %v", r)
		}
	}()
	progress(o)
	return ""
}

// safeItem is item with a panic turned into a failed outcome: one item's
// bug must not lose the outcomes of the others or leave the run unreported.
// A panic once the branch stage issued update-ref -d keeps the commit as
// DeletedTip (Ruling J): the branch may be gone.
func (e Engine) safeItem(it Item, dry bool) (o Outcome) {
	var pending string
	defer func() {
		if r := recover(); r != nil {
			if pending == "" {
				o = NewOutcome(it, WtFailed, BrNotAttempted, fmt.Sprintf("internal error: %v", r))
				return
			}
			wt := WtFailed
			if it.Kind == cleanup.KindBranch {
				// A branch-only item has no worktree that could have failed.
				wt = WtNotRequested
			}
			o = NewOutcome(it, wt, BrRetainedUnknown,
				fmt.Sprintf("internal error after a branch deletion; restore with the tip %s: %v", pending, r))
			o.DeletedTip = pending
		}
	}()
	return e.item(it, dry, &pending)
}

// item is Run's and Plan's path for one item. The first check: it must have
// come unmodified from ItemPreview.Confirmed or ConfirmedDeletingBranch
// (Ruling L). Preview, which takes ItemFromRow items as input, does not
// come through here. pending is safeItem's (itemCtx.pending).
func (e Engine) item(it Item, dry bool, pending *string) Outcome {
	if !it.confirmed() {
		return refuse(it, "invalid target: not confirmed")
	}
	g := e.gate(it)
	if g.stop != nil {
		return *g.stop
	}
	g.pending = pending
	o, _ := e.act(it, g, dry)
	return o
}

// gated is the engine's decision about one item before anything is touched:
// either stop (refused or skipped, final) or the fresh row and what acting
// on it needs. Run, Plan and Preview all decide through gate, so a preview
// can never disagree with a run about a refusal.
type gated struct {
	stop  *Outcome    // non-nil: the item's final outcome (refused or skipped)
	fresh cleanup.Row // the re-classification (zero when none was read)
	// hasFresh: fresh was read (the rescan ran and found the target).
	hasFresh bool
	// procs and procErr are the live-process check's answer (worktree items).
	procs   []Proc
	procErr error
	// need is what the fresh row needs approved (needsOf); gitForce: git
	// must be passed --force (dirty, and the item has Approved.Force).
	need     Needs
	gitForce bool
	// pending is handed to the branch stage (itemCtx.pending); nil in a
	// preview.
	pending *string
}

// needsOf is what removing an item of kind k whose fresh row is fresh needs
// approved, the one rule Run enforces and Preview reports: Confirm when it
// is not proven or would lose anything (a loss line); Force for uncommitted
// changes or a detached HEAD's unique commits; Delete for a branch item
// whose branch is not proven merged (it does nothing without deleting it).
// A worktree item's unproven branch is not a need: without Approved.Delete
// it is kept.
func needsOf(k cleanup.Kind, fresh cleanup.Row) Needs {
	return Needs{
		Confirm: fresh.Status != cleanup.StatusProven || len(cleanup.LossLines(fresh)) > 0,
		Force:   fresh.Losses.Dirty || fresh.Losses.DetachedUnique,
		Delete:  k == cleanup.KindBranch && !fresh.BranchProven,
	}
}

// stopWith is a gate's final answer o; with a fresh row (has), o carries
// its loss lines, so a refusal or skip still says what was at stake.
func stopWith(o Outcome, fresh cleanup.Row, has bool) gated {
	if has {
		o.LossLines = lossLines(fresh)
	}
	return gated{stop: &o, fresh: fresh, hasFresh: has}
}

// lossLines are fresh's loss lines (cleanup.LossLines), one sanitised line
// each: Outcome.LossLines.
func lossLines(fresh cleanup.Row) []string { return oneLines(cleanup.LossLines(fresh)) }

// gate runs every check for it in its fixed order (worktreeGate,
// branchGate); an item of any other kind is refused.
func (e Engine) gate(it Item) gated {
	switch it.Kind {
	case cleanup.KindWorktree:
		return e.worktreeGate(it)
	case cleanup.KindBranch:
		return e.branchGate(it)
	}
	return stopWith(NewOutcome(it, WtRefused, BrNotAttempted, fmt.Sprintf("invalid target: kind %q is not removable", it.Kind)), cleanup.Row{}, false)
}

// act carries out an item gate let through (dry: decide the branch stage
// only, change nothing). It also returns the branch stage's warnings, which
// the Outcome holds after any process warnings.
func (e Engine) act(it Item, g gated, dry bool) (Outcome, []string) {
	if it.Kind == cleanup.KindWorktree {
		return e.worktreeAct(it, g, dry)
	}
	return e.branchAct(it, g, dry)
}

// refuse is a refusal of it's worktree: nothing was touched.
func refuse(it Item, reason string) Outcome {
	return NewOutcome(it, WtRefused, BrNotAttempted, reason)
}

// skipped is it skipped because it changed since the user saw it.
func skipped(it Item, reason string) Outcome {
	return NewOutcome(it, WtSkippedChanged, BrNotAttempted, reason)
}

// worktreeGate runs the gates for one worktree item in their fixed order;
// worktreeAct then (unless dry) removes it and hands over to the branch
// stage.
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
func (e Engine) worktreeGate(it Item) gated {
	// 1. A target no row could have produced, or a "saw" nothing could be
	// compared with.
	if why := e.invalidItem(it); why != "" {
		return stopWith(refuse(it, "invalid target: "+why), cleanup.Row{}, false)
	}

	// 2. Live processes: a warning, never a refusal. Before the rescan, so
	// it stays out of the compare window.
	procs, procErr := e.findProcs(it.Path)

	// 3. Re-classify it now.
	fresh, err := rescan(e.G, e.Prior, e.In, e.classifyOpts(), cleanup.Target{Kind: cleanup.KindWorktree, Path: it.Path, Ref: it.Ref})
	stop := func(o Outcome) gated {
		g := stopWith(o, fresh, err == nil)
		g.procs, g.procErr = procs, procErr
		return g
	}
	if errors.Is(err, cleanup.ErrTargetGone) {
		return stop(skipped(it, "no longer exists"))
	}
	if err != nil {
		return stop(refuse(it, "could not re-check: "+err.Error()))
	}

	// 4. Exact identity: Rescan folds case, but refs/heads/Topic and
	// refs/heads/topic can be two branches.
	if it.Ref != "" && fresh.Ref != it.Ref {
		return stop(refuse(it, fmt.Sprintf("branch spelling changed: %s is now %s", it.Ref, fresh.Ref)))
	}
	if it.Ref == "" && fresh.Ref != "" {
		return stop(refuse(it, "worktree is now on "+fresh.Ref+" (was detached)"))
	}

	// 5. Hard blockers: no option overrides them.
	if len(fresh.Blockers) > 0 {
		return stop(refuse(it, blockerReason(fresh.Blockers)))
	}
	if fresh.Status == cleanup.StatusInUse {
		return stop(refuse(it, "in use"))
	}

	// 6. An unknown is never approved, Force or not.
	if fresh.Losses.GatesUnknown {
		return stop(refuse(it, "could not read the worktree's state"))
	}

	// 7. Changed since the user saw it: status, commit, what would be lost.
	if fresh.Status.WorseThan(it.Saw) {
		return stop(skipped(it, fmt.Sprintf("status changed since it was listed (%s, was %s)", fresh.Status, it.Saw)))
	}
	tip := fresh.Tip
	if fresh.Ref == "" {
		tip = fresh.Head
	}
	if tip == "" {
		return stop(refuse(it, "could not read the worktree's commit"))
	}
	if tip != it.SawTip {
		return stop(skipped(it, "branch moved since it was listed"))
	}
	if lossesWorse(fresh.Losses, it.SawLosses) {
		return stop(skipped(it, "what would be lost changed since it was listed"))
	}

	// 8. What only this item's own approvals allow (Ruling K).
	g := gated{fresh: fresh, hasFresh: true, procs: procs, procErr: procErr, need: needsOf(it.Kind, fresh)}
	if why := unapproved(it, fresh, g.need); why != "" {
		return stop(refuse(it, why))
	}
	g.gitForce = fresh.Losses.Dirty // git refuses a dirty worktree without it

	// 8b. git skips its "contains submodules" check under --force and deletes
	// an initialised submodule's repository with the tree (Ruling X): the
	// force path is refused for such a worktree, never taken.
	if g.gitForce {
		if why := submodulesIn(fresh.Path); why != "" {
			return stop(refuse(it, why))
		}
	}

	// 9. Defence in depth at the deletion site: git is handed only a valid
	// path that is the one the user ticked.
	if why := e.invalidPath(fresh.Path); why != "" {
		return stop(refuse(it, "invalid target: "+why))
	}
	if cleanup.ResolvePath(fresh.Path) != cleanup.ResolvePath(it.Path) {
		return stop(refuse(it, "path changed: the re-check found "+fresh.Path))
	}
	return g
}

// submodulesIn says why the worktree at path may not be removed with
// --force because of submodules, or "": its admin dir holds a modules/
// directory (a submodule repository git would delete), or `git submodule
// status` lists an initialised module (a line not starting with '-'). Either
// read failing refuses too (fail closed).
func submodulesIn(path string) string {
	const refused = "contains submodules; remove by hand"
	g := cleanup.Git{Dir: path}
	out, err := probeGit(g, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil || out.Code != 0 || strings.TrimSpace(out.Stdout) == "" {
		return "could not check for submodules (the worktree's git dir); remove by hand"
	}
	if _, err := os.Stat(filepath.Join(strings.TrimSpace(out.Stdout), "modules")); err == nil {
		return refused
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "could not check for submodules: " + err.Error() + "; remove by hand"
	}
	out, err = probeGit(g, "submodule", "status")
	if err != nil || out.Code != 0 {
		return "could not check for submodules (git submodule status); remove by hand"
	}
	for _, line := range strings.Split(out.Stdout, "\n") {
		if line != "" && !strings.HasPrefix(line, "-") {
			return refused
		}
	}
	return ""
}

// unapproved says why it's own approvals do not cover need, or "": Force
// first (uncommitted changes, then a detached HEAD's unique commits), then
// Confirm. Delete is the branch stage's (a branch not approved for deletion
// is kept, never refused here).
func unapproved(it Item, fresh cleanup.Row, need Needs) string {
	switch {
	case need.Force && !it.Approved.Force && fresh.Losses.Dirty:
		return "uncommitted changes; --force required"
	case need.Force && !it.Approved.Force:
		return "commits that exist only in this worktree; --force required"
	case need.Confirm && !it.Approved.Confirm:
		return fmt.Sprintf("needs confirmation: %s, not proven (or something would be lost)", fresh.Status)
	}
	return ""
}

// worktreeAct removes a worktree item worktreeGate let through, then runs
// the branch stage; dry decides the branch stage and removes nothing. It
// returns the branch stage's warnings apart too (the Outcome carries the
// process warnings first, then those).
func (e Engine) worktreeAct(it Item, g gated, dry bool) (Outcome, []string) {
	fresh := g.fresh
	o := NewOutcome(it, WtRemoved, BrNotAttempted, "")
	o.LossLines = lossLines(fresh)
	for _, w := range procWarnings(g.procs, g.procErr) {
		o.AddWarning(w)
	}

	ic := itemCtx{item: it, fresh: fresh, removedPath: fresh.Path, considerBranch: true, dry: dry, pending: g.pending}

	// 10. A dry run removes nothing; it still decides the branch stage, as
	// the run would after a removal.
	if dry {
		r := e.deleteBranch(ic, fresh)
		r.Reason = "dry run: would remove; " + strings.TrimPrefix(r.Reason, "dry run: ")
		return o.applyBranch(r), oneLines(r.Warnings)
	}

	// 11. Remove and verify.
	if wt, reason := e.removeWorktree(fresh.Path, g.gitForce); wt != WtRemoved {
		o.Worktree = wt
		o.SetReason(reason)
		return o, nil
	}

	// 12. The branch stage.
	r := e.deleteBranch(ic, fresh)
	return o.applyBranch(r), oneLines(r.Warnings)
}

// oneLines is ws, each as one sanitised line.
func oneLines(ws []string) []string {
	var out []string
	for _, w := range ws {
		out = append(out, cleanup.OneLine(w))
	}
	return out
}

// applyBranch records the branch stage's result on o: its outcome, its
// reason and its warnings (each one sanitised line).
func (o Outcome) applyBranch(r branchResult) Outcome {
	o.Branch = r.Out
	o.DeletedTip = r.DeletedTip
	o.SetReason(r.Reason)
	for _, w := range r.Warnings {
		o.AddWarning(w)
	}
	return o
}

// branchGate runs the gates for one branch-only item (a local branch with
// no worktree) in the worktree item's order; branchAct then hands it to the
// branch stage. A refused or skipped item's branch is not_attempted.
func (e Engine) branchGate(it Item) gated {
	// 1. A target no row could have produced.
	if why := invalidBranchItem(it); why != "" {
		return stopWith(refuse(it, "invalid target: "+why), cleanup.Row{}, false)
	}

	// 2. Re-classify it now. A branch a worktree now holds is an error here
	// (Rescan gives no branch row for it): refused.
	fresh, err := rescan(e.G, e.Prior, e.In, e.classifyOpts(), cleanup.Target{Kind: cleanup.KindBranch, Ref: it.Ref})
	refuseB := func(reason string) gated { return stopWith(refuse(it, reason), fresh, err == nil) }
	skipB := func(reason string) gated { return stopWith(skipped(it, reason), fresh, err == nil) }
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
	// 7. What only this item's own approvals allow (Ruling K): Confirm here;
	// Delete in the branch stage (without it the branch is kept).
	g := gated{fresh: fresh, hasFresh: true, need: needsOf(it.Kind, fresh)}
	if why := unapproved(it, fresh, g.need); why != "" {
		return refuseB(why)
	}
	return g
}

// branchAct is the branch stage (8, 9) for a branch item branchGate let
// through; a dry run decides without writing.
func (e Engine) branchAct(it Item, g gated, dry bool) (Outcome, []string) {
	r := e.deleteBranch(itemCtx{item: it, fresh: g.fresh, considerBranch: true, dry: dry, pending: g.pending}, g.fresh)
	o := NewOutcome(it, WtNotRequested, BrNotAttempted, "")
	o.LossLines = lossLines(g.fresh)
	return o.applyBranch(r), oneLines(r.Warnings)
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

// findProcs asks Opts.Procs for the live processes running in dir (none,
// and no error, without one).
func (e Engine) findProcs(dir string) ([]Proc, error) {
	if e.Opts.Procs == nil {
		return nil, nil
	}
	return e.Opts.Procs.InDir(dir)
}

// procWarnings are the warnings for live processes ps running in a worktree,
// or for a check that could not run (err). They never refuse.
func procWarnings(ps []Proc, err error) []string {
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
