package removal

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestProgressPanicKeepsOutcomes: a caller's progress callback that panics
// after a branch was deleted must not take the run's outcomes with it (the
// panic escaped Run and the caller got nothing: no outcome, no restore id).
// Run keeps going, returns every outcome with its DeletedTip, and records
// the panic as a warning.
func TestProgressPanicKeepsOutcomes(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Branch("topic")
	f.Branch("other")
	s, r := f.report()
	a, b := f.row(r, cleanup.KindBranch, "topic"), f.row(r, cleanup.KindBranch, "other")
	var got []Outcome
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("panic escaped Run: %v", rec)
			}
		}()
		got = f.engine(s, f.inputs(), Options{}).Run([]Item{itemOf(a), itemOf(b)}, func(Outcome) { panic("ui bug") })
	}()
	if len(got) != 2 {
		t.Fatalf("%d outcomes, want 2", len(got))
	}
	for i, row := range []cleanup.Row{a, b} {
		o := got[i]
		if o.Branch != BrDeleted || o.DeletedTip != row.Tip {
			t.Errorf("%s: branch %s tip %q; want deleted with tip %s", row.Name, o.Branch, o.DeletedTip, row.Tip)
		}
		if !strings.Contains(strings.Join(o.Warnings, "\n"), "progress callback panicked: ui bug") {
			t.Errorf("%s: warnings %q do not record the callback's panic", row.Name, o.Warnings)
		}
	}
}

// TestBranchItemPanicReportsWorktreeNotRequested: a branch-only item has no
// worktree, so a panic after its update-ref must not report its worktree as
// failed (a script would read "the worktree removal failed" for a worktree
// that never existed); the restore id is still kept.
func TestBranchItemPanicReportsWorktreeNotRequested(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Branch("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindBranch, "topic")
	hookAfterUpdateRef(t, func() { panic("boom") })
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtNotRequested {
		t.Errorf("worktree %s, want not_requested", o.Worktree)
	}
	if o.DeletedTip != row.Tip || o.Branch != BrRetainedUnknown {
		t.Errorf("branch %s tip %q; want retained_unknown with tip %s", o.Branch, o.DeletedTip, row.Tip)
	}
}
