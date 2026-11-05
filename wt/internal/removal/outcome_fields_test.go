package removal

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestDeletedTipThroughTheEngine runs the real engine (Run, Plan) on fresh
// repositories and pins Outcome.DeletedTip (Ruling J): the full commit id
// when the run deleted the branch, deleted and restored it (a worktree took
// it in the window), deleted it and could not restore it, or may have
// deleted it (git failed and the ref is gone); empty for a dry run, for
// every kept branch (unmerged, moved before the delete, held elsewhere,
// git reporting a delete that did not happen) and for a refused item. Why:
// the reason is capped at 200 runes, so this field is the one record of a
// deleted commit that no branch-name length can cut.
func TestDeletedTipThroughTheEngine(t *testing.T) {
	type tc struct {
		run      func(t *testing.T) (Outcome, string)
		wantBr   BranchOutcome
		wantsTip bool
	}
	lateCheckout := func(f *fixture) func() {
		return func() {
			p := filepath.Join(f.Dir, ".worktrees", "late")
			f.git("worktree", "add", "--detach", p)
			gitIn(f.t, p, "symbolic-ref", "HEAD", "refs/heads/topic")
		}
	}
	// branchItem is a fresh repository with a proven local branch topic, its
	// sealed item, and the engine over it.
	branchItem := func(t *testing.T) (*fixture, Engine, Item, string) {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		return f, f.engine(s, f.inputs(), Options{}), itemOf(row), row.Tip
	}
	cases := map[string]tc{
		"deleted": {func(t *testing.T) (Outcome, string) {
			noGh(t)
			f := newFixture(t)
			f.mergedWorktree("topic")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			return one(t, f.engine(s, f.inputs(), Options{}), itemOf(row)), row.Tip
		}, BrDeleted, true},
		"restored": {func(t *testing.T) (Outcome, string) {
			f, e, it, tip := branchItem(t)
			hookAfterUpdateRef(t, lateCheckout(f))
			return one(t, e, it), tip
		}, BrRetainedCheckedOut, true},
		"restore failed": {func(t *testing.T) (Outcome, string) {
			f, e, it, tip := branchItem(t)
			hookAfterUpdateRef(t, lateCheckout(f))
			n := 0
			stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
				if n++; n == 1 {
					return g.Run(args...)
				}
				return cleanup.Out{Code: 1, Stderr: "fatal: cannot lock"}, nil
			})
			return one(t, e, it), tip
		}, BrRetainedUnknown, true},
		"gone after a git error": {func(t *testing.T) (Outcome, string) {
			f, e, it, tip := branchItem(t)
			hookBeforeUpdateRef(t, func() { f.git("update-ref", "-d", "refs/heads/topic") })
			return one(t, e, it), tip
		}, BrRetainedMoved, true},
		"deleted, then unreadable": {func(t *testing.T) (Outcome, string) {
			f, e, it, tip := branchItem(t)
			packed := f.git("rev-parse", "--path-format=absolute", "--git-path", "packed-refs")
			stubRefGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
				out, err := g.Run(args...)
				// The re-read after git's reported delete now fails.
				f.write(filepath.Dir(packed), filepath.Base(packed), "not a packed-refs line\n")
				return out, err
			})
			return one(t, e, it), tip
		}, BrRetainedUnknown, true},
		"dry run": {func(t *testing.T) (Outcome, string) {
			_, e, it, tip := branchItem(t)
			out := e.Plan([]Item{it})
			return out[0], tip
		}, BrDeleted, false},
		"dry run option": {func(t *testing.T) (Outcome, string) {
			_, e, it, tip := branchItem(t)
			e.Opts.DryRun = true
			return one(t, e, it), tip
		}, BrDeleted, false},
		"retained unmerged": {func(t *testing.T) (Outcome, string) {
			noGh(t)
			f := newFixture(t)
			f.unmergedPushed("topic")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			return one(t, f.engine(s, f.inputs(), Options{}), itemOf(row)), row.Tip
		}, BrRetainedUnmerged, false},
		"moved before the delete": {func(t *testing.T) (Outcome, string) {
			f, e, it, tip := branchItem(t)
			newTip := f.git("commit-tree", "-p", tip, "-m", "raced", tip+"^{tree}")
			hookBeforeUpdateRef(t, func() { f.git("update-ref", "refs/heads/topic", newTip) })
			return one(t, e, it), tip
		}, BrRetainedMoved, false},
		"held elsewhere": {func(t *testing.T) (Outcome, string) {
			noGh(t)
			f := newFixture(t)
			f.mergedWorktree("topic")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			f.forcedWorktreeOn("other", "topic")
			return one(t, f.engine(s, f.inputs(), Options{}), itemOf(row)), row.Tip
		}, BrRetainedCheckedOut, false},
		"git reports a delete that did not happen": {func(t *testing.T) (Outcome, string) {
			_, e, it, tip := branchItem(t)
			stubRefGit(t, func(cleanup.Git, ...string) (cleanup.Out, error) { return cleanup.Out{}, nil })
			return one(t, e, it), tip
		}, BrRetainedUnknown, false},
		"refused": {func(t *testing.T) (Outcome, string) {
			noGh(t)
			f := newFixture(t)
			p := f.mergedWorktree("topic")
			f.Dirty(p, "wip.txt")
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			return one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true)), row.Tip
		}, BrNotAttempted, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			o, tip := c.run(t)
			if o.Branch != c.wantBr {
				t.Fatalf("outcome %+v, want branch %s", o, c.wantBr)
			}
			want := ""
			if c.wantsTip {
				want = tip
			}
			if o.DeletedTip != want {
				t.Errorf("DeletedTip %q, want %q (reason %q)", o.DeletedTip, want, o.Reason)
			}
			if c.wantsTip && !commitID.MatchString(o.DeletedTip) {
				t.Errorf("DeletedTip %q is not a full commit id", o.DeletedTip)
			}
		})
	}
}

