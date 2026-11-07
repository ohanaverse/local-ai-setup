package removal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// mergedPR is a gh JSON listing with one MERGED PR #8 into main whose head is
// head and whose merge commit is merge.
func mergedPR(head, merge string) []byte {
	return []byte(fmt.Sprintf(`[{"number":8,"state":"MERGED","headRefOid":%q,"baseRefName":"main","mergeCommit":{"oid":%q},"isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`, head, merge))
}

// stubRemoteGh stubs gh as the world of a merged origin branch: the head
// lookup answers headJSON for "topic" (and none elsewhere), the base lookup
// answers baseJSON. It restores both afterwards.
func stubRemoteGh(t *testing.T, headJSON, baseJSON []byte) {
	t.Helper()
	t.Cleanup(cleanup.StubPRListForTest(func(_, b string) ([]byte, error) {
		if b == "topic" {
			return headJSON, nil
		}
		return []byte("[]"), nil
	}))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return baseJSON, nil }))
}

// remoteWorld is a repo whose origin has a merged branch "topic" (at main's
// tip, with a merged PR at that tip), listed after a clean fetch: the row is
// proven.
type remoteWorld struct {
	f   *fixture
	tip string
	s   cleanup.Snapshot
	r   cleanup.Report
	row cleanup.Row
}

func newRemoteWorld(t *testing.T) remoteWorld {
	t.Helper()
	f := newFixture(t)
	f.Branch("topic")
	f.Push("topic")
	tip := f.git("rev-parse", "main")
	stubRemoteGh(t, mergedPR(tip, tip), []byte("[]"))
	in := f.inputs()
	in.Fetch = true
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	r := cleanup.Classify(s, cleanup.Options{Now: later()})
	row := f.row(r, cleanup.KindRemote, "topic")
	if row.Status != cleanup.StatusProven {
		t.Fatalf("fixture row is %s, want proven", row.Status)
	}
	return remoteWorld{f, tip, s, r, row}
}

func (w remoteWorld) engine(o Options) Engine { return w.f.engine(w.s, w.f.inputs(), o) }

// confirmed is the Item the CLI/TUI would hand Run: previewed, then confirmed
// with the preview's own needs.
func (w remoteWorld) confirmed(t *testing.T, e Engine) Item {
	t.Helper()
	p := previewOne(t, e, ItemFromRow(w.row))
	it, err := p.Confirmed(p.Needs)
	if err != nil {
		t.Fatalf("Confirmed: %v (approval %s, reason %q)", err, p.Approval, p.Reason)
	}
	return it
}

// originHas is origin's tip for branch (from ls-remote), or "".
func (w remoteWorld) originHas(branch string) string {
	out := gitIn(w.f.t, w.f.Origin, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
	return out
}

// otherClone clones origin into a fresh directory with an identity.
func (w remoteWorld) otherClone(t *testing.T) string {
	t.Helper()
	c := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(c), "clone", w.f.Origin, c)
	gitIn(t, c, "config", "user.email", "t@example.com")
	gitIn(t, c, "config", "user.name", "T")
	gitIn(t, c, "config", "commit.gpgsign", "false")
	return c
}

// moveOrigin pushes a new commit to origin's topic from another clone and
// returns its id.
func (w remoteWorld) moveOrigin(t *testing.T) string {
	t.Helper()
	c := w.otherClone(t)
	gitIn(t, c, "checkout", "topic")
	gitIn(t, c, "commit", "--allow-empty", "-m", "late work")
	gitIn(t, c, "push", "origin", "topic")
	return gitIn(t, c, "rev-parse", "HEAD")
}

// recordPush wraps the remote runner: it records every argv and runs the real
// command.
func recordPush(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	old := runRemoteGit
	runRemoteGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		calls = append(calls, slices.Clone(args))
		return old(g, args...)
	}
	t.Cleanup(func() { runRemoteGit = old })
	return &calls
}

func stubProbe(t *testing.T, fn func(old func(cleanup.Git, ...string) (cleanup.Out, error), g cleanup.Git, args ...string) (cleanup.Out, error)) {
	t.Helper()
	old := probeGit
	probeGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return fn(old, g, args...) }
	t.Cleanup(func() { probeGit = old })
}

