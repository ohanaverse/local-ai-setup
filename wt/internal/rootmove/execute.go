package rootmove

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// Outcome is what a move did. Created says the new worktree exists (a failed
// outcome with Created set names a destination to keep); Completed lists
// the steps that finished; Record is the rescue record the release relied
// on (nil when the root had nothing uncommitted, or none was written);
// FastForwarded says the default branch was fast-forwarded (step 6).
type Outcome struct {
	Status        rescue.Status
	Plan          Plan
	Code, Reason  string
	Created       bool
	Completed     []string
	Record        *rescue.Record
	FastForwarded bool
}

// step names a step of Execute that can fail: the key of the recovery
// table.
type step int

const (
	stepCreate      step = iota + 1 // 1: the detached worktree
	stepCarry                       // 2: carry and verify
	stepRecord                      // 3: the record
	stepBranch                      // 3b: the new branch in the destination (a detached root)
	stepBeforeReset                 // 4, before the reset: a refused release
	stepAfterReset                  // 4, after the reset: a *removal.ReleaseFailed
	stepDestSwitch                  // 5: the destination onto the branch
	stepBranchMoved                 // 5, the branch moved before the switch: not switched
	stepReadBack                    // 7: the final read-back
)

// branchDonePrefix starts the Completed line step 3b adds; EncodeResult reads
// it as branch_created.
const branchDonePrefix = "created the branch "

// run is one Execute's outcome and what the recovery text needs.
type run struct {
	o Outcome
	p *Plan
}

func (r *run) done(s string) { r.o.Completed = append(r.o.Completed, s) }

// refuse ends the run as refused: nothing was created.
func (r *run) refuse(code, format string, a ...any) Outcome {
	r.o.Status, r.o.Code, r.o.Reason = rescue.StatusRefused, code, fmt.Sprintf(format, a...)
	return r.o
}

// fail ends the run as failed at s: the reason is what happened, then the
// recovery table's sentence for s (rf is the release failure at
// stepAfterReset).
func (r *run) fail(s step, rf *removal.ReleaseFailed, code, format string, a ...any) Outcome {
	what := strings.TrimRight(fmt.Sprintf(format, a...), ". ")
	r.o.Status, r.o.Code = rescue.StatusFailed, code
	r.o.Reason = what + ". " + r.recovery(s, rf)
	return r.o
}

// recordNote is ", the copy and the record" when a record exists, else
// ", the copy".
func (r *run) recordNote() string {
	if r.o.Record != nil {
		return ", the copy and the record " + r.o.Record.Ref
	}
	return " and the copy"
}

// work says where the root's uncommitted work is once the copy exists.
func (r *run) work() string {
	if r.o.Record == nil {
		if r.p.Detached {
			return "there was nothing uncommitted; the commits are held by " + r.holders()
		}
		return "there was nothing uncommitted; the branch's commits are on " + r.p.Branch + ", which never moved"
	}
	return fmt.Sprintf("the work is in the copy at %s and in the record %s", r.p.DestPath, r.o.Record.Ref)
}

// holders names what keeps a detached root's commits: the branch created for
// them (once it exists), or the branches that already hold them.
func (r *run) holders() string {
	if r.p.NewBranch != "" {
		return "the branch " + r.p.NewBranch + " (and the root's HEAD)"
	}
	return strings.Join(r.p.ReachableFrom, ", ")
}

// shortHead is the first eight characters of the plan's HEAD.
func (r *run) shortHead() string {
	if len(r.p.Head) > 8 {
		return r.p.Head[:8]
	}
	return r.p.Head
}

// rootWas names what the root was on: its branch, or its detached HEAD.
func (r *run) rootWas() string {
	if r.p.Detached {
		return "its detached HEAD " + r.shortHead()
	}
	return r.p.Branch
}