// TestOutcomeLossLines pins Outcome.LossLines through the real engine: the
// loss lines of the fresh re-check the engine decided on, also for an item
// it refused after reading that row (a dirty worktree without --force) or
// removed (an ignored file); none for an item refused before any fresh row
// existed (an invalid target, an unconfirmed item). Why: the result document
// reports what the user was shown would be lost; an item refused for its
// loss must still say what that loss was.
func TestOutcomeLossLines(t *testing.T) {
	t.Run("refused with a fresh row", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Dirty(p, "wip.txt")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, false))
		if o.Worktree != WtRefused || !slices.Contains(o.LossLines, "uncommitted changes will be lost") {
			t.Fatalf("outcome %+v, want refused carrying the uncommitted-changes loss line", o)
		}
		if !slices.Equal(o.LossLines, cleanup.LossLines(row)) {
			t.Errorf("loss lines %q, want the fresh row's %q", o.LossLines, cleanup.LossLines(row))
		}
	})
	t.Run("removed with an ignored file", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.mergedWorktree("topic")
		f.Ignored(p, ".env")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
		if o.Worktree != WtRemoved || len(o.LossLines) != 1 || !slices.Equal(o.LossLines, cleanup.LossLines(row)) {
			t.Fatalf("outcome %+v, want removed carrying %q", o, cleanup.LossLines(row))
		}
	})
	t.Run("branch item with an unpushed commit", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		f.git("switch", "-c", "topic")
		f.Commit(f.Dir, "topic.txt", "work", "topic work")
		f.git("switch", "main")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), false, true))
		if o.Branch != BrDeleted || !slices.Contains(o.LossLines, "1 commit exists only on this machine") {
			t.Fatalf("outcome %+v, want deleted carrying the unpushed-commit loss line", o)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.mergedWorktree("topic")
		f.Ignored(p, ".env")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		o := f.engine(s, f.inputs(), Options{}).Plan([]Item{itemOf(row)})[0]
		if !slices.Equal(o.LossLines, cleanup.LossLines(row)) || len(o.LossLines) == 0 {
			t.Fatalf("plan %+v, want %q", o, cleanup.LossLines(row))
		}
	})
	t.Run("invalid item", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Dirty(p, "wip.txt")
		s, r := f.report()
		it := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
		it.Saw = ""
		o := one(t, f.engine(s, f.inputs(), Options{}), seal(it))
		if o.Worktree != WtRefused || len(o.LossLines) != 0 {
			t.Fatalf("outcome %+v, want refused with no loss lines", o)
		}
	})
	t.Run("not confirmed", func(t *testing.T) {
		noGh(t)
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Dirty(p, "wip.txt")
		s, r := f.report()
		it := ItemFromRow(f.row(r, cleanup.KindWorktree, "topic"))
		o := one(t, f.engine(s, f.inputs(), Options{}), it)
		if o.Worktree != WtRefused || len(o.LossLines) != 0 {
			t.Fatalf("outcome %+v, want refused with no loss lines", o)
		}
	})
}
