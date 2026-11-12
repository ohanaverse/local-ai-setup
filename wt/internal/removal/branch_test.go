package removal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// refTip is ref's commit as for-each-ref lists it under that exact spelling,
// and whether it exists (never rev-parse: on a case-insensitive volume it
// resolves another spelling's ref file).
func (f *fixture) refTip(ref string) (string, bool) {
	f.t.Helper()
	out := f.git("for-each-ref", "--format=%(refname) %(objectname)", ref)
	for _, line := range strings.Split(out, "\n") {
		name, tip, ok := strings.Cut(line, " ")
		if ok && name == ref {
			return tip, true
		}
	}
	return "", false
}

// state is the repository's refs, worktrees and working-tree status: what a
// dry run must leave byte-for-byte as it was.
func (f *fixture) state() string {
	f.t.Helper()
	return f.git("worktree", "list", "--porcelain") + "\n" + f.git("for-each-ref") + "\n" + f.git("status", "--porcelain", "--ignored")
}

// unmergedPushed makes a worktree on branch with one commit main lacks,
// pushed (so nothing is unpushed, and nothing proves it merged).
func (f *fixture) unmergedPushed(branch string) string {
	f.t.Helper()
	p := f.Worktree(branch)
	f.Commit(p, branch+".txt", "work", branch+" work")
	f.Push(branch)
	return p
}

// mergedWorktree makes a worktree on branch with one commit, pushed and
// squash-merged into main: its branch is proven.
func (f *fixture) mergedWorktree(branch string) string {
	f.t.Helper()
	p := f.Worktree(branch)
	f.Commit(p, branch+".txt", "work", branch+" work")
	f.Push(branch)
	f.SquashMerge(branch)
	return p
}

// brOf is a branch stage result as its outcome and reason.
func brOf(r branchResult) (BranchOutcome, string) { return r.Out, r.Reason }

// stubRefGit swaps the branch-deletion runner for the rest of the test.
func stubRefGit(t *testing.T, fn func(g cleanup.Git, args ...string) (cleanup.Out, error)) {
	t.Helper()
	old := runRefGit
	runRefGit = fn
	t.Cleanup(func() { runRefGit = old })
}

// recordRefGit records every branch-deletion git call, then runs it.
func recordRefGit(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		calls = append(calls, append([]string(nil), args...))
		return g.Run(args...)
	})
	return &calls
}

// hookBeforeUpdateRef runs fn in the window between the last check and the
// update-ref, for the rest of the test.
func hookBeforeUpdateRef(t *testing.T, fn func()) {
	t.Helper()
	old := beforeUpdateRef
	beforeUpdateRef = fn
	t.Cleanup(func() { beforeUpdateRef = old })
}

// TestBranchDeletedAfterWorktreeRemoval removes a squash-merged worktree: its
// branch is proven, so the branch goes too, with a compare-and-swap on the tip
// the re-check read (the exact argv), and the reason carries the short tip so
// the user can put it back.
func TestBranchDeletedAfterWorktreeRemoval(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.mergedWorktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if !row.BranchProven {
		t.Fatalf("precondition: branch not proven: %+v", row)
	}
	calls := recordRefGit(t)
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %+v, want removed / deleted", o)
	}
	if exists(t, p) {
		t.Error("worktree still on disk")
	}
	if _, ok := f.refTip("refs/heads/topic"); ok {
		t.Error("refs/heads/topic still exists")
	}
	want := [][]string{{"update-ref", "--no-deref", "-d", "refs/heads/topic", row.Tip}}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("git calls %q, want %q", *calls, want)
	}
	if !strings.Contains(o.Reason, "deleted refs/heads/topic (was "+row.Tip+";") {
		t.Errorf("reason %q lacks the deletion and full tip", o.Reason)
	}
}

// TestBranchRetainedUnmergedWithoutD removes a worktree whose branch holds a
// commit nothing proves merged, without -D: the worktree goes (the commit is
// on the branch), the branch stays at its tip, and the outcome is
// retained_unmerged, git's own -d answer (exit 0, not a failure).
func TestBranchRetainedUnmergedWithoutD(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.unmergedPushed("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.BranchProven {
		t.Fatalf("precondition: branch proven: %+v", row)
	}
	calls := recordRefGit(t)
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrRetainedUnmerged {
		t.Fatalf("outcome %+v, want removed / retained_unmerged", o)
	}
	if exists(t, p) {
		t.Error("worktree still on disk")
	}
	if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != row.Tip {
		t.Errorf("refs/heads/topic = %q (exists %v), want intact at %s", tip, ok, row.Tip)
	}
	if len(*calls) != 0 {
		t.Errorf("git calls %q, want none", *calls)
	}
}

// TestBranchDeletedWithDOnUnmerged removes the same unmerged worktree with
// -D (Approved.Delete): the branch is deleted, the reason carries the full
// tip right after the ref and the restore command last, and a warning says
// no more than git keeps: the commits live only in the object store until a
// gc prunes them, which can be the next one.
func TestBranchDeletedWithDOnUnmerged(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	// Unpushed: no other ref holds the commit, so the gc caveat applies (#531).
	f.Commit(f.Worktree("topic"), "topic.txt", "work", "topic work")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %+v, want removed / deleted", o)
	}
	if _, ok := f.refTip("refs/heads/topic"); ok {
		t.Error("refs/heads/topic still exists")
	}
	want := "deleted refs/heads/topic (was " + row.Tip + "; restore: git branch topic " + row.Tip + ")"
	if o.Reason != want {
		t.Errorf("reason %q, want %q", o.Reason, want)
	}
	if !slices.Contains(o.Warnings, gcCaveat) {
		t.Errorf("warnings %q lack the gc caveat", o.Warnings)
	}
}

// TestBranchWithDStillHonoursOtherRules: -D skips only the proven check. A
// branch checked out in another worktree and the default branch are still
// retained with it (update-ref -d has no checked-out protection of its own).
func TestBranchWithDStillHonoursOtherRules(t *testing.T) {
	t.Run("checked out elsewhere", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.unmergedPushed("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		other := filepath.Join(f.Dir, ".worktrees", "other")
		f.git("worktree", "add", "--force", other, "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRemoved || o.Branch != BrRetainedCheckedOut {
			t.Fatalf("outcome %+v, want removed / retained_checked_out", o)
		}
		if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != row.Tip {
			t.Errorf("refs/heads/topic = %q (exists %v), want intact", tip, ok)
		}
	})
	t.Run("default branch", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		s, _ := f.report()
		mainTip, _ := f.refTip("refs/heads/main")
		e := f.engine(s, f.inputs(), Options{})
		br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, cleanup.Row{Ref: "refs/heads/main", Tip: mainTip}))
		if br != BrRetainedDefault {
			t.Fatalf("got %s (%s), want retained_default", br, reason)
		}
		if tip, ok := f.refTip("refs/heads/main"); !ok || tip != mainTip {
			t.Error("main touched")
		}
	})
}