// destIs says what the verified copy is, once it exists and the release is
// not done: detached at the branch's tip, detached at the root's commit, or
// (after step 3b) on the new branch.
func (r *run) destIs(afterBranch bool) string {
	switch {
	case !r.p.Detached:
		return "detached at the tip of " + r.p.Branch
	case r.p.NewBranch != "" && afterBranch:
		return "on the new branch " + r.p.NewBranch
	}
	return "detached at " + r.shortHead()
}

// recovery is the spec's recovery table, keyed by the step that failed:
// where the root, the new worktree and the work are, and how to finish.
func (r *run) recovery(s step, rf *removal.ReleaseFailed) string {
	p := r.p
	b, d, dest := p.Branch, p.Default, p.DestPath
	finish := fmt.Sprintf("To finish by hand: `git switch %s` in the root, then `git switch %s` in %s (running `wt restore-root` again is refused, the directory exists).", d, b, dest)
	if p.Detached {
		// The new worktree is already final (detached, or on the new branch):
		// there is nothing to switch there.
		finish = fmt.Sprintf("To finish by hand: `git switch %s` in the root (after `git status` there).", d)
	}
	// A root that may hold leftovers (the reset may not have run, carried
	// files may remain) must be looked at before it is switched, or the
	// switch carries them onto the default.
	finishDirty := fmt.Sprintf("To finish by hand: first run `git status` in the root and make sure whatever it lists is also in the copy at %s before you clean it, then `git switch %s` in the root, then `git switch %s` in %s (running `wt restore-root` again is refused, the directory exists).", dest, d, b, dest)
	if p.Detached {
		finishDirty = fmt.Sprintf("To finish by hand: first run `git status` in the root and make sure whatever it lists is also in the copy at %s before you clean it, then `git switch %s` in the root.", dest, d)
	}
	switch s {
	case stepCreate:
		if r.o.Created {
			return fmt.Sprintf("The root is untouched and all the work is still in it; the directory %s was kept (nothing was rolled back).", dest)
		}
		return "The root is untouched and all the work is still in it; no worktree was made."
	case stepCarry:
		return fmt.Sprintf("The root is untouched and all the work is still in it; the half-built worktree at %s was kept (nothing was rolled back).", dest)
	case stepRecord:
		return fmt.Sprintf("The root is untouched and the work is in it and in the verified copy at %s, which was kept; without a record the root was not released.", dest)
	case stepBranch:
		return fmt.Sprintf("The root is untouched, still detached at %s with its work. The verified copy at %s was kept, detached. Nothing was released, so the commits are held only by the root's HEAD%s. Give them a branch before anything else: `git switch -c <name>` in %s, then check `git status` in the root and `git switch %s` there.",
			r.shortHead(), dest, r.recordNote(), dest, d)
	case stepBeforeReset:
		where := "the work is in it and in the verified copy at " + dest
		if r.o.Record != nil {
			where += ", recorded at " + r.o.Record.Ref
		} else if !p.rp.HasDirt() && p.Detached {
			where = "the new worktree at " + dest + " is a clean checkout of " + r.shortHead()
			if p.NewBranch != "" {
				where += ", on the new branch " + p.NewBranch
			}
		} else if !p.rp.HasDirt() {
			where = "the new worktree at " + dest + " is a clean checkout of " + b
		}
		if p.Detached && !p.rp.HasDirt() && dest == "" {
			where = "the root is unchanged"
		}
		return fmt.Sprintf("In the root nothing was reset or removed, and %s.", where)
	case stepAfterReset:
		if p.Detached {
			b = r.rootWas()
		}
		var root string
		switch rf.Stage {
		case removal.StageReset:
			root = fmt.Sprintf("The root is still on %s; the reset did not finish", b)
		case removal.StageRemoveUntracked:
			root = fmt.Sprintf("The root was reset on %s and %d of its %d carried untracked files were removed; the rest are still there (git status there lists them)", b, rf.Result.Removed, len(p.rp.UntrackedPaths()))
		case removal.StageVerifyClean:
			root = fmt.Sprintf("The root was reset on %s but is not clean (git status there lists what is left)", b)
		case removal.StageSwitch:
			root = fmt.Sprintf("The root was reset and clean on %s; it was not switched to %s", b, d)
		default:
			root = "The root's state is not known"
		}
		if rf.Uncertain {
			root += "; the root may be partly changed: run git status there"
		}
		how := finishDirty
		if rf.Stage == removal.StageSwitch && !rf.Uncertain {
			how = finish
		}
		if p.Detached && dest == "" {
			return fmt.Sprintf("%s; nothing was uncommitted and no worktree was made, and the commits are held by %s. %s", root, r.holders(), how)
		}
		return fmt.Sprintf("%s. The new worktree at %s is the verified copy, %s; %s. %s", root, dest, r.destIs(true), r.work(), how)
	case stepDestSwitch:
		return fmt.Sprintf("The root is on %s, clean. The new worktree at %s is detached at the tip of %s with all the work (%s): `git switch %s` there finishes it.", d, dest, b, r.work(), b)
	case stepBranchMoved:
		return fmt.Sprintf("The root is on %s, clean. The new worktree at %s was left detached at %s with all the work (%s), so nothing was lost; do not switch it onto %s blindly, which would lay the work over the moved branch: decide where the work belongs first.", d, dest, p.Head, r.work(), b)
	case stepReadBack:
		if p.Detached && dest == "" {
			return fmt.Sprintf("Nothing was undone; the commits are held by %s.", r.holders())
		}
		return fmt.Sprintf("Nothing was undone; %s.", r.work())
	}
	return "Nothing was undone."
}