// TestRemotePreviewNeedsTheRemoteApproval previews a proven origin row and
// tries to confirm it with each other approval. What: the preview asks for
// Needs.Remote alone (ApprovalNeedsRemote); Confirm, Force, Delete never
// stand in for it, and Remote covers nothing else. Why: Ruling K says
// approvals are per item and purpose-specific; if --accept-loss or the plain
// typed "delete" could delete origin branches, a mixed batch would publish a
// deletion nobody asked for.
func TestRemotePreviewNeedsTheRemoteApproval(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	p := previewOne(t, e, ItemFromRow(w.row))
	if p.Approval != ApprovalNeedsRemote || p.Needs != (Needs{Remote: true}) {
		t.Fatalf("approval %s needs %+v (reason %q), want needs_remote / {Remote}", p.Approval, p.Needs, p.Reason)
	}
	if p.Plan.Branch != BrDeleted || !strings.Contains(p.Plan.Reason, "dry run") {
		t.Fatalf("plan %+v, want a dry-run delete", p.Plan)
	}
	for name, ap := range map[string]Needs{
		"nothing":        {},
		"confirm":        {Confirm: true},
		"all but remote": {Confirm: true, Force: true, Delete: true},
		"remote+confirm": {Remote: true, Confirm: true},
		"remote+delete":  {Remote: true, Delete: true},
	} {
		if _, err := p.Confirmed(ap); err == nil {
			t.Errorf("%s: Confirmed accepted %+v", name, ap)
		}
		if _, err := p.ConfirmedDeletingBranch(ap); err == nil {
			t.Errorf("%s: ConfirmedDeletingBranch accepted %+v", name, ap)
		}
	}
	if _, err := p.Confirmed(Needs{Remote: true}); err != nil {
		t.Fatalf("Confirmed(Remote): %v", err)
	}
	// A Remote approval on a local branch item is refused too.
	f := newFixture(t)
	noGh(t)
	f.unmergedBranch("loc")
	ls, r := f.report()
	lp := previewOne(t, f.engine(ls, f.inputs(), Options{}), ItemFromRow(f.row(r, cleanup.KindBranch, "loc")))
	if _, err := lp.Confirmed(Needs{Confirm: true, Delete: true, Remote: true}); err == nil {
		t.Error("a local branch item accepted the Remote approval")
	}
}

// TestRemoteDeleteHappyPath deletes a proven origin branch. What: origin's ref
// is gone (verified by a fresh read, not the push's exit code), the tracking
// ref is pruned by the push itself, DeletedTip is the full sha, and the
// reason leads with it and ends with the restore command. Why: this is the
// whole feature, and the restore line is the only undo this tool can offer.
func TestRemoteDeleteHappyPath(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Worktree != WtNotRequested || o.Branch != BrDeleted || o.DeletedTip != w.tip {
		t.Fatalf("outcome %+v", o)
	}
	if got := w.originHas("topic"); got != "" {
		t.Fatalf("origin still has topic at %s", got)
	}
	if _, ok := w.f.refTip("refs/remotes/origin/topic"); ok {
		t.Error("the tracking ref was not pruned by the push")
	}
	if !strings.Contains(o.Reason, "origin/topic (was "+w.tip+")") || !strings.HasSuffix(o.Reason, "git push origin "+w.tip+":refs/heads/topic") {
		t.Errorf("reason %q lacks the sha-first / restore-last shape", o.Reason)
	}
	if !strings.Contains(strings.Join(o.Warnings, "\n"), "Restore branch") {
		t.Errorf("warnings %v do not name GitHub's Restore branch", o.Warnings)
	}
	want := []string{"push", "--porcelain", "--no-verify", "--force-with-lease=refs/heads/topic:" + w.tip, "origin", ":refs/heads/topic"}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], want) {
		t.Fatalf("push argv %v, want exactly %v", *calls, want)
	}
	for _, a := range (*calls)[0] {
		if a == "-f" || a == "--force" || strings.HasPrefix(a, "+") || a == "--force-with-lease" {
			t.Errorf("argument %q is a bare or unleased force", a)
		}
	}
}

// TestRemoteWarnsOfALocalBranchTrackingIt deletes origin/topic while a local
// topic still tracks it. What: the local branch is untouched and a warning
// names it. Why: a remote row is separate from the local branch of that name;
// the local gates must not be bypassed as a side effect.
func TestRemoteWarnsOfALocalBranchTrackingIt(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	o := one(t, e, it)
	if o.Branch != BrDeleted {
		t.Fatalf("%+v", o)
	}
	if _, ok := w.f.refTip("refs/heads/topic"); !ok {
		t.Fatal("the local branch was deleted")
	}
	if !strings.Contains(strings.Join(o.Warnings, "\n"), "local branch topic") {
		t.Errorf("warnings %v do not name the local branch", o.Warnings)
	}
}