// TestBranchCASLosesRace moves the branch in the window between the last
// check and the update-ref: git refuses the stale old value, the outcome is
// retained_moved, and the branch is intact at its NEW tip. Without the
// compare-and-swap the new commit would be deleted unseen.
func TestBranchCASLosesRace(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	newTip := f.git("commit-tree", "-p", row.Tip, "-m", "raced", row.Tip+"^{tree}")
	hookBeforeUpdateRef(t, func() { f.git("update-ref", "refs/heads/topic", newTip) })
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrRetainedMoved {
		t.Fatalf("outcome %+v, want removed / retained_moved", o)
	}
	if !strings.Contains(o.Reason, "moved") {
		t.Errorf("reason %q does not say the branch moved", o.Reason)
	}
	if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != newTip {
		t.Errorf("refs/heads/topic = %q (exists %v), want intact at the new tip %s", tip, ok, newTip)
	}
}

// TestBranchCASRefGoneIsMoved deletes the branch in the same window: the
// compare-and-swap fails, the re-read finds nothing, and that is also
// retained_moved (fail closed: it cannot tell whether git deleted it before
// failing, as after a timeout), saying so and giving the full tip.
func TestBranchCASRefGoneIsMoved(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	hookBeforeUpdateRef(t, func() { f.git("update-ref", "-d", "refs/heads/topic") })
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrRetainedMoved {
		t.Fatalf("outcome %+v, want removed / retained_moved", o)
	}
	if !strings.Contains(o.Reason, "gone after git error; it may have been deleted by this run") || !strings.Contains(o.Reason, row.Tip) {
		t.Errorf("reason %q, want the gone-after-error wording and the full tip", o.Reason)
	}
}

// TestBranchRetainedWhenCheckedOutElsewhere adds a second worktree on the
// same branch AFTER the list was made: only a fresh worktree list, read after
// the removal, sees it. update-ref -d would delete a checked-out branch.
func TestBranchRetainedWhenCheckedOutElsewhere(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	other := filepath.Join(f.Dir, ".worktrees", "other")
	f.git("worktree", "add", "--force", other, "topic")
	calls := recordRefGit(t)
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrRetainedCheckedOut {
		t.Fatalf("outcome %+v, want removed / retained_checked_out", o)
	}
	if !strings.Contains(strings.Join(o.Warnings, "\n"), "other") {
		t.Errorf("warnings %q do not name the other worktree", o.Warnings)
	}
	if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != row.Tip {
		t.Errorf("refs/heads/topic = %q (exists %v), want intact", tip, ok)
	}
	if len(*calls) != 0 {
		t.Errorf("git calls %q, want none", *calls)
	}
}

// TestCheckedOutElsewhereIsCaseFolded points a second worktree's HEAD at
// refs/heads/Feature/x while the removed one was on refs/heads/feature/x. On
// a case-insensitive volume (the macOS default) both spellings are one ref
// file, so the branch IS checked out there; on a case-sensitive one they are
// two refs and retaining is an over-match that only keeps more. Either way
// the branch must be retained. The comparison itself is pinned directly too.
func TestCheckedOutElsewhereIsCaseFolded(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("feature/x")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "feature/x")
	if row.Ref != "refs/heads/feature/x" || !row.BranchProven {
		t.Fatalf("precondition: row %+v", row)
	}
	other := filepath.Join(f.Dir, ".worktrees", "other")
	f.git("worktree", "add", "--detach", other)
	gitIn(t, other, "symbolic-ref", "HEAD", "refs/heads/Feature/x")
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrRetainedCheckedOut {
		t.Fatalf("outcome %+v, want removed / retained_checked_out", o)
	}
	if _, ok := f.refTip("refs/heads/feature/x"); !ok {
		t.Error("refs/heads/feature/x was deleted")
	}

	if !refHeld([]string{"refs/heads/main", "refs/heads/Feature/X"}, "refs/heads/feature/x") {
		t.Error("refHeld does not fold case")
	}
	if refHeld([]string{"", "refs/heads/feature/y"}, "refs/heads/feature/x") {
		t.Error("refHeld matched another branch")
	}
}

// TestBranchRetainedDefault: the default branch, in any case spelling, is
// never deleted, and an unresolved default branch is a refusal (fail
// closed): with no base nothing can say the ref is not the default.
func TestBranchRetainedDefault(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Branch("topic")
	s, _ := f.report()
	mainTip, _ := f.refTip("refs/heads/main")
	topicTip, _ := f.refTip("refs/heads/topic")
	for _, c := range []struct {
		name string
		snap func(cleanup.Snapshot) cleanup.Snapshot
		ref  string
		tip  string
	}{
		{"the default ref", func(s cleanup.Snapshot) cleanup.Snapshot { return s }, "refs/heads/main", mainTip},
		{"the default ref case-folded", func(s cleanup.Snapshot) cleanup.Snapshot { return s }, "refs/heads/MAIN", mainTip},
		{"a remote form of the default", func(s cleanup.Snapshot) cleanup.Snapshot { return s }, "refs/heads/origin/main", mainTip},
		{"no default branch", func(s cleanup.Snapshot) cleanup.Snapshot { s.NoDefault = true; s.Base = cleanup.Base{}; return s }, "refs/heads/topic", topicTip},
		{"empty default name", func(s cleanup.Snapshot) cleanup.Snapshot { s.Base.Default = ""; return s }, "refs/heads/topic", topicTip},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, d := range []bool{false, true} {
				e := f.engine(c.snap(s), f.inputs(), Options{})
				br, reason := brOf(e.deleteBranch(itemCtx{item: Item{Approved: Needs{Confirm: true, Delete: d}}, considerBranch: true}, cleanup.Row{Ref: c.ref, Tip: c.tip, BranchProven: true}))
				if br != BrRetainedDefault {
					t.Fatalf("-D=%v: got %s (%s), want retained_default", d, br, reason)
				}
			}
		})
	}

	// Through the engine: a list made with no default branch, a branch item.
	t.Run("engine, unresolved default", func(t *testing.T) {
		s2 := s
		s2.NoDefault, s2.Base = true, cleanup.Base{}
		r2 := cleanup.Classify(s2, cleanup.Options{Now: later()})
		row := f.row(r2, cleanup.KindBranch, "topic")
		it := itemOf(row)
		o := one(t, f.engine(s2, f.inputs(), Options{}), approve(it, false, true))
		if o.Worktree != WtNotRequested || o.Branch != BrRetainedDefault {
			t.Fatalf("outcome %+v, want not_requested / retained_default", o)
		}
	})
	if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != topicTip {
		t.Error("topic touched")
	}
	if tip, ok := f.refTip("refs/heads/main"); !ok || tip != mainTip {
		t.Error("main touched")
	}
}