// Execute moves the root's branch into the new worktree a checked plan
// names, carrying its uncommitted work, and puts the root on the default
// branch. The steps, each re-checking the one before:
//
//  0. unless SkipStartRecheck: the root still equals Preflight's fingerprint
//     (else refused source_changed); a re-check that cannot read is refused
//     unreadable; nothing is created before this;
//  1. worktree.CreateDetachedAt: the destination, detached at the branch tip;
//  2. with uncommitted work, rescue's ExecuteInto carries and verifies it,
//  3. and records it; the record is required (record_not_written otherwise);
//  4. removal.ReleaseRoot with the verified copy, the record and the
//     fingerprint as its proofs: the one step that resets the root and
//     switches it to the default;
//  5. `git switch <branch>` in the destination, hooks off;
//  6. the fast-forward of the default, only when strictly behind origin;
//  7. a read-back of both checkouts; moved is claimed only after it.
//
// A failure stops at its step with nothing rolled back; the reason ends with
// the recovery table's sentence for that step (where the root, the new
// worktree and the work are). Execute itself never resets, removes or
// rewrites anything: the root changes only inside ReleaseRoot.
func (e Engine) Execute(p *Plan) Outcome {
	r := &run{o: Outcome{Plan: *p}, p: p}
	r.o.Plan.Warnings = append([]string(nil), p.Warnings...)
	rp := p.rp
	if rp == nil {
		return r.refuse(rescue.CodeUnreadable, "the plan was not made by Preflight; nothing was created")
	}
	rootGit := e.gitAt(p.Repo)

	// 0. The root must still be what was checked and shown.
	if !p.skipStart {
		// A re-check that cannot read is refused: nothing exists yet.
		if d, err := rp.SourceUnchanged(); err != nil {
			return r.refuse(rescue.CodeUnreadable, "could not re-read the root: %s; nothing was created", cleanup.Flatten(err.Error()))
		} else if d != "" {
			return r.refuse(rescue.CodeSourceChanged, "the root changed since it was checked (%s); nothing was created", cleanup.OneLine(d))
		}
	}

	// 0b. A detached root is still detached at the same commit, a branch
	// still holds it (or, with --branch, the name is still free). Reads only.
	if p.Detached {
		if why, err := e.recheckDetached(rootGit, p); err != nil {
			return r.refuse(rescue.CodeUnreadable, "could not re-read the root's HEAD: %s; nothing was created", cleanup.Flatten(err.Error()))
		} else if why != "" {
			return r.refuse(rescue.CodeSourceChanged, "the root changed since it was checked (%s); nothing was created", cleanup.OneLine(why))
		}
	}

	// A clean root on a detached HEAD that a branch holds has nothing to
	// keep: no worktree, no record; only the release switches the root.
	noWorktree := p.Detached && p.NewBranch == "" && !rp.HasDirt()
	var destGit cleanup.Git
	if !noWorktree {
		// 1. The destination, detached at the branch tip (a detached root:
		// at its commit, with no branch to compare).
		var dest string
		var err error
		if p.Detached {
			dest, err = worktree.CreateDetachedAtCommit(p.Repo, p.DirName, p.Head)
		} else {
			dest, err = worktree.CreateDetachedAt(p.Repo, p.DirName, p.Branch, p.Head)
		}
		if err != nil {
			var notMade *worktree.NotCreatedError
			var addFailed *worktree.AddFailedError
			switch {
			case errors.As(err, &notMade) && notMade.NameUnavailable:
				return r.refuse(rescue.CodeNameUnavailable, "the new worktree's directory was taken since the check (%v); pass --to NAME to choose another; nothing was created", err)
			case errors.As(err, &addFailed) && addFailed.DirLeft:
				r.o.Created = true
			}
			return r.fail(stepCreate, nil, rescue.CodeCreateFailed, "could not create the new worktree: %v", err)
		}
		r.o.Created = true
		p.DestPath, r.o.Plan.DestPath = dest, dest
		r.done("created the worktree " + dest + " detached at " + p.Head)
		destGit = e.gitAt(dest)
	}

	// 2-3. Carry, verify and record the uncommitted work.
	if rp.HasDirt() {
		re := e.Rescue
		re.ForRoot = true
		ro := e.Hooks.into(re, rp, p.DestPath)
		r.o.Completed = append(r.o.Completed, ro.Completed...)
		if n := len(rp.Warnings); len(ro.Plan.Warnings) > n {
			r.o.Plan.Warnings = append(r.o.Plan.Warnings, ro.Plan.Warnings[n:]...)
		}
		switch {
		case ro.Status != rescue.StatusMoved:
			code := ro.Code
			if code == "" {
				code = rescue.CodeCarryFailed
			}
			return r.fail(stepCarry, nil, code, "%s", cleanup.Flatten(ro.Reason))
		case ro.Record == nil:
			why := "the record could not be written"
			for _, w := range ro.Plan.Warnings {
				if s, ok := strings.CutPrefix(w, rescue.RecordWarningPrefix); ok {
					why += ": " + s
				}
			}
			return r.fail(stepRecord, nil, CodeRecordNotWritten, "%s", why)
		}
		r.o.Record = ro.Record
	}

	// 3b. A detached root whose commits no branch holds: the branch the user
	// named is created at the root's commit in the new worktree, never in the
	// root, and before the release, which re-proves that a branch holds the
	// commits. A failure leaves the root untouched.
	if p.NewBranch != "" {
		if code, msg := e.createBranch(destGit, p); code != "" {
			return r.fail(stepBranch, nil, code, "%s", msg)
		}
		r.done(branchDonePrefix + p.NewBranch + " at " + p.Head + " in the new worktree")
	}

	// 4. The release: the one step that changes the root, behind removal's
	// own proofs.
	e.Hooks.before()
	rel := removal.RootRelease{
		Root:               p.Repo,
		Default:            p.Default,
		NothingUncommitted: !rp.HasDirt(),
		Unchanged:          rp.SourceUnchanged,
	}
	if p.Detached {
		rel.DetachedAt = p.Head
	}
	if rp.HasDirt() {
		rel.Verified, rel.RecordRef, rel.Untracked = true, r.o.Record.Ref, rp.UntrackedPaths()
	}
	res, err := e.Hooks.release(rootGit, rel)
	if err != nil {
		var refused *removal.ReleaseRefused
		var failed *removal.ReleaseFailed
		switch {
		case errors.As(err, &refused):
			return r.fail(stepBeforeReset, nil, CodeReleaseRefused, "%s", cleanup.Flatten(err.Error()))
		case errors.As(err, &failed):
			return r.fail(stepAfterReset, failed, CodeReleaseFailed, "%s", cleanup.Flatten(err.Error()))
		}
		return r.fail(stepAfterReset, &removal.ReleaseFailed{Uncertain: true}, CodeReleaseFailed, "releasing the root failed: %s", cleanup.Flatten(err.Error()))
	}
	r.done(fmt.Sprintf("released the root: reset, %d carried untracked files removed, switched to %s", res.Removed, p.Default))
	e.Hooks.after()

	// 5. The destination onto the branch, which the root no longer holds.
	// The branch ref never moves; if it did, the switch would carry the
	// work onto another commit, so it is checked first. A detached root has
	// no branch to move: its destination is final (detached, or on the branch
	// step 3b made).
	if !p.Detached {
		if tip, err := revCommit(destGit, "refs/heads/"+p.Branch); err != nil {
			return r.fail(stepDestSwitch, nil, CodeSwitchFailed, "could not read the branch %s: %v", p.Branch, err)
		} else if tip != p.Head {
			return r.fail(stepBranchMoved, nil, CodeSwitchFailed, "the branch %s moved to %s since the move started, so the new worktree was not switched onto it", p.Branch, tip)
		}
		if o, err := e.Hooks.step(destGit, slices.Concat(cleanup.WriteConfig(), []string{"switch", "--no-overwrite-ignore", p.Branch})...); err != nil {
			return r.fail(stepDestSwitch, nil, CodeSwitchFailed, "git switch %s in the new worktree: %v", p.Branch, err)
		} else if o.Code != 0 {
			return r.fail(stepDestSwitch, nil, CodeSwitchFailed, "git switch %s in the new worktree: exit %d: %s", p.Branch, o.Code, cleanup.OneLine(o.Stderr))
		}
		r.done("switched the new worktree onto " + p.Branch)
	}

	// 6. The fast-forward of the default (warnings only).
	e.fastForward(rootGit, r)

	// 7. Read back both checkouts; only then is it moved.
	if bad, err := e.readBack(rootGit, destGit, p, noWorktree); err != nil {
		return r.fail(stepReadBack, nil, rescue.CodeVerifyFailed, "the final read-back failed: %v", err)
	} else if bad != "" {
		return r.fail(stepReadBack, nil, rescue.CodeVerifyFailed, "the final read-back does not match: %s", bad)
	}
	switch {
	case noWorktree:
		r.done("read back: the root is on " + p.Default + " and clean; its former detached HEAD " + r.shortHead() + " stays on " + r.holders())
	case p.Detached && p.NewBranch != "":
		r.done("read back: the root is on " + p.Default + " and clean; the new worktree is on " + p.NewBranch + " with the work")
	case p.Detached:
		r.done("read back: the root is on " + p.Default + " and clean; the new worktree is detached at " + r.shortHead() + " with the work")
	default:
		r.done("read back: the root is on " + p.Default + " and clean; the new worktree is on " + p.Branch + " with the work")
	}
	r.o.Status = rescue.StatusMoved
	return r.o
}

