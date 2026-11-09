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

// Test seams. Each is a package variable only so a test can make one step
// fail or watch it; production code never reassigns them.
var (
	// executeInto is steps 2-3: carry, verify and record into the new
	// worktree (recovery rows 2 and 3).
	executeInto = func(e rescue.Engine, p *rescue.Plan, dest string) rescue.Outcome { return e.ExecuteInto(p, dest) }
	// beforeRelease fires after the work was carried and recorded, just
	// before the claim re-check and the release (row 4 before the reset).
	beforeRelease = func() {}
	// releaseRoot is step 4, the one step that resets and switches the root;
	// rootmove only names it (row 4 after the reset).
	releaseRoot = removal.ReleaseRoot
	// stepGit runs the git of steps 5-6 (rows 5 and 6). Not runGit, which
	// is internal/removal's unrelated seam.
	stepGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }
	// afterRelease fires once the release succeeded, before step 5 (row 7).
	afterRelease = func() {}
)

// destSwitchConfig are the -c settings of the destination's switch: hooks
// off (a hook is arbitrary code) and no submodule recursion, as the root's
// own switch in removal.ReleaseRoot.
var destSwitchConfig = []string{"-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false"}

// readStatusArgs read every uncommitted change of a checkout, untracked
// files one by one and submodule changes whatever their config says.
var readStatusArgs = []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none"}

// step names a step of Execute that can fail: the key of the recovery
// table.
type step int

const (
	stepCreate      step = iota + 1 // 1: the detached worktree
	stepCarry                       // 2: carry and verify
	stepRecord                      // 3: the record
	stepBeforeReset                 // 4, before the reset: the claim re-check or a refused release
	stepAfterReset                  // 4, after the reset: a *removal.ReleaseFailed
	stepDestSwitch                  // 5: the destination onto the branch
	stepBranchMoved                 // 5, the branch moved before the switch: not switched
	stepReadBack                    // 7: the final read-back
)

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

// work says where the root's uncommitted work is once the copy exists.
func (r *run) work() string {
	if r.o.Record == nil {
		return "there was nothing uncommitted; the branch's commits are on " + r.p.Branch + ", which never moved"
	}
	return fmt.Sprintf("the work is in the copy at %s and in the record %s", r.p.DestPath, r.o.Record.Ref)
}

// recovery is the spec's recovery table, keyed by the step that failed:
// where the root, the new worktree and the work are, and how to finish.
func (r *run) recovery(s step, rf *removal.ReleaseFailed) string {
	p := r.p
	b, d, dest := p.Branch, p.Default, p.DestPath
	finish := fmt.Sprintf("To finish by hand: `git switch %s` in the root, then `git switch %s` in %s (running `wt restore-root` again is refused, the directory exists).", d, b, dest)
	// A root that may hold leftovers (the reset may not have run, carried
	// files may remain) must be looked at before it is switched, or the
	// switch carries them onto the default.
	finishDirty := fmt.Sprintf("To finish by hand: first run `git status` in the root and make sure whatever it lists is also in the copy at %s before you clean it, then `git switch %s` in the root, then `git switch %s` in %s (running `wt restore-root` again is refused, the directory exists).", dest, d, b, dest)
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
	case stepBeforeReset:
		where := "the work is in it and in the verified copy at " + dest
		if r.o.Record != nil {
			where += ", recorded at " + r.o.Record.Ref
		} else if !p.rp.HasDirt() {
			where = "the new worktree at " + dest + " is a clean checkout of " + b
		}
		return fmt.Sprintf("In the root nothing was reset or removed, and %s.", where)
	case stepAfterReset:
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
		return fmt.Sprintf("%s. The new worktree at %s is the verified copy, detached at the tip of %s; %s. %s", root, dest, b, r.work(), how)
	case stepDestSwitch:
		return fmt.Sprintf("The root is on %s, clean. The new worktree at %s is detached at the tip of %s with all the work (%s): `git switch %s` there finishes it.", d, dest, b, r.work(), b)
	case stepBranchMoved:
		return fmt.Sprintf("The root is on %s, clean. The new worktree at %s was left detached at %s with all the work (%s), so nothing was lost; do not switch it onto %s blindly, which would lay the work over the moved branch: decide where the work belongs first.", d, dest, p.Head, r.work(), b)
	case stepReadBack:
		return fmt.Sprintf("Nothing was undone; %s.", r.work())
	}
	return "Nothing was undone."
}