// TestBranchNotAttemptedForDetached removes a detached worktree: there is no
// branch, so the branch stage is not_attempted and says why.
func TestBranchNotAttemptedForDetached(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := filepath.Join(f.Dir, ".worktrees", "loose")
	f.git("worktree", "add", "--detach", p)
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "loose")
	before := f.git("for-each-ref")
	o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
	if o.Worktree != WtRemoved || o.Branch != BrNotAttempted || !strings.Contains(o.Reason, "detached") {
		t.Fatalf("outcome %+v, want removed / not_attempted naming the detached HEAD", o)
	}
	if after := f.git("for-each-ref"); after != before {
		t.Errorf("refs changed:\n%s\n%s", before, after)
	}
}

// TestBranchOnlyItem deletes a local branch that has no worktree: proven, it
// goes without -D; unproven, it is retained_unmerged without -D and deleted
// with it. The worktree outcome is not_requested throughout.
func TestBranchOnlyItem(t *testing.T) {
	t.Run("proven", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		if row.Status != cleanup.StatusProven {
			t.Fatalf("precondition: %s", row.Status)
		}
		o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
		if o.Worktree != WtNotRequested || o.Branch != BrDeleted {
			t.Fatalf("outcome %+v, want not_requested / deleted", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); ok {
			t.Error("topic still exists")
		}
	})
	for _, d := range []bool{false, true} {
		name := "unproven without -D"
		want := BrRetainedUnmerged
		if d {
			name, want = "unproven with -D", BrDeleted
		}
		t.Run(name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.unmergedPushed("topic")
			f.git("worktree", "remove", p)
			s, r := f.report()
			row := f.row(r, cleanup.KindBranch, "topic")
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, d))
			if o.Worktree != WtNotRequested || o.Branch != want {
				t.Fatalf("outcome %+v, want not_requested / %s", o, want)
			}
			if _, ok := f.refTip("refs/heads/topic"); ok == d {
				t.Errorf("topic exists %v after %s", ok, want)
			}
		})
	}
}