// TestRemoteNeverPushesWithoutTheRemoteApproval hands Run an item sealed with
// every approval except Remote. What: refused, no push issued, origin intact.
// Why: the approval gate is the last guard before a public delete.
func TestRemoteNeverPushesWithoutTheRemoteApproval(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	calls := recordPush(t)
	it := seal(Item{Kind: cleanup.KindRemote, Ref: w.row.Ref, Name: "topic", Saw: cleanup.StatusProven, SawTip: w.tip,
		Approved: Needs{Confirm: true, Force: true, Delete: true}})
	o := one(t, e, it)
	if o.Worktree != WtRefused || o.Branch != BrNotAttempted || !strings.Contains(o.Reason, "needs") || len(*calls) != 0 || w.originHas("topic") != w.tip {
		t.Fatalf("outcome %+v, pushes %v", o, *calls)
	}
	// An unsealed hand-built item is refused as not confirmed.
	bare := Item{Kind: cleanup.KindRemote, Ref: w.row.Ref, Name: "topic", Saw: cleanup.StatusProven, SawTip: w.tip, Approved: Needs{Remote: true}}
	if o := one(t, e, bare); o.Worktree != WtRefused || !strings.Contains(o.Reason, "not confirmed") || len(*calls) != 0 {
		t.Fatalf("outcome %+v", o)
	}
}

// TestRemoteTipMovedBeforeThePreflight lets someone push to origin's topic
// after the list. What: the preflight ls-remote sees another sha, nothing is
// pushed, the outcome is retained_moved and origin keeps the new commit. Why:
// the tracking ref is only as fresh as the last fetch; the live read protects
// work pushed since.
func TestRemoteTipMovedBeforeThePreflight(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	moved := w.moveOrigin(t)
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch != BrRetainedMoved || o.DeletedTip != "" || len(*calls) != 0 || w.originHas("topic") != moved {
		t.Fatalf("outcome %+v pushes %v origin %s", o, *calls, w.originHas("topic"))
	}
}

// TestRemoteTipMovedBetweenPreflightAndPush moves origin's topic inside the
// window after the preflight read. What: the push IS issued, the lease
// rejects it, origin keeps the new commit and the outcome is retained_moved.
// Why: this is the race the explicit-sha lease exists for.
func TestRemoteTipMovedBetweenPreflightAndPush(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	var moved string
	old := beforePush
	beforePush = func() { moved = w.moveOrigin(t) }
	t.Cleanup(func() { beforePush = old })
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch != BrRetainedMoved || len(*calls) != 1 || w.originHas("topic") != moved {
		t.Fatalf("outcome %+v pushes %v origin %s want %s", o, *calls, w.originHas("topic"), moved)
	}
}

// TestRemoteAlreadyGone deletes the branch on origin behind our back. What:
// retained_moved ("gone"), no push. Why: a delete of a missing ref is not a
// success of ours and must not be reported as one.
func TestRemoteAlreadyGone(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	gitIn(t, w.f.Origin, "update-ref", "-d", "refs/heads/topic")
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch != BrRetainedMoved || len(*calls) != 0 || o.DeletedTip != "" {
		t.Fatalf("outcome %+v pushes %v", o, *calls)
	}
}