// recheckDetached re-reads, at the start of Execute, what Preflight decided
// about a detached root: HEAD is still detached at the plan's commit, a
// branch still holds it (when no branch is to be created), and the name to
// create is still free. "" when all hold, else what changed.
func (e Engine) recheckDetached(rootGit cleanup.Git, p *Plan) (string, error) {
	ref, err := symbolicHead(rootGit)
	if err != nil {
		return "", err
	}
	if ref != "" {
		return "the root is on " + describeRef(ref) + " now, not a detached HEAD", nil
	}
	head, err := revCommit(rootGit, "HEAD")
	if err != nil {
		return "", err
	}
	if head != p.Head {
		return "the root's HEAD moved from " + p.Head + " to " + head, nil
	}
	if p.NewBranch != "" {
		if tip, ok, err := resolveCommit(rootGit, "refs/heads/"+p.NewBranch); err != nil {
			return "", err
		} else if ok {
			return "the branch " + p.NewBranch + " appeared (at " + tip + ")", nil
		}
		return "", nil
	}
	held, err := cleanup.BranchesHolding(rootGit, head)
	if err != nil {
		return "", err
	}
	if len(held) == 0 {
		return "no branch holds the root's HEAD any more", nil
	}
	return "", nil
}

// createBranch is step 3b: `git switch --no-track -c NAME` in the new
// worktree, which is detached at the plan's commit. It creates only: it
// refuses an existing name, and the name and the worktree's HEAD are read
// first. It returns a failure code and the reason ("" code for success); the
// reason says what happened, not the recovery (run.fail appends that).
func (e Engine) createBranch(destGit cleanup.Git, p *Plan) (code, msg string) {
	name := p.NewBranch
	if tip, ok, err := resolveCommit(destGit, "refs/heads/"+name); err != nil {
		return CodeBranchCreateFailed, fmt.Sprintf("could not read the branch %s: %v", name, err)
	} else if ok {
		return CodeBranchCreateFailed, fmt.Sprintf("the branch %s appeared (at %s) since the check; it was not touched; pick another name", name, tip)
	}
	if head, err := revCommit(destGit, "HEAD"); err != nil {
		return CodeBranchCreateFailed, fmt.Sprintf("could not read the new worktree's HEAD: %v", err)
	} else if head != p.Head {
		return CodeBranchCreateFailed, fmt.Sprintf("the new worktree is at %s, not %s", head, p.Head)
	}
	args := slices.Concat(cleanup.WriteConfig(), []string{"switch", "--no-track", "-c", name})
	o, err := e.Hooks.step(destGit, args...)
	if err == nil && o.Code == 0 {
		if tip, rerr := revCommit(destGit, "refs/heads/"+name); rerr != nil {
			return CodeBranchCreateFailed, fmt.Sprintf("the branch %s was not read back after it was created: %v", name, rerr)
		} else if tip != p.Head {
			return CodeBranchCreateFailed, fmt.Sprintf("the branch %s is at %s, not %s", name, tip, p.Head)
		}
		return "", ""
	}
	why := ""
	if err != nil {
		why = err.Error()
	} else {
		why = fmt.Sprintf("exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}
	// Say whether a racing creator got there first: the existing branch is
	// not ours and was not touched.
	if tip, ok, rerr := resolveCommit(destGit, "refs/heads/"+name); rerr == nil && ok {
		return CodeBranchCreateFailed, fmt.Sprintf("the branch %s appeared (at %s) since the check; it was not touched; pick another name", name, tip)
	}
	return CodeBranchCreateFailed, fmt.Sprintf("git switch -c %s in the new worktree: %s", name, why)
}

// readBack is step 7: the root is on refs/heads/<Default> with an empty
// status; the destination is on refs/heads/<Branch> at Head, the branch ref
// is still Head, and its status and files equal the verified state
// (rescue.Plan.DestinationMatches, the comparison the copy was verified
// with). It returns every mismatch found, with both checkouts' states named
// ("" when all hold); a read failure is an error.
func (e Engine) readBack(rootGit, destGit cleanup.Git, p *Plan, rootOnly bool) (string, error) {
	var bad []string
	rootRef, err := symbolicHead(rootGit)
	if err != nil {
		return "", fmt.Errorf("the root's branch: %w", err)
	}
	if rootRef != "refs/heads/"+p.Default {
		bad = append(bad, fmt.Sprintf("the root is on %s, not %s", describeRef(rootRef), p.Default))
	}
	st, err := rootGit.Text(cleanup.StatusArgs()...)
	if err != nil {
		return "", fmt.Errorf("the root's status: %w", err)
	}
	if st != "" {
		bad = append(bad, "the root is not clean: "+cleanup.OneLine(strings.ReplaceAll(st, "\x00", " ")))
	}
	if rootOnly {
		// A clean root on a detached HEAD a branch holds: no worktree was made.
		return strings.Join(bad, "; "), nil
	}
	destRef, err := symbolicHead(destGit)
	if err != nil {
		return "", fmt.Errorf("the new worktree's branch: %w", err)
	}
	if p.Detached && p.NewBranch == "" {
		// Detached at Head, no ref: only the commit and the work are read.
		if destRef != "" {
			bad = append(bad, fmt.Sprintf("the new worktree %s is on %s, not a detached HEAD", p.DestPath, describeRef(destRef)))
		}
		if head, err := revCommit(destGit, "HEAD"); err != nil {
			return "", fmt.Errorf("the new worktree's HEAD: %w", err)
		} else if head != p.Head {
			bad = append(bad, fmt.Sprintf("the new worktree %s is at %s, not %s", p.DestPath, head, p.Head))
		}
		if d, err := p.rp.DestinationMatches(p.DestPath); err != nil {
			return "", fmt.Errorf("the new worktree's status: %w", err)
		} else if d != "" {
			bad = append(bad, fmt.Sprintf("the new worktree %s does not hold the verified state: %s", p.DestPath, cleanup.OneLine(d)))
		}
		return strings.Join(bad, "; "), nil
	}
	if destRef != "refs/heads/"+p.Branch {
		bad = append(bad, fmt.Sprintf("the new worktree %s is on %s, not %s", p.DestPath, describeRef(destRef), p.Branch))
	}
	if head, err := revCommit(destGit, "HEAD"); err != nil {
		return "", fmt.Errorf("the new worktree's HEAD: %w", err)
	} else if head != p.Head {
		bad = append(bad, fmt.Sprintf("the new worktree %s is at %s, not the branch tip %s", p.DestPath, head, p.Head))
	}
	if tip, err := revCommit(destGit, "refs/heads/"+p.Branch); err != nil {
		return "", fmt.Errorf("the branch %s: %w", p.Branch, err)
	} else if tip != p.Head {
		bad = append(bad, fmt.Sprintf("the branch %s is at %s, not %s", p.Branch, tip, p.Head))
	}
	if d, err := p.rp.DestinationMatches(p.DestPath); err != nil {
		return "", fmt.Errorf("the new worktree's status: %w", err)
	} else if d != "" {
		bad = append(bad, fmt.Sprintf("the new worktree %s does not hold the verified state: %s", p.DestPath, cleanup.OneLine(d)))
	}
	return strings.Join(bad, "; "), nil
}

// symbolicHead is g's checked-out branch ref, "" on a detached HEAD.
func symbolicHead(g cleanup.Git) (string, error) {
	o, err := g.Run("symbolic-ref", "-q", "HEAD")
	if err != nil {
		return "", err
	}
	switch o.Code {
	case 0:
		return strings.TrimSpace(o.Stdout), nil
	case 1:
		return "", nil
	}
	return "", fmt.Errorf("git symbolic-ref: exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
}

func describeRef(ref string) string {
	if ref == "" {
		return "a detached HEAD"
	}
	return cleanup.OneLine(ref)
}

// revCommit is the commit ref names in g.
func revCommit(g cleanup.Git, ref string) (string, error) {
	o, err := g.Run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if o.Code != 0 {
		return "", fmt.Errorf("%s does not resolve to a commit (exit %d)", cleanup.OneLine(ref), o.Code)
	}
	return strings.TrimSpace(o.Stdout), nil
}