// Execute moves the root's branch into the new worktree a checked plan
// names, carrying its uncommitted work, and puts the root on the default
// branch. The steps, each re-checking the one before:
//
//  0. unless SkipStartRecheck: the root still equals Preflight's fingerprint
//     (else refused source_changed) and no other live wt session holds it
//     (else refused root_held); nothing is created before this;
//  1. worktree.CreateDetachedAt: the destination, detached at the branch tip;
//  2. with uncommitted work, rescue's ExecuteInto carries and verifies it,
//  3. and records it; the record is required (record_not_written otherwise);
//  4. the claim re-check, always, then removal.ReleaseRoot with the
//     verified copy, the record and the fingerprint as its proofs: the one
//     step that resets the root and switches it to the default;
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
		if d, err := rp.SourceUnchanged(); err != nil {
			r.o.Status, r.o.Code = rescue.StatusFailed, rescue.CodeUnreadable
			r.o.Reason = fmt.Sprintf("could not re-read the root: %v; nothing was created", err)
			return r.o
		} else if d != "" {
			return r.refuse(rescue.CodeSourceChanged, "the root changed since it was checked (%s); nothing was created", cleanup.OneLine(d))
		}
		if held, err := e.heldBy(rootGit, p.Repo); err != nil {
			r.o.Status, r.o.Code = rescue.StatusFailed, rescue.CodeUnreadable
			r.o.Reason = fmt.Sprintf("%v; nothing was created", err)
			return r.o
		} else if held != "" {
			return r.refuse(CodeRootHeld, "another wt session started in the root since it was checked: %s; nothing was created", held)
		}
	}

	// 1. The destination, detached at the branch tip.
	dest, err := worktree.CreateDetachedAt(p.Repo, p.DirName, p.Branch, p.Head)
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
	destGit := e.gitAt(dest)

	// 2-3. Carry, verify and record the uncommitted work.
	if rp.HasDirt() {
		re := e.Rescue
		re.ForRoot = true
		ro := executeInto(re, rp, dest)
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

	// 4. The claim once more, then the release.
	beforeRelease()
	if held, err := e.heldBy(rootGit, p.Repo); err != nil {
		return r.fail(stepBeforeReset, nil, CodeReleaseRefused, "%v, so the root was not released", err)
	} else if held != "" {
		return r.fail(stepBeforeReset, nil, CodeRootHeld, "another wt session started in the root: %s, so the root was not released", held)
	}
	rel := removal.RootRelease{
		Root:               p.Repo,
		Default:            p.Default,
		NothingUncommitted: !rp.HasDirt(),
		Unchanged:          rp.SourceUnchanged,
	}
	if rp.HasDirt() {
		rel.Verified, rel.RecordRef, rel.Untracked = true, r.o.Record.Ref, rp.UntrackedPaths()
	}
	res, err := releaseRoot(rootGit, rel)
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
	afterRelease()

	// 5. The destination onto the branch, which the root no longer holds.
	// The branch ref never moves; if it did, the switch would carry the
	// work onto another commit, so it is checked first.
	if tip, err := revCommit(destGit, "refs/heads/"+p.Branch); err != nil {
		return r.fail(stepDestSwitch, nil, CodeSwitchFailed, "could not read the branch %s: %v", p.Branch, err)
	} else if tip != p.Head {
		return r.fail(stepBranchMoved, nil, CodeSwitchFailed, "the branch %s moved to %s since the move started, so the new worktree was not switched onto it", p.Branch, tip)
	}
	if o, err := stepGit(destGit, slices.Concat(destSwitchConfig, []string{"switch", "--no-overwrite-ignore", p.Branch})...); err != nil {
		return r.fail(stepDestSwitch, nil, CodeSwitchFailed, "git switch %s in the new worktree: %v", p.Branch, err)
	} else if o.Code != 0 {
		return r.fail(stepDestSwitch, nil, CodeSwitchFailed, "git switch %s in the new worktree: exit %d: %s", p.Branch, o.Code, cleanup.OneLine(o.Stderr))
	}
	r.done("switched the new worktree onto " + p.Branch)

	// 6. The fast-forward of the default (warnings only).
	e.fastForward(rootGit, r)

	// 7. Read back both checkouts; only then is it moved.
	if bad, err := e.readBack(rootGit, destGit, p); err != nil {
		return r.fail(stepReadBack, nil, rescue.CodeVerifyFailed, "the final read-back failed: %v", err)
	} else if bad != "" {
		return r.fail(stepReadBack, nil, rescue.CodeVerifyFailed, "the final read-back does not match: %s", bad)
	}
	r.done("read back: the root is on " + p.Default + " and clean; the new worktree is on " + p.Branch + " with the work")
	r.o.Status = rescue.StatusMoved
	return r.o
}

// readBack is step 7: the root is on refs/heads/<Default> with an empty
// status; the destination is on refs/heads/<Branch> at Head, the branch ref
// is still Head, and its status and files equal the verified state
// (rescue.Plan.DestinationMatches, the comparison the copy was verified
// with). It returns every mismatch found, with both checkouts' states named
// ("" when all hold); a read failure is an error.
func (e Engine) readBack(rootGit, destGit cleanup.Git, p *Plan) (string, error) {
	var bad []string
	rootRef, err := symbolicHead(rootGit)
	if err != nil {
		return "", fmt.Errorf("the root's branch: %w", err)
	}
	if rootRef != "refs/heads/"+p.Default {
		bad = append(bad, fmt.Sprintf("the root is on %s, not %s", describeRef(rootRef), p.Default))
	}
	st, err := gitText(rootGit, readStatusArgs...)
	if err != nil {
		return "", fmt.Errorf("the root's status: %w", err)
	}
	if st != "" {
		bad = append(bad, "the root is not clean: "+cleanup.OneLine(strings.ReplaceAll(st, "\x00", " ")))
	}
	destRef, err := symbolicHead(destGit)
	if err != nil {
		return "", fmt.Errorf("the new worktree's branch: %w", err)
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