// TestBranchOnlyItemIdentityIsEnforced: a branch item acts only on the exact
// ref the user ticked. A case-only spelling difference (two refs on a
// case-sensitive volume, one file on a case-insensitive one) is refused, and
// a branch a worktree now holds is refused by the re-check.
func TestBranchOnlyItemIdentityIsEnforced(t *testing.T) {
	t.Run("rescan returns another spelling", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		old := rescan
		rescan = func(g cleanup.Git, p cleanup.Snapshot, in cleanup.GatherInputs, o cleanup.Options, tg cleanup.Target) (cleanup.Row, error) {
			fr, err := old(g, p, in, o, tg)
			fr.Ref = "refs/heads/Topic"
			return fr, err
		}
		t.Cleanup(func() { rescan = old })
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRefused || o.Branch != BrNotAttempted || !strings.Contains(o.Reason, "spelling") {
			t.Fatalf("outcome %+v, want refused naming the spelling", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
	t.Run("item names another spelling", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		it := itemOf(f.row(r, cleanup.KindBranch, "topic"))
		it.Ref = "refs/heads/Topic"
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(it, false, true))
		if o.Branch != BrNotAttempted || (o.Worktree != WtRefused && o.Worktree != WtSkippedChanged) {
			t.Fatalf("outcome %+v, want refused or skipped, branch not attempted", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
	t.Run("a worktree now holds it", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		f.git("worktree", "add", filepath.Join(f.Dir, ".worktrees", "topic"), "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRefused || o.Branch != BrNotAttempted || !strings.Contains(o.Reason, "checked out") {
			t.Fatalf("outcome %+v, want refused naming the checkout", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
}

// TestOutcomeReportsWorktreeRemovedBranchRetained: one outcome carries both
// halves, worktree removed and branch retained, with a reason that names the
// branch, why it stayed, and the flag that would delete it.
func TestOutcomeReportsWorktreeRemovedBranchRetained(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedPushed("topic")
	s, r := f.report()
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if o.Worktree != WtRemoved || o.Branch != BrRetainedUnmerged {
		t.Fatalf("outcome %+v, want removed / retained_unmerged", o)
	}
	for _, w := range []string{"refs/heads/topic", "not proven merged", "-D"} {
		if !strings.Contains(o.Reason, w) {
			t.Errorf("reason %q lacks %q", o.Reason, w)
		}
	}
}

// TestBranchNetZeroUnpushedNeedsD: a branch whose commits undo each other
// and were never pushed is demoted from proven (PR A's net_zero_unpushed):
// its commits exist only here. Without -D its branch is retained; with -D it
// is deleted.
func TestBranchNetZeroUnpushedNeedsD(t *testing.T) {
	for _, d := range []bool{false, true} {
		want := BrRetainedUnmerged
		if d {
			want = BrDeleted
		}
		t.Run(string(want), func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Commit(p, "secret.txt", "s", "add secret")
			gitIn(t, p, "revert", "--no-edit", "HEAD")
			f.Commit(f.Dir, "m", "m", "m")
			f.git("push", "origin", "main")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			if row.BranchProven || !hasReason(row, cleanup.ReasonNetZeroUnpushed) {
				t.Fatalf("precondition: row %+v, want net_zero_unpushed and not proven", row)
			}
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, d))
			if o.Worktree != WtRemoved || o.Branch != want {
				t.Fatalf("outcome %+v, want removed / %s", o, want)
			}
		})
	}
}

func hasReason(row cleanup.Row, code string) bool {
	for _, r := range row.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

// TestBranchProvenIsTheBranchVerdictNotTheWorktreeStatus (Ruling A): a dirty
// worktree whose branch is squash-merged has a worse combined status, but the
// BRANCH is proven. Removed with Force, its branch is deleted, not retained:
// the dirt went with the worktree the user approved, the branch holds nothing
// the base lacks.
func TestBranchProvenIsTheBranchVerdictNotTheWorktreeStatus(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.mergedWorktree("topic")
	f.Dirty(p, "wip.txt")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Status == cleanup.StatusProven || !row.BranchProven {
		t.Fatalf("precondition: status %s branch proven %v", row.Status, row.BranchProven)
	}
	o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), true, false))
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %+v, want removed / deleted", o)
	}
}

// TestPlanBranchStageMutatesNothing plans a mix of worktree and branch items
// covering each branch rule a dry run can reach, checks refs and worktrees
// are byte-for-byte unchanged, then runs them: each branch decision the plan
// made is the one the run makes.
func TestPlanBranchStageMutatesNothing(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	f.unmergedPushed("topic2")
	f.Worktree("topic3")
	detached := filepath.Join(f.Dir, ".worktrees", "det")
	f.git("worktree", "add", "--detach", detached)
	f.Branch("loose")
	q := f.unmergedPushed("loose2")
	f.git("worktree", "remove", q)
	s, r := f.report()
	f.git("worktree", "add", "--force", filepath.Join(f.Dir, ".worktrees", "holder"), "topic3")
	items := []Item{
		itemOf(f.row(r, cleanup.KindWorktree, "topic")),
		itemOf(f.row(r, cleanup.KindWorktree, "topic2")),
		itemOf(f.row(r, cleanup.KindWorktree, "topic3")),
		itemOf(f.row(r, cleanup.KindWorktree, "det")),
		itemOf(f.row(r, cleanup.KindBranch, "loose")),
		itemOf(f.row(r, cleanup.KindBranch, "loose2")),
	}
	want := []BranchOutcome{BrDeleted, BrRetainedUnmerged, BrRetainedCheckedOut, BrNotAttempted, BrDeleted, BrRetainedUnmerged}
	calls := recordRefGit(t)
	before := f.state()
	e := f.engine(s, f.inputs(), Options{})
	plan := e.Plan(items)
	if after := f.state(); after != before {
		t.Fatalf("Plan changed the repository:\nbefore %s\nafter %s", before, after)
	}
	if len(*calls) != 0 {
		t.Fatalf("Plan ran %q", *calls)
	}
	for i, o := range plan {
		if o.Branch != want[i] {
			t.Errorf("plan %s: branch %s (%s), want %s", items[i].Name, o.Branch, o.Reason, want[i])
		}
		if o.Branch == BrDeleted && !strings.Contains(o.Reason, "would delete "+items[i].Ref) {
			t.Errorf("plan %s: reason %q, want a dry-run deletion", items[i].Name, o.Reason)
		}
	}
	run := e.Run(items, nil)
	for i := range items {
		if plan[i].Worktree != run[i].Worktree || plan[i].Branch != run[i].Branch {
			t.Errorf("item %s: plan %s/%s, run %s/%s (%s)", items[i].Name, plan[i].Worktree, plan[i].Branch, run[i].Worktree, run[i].Branch, run[i].Reason)
		}
	}
}

// TestBranchDeletionRefusedOnStaleSawFields: a branch item that no row could
// have produced (no Saw, an untickable Saw, no or short SawTip, a ref outside
// refs/heads/, a path) is refused before anything is read, and nothing is
// deleted: the approval check cannot judge it.
func TestBranchDeletionRefusedOnStaleSawFields(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Branch("topic")
	s, r := f.report()
	base := itemOf(f.row(r, cleanup.KindBranch, "topic"))
	for _, c := range []struct {
		name string
		mod  func(*Item)
	}{
		{"empty saw", func(it *Item) { it.Saw = "" }},
		{"in_use saw", func(it *Item) { it.Saw = cleanup.StatusInUse }},
		{"bogus saw", func(it *Item) { it.Saw = "bogus" }},
		{"no tip", func(it *Item) { it.SawTip = "" }},
		{"short tip", func(it *Item) { it.SawTip = base.SawTip[:7] }},
		{"short ref", func(it *Item) { it.Ref = "topic" }},
		{"remote ref", func(it *Item) { it.Ref = "refs/remotes/origin/topic" }},
		{"tag ref", func(it *Item) { it.Ref = "refs/tags/topic" }},
		{"no ref", func(it *Item) { it.Ref = "" }},
		{"a path", func(it *Item) { it.Path = f.Dir }},
	} {
		t.Run(c.name, func(t *testing.T) {
			calls := recordRefGit(t)
			it := base
			c.mod(&it)
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(it, false, true))
			if o.Worktree != WtRefused || o.Branch != BrNotAttempted || !strings.Contains(o.Reason, "invalid target") {
				t.Fatalf("outcome %+v, want refused as an invalid target", o)
			}
			if len(*calls) != 0 {
				t.Errorf("git calls %q", *calls)
			}
		})
	}
	if _, ok := f.refTip("refs/heads/topic"); !ok {
		t.Error("topic deleted")
	}
}

// TestBranchItemSkippedWhenChanged: a branch item whose tip moved, or whose
// status got worse, since it was listed is skipped_changed and kept: the user
// approved the branch as it was.
func TestBranchItemSkippedWhenChanged(t *testing.T) {
	t.Run("tip moved", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		newTip := f.git("commit-tree", "-p", row.Tip, "-m", "later", row.Tip+"^{tree}")
		f.git("update-ref", "refs/heads/topic", newTip)
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtSkippedChanged || o.Branch != BrNotAttempted {
			t.Fatalf("outcome %+v, want skipped_changed / not_attempted", o)
		}
		if tip, _ := f.refTip("refs/heads/topic"); tip != newTip {
			t.Error("topic moved or deleted")
		}
	})
	t.Run("gone", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		f.git("update-ref", "-d", "refs/heads/topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
		if o.Worktree != WtSkippedChanged || o.Branch != BrNotAttempted {
			t.Fatalf("outcome %+v, want skipped_changed / not_attempted", o)
		}
	})
	t.Run("more unpushed than seen", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.unmergedPushed("topic")
		f.git("worktree", "remove", p)
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		it := itemOf(row)
		it.Saw = cleanup.StatusUniqueWork
		it.SawLosses.Unpushed = 0
		fresh := row
		fresh.Losses.Unpushed = 3
		old := rescan
		rescan = func(cleanup.Git, cleanup.Snapshot, cleanup.GatherInputs, cleanup.Options, cleanup.Target) (cleanup.Row, error) {
			return fresh, nil
		}
		t.Cleanup(func() { rescan = old })
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(it, false, true))
		if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "lost") {
			t.Fatalf("outcome %+v, want skipped_changed naming the loss", o)
		}
	})
}

// withD is an item approved for -D, for calls straight into the branch
// stage (the approval is per item, Ruling K).
var withD = Item{Approved: Needs{Confirm: true, Delete: true}}

// branchCase is a fresh repository with a proven local branch topic, the
// engine over it (callers pass withD for -D), and topic's tip: one per
// subtest, so a wrong deletion in one case cannot mask another.
func branchCase(t *testing.T) (*fixture, Engine, cleanup.Row) {
	t.Helper()
	noGh(t)
	f := newFixture(t)
	f.Branch("topic")
	s, _ := f.report()
	tip, _ := f.refTip("refs/heads/topic")
	return f, f.engine(s, f.inputs(), Options{}), cleanup.Row{Ref: "refs/heads/topic", Tip: tip, BranchProven: true}
}

