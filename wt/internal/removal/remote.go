package removal

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// This file is the origin-branch stage: the only code in wt that writes to a
// remote. It deletes one branch on origin, by an explicit-sha lease push, only
// for a row proven by this run's fetch and by live checks that all name the
// same tip (the listed row, the fresh tracking ref, a fresh ls-remote, and the
// merged PR's head). It reuses the frozen outcome strings: an acted-on remote
// item has worktree not_requested and a branch outcome; a gate stop is
// refused / skipped_changed with the branch not_attempted.

// remoteTimeout bounds each network call of this stage (ls-remote, push).
const remoteTimeout = 120 * time.Second

// runRemoteGit runs the stage's one write, the push. It is a seam: tests
// record the argument vector, make it fail, or time it out.
var runRemoteGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }

// beforePush runs between the stage's last read and its push. It is a test
// seam (a test moves origin's branch there); it does nothing in wt.
var beforePush = func() {}

// remoteGit is the Git every network call of this stage uses: the engine's
// repository, never prompting, with a bounded time.
func (e Engine) remoteGit() cleanup.Git {
	g := e.G
	g.Env = append(append([]string(nil), g.Env...), cleanup.NonInteractive(g.Dir)...)
	g.Timeout = remoteTimeout
	return g
}

// remoteName is the branch name of an origin tracking ref, or "".
func remoteName(ref string) string { return strings.TrimPrefix(ref, "refs/remotes/origin/") }

// safeRefName reports whether name can sit in a refspec and a lease without
// being read as an option, a pattern or a second refspec.
func safeRefName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") ||
		strings.Contains(name, "//") || strings.Contains(name, "@{") || name == "@" {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return false
		}
	}
	return true
}

// invalidRemoteItem says why a remote item cannot be acted on before anything
// is read, or "": the ref must be an origin tracking ref with a safe name, the
// item has no path, only a proven row is removable, and SawTip is a full id.
func invalidRemoteItem(it Item) string {
	switch {
	case !strings.HasPrefix(it.Ref, "refs/remotes/origin/"):
		return fmt.Sprintf("ref %q is not an origin branch (only origin branches can be removed)", it.Ref)
	case !safeRefName(remoteName(it.Ref)):
		return fmt.Sprintf("branch name %q is not a safe ref name", remoteName(it.Ref))
	case it.Path != "":
		return "a remote item has no path"
	case it.Saw != cleanup.StatusProven:
		return fmt.Sprintf("only a proven origin branch can be removed (status seen %q)", it.Saw)
	case !cleanup.IsFullCommitID(it.SawTip):
		return fmt.Sprintf("commit seen %q is not a full commit id", it.SawTip)
	}
	return ""
}

// remoteGate runs the checks for one origin item in their fixed order:
//
//  1. the item itself (invalidRemoteItem);
//  2. the default branch, or no default resolved: retained_default;
//  3. a live re-classification (cleanup.Rescan asks gh again for this branch);
//  4. exact identity of the ref's spelling;
//  5. hard blockers (an open PR on it, a PR targeting it...);
//  6. an unverifiable row (gh, the fetch, the merge check) is refused;
//  7. a worse status or a moved tip is skipped_changed;
//  8. the PR proof, re-checked here rather than trusted from the classifier:
//     a MERGED PR into the default branch whose head is exactly this tip and
//     whose merge commit git says is an ancestor of the base right now;
//  9. the Remote approval.
func (e Engine) remoteGate(it Item) gated {
	if why := invalidRemoteItem(it); why != "" {
		return stopWith(refuse(it, "invalid target: "+why), cleanup.Row{}, false)
	}
	name := remoteName(it.Ref)
	if e.Prior.NoDefault || e.Prior.Base.Default == "" || strings.EqualFold(name, e.Prior.Base.Default) || name == "HEAD" {
		return stopWith(NewOutcome(it, WtNotRequested, BrRetainedDefault, "origin/"+name+" is the default branch, or the default branch is unresolved"), cleanup.Row{}, false)
	}

	fresh, err := rescan(e.G, e.Prior, e.In, e.classifyOpts(), cleanup.Target{Kind: cleanup.KindRemote, Ref: it.Ref})
	has := err == nil
	refuseR := func(reason string) gated { return stopWith(refuse(it, reason), fresh, has) }
	skipR := func(reason string) gated { return stopWith(skipped(it, reason), fresh, has) }
	if errors.Is(err, cleanup.ErrTargetGone) {
		return skipR("no longer exists")
	}
	if err != nil {
		return refuseR("could not re-check: " + err.Error())
	}
	if fresh.Ref != it.Ref {
		return refuseR(fmt.Sprintf("branch spelling changed: %s is now %s", it.Ref, fresh.Ref))
	}
	if fresh.HardStop != "" {
		return refuseR(blockerReason(fresh.Blockers, fresh.Kind))
	}
	if fresh.Unknown {
		detail := ""
		for _, r := range fresh.Reasons {
			if r.Detail != "" {
				detail = r.Detail
				break
			}
		}
		return refuseR("could not verify it is safe to delete: " + detail)
	}
	if fresh.Status.WorseThan(it.Saw) {
		return skipR(fmt.Sprintf("status changed since it was listed (%s, was %s)", fresh.Status, it.Saw))
	}
	if fresh.Tip != it.SawTip {
		return skipR("branch moved since it was listed")
	}
	if pr := fresh.PR; pr == nil || pr.State != "MERGED" || pr.BaseRef != e.Prior.Base.Default || pr.HeadRefOid != fresh.Tip {
		return refuseR("no merged PR into " + e.Prior.Base.Default + " with this exact head")
	}
	switch e.mergeInBase(fresh.PR.MergeCommit) {
	case cleanup.TriNo:
		return refuseR("the merged PR's merge commit is not in " + e.Prior.Base.Ref)
	case cleanup.TriUnknown:
		return refuseR("could not check that the merged PR's merge commit is in " + e.Prior.Base.Ref)
	}
	g := gated{fresh: fresh, hasFresh: true, need: needsOf(it.Kind, fresh)}
	if !it.Approved.Remote {
		return refuseR("needs --remote: deleting an origin branch is visible to everyone with access to the repository")
	}
	return g
}