// TestRemoteServerRefusesTheDelete makes origin reject the delete, by a
// pre-receive hook and by receive.denyDeletes. What: the ref stays, the
// outcome is retained_unknown carrying the server's text as a warning, and no
// restore tip is claimed. Why: branch protection and hooks are normal on a
// real forge; the answer must come from re-reading the ref, not from hoping
// the push worked.
func TestRemoteServerRefusesTheDelete(t *testing.T) {
	for name, setup := range map[string]func(w remoteWorld){
		"pre-receive hook": func(w remoteWorld) {
			h := filepath.Join(w.f.Origin, "hooks", "pre-receive")
			if err := os.WriteFile(h, []byte("#!/bin/sh\necho no-deletes-here >&2\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"denyDeletes": func(w remoteWorld) { gitIn(t, w.f.Origin, "config", "receive.denyDeletes", "true") },
	} {
		t.Run(name, func(t *testing.T) {
			w := newRemoteWorld(t)
			e := w.engine(Options{})
			it := w.confirmed(t, e)
			setup(w)
			o := one(t, e, it)
			if o.Branch != BrRetainedUnknown || o.DeletedTip != "" || w.originHas("topic") != w.tip {
				t.Fatalf("outcome %+v origin %s", o, w.originHas("topic"))
			}
			if len(o.Warnings) == 0 {
				t.Error("no warning carries the server's reason")
			}
		})
	}
}

// TestRemoteReadsThroughThePushURL gives origin a separate pushurl (a second
// bare repo holding the same branch) and deletes through it. What: the
// delete lands on the push destination, and the result is deleted with a
// restore tip, not retained_unknown. Why: the fetch URL still lists the
// branch, so a read through it would call a push that really deleted the ref
// "not deleted" and drop the restore sha.
func TestRemoteReadsThroughThePushURL(t *testing.T) {
	w := newRemoteWorld(t)
	pushDest := filepath.Join(t.TempDir(), "p.git")
	gitIn(t, filepath.Dir(pushDest), "clone", "--bare", w.f.Origin, pushDest)
	w.f.git("remote", "set-url", "--push", "origin", pushDest)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	o := one(t, e, it)
	if o.Branch != BrDeleted || o.DeletedTip != w.tip {
		t.Fatalf("outcome %+v, want deleted with tip %s", o, w.tip)
	}
	if got := gitIn(t, pushDest, "for-each-ref", "refs/heads/topic"); got != "" {
		t.Fatalf("push destination still has topic: %s", got)
	}
}

// TestRemoteGateChecksTheProofItself feeds remoteGate a rescan that returns a
// proven-looking row with one fact wrong, bypassing the classifier. What: a
// PR with another head, a PR into another base, a moved tip and an Unknown
// row are each stopped (refused or skipped_changed) with nothing pushed.
// Why: step 8 and the tip/unknown checks are the last gate before a public
// delete; they must hold even if the classifier's remote rung regresses.
func TestRemoteGateChecksTheProofItself(t *testing.T) {
	cases := map[string]func(r *cleanup.Row, tip string){
		"PR head differs": func(r *cleanup.Row, tip string) { r.PR.HeadRefOid = strings.Repeat("9", 40) },
		"PR base differs": func(r *cleanup.Row, tip string) { r.PR.BaseRef = "other" },
		"PR not merged":   func(r *cleanup.Row, tip string) { r.PR.State = "CLOSED" },
		"no PR":           func(r *cleanup.Row, tip string) { r.PR = nil },
		"tip moved": func(r *cleanup.Row, tip string) {
			r.Tip = strings.Repeat("8", 40)
			r.PR.HeadRefOid = r.Tip // the PR proof still agrees: only the tip check can stop it
		},
		"unknown": func(r *cleanup.Row, tip string) { r.Unknown = true },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			w := newRemoteWorld(t)
			e := w.engine(Options{})
			it := w.confirmed(t, e)
			old := rescan
			rescan = func(g cleanup.Git, p cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
				r, err := old(g, p, in, o, tg)
				if r.PR != nil {
					pr := *r.PR
					r.PR = &pr
				}
				mut(&r, w.tip)
				return r, err
			}
			t.Cleanup(func() { rescan = old })
			calls := recordPush(t)
			o := one(t, e, it)
			if o.Branch == BrDeleted || len(*calls) != 0 || w.originHas("topic") != w.tip {
				t.Fatalf("outcome %+v pushes %v", o, *calls)
			}
			if o.Worktree != WtRefused && o.Worktree != WtSkippedChanged {
				t.Fatalf("worktree %s, want refused or skipped_changed", o.Worktree)
			}
		})
	}
}

// TestRemotePreflightReadFailure makes ls-remote fail. What: retained_unknown
// and no push. Why: an unreadable origin is never read as "unchanged".
func TestRemotePreflightReadFailure(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	stubProbe(t, func(old func(cleanup.Git, ...string) (cleanup.Out, error), g cleanup.Git, args ...string) (cleanup.Out, error) {
		if slices.Contains(args, "ls-remote") {
			return cleanup.Out{}, errors.New("network down")
		}
		return old(g, args...)
	})
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch != BrRetainedUnknown || len(*calls) != 0 {
		t.Fatalf("outcome %+v pushes %v", o, *calls)
	}
}

// TestRemotePushTimeoutIsSettledByReading times the push out in two worlds:
// the ref was deleted before the timeout, and it was not. What: a fresh read
// decides (deleted / retained_unknown); when even the read fails the outcome
// is retained_unknown WITH the restore tip. Why: a timeout after a write may
// have deleted the ref; the exit status alone is never the answer.
func TestRemotePushTimeoutIsSettledByReading(t *testing.T) {
	run := func(t *testing.T, doPush, breakRead bool) Outcome {
		w := newRemoteWorld(t)
		e := w.engine(Options{})
		it := w.confirmed(t, e)
		old := runRemoteGit
		runRemoteGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
			if doPush {
				_, _ = old(g, args...)
			}
			if breakRead {
				n := 0
				stubProbe(t, func(o func(cleanup.Git, ...string) (cleanup.Out, error), g cleanup.Git, a ...string) (cleanup.Out, error) {
					if slices.Contains(a, "ls-remote") {
						n++
						return cleanup.Out{}, errors.New("network down")
					}
					return o(g, a...)
				})
			}
			return cleanup.Out{}, context.DeadlineExceeded
		}
		t.Cleanup(func() { runRemoteGit = old })
		return one(t, e, it)
	}
	if o := run(t, true, false); o.Branch != BrDeleted || o.DeletedTip == "" {
		t.Errorf("deleted before the timeout: %+v", o)
	}
	if o := run(t, false, false); o.Branch != BrRetainedUnknown || o.DeletedTip != "" {
		t.Errorf("timeout, ref still there: %+v", o)
	}
	if o := run(t, true, true); o.Branch != BrRetainedUnknown || o.DeletedTip == "" {
		t.Errorf("timeout and unreadable: %+v, want retained_unknown with the tip", o)
	}
}