// TestBranchRetainedUnknown: anything the branch stage cannot read keeps the
// branch: an Unknown fresh row, a tip that is not a commit id, a worktree
// list that fails, a worktree whose holders cannot be read, a delete git
// refuses with the ref unmoved (its stderr is the reason), and a delete git
// reports done that left the ref in place. Each case has its own repository.
func TestBranchRetainedUnknown(t *testing.T) {
	check := func(t *testing.T, f *fixture, e Engine, fresh cleanup.Row, tip, want string) {
		t.Helper()
		r := e.deleteBranch(itemCtx{item: withD, considerBranch: true}, fresh)
		// The detail may be in the reason or, with a path or git's text,
		// in a warning.
		text := r.Reason + "\n" + strings.Join(r.Warnings, "\n")
		if r.Out != BrRetainedUnknown || !strings.Contains(text, want) {
			t.Fatalf("got %s %q, want retained_unknown naming %q", r.Out, text, want)
		}
		if got, ok := f.refTip("refs/heads/topic"); !ok || got != tip {
			t.Fatal("topic touched")
		}
	}
	t.Run("unknown row", func(t *testing.T) {
		f, e, good := branchCase(t)
		u := good
		u.Unknown = true
		u.Reasons = []cleanup.Reason{{Code: cleanup.ReasonBranchUnknown}}
		check(t, f, e, u, good.Tip, "could not be read")
	})
	t.Run("unknown row with no codes, under -D", func(t *testing.T) {
		// Nothing says the unknown is only the merge verdict: keep it.
		f, e, good := branchCase(t)
		u := good
		u.Unknown = true
		check(t, f, e, u, good.Tip, "could not be read")
	})
	t.Run("sha256 tip", func(t *testing.T) {
		f, e, good := branchCase(t)
		u := good
		u.Tip = strings.Repeat("a", 64)
		check(t, f, e, u, good.Tip, "object format not supported")
	})
	t.Run("no tip", func(t *testing.T) {
		f, e, good := branchCase(t)
		u := good
		u.Tip = ""
		check(t, f, e, u, good.Tip, "commit")
	})
	t.Run("list fails", func(t *testing.T) {
		f, e, good := branchCase(t)
		old := listWorktrees
		listWorktrees = func(cleanup.Git, string) ([]cleanup.WorktreeFact, error) { return nil, errors.New("list boom") }
		t.Cleanup(func() { listWorktrees = old })
		check(t, f, e, good, good.Tip, "list boom")
	})
	t.Run("holders unreadable", func(t *testing.T) {
		f, e, good := branchCase(t)
		old := heldRefs
		heldRefs = func(cleanup.Git, cleanup.WorktreeFact) ([]string, error) { return nil, errors.New("holder boom") }
		t.Cleanup(func() { heldRefs = old })
		check(t, f, e, good, good.Tip, "holder boom")
	})
	t.Run("git refuses, ref unmoved", func(t *testing.T) {
		f, e, good := branchCase(t)
		stubRefGit(t, func(cleanup.Git, ...string) (cleanup.Out, error) {
			return cleanup.Out{Code: 1, Stderr: "fatal: lock\x1b boom\nsecond line\n"}, nil
		})
		check(t, f, e, good, good.Tip, "lock boom second line") // one sanitised line
	})
	t.Run("git says done, ref still there", func(t *testing.T) {
		f, e, good := branchCase(t)
		stubRefGit(t, func(cleanup.Git, ...string) (cleanup.Out, error) { return cleanup.Out{}, nil })
		check(t, f, e, good, good.Tip, "still exists")
	})
	t.Run("not considered", func(t *testing.T) {
		_, e, good := branchCase(t)
		if br, _ := brOf(e.deleteBranch(itemCtx{}, good)); br != BrNotAttempted {
			t.Fatalf("got %s, want not_attempted when the worktree stage did not succeed", br)
		}
	})
}

// forcedWorktreeOn adds a worktree at name that has branch checked out (forced:
// another worktree may hold it too) and returns its path.
func (f *fixture) forcedWorktreeOn(name, branch string) string {
	f.t.Helper()
	p := filepath.Join(f.Dir, ".worktrees", name)
	f.git("worktree", "add", "--force", p, branch)
	return p
}

// stoppedRebase leaves the worktree at p (on its branch, which has at least
// one commit of its own) in a rebase stopped by a failing --exec: HEAD is
// detached and rebase-merge/head-name holds the branch.
func stoppedRebase(t *testing.T, p string) {
	t.Helper()
	cmd := exec.Command("git", "-C", p, "rebase", "--force-rebase", "--exec", "false", "HEAD~1")
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	if b, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("rebase did not stop:\n%s", b)
	}
	if b := gitIn(t, p, "status"); !strings.Contains(b, "rebas") {
		t.Fatalf("precondition: no rebase in progress:\n%s", b)
	}
}