// mergeInBase asks git whether the merge commit is an ancestor of the base
// ref as it is now (never the listing's answer).
func (e Engine) mergeInBase(merge string) cleanup.Tri {
	if !cleanup.IsFullCommitID(merge) {
		return cleanup.TriUnknown
	}
	base := "refs/remotes/" + e.Prior.Base.Ref
	if e.Prior.Base.Fallback {
		base = "refs/heads/" + e.Prior.Base.Ref
	}
	out, err := probeGit(e.G, "merge-base", "--is-ancestor", merge, base)
	switch {
	case err != nil:
		return cleanup.TriUnknown
	case out.Code == 0:
		return cleanup.TriYes
	case out.Code == 1:
		return cleanup.TriNo
	}
	return cleanup.TriUnknown
}

// lsRemote reads origin's ref for name, at origin's push URL, with `git ls-remote --exit-code`: the
// sha and found=true, found=false when origin has no such branch, or an error
// when it cannot be read. Only a line naming exactly refs/heads/<name> counts
// (a pattern also matches refs ending in it), and exactly one must.
func (e Engine) lsRemote(name string) (sha string, found bool, err error) {
	ref := "refs/heads/" + name
	// Read through the URL the push will use: remote.origin.pushurl (and
	// pushInsteadOf) can send writes somewhere the fetch URL does not reach,
	// and a read of the wrong repository would misjudge the push.
	dest, derr := probeGit(e.remoteGit(), "remote", "get-url", "--push", "origin")
	if derr != nil {
		return "", false, derr
	}
	url := strings.TrimSpace(dest.Stdout)
	if dest.Code != 0 || url == "" {
		return "", false, fmt.Errorf("git remote get-url --push origin exited %d: %s", dest.Code, cleanup.OneLine(dest.Stderr))
	}
	out, perr := probeGit(e.remoteGit(), "ls-remote", "--exit-code", url, ref)
	if perr != nil {
		return "", false, perr
	}
	switch out.Code {
	case 2:
		return "", false, nil
	case 0:
	default:
		return "", false, fmt.Errorf("git ls-remote exited %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}
	var shas []string
	for _, line := range strings.Split(strings.TrimSpace(out.Stdout), "\n") {
		s, r, ok := strings.Cut(line, "\t")
		if ok && r == ref {
			shas = append(shas, s)
		}
	}
	if len(shas) != 1 || !cleanup.IsFullCommitID(shas[0]) {
		return "", false, fmt.Errorf("git ls-remote gave %d matching lines for %s", len(shas), ref)
	}
	return shas[0], true, nil
}

// remoteAct deletes the origin branch a remoteGate let through, or reports
// what it would (dry). Order: a live preflight read (precise reason, and no
// push that cannot succeed), the push with an explicit-sha lease (the real
// compare-and-swap: the server compares the sha), then a fresh read to judge
// the result. The push's exit status is never the answer.
func (e Engine) remoteAct(it Item, g gated, dry bool) (Outcome, []string) {
	name := remoteName(it.Ref)
	sha := g.fresh.Tip
	o := NewOutcome(it, WtNotRequested, BrNotAttempted, "")
	o.LossLines = lossLines(g.fresh)
	restore := fmt.Sprintf("git push origin %s:refs/heads/%s", sha, name)
	set := func(br BranchOutcome, reason string) (Outcome, []string) {
		o.Branch = br
		o.SetReason(reason)
		return o, nil
	}

	cur, found, err := e.lsRemote(name)
	switch {
	case err != nil:
		return set(BrRetainedUnknown, "could not read origin/"+name+" to confirm its tip: "+err.Error())
	case !found:
		return set(BrRetainedMoved, "origin/"+name+" is already gone from origin; nothing was pushed")
	case cur != sha:
		return set(BrRetainedMoved, fmt.Sprintf("origin/%s moved to %s since it was listed; nothing was pushed", name, cur))
	}
	if dry {
		return set(BrDeleted, fmt.Sprintf("dry run: would delete origin/%s (was %s); restore: %s", name, sha, restore))
	}

	beforePush()
	if g.pending != nil {
		*g.pending = sha // a panic from here keeps the restore id
	}
	out, perr := runRemoteGit(e.remoteGit(), "push", "--porcelain", "--no-verify",
		"--force-with-lease=refs/heads/"+name+":"+sha, "origin", ":refs/heads/"+name)
	o.DeletedTip = sha // the ref may be gone until a read proves it is not
	pushFailed := perr != nil && !errors.Is(perr, context.DeadlineExceeded) || perr == nil && out.Code != 0
	pushText := cleanup.OneLine(strings.TrimSpace(out.Stdout + " " + out.Stderr))
	timedOut := errors.Is(perr, context.DeadlineExceeded)

	now, still, rerr := e.lsRemote(name)
	switch {
	case rerr != nil:
		reason := fmt.Sprintf("origin/%s (was %s): could not read origin after the push; it may be deleted; restore if needed: %s", name, sha, restore)
		o.AddWarning("read after the push failed: " + rerr.Error())
		return set(BrRetainedUnknown, reason)
	case !still && pushFailed:
		// Our push failed, yet the ref is gone: someone else deleted it. Not
		// our deletion, so no DeletedTip and no restore command.
		o.DeletedTip = ""
		o.AddWarning("push failed: " + cmp.Or(pushText, fmt.Sprint(perr)))
		return set(BrRetainedMoved, fmt.Sprintf("origin/%s is already gone from origin (deleted by someone else; wt's push failed, so wt deleted nothing)", name))
	case !still:
		o.Branch = BrDeleted
		o.SetReason(fmt.Sprintf("origin/%s (was %s) deleted; restore: %s", name, sha, restore))
		o.AddWarning("GitHub's 'Restore branch' button on the merged PR also brings it back")
		if timedOut {
			o.AddWarning("the push timed out, but origin no longer has the branch")
		}
		if loc := e.localTracking(name); loc != "" {
			o.AddWarning(fmt.Sprintf("local branch %s still exists; its upstream is now gone", loc))
		}
		o.AddWarning("your clone keeps the commit until git gc prunes it")
		return o, nil
	case now == sha:
		// Nothing was deleted: the ref is still there at the same commit.
		o.DeletedTip = ""
		if pushText != "" {
			o.AddWarning("push said: " + pushText)
		}
		if perr != nil {
			o.AddWarning("push failed: " + perr.Error())
		}
		if strings.Contains(pushText, "stale info") {
			return set(BrRetainedMoved, fmt.Sprintf("origin/%s was not deleted: the lease on %s was refused", name, sha))
		}
		return set(BrRetainedUnknown, fmt.Sprintf("origin/%s was not deleted (origin refused or the push failed)", name))
	default:
		// Present at another commit: it moved (or was re-created) meanwhile.
		return set(BrRetainedMoved, fmt.Sprintf("origin/%s (was %s) is now at %s: it moved during the delete; restore the old tip if needed: %s", name, sha, now, restore))
	}
}

// localTracking is the name of a local branch whose upstream is origin/<name>,
// or "" (none, or it could not be read: the warning is a courtesy).
func (e Engine) localTracking(name string) string {
	out, err := probeGit(e.G, "for-each-ref", "--format=%(refname:short)%09%(upstream)", "refs/heads/")
	if err != nil || out.Code != 0 {
		return ""
	}
	for _, line := range strings.Split(out.Stdout, "\n") {
		b, up, ok := strings.Cut(line, "\t")
		if ok && up == "refs/remotes/origin/"+name {
			return b
		}
	}
	return ""
}