// TestRemotePanicAfterThePushKeepsTheTip panics inside the push runner. What:
// the outcome is failed-safe (retained_unknown) with DeletedTip set. Why: the
// ref may be gone; the sha is the one record needed to restore it.
func TestRemotePanicAfterThePushKeepsTheTip(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	it := w.confirmed(t, e)
	old := runRemoteGit
	runRemoteGit = func(cleanup.Git, ...string) (cleanup.Out, error) { panic("boom") }
	t.Cleanup(func() { runRemoteGit = old })
	o := one(t, e, it)
	if o.Branch != BrRetainedUnknown || o.DeletedTip != w.tip || !strings.Contains(o.Reason, w.tip) {
		t.Fatalf("outcome %+v", o)
	}
}

// TestRemoteReverifiesAgainstGh changes gh's answers after the list. What:
// a PR opened on the head, a PR opened against the branch, the merged PR's
// head differing from the tip, and gh failing each stop the item before any
// push. Why: the engine re-asks gh for origin items; the delete closes PRs.
func TestRemoteReverifiesAgainstGh(t *testing.T) {
	cases := map[string]func(w remoteWorld){
		"open PR on the head": func(w remoteWorld) {
			stubRemoteGh(t, []byte(fmt.Sprintf(`[{"number":9,"state":"OPEN","headRefOid":%q,"baseRefName":"main","isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`, w.tip)), []byte("[]"))
		},
		"PR opened against it": func(w remoteWorld) {
			stubRemoteGh(t, mergedPR(w.tip, w.tip), []byte(`[{"number":40,"headRefName":"c","isCrossRepository":true}]`))
		},
		"merged PR head differs": func(w remoteWorld) {
			stubRemoteGh(t, mergedPR(strings.Repeat("9", 40), w.tip), []byte("[]"))
		},
		"gh fails": func(w remoteWorld) {
			t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh down") }))
		},
		"base query fails": func(w remoteWorld) {
			t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh down") }))
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			w := newRemoteWorld(t)
			e := w.engine(Options{})
			it := w.confirmed(t, e)
			mut(w)
			calls := recordPush(t)
			o := one(t, e, it)
			if o.Branch == BrDeleted || len(*calls) != 0 || w.originHas("topic") != w.tip {
				t.Fatalf("outcome %+v pushes %v", o, *calls)
			}
			if o.Worktree != WtRefused && o.Worktree != WtSkippedChanged {
				t.Fatalf("worktree %s, want refused or skipped_changed", o.Worktree)
			}
		})
	}
}