// TestBranchHeldByRebaseOrBisect: a branch a stopped rebase or a bisect in
// another worktree will write back is held there although that worktree's
// HEAD is detached (git branch -D refuses it). The worktree item's branch is
// retained_checked_out, a branch item is refused by the re-check, and a
// rebase whose holder cannot be read keeps the branch (fail closed).
func TestBranchHeldByRebaseOrBisect(t *testing.T) {
	t.Run("worktree item, rebase elsewhere", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.mergedWorktree("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		stoppedRebase(t, f.forcedWorktreeOn("other", "topic"))
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRemoved || o.Branch != BrRetainedCheckedOut {
			t.Fatalf("outcome %+v, want removed / retained_checked_out", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
	t.Run("worktree item, bisect elsewhere", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.Worktree("topic")
		for i := range 3 {
			f.Commit(p, "b.txt", strings.Repeat("x", i+1), "b")
		}
		f.Push("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		other := f.forcedWorktreeOn("other", "topic")
		gitIn(t, other, "bisect", "start", "HEAD", "main")
		if b := gitIn(t, other, "worktree", "list", "--porcelain"); !strings.Contains(b, "detached") {
			t.Fatalf("precondition: bisect did not detach:\n%s", b)
		}
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRemoved || o.Branch != BrRetainedCheckedOut {
			t.Fatalf("outcome %+v, want removed / retained_checked_out", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
	t.Run("worktree item, nested rebase holder lost its .git file", func(t *testing.T) {
		// The holder sits inside the primary checkout (.worktrees/other):
		// git run in it would answer for the outer repository.
		noGh(t)
		f := newFixture(t)
		f.mergedWorktree("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		other := f.forcedWorktreeOn("other", "topic")
		stoppedRebase(t, other)
		if err := os.Remove(filepath.Join(other, ".git")); err != nil {
			t.Fatal(err)
		}
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRemoved || o.Branch != BrRetainedCheckedOut {
			t.Fatalf("outcome %+v, want removed / retained_checked_out", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
	t.Run("branch item, rebase in its worktree", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		stoppedRebase(t, f.mergedWorktree("topic"))
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Worktree != WtRefused || o.Branch != BrNotAttempted || !strings.Contains(o.Reason, "rebased or bisected") {
			t.Fatalf("outcome %+v, want refused naming the rebase", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
	t.Run("unreadable holder", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.mergedWorktree("topic")
		s, r := f.report()
		other := filepath.Join(f.Dir, ".worktrees", "other")
		f.git("worktree", "add", "--detach", other)
		marker := gitIn(t, other, "rev-parse", "--path-format=absolute", "--git-path", "rebase-merge")
		if err := os.MkdirAll(marker, 0o755); err != nil { // a rebase with no head-name
			t.Fatal(err)
		}
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(f.row(r, cleanup.KindWorktree, "topic")), false, true))
		if o.Worktree != WtRemoved || o.Branch != BrRetainedUnknown {
			t.Fatalf("worktree item %+v, want removed / retained_unknown", o)
		}
		f.Branch("topic2")
		s2, r2 := f.report()
		o = one(t, f.engine(s2, f.inputs(), Options{}), approve(itemOf(f.row(r2, cleanup.KindBranch, "topic2")), false, true))
		if o.Worktree != WtRefused || o.Branch != BrNotAttempted {
			t.Fatalf("branch item %+v, want refused", o)
		}
		for _, ref := range []string{"refs/heads/topic", "refs/heads/topic2"} {
			if _, ok := f.refTip(ref); !ok {
				t.Errorf("%s deleted", ref)
			}
		}
	})
}

// ghDown stubs gh pr list to fail for every branch, for the rest of the
// test: no PR state can be known.
func ghDown(t *testing.T) {
	t.Helper()
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh: offline") }))
}

// TestBranchDWhenOnlyMergeVerdictUnknown (Ruling H): -D deletes whatever the
// merge verdict, so an Unknown that only answers "is it merged?" (gh down:
// pr_unknown) does not keep a branch under -D; without -D it does. An
// Unknown about what would be lost (unpushed_unknown) or one the engine does
// not know keeps it even with -D.
func TestBranchDWhenOnlyMergeVerdictUnknown(t *testing.T) {
	for _, d := range []bool{true, false} {
		want := BrRetainedUnknown
		if d {
			want = BrDeleted
		}
		t.Run("gh down, -D="+map[bool]string{true: "yes", false: "no"}[d], func(t *testing.T) {
			ghDown(t)
			f := newFixture(t)
			p := f.unmergedPushed("topic")
			f.git("worktree", "remove", p)
			s, r := f.report()
			row := f.row(r, cleanup.KindBranch, "topic")
			if !row.Unknown || !hasReason(row, cleanup.ReasonPRUnknown) {
				t.Fatalf("precondition: row %+v, want Unknown with pr_unknown", row)
			}
			o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, d))
			if o.Worktree != WtNotRequested || o.Branch != want {
				t.Fatalf("outcome %+v, want not_requested / %s", o, want)
			}
		})
	}
	for _, code := range []string{cleanup.ReasonUnpushedUnknown, cleanup.ReasonBranchUnknown, "a_code_added_later"} {
		t.Run(code+" keeps it under -D", func(t *testing.T) {
			f, e, good := branchCase(t)
			u := good
			u.Unknown = true
			u.Reasons = []cleanup.Reason{{Code: cleanup.ReasonPRUnknown}, {Code: code}}
			if br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, u)); br != BrRetainedUnknown {
				t.Fatalf("got %s %q, want retained_unknown", br, reason)
			}
			if _, ok := f.refTip("refs/heads/topic"); !ok {
				t.Error("topic deleted")
			}
		})
	}
}

// TestBranchItemTipAndStatusChecksPinned pins the two "changed since it was
// listed" checks of a branch item one at a time, under -D (so the proven
// check cannot stand in for them): a tip that moved with nothing else
// worse, and a status that got worse on the same tip (the branch became too
// young to prove at the run's clock).
func TestBranchItemTipAndStatusChecksPinned(t *testing.T) {
	t.Run("tip moved, status not worse", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		it := itemOf(row)
		it.Saw = cleanup.StatusUniqueWork           // nothing ranks worse
		it.SawLosses = cleanup.Losses{Unpushed: 99} // nor loses more
		newTip := f.git("commit-tree", "-p", row.Tip, "-m", "later", row.Tip+"^{tree}")
		f.git("update-ref", "refs/heads/topic", newTip)
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(it, false, true))
		if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "moved") {
			t.Fatalf("outcome %+v, want skipped_changed naming the move", o)
		}
		if tip, _ := f.refTip("refs/heads/topic"); tip != newTip {
			t.Error("topic deleted or moved")
		}
	})
	t.Run("status worse, same tip", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		if row.Status != cleanup.StatusProven {
			t.Fatalf("precondition: %s", row.Status)
		}
		// The run's clock is now: the branch is younger than the min age.
		o := one(t, f.engine(s, f.inputs(), Options{Now: time.Now}), approve(itemOf(row), false, true))
		if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "status changed") {
			t.Fatalf("outcome %+v, want skipped_changed naming the status", o)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Error("topic deleted")
		}
	})
}

// TestBranchDeleteDoesNotDeref: a ref that is (or became) a symbolic ref is
// deleted itself, never its target: --no-deref. Without it git deletes the
// branch the symref points at.
func TestBranchDeleteDoesNotDeref(t *testing.T) {
	f, e, good := branchCase(t)
	f.git("symbolic-ref", "refs/heads/alias", "refs/heads/topic")
	alias := good
	alias.Ref = "refs/heads/alias"
	br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, alias))
	if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != good.Tip {
		t.Fatalf("the symref's target was deleted (%s %q)", br, reason)
	}
}

// hookAfterUpdateRef runs fn between a confirmed delete and the re-check of
// the worktrees, for the rest of the test.
func hookAfterUpdateRef(t *testing.T, fn func()) {
	t.Helper()
	old := afterUpdateRef
	afterUpdateRef = fn
	t.Cleanup(func() { afterUpdateRef = old })
}

// TestBranchRestoredWhenCheckedOutDuringDelete: a worktree that checks the
// branch out in the window around the update-ref would be left on a deleted
// branch. The re-check after the delete sees it and puts the branch back,
// create-only, at its tip (retained_checked_out); if that restore fails the
// outcome is retained_unknown with the full tip to restore by hand.
func TestBranchRestoredWhenCheckedOutDuringDelete(t *testing.T) {
	checkout := func(f *fixture) func() {
		return func() {
			p := filepath.Join(f.Dir, ".worktrees", "late")
			f.git("worktree", "add", "--detach", p)
			gitIn(f.t, p, "symbolic-ref", "HEAD", "refs/heads/topic")
		}
	}
	t.Run("restored", func(t *testing.T) {
		f, e, good := branchCase(t)
		hookAfterUpdateRef(t, checkout(f))
		br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, good))
		if br != BrRetainedCheckedOut || !strings.Contains(reason, "restored") {
			t.Fatalf("got %s %q, want retained_checked_out, restored", br, reason)
		}
		if tip, ok := f.refTip("refs/heads/topic"); !ok || tip != good.Tip {
			t.Errorf("topic = %q (exists %v), want restored at %s", tip, ok, good.Tip)
		}
	})
	t.Run("restore fails", func(t *testing.T) {
		f, e, good := branchCase(t)
		hookAfterUpdateRef(t, checkout(f))
		var calls [][]string
		stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
			calls = append(calls, args)
			if len(calls) == 1 {
				return g.Run(args...)
			}
			return cleanup.Out{Code: 1, Stderr: "fatal: cannot lock"}, nil
		})
		br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, good))
		if br != BrRetainedUnknown || !strings.Contains(reason, "git branch topic "+good.Tip) {
			t.Fatalf("got %s %q, want retained_unknown with the full tip", br, reason)
		}
		want := []string{"update-ref", "refs/heads/topic", good.Tip, strings.Repeat("0", 40)}
		if len(calls) != 2 || !reflect.DeepEqual(calls[1], want) {
			t.Errorf("calls %q, want a create-only restore %q", calls, want)
		}
	})
	t.Run("restore reports success but the ref is not back", func(t *testing.T) {
		f, e, good := branchCase(t)
		hookAfterUpdateRef(t, checkout(f))
		n := 0
		stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
			if n++; n == 1 {
				return g.Run(args...)
			}
			return cleanup.Out{}, nil // claims the restore, writes nothing
		})
		br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, good))
		if br != BrRetainedUnknown || !strings.Contains(reason, good.Tip) {
			t.Fatalf("got %s %q, want retained_unknown with the full tip", br, reason)
		}
	})
	t.Run("re-created in the window", func(t *testing.T) {
		// Someone made a new topic at another commit: the create-only
		// restore is refused, and `git branch topic <sha>` would fail too.
		f, e, good := branchCase(t)
		other := f.git("commit-tree", "-p", good.Tip, "-m", "new", good.Tip+"^{tree}")
		hookAfterUpdateRef(t, func() {
			checkout(f)()
			f.git("update-ref", "refs/heads/topic", other)
		})
		br, reason := brOf(e.deleteBranch(itemCtx{item: withD, considerBranch: true}, good))
		if br != BrRetainedUnknown || !strings.Contains(reason, "re-created at "+other[:7]) ||
			!strings.Contains(reason, "the deleted commit was "+good.Tip) || strings.Contains(reason, "git branch") {
			t.Fatalf("got %s %q, want retained_unknown naming the re-creation and the deleted commit, no restore command", br, reason)
		}
		if tip, _ := f.refTip("refs/heads/topic"); tip != other {
			t.Errorf("topic at %s, want the re-created %s", tip, other)
		}
	})
	t.Run("gc caveat follows the same rule when not restored", func(t *testing.T) {
		// branchCase's tip is on main: nothing to lose to a gc, whatever
		// became of the restore.
		for _, name := range []string{"re-created", "restore fails"} {
			f, e, good := branchCase(t)
			other := f.git("commit-tree", "-p", good.Tip, "-m", "new", good.Tip+"^{tree}")
			hookAfterUpdateRef(t, func() {
				checkout(f)()
				if name == "re-created" {
					f.git("update-ref", "refs/heads/topic", other)
				}
			})
			if name == "restore fails" {
				n := 0
				stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
					if n++; n == 1 {
						return g.Run(args...)
					}
					return cleanup.Out{Code: 1, Stderr: "fatal: cannot lock"}, nil
				})
			}
			res := e.deleteBranch(itemCtx{item: withD, considerBranch: true}, good)
			if slices.Contains(res.Warnings, gcCaveat) {
				t.Errorf("%s: warnings %q carry the gc caveat for a tip held by main", name, res.Warnings)
			}
		}
	})
}

