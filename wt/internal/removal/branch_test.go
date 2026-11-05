package removal

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	want := [][]string{{"update-ref", "-d", "refs/heads/topic", row.Tip}}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("git calls %q, want %q", *calls, want)
	}
	if !strings.Contains(o.Reason, "deleted refs/heads/topic (was "+row.Tip[:7]) {
		t.Errorf("reason %q lacks the deletion and short tip", o.Reason)
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
// -D (DeleteUnmerged): the branch is deleted, and the reason carries the short
// tip, since the commits now live only in the object store until gc.
func TestBranchDeletedWithDOnUnmerged(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.unmergedPushed("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
	if o.Worktree != WtRemoved || o.Branch != BrDeleted {
		t.Fatalf("outcome %+v, want removed / deleted", o)
	}
	if _, ok := f.refTip("refs/heads/topic"); ok {
		t.Error("refs/heads/topic still exists")
	}
	if !strings.Contains(o.Reason, row.Tip[:7]) || !strings.Contains(o.Reason, "git branch topic "+row.Tip[:7]) {
		t.Errorf("reason %q lacks the short tip and how to restore it", o.Reason)
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
		o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
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
		e := f.engine(s, f.inputs(), Options{DeleteUnmerged: true})
		br, reason := e.deleteBranch(itemCtx{considerBranch: true}, cleanup.Row{Ref: "refs/heads/main", Tip: mainTip})
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
// retained_moved (someone else changed it), never deleted or unknown.
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
	if !strings.Contains(o.Reason, "other") {
		t.Errorf("reason %q does not name the other worktree", o.Reason)
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

	facts := []cleanup.WorktreeFact{{Path: "/r", Branch: "refs/heads/main"}, {Path: "/r/.worktrees/y", Branch: "refs/heads/Feature/X"}}
	if got := checkedOutIn(facts, "refs/heads/feature/x", ""); got != "/r/.worktrees/y" {
		t.Errorf("checkedOutIn = %q, want the folded match", got)
	}
	if got := checkedOutIn(facts, "refs/heads/feature/x", "/r/.worktrees/y"); got != "" {
		t.Errorf("checkedOutIn with the removed path excluded = %q, want none", got)
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
				e := f.engine(c.snap(s), f.inputs(), Options{DeleteUnmerged: d})
				br, reason := e.deleteBranch(itemCtx{considerBranch: true}, cleanup.Row{Ref: c.ref, Tip: c.tip, BranchProven: true})
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
		o := one(t, f.engine(s2, f.inputs(), Options{DeleteUnmerged: true}), it)
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
	o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
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
			o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: d}), itemOf(row))
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
		o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
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
		o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), it)
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
		o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
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
			o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: d}), itemOf(row))
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
	o := one(t, f.engine(s, f.inputs(), Options{Force: true}), itemOf(row))
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
			o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), it)
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
		o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
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
		o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), it)
		if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "lost") {
			t.Fatalf("outcome %+v, want skipped_changed naming the loss", o)
		}
	})
}

// TestBranchRetainedUnknown: anything the branch stage cannot read keeps the
// branch: an Unknown fresh row, a tip that is not a commit id, a worktree
// list that fails, a delete git refuses with the ref unmoved (its stderr is
// the reason), and a delete git reports done that left the ref in place.
func TestBranchRetainedUnknown(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Branch("topic")
	s, _ := f.report()
	tip, _ := f.refTip("refs/heads/topic")
	good := cleanup.Row{Ref: "refs/heads/topic", Tip: tip, BranchProven: true}
	e := f.engine(s, f.inputs(), Options{DeleteUnmerged: true})
	check := func(t *testing.T, fresh cleanup.Row, want string) {
		t.Helper()
		br, reason := e.deleteBranch(itemCtx{considerBranch: true}, fresh)
		if br != BrRetainedUnknown || !strings.Contains(reason, want) {
			t.Fatalf("got %s %q, want retained_unknown naming %q", br, reason, want)
		}
		if got, ok := f.refTip("refs/heads/topic"); !ok || got != tip {
			t.Fatal("topic touched")
		}
	}
	t.Run("unknown row", func(t *testing.T) {
		u := good
		u.Unknown = true
		check(t, u, "could not be read")
	})
	t.Run("no tip", func(t *testing.T) {
		u := good
		u.Tip = ""
		check(t, u, "commit")
	})
	t.Run("list fails", func(t *testing.T) {
		old := listWorktrees
		listWorktrees = func(cleanup.Git, string) ([]cleanup.WorktreeFact, error) { return nil, errors.New("list boom") }
		t.Cleanup(func() { listWorktrees = old })
		check(t, good, "list boom")
	})
	t.Run("git refuses, ref unmoved", func(t *testing.T) {
		stubRefGit(t, func(cleanup.Git, ...string) (cleanup.Out, error) {
			return cleanup.Out{Code: 1, Stderr: "fatal: lock\x1b boom\nsecond line\n"}, nil
		})
		check(t, good, "lock boom second line") // one sanitised line
	})
	t.Run("git says done, ref still there", func(t *testing.T) {
		stubRefGit(t, func(cleanup.Git, ...string) (cleanup.Out, error) { return cleanup.Out{}, nil })
		check(t, good, "still exists")
	})
	t.Run("not considered", func(t *testing.T) {
		br, _ := e.deleteBranch(itemCtx{}, good)
		if br != BrNotAttempted {
			t.Fatalf("got %s, want not_attempted when the worktree stage did not succeed", br)
		}
	})
}