// TestRemoteRefusesWhatIsNotAProvenOriginBranch hands sealed items for the
// default branch, another remote, an unproven row and a malformed name. What:
// none pushes; the default is retained_default; the rest are refused with
// "invalid target". Why: defence in depth behind the classifier, the way the
// worktree and branch stages check what they are handed.
func TestRemoteRefusesWhatIsNotAProvenOriginBranch(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{})
	calls := recordPush(t)
	mk := func(ref string, saw cleanup.Status, tip string) Item {
		return seal(Item{Kind: cleanup.KindRemote, Ref: ref, Name: "x", Saw: saw, SawTip: tip, Approved: Needs{Remote: true}})
	}
	cases := map[string]struct {
		it   Item
		want BranchOutcome
	}{
		"default branch":    {mk("refs/remotes/origin/main", cleanup.StatusProven, w.tip), BrRetainedDefault},
		"other remote":      {mk("refs/remotes/upstream/topic", cleanup.StatusProven, w.tip), BrNotAttempted},
		"less sure":         {mk(w.row.Ref, cleanup.StatusLessSure, w.tip), BrNotAttempted},
		"option-like name":  {mk("refs/remotes/origin/-D", cleanup.StatusProven, w.tip), BrNotAttempted},
		"colon in name":     {mk("refs/remotes/origin/a:b", cleanup.StatusProven, w.tip), BrNotAttempted},
		"short sha":         {mk(w.row.Ref, cleanup.StatusProven, "abc"), BrNotAttempted},
		"local branch form": {mk("refs/heads/topic", cleanup.StatusProven, w.tip), BrNotAttempted},
	}
	for name, c := range cases {
		o := one(t, e, c.it)
		if o.Branch != c.want || o.Branch == BrDeleted {
			t.Errorf("%s: %+v", name, o)
		}
		if c.want == BrNotAttempted && (o.Worktree != WtRefused || !strings.Contains(o.Reason, "invalid target")) {
			t.Errorf("%s: %+v, want refused: invalid target", name, o)
		}
	}
	if len(*calls) != 0 || w.originHas("topic") != w.tip {
		t.Fatalf("a push was issued: %v", *calls)
	}
}

// TestRemoteWithoutACleanFetchIsRefused takes a list built with --no-fetch.
// What: its remote row is not proven, so the item built from it is refused,
// and a sealed item claiming proven is refused by the fresh re-check (the
// fetch result is carried, not re-run). Why: without this run's fetch a
// dependent PR can be missed.
func TestRemoteWithoutACleanFetchIsRefused(t *testing.T) {
	w := newRemoteWorld(t)
	f := w.f
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, f.inputs()) // Fetch false
	if err != nil {
		t.Fatal(err)
	}
	r := cleanup.Classify(s, cleanup.Options{Now: later()})
	row := f.row(r, cleanup.KindRemote, "topic")
	if row.Status == cleanup.StatusProven {
		t.Fatal("a row from an unfetched list is proven")
	}
	e := f.engine(s, f.inputs(), Options{})
	calls := recordPush(t)
	o := one(t, e, seal(Item{Kind: cleanup.KindRemote, Ref: row.Ref, Name: "topic", Saw: cleanup.StatusProven, SawTip: w.tip, Approved: Needs{Remote: true}}))
	if o.Branch == BrDeleted || len(*calls) != 0 || w.originHas("topic") != w.tip {
		t.Fatalf("outcome %+v pushes %v", o, *calls)
	}
}

// TestRemoteDryRunReadsButNeverPushes runs the item with Options.DryRun. What:
// the reads happen (the preflight sees the moved tip when it moved) and no
// push is issued; an unmoved branch reports "dry run: would delete". Why: a
// dry run says exactly what a run would do.
func TestRemoteDryRunReadsButNeverPushes(t *testing.T) {
	w := newRemoteWorld(t)
	e := w.engine(Options{DryRun: true})
	it := w.confirmed(t, e)
	calls := recordPush(t)
	o := one(t, e, it)
	if o.Branch != BrDeleted || !strings.HasPrefix(o.Reason, "dry run: would delete origin/topic") || len(*calls) != 0 || o.DeletedTip != "" || w.originHas("topic") != w.tip {
		t.Fatalf("outcome %+v pushes %v", o, *calls)
	}
	moved := w.moveOrigin(t)
	o = one(t, e, it)
	if o.Branch != BrRetainedMoved || w.originHas("topic") != moved {
		t.Fatalf("dry run after a move: %+v", o)
	}
}