// longName is a 45-character branch name.
var longName = "topic-" + strings.Repeat("n", 39)

// longPath is a worktree path of about 200 characters under f's repository.
func (f *fixture) longPath(tag string) string {
	base := filepath.Join(f.Dir, ".worktrees", tag+"-")
	return base + strings.Repeat("p", max(1, 200-len(base)))
}

// TestBranchReasonKeepsFullTipUnderTheCap runs deletions through the real
// engine (Outcome.SetReason caps a reason at 200 runes) with a 45-character
// branch name and ~200-character worktree paths. The full commit id must
// survive the cap in the deleted, restored and restore-failed reasons: it is
// the only record of the deleted commit. Paths and git's text go to
// warnings.
func TestBranchReasonKeepsFullTipUnderTheCap(t *testing.T) {
	ref := "refs/heads/" + longName
	t.Run("deleted", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.longPath("wt")
		f.git("worktree", "add", "-b", longName, p)
		f.Commit(p, "x.txt", "x", "x")
		f.Push(longName)
		f.SquashMerge(longName)
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, longName)
		o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
		if o.Branch != BrDeleted || !strings.Contains(o.Reason, ref+" (was "+row.Tip) {
			t.Fatalf("outcome %+v, want deleted with the full tip after the ref", o)
		}
	})
	for _, failRestore := range []bool{false, true} {
		name, want := "restored", BrRetainedCheckedOut
		if failRestore {
			name, want = "restore failed", BrRetainedUnknown
		}
		t.Run(name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			f.Branch(longName)
			s, r := f.report()
			row := f.row(r, cleanup.KindBranch, longName)
			hookAfterUpdateRef(t, func() {
				p := f.longPath("late")
				f.git("worktree", "add", "--detach", p)
				gitIn(t, p, "symbolic-ref", "HEAD", ref)
			})
			if failRestore {
				n := 0
				stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
					if n++; n == 1 {
						return g.Run(args...)
					}
					return cleanup.Out{Code: 1, Stderr: strings.Repeat("fatal: long git error ", 20)}, nil
				})
			}
			o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
			if o.Branch != want || !strings.Contains(o.Reason, ref+" (was "+row.Tip+")") {
				t.Fatalf("outcome %+v, want %s with the full tip after the ref", o, want)
			}
			if !strings.Contains(strings.Join(o.Warnings, "\n"), "late-") {
				t.Errorf("warnings %q do not name the worktree", o.Warnings)
			}
		})
	}
}

// TestBranchDeleteDropsUpstreamConfig deletes a branch that has an upstream
// configured and requires its [branch "<name>"] section to go with it, as
// `git branch -D` does. Why: update-ref -d leaves remote/merge behind, and the
// printed restore command (`git branch <name> <sha>`) would then make a
// branch that silently inherits a stale upstream.
func TestBranchDeleteDropsUpstreamConfig(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.mergedWorktree("topic")
	f.git("config", "branch.topic.remote", "origin")
	f.git("config", "branch.topic.merge", "refs/heads/topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Branch != BrDeleted {
		t.Fatalf("outcome %+v, want deleted", o)
	}
	if got := f.git("config", "--list"); strings.Contains(got, "branch.topic.") {
		t.Errorf("branch.topic.* config left behind:\n%s", got)
	}
}

// TestBranchGcCaveatOnlyForUnreachableTip checks the gc warning follows the
// truth (#531): a deleted branch whose tip another ref still holds (the
// creation commit on main, a remote-tracking ref, a sibling branch) loses
// nothing to a gc, so no caveat; a tip only the deleted branch held keeps
// it. Why it matters: a gc caveat on a fresh branch sitting on main alarms
// for nothing, so the user-driven cleanup list must reserve it for a truly
// unreachable tip.
func TestBranchGcCaveatOnlyForUnreachableTip(t *testing.T) {
	run := func(t *testing.T, setup func(f *fixture) string, del bool) Outcome {
		t.Helper()
		noGh(t)
		f := newFixture(t)
		setup(f)
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, del))
		if o.Branch != BrDeleted {
			t.Fatalf("outcome %+v, want branch deleted", o)
		}
		return o
	}
	t.Run("tip on main", func(t *testing.T) {
		o := run(t, func(f *fixture) string { return f.Worktree("topic") }, false)
		if slices.Contains(o.Warnings, gcCaveat) {
			t.Errorf("warnings %q carry the gc caveat for a tip on main", o.Warnings)
		}
		if !strings.Contains(o.Reason, "restore: git branch topic ") {
			t.Errorf("reason %q lost the restore command", o.Reason)
		}
	})
	t.Run("tip on another remote-tracking ref", func(t *testing.T) {
		o := run(t, func(f *fixture) string {
			p := f.unmergedPushed("topic")
			f.git("push", "origin", "topic:refs/heads/other")
			f.git("fetch", "origin")
			return p
		}, true)
		if slices.Contains(o.Warnings, gcCaveat) {
			t.Errorf("warnings %q carry the gc caveat for a tip on origin/other", o.Warnings)
		}
	})
	// D2: origin/<the deleted branch's own name> goes in the same cleanup (or
	// is what the user removes), so it does not hold the tip.
	t.Run("tip only on origin of its own name", func(t *testing.T) {
		o := run(t, func(f *fixture) string { return f.unmergedPushed("topic") }, true)
		if !slices.Contains(o.Warnings, gcCaveat) {
			t.Errorf("warnings %q lack the gc caveat: only origin/topic holds the tip", o.Warnings)
		}
	})
	// D1: only refs/heads and refs/remotes hold a tip.
	t.Run("tip held only by a tag", func(t *testing.T) {
		o := run(t, func(f *fixture) string {
			p := f.Worktree("topic")
			f.Commit(p, "topic.txt", "work", "topic work")
			f.git("tag", "keep", "topic")
			return p
		}, true)
		if !slices.Contains(o.Warnings, gcCaveat) {
			t.Errorf("warnings %q lack the gc caveat: a tag does not count", o.Warnings)
		}
	})
	t.Run("tip held only by a stash entry", func(t *testing.T) {
		o := run(t, func(f *fixture) string {
			p := f.Worktree("topic")
			sha := f.Commit(p, "topic.txt", "work", "topic work")
			f.git("update-ref", "refs/stash", sha)
			return p
		}, true)
		if !slices.Contains(o.Warnings, gcCaveat) {
			t.Errorf("warnings %q lack the gc caveat: refs/stash does not count", o.Warnings)
		}
	})
	// A git failure while asking keeps the caveat (fail closed), through the
	// probeGit seam.
	t.Run("holder probe fails", func(t *testing.T) {
		for _, asErr := range []bool{true, false} {
			failProbe(t, "for-each-ref", "--count=2", asErr)
			o := run(t, func(f *fixture) string { return f.Worktree("topic") }, false)
			if !slices.Contains(o.Warnings, gcCaveat) {
				t.Errorf("asErr=%v: warnings %q lack the gc caveat after a failed probe", asErr, o.Warnings)
			}
		}
	})
	t.Run("tip held by no other ref", func(t *testing.T) {
		o := run(t, func(f *fixture) string {
			p := f.Worktree("topic")
			f.Commit(p, "topic.txt", "work", "topic work")
			return p
		}, true)
		if !slices.Contains(o.Warnings, gcCaveat) {
			t.Errorf("warnings %q lack the gc caveat for an unreachable tip", o.Warnings)
		}
	})
}

// TestDeleteDefaultBranchRowNeedsAcceptLoss: the default branch's local head,
// listed with the default_branch floor, is refused without accept-loss and
// allowed with it.
func TestDeleteDefaultBranchRowNeedsAcceptLoss(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.git("switch", "-c", "feat")
	s, r := f.report()
	row := f.row(r, cleanup.KindBranch, "main")
	if row.Status != cleanup.StatusLessSure || row.HardStop != "" {
		t.Fatalf("precondition: %s hardstop %q", row.Status, row.HardStop)
	}
	e := f.engine(s, f.inputs(), Options{})
	bare := itemOf(row)
	bare.Approved = Needs{}
	bare = seal(bare)
	o := one(t, e, bare)
	if o.Branch == BrDeleted || !strings.Contains(o.Reason, "needs confirmation") {
		t.Fatalf("without accept-loss: %+v, want refused needing confirmation", o)
	}
	if _, ok := f.refTip("refs/heads/main"); !ok {
		t.Fatal("main deleted without accept-loss")
	}
	o = one(t, e, approve(itemOf(row), false, true))
	if o.Branch != BrDeleted {
		t.Fatalf("with accept-loss: %+v, want deleted", o)
	}
	if _, ok := f.refTip("refs/heads/main"); ok {
		t.Error("main still exists")
	}
}

// TestDeleteFallbackDefaultBranchRefused: with no remote default the base is
// the local main itself, so nothing different proves it; main stays
// (retained_default) even with every approval, and its own commits survive.
func TestDeleteFallbackDefaultBranchRefused(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	f.git("switch", "-c", "topic")
	f.git("switch", "main")
	f.Commit(f.Dir, "only.txt", "only", "only-on-main")
	f.git("switch", "topic")
	s, r := f.report()
	if !s.Base.Fallback {
		t.Fatalf("precondition: base %+v, want the local main", s.Base)
	}
	row := f.row(r, cleanup.KindBranch, "main")
	e := f.engine(s, f.inputs(), Options{})
	o := one(t, e, approve(itemOf(row), false, true))
	if o.Branch != BrRetainedDefault {
		t.Fatalf("outcome %+v, want %s", o, BrRetainedDefault)
	}
	if _, ok := f.refTip("refs/heads/main"); !ok {
		t.Fatal("main deleted although the base is main itself")
	}
}
