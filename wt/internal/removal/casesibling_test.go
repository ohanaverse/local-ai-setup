package removal

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// caseSiblings makes Topic packed and topic loose at the same commit: on a
// case-insensitive volume `update-ref -d refs/heads/Topic` removes both
// (the loose file answers to either spelling, the packed line is dropped),
// on a case-sensitive one they are two plain branches. Either way the
// engine must keep Topic (Ruling X).
func caseSiblings(f *fixture, worktree bool) string {
	f.t.Helper()
	p := ""
	if worktree {
		p = f.Worktree("Topic")
	} else {
		f.Branch("Topic")
	}
	f.git("pack-refs", "--all")
	f.git("update-ref", "refs/heads/topic", "HEAD")
	return p
}

// rowByRef is the report's one row of kind k whose Ref is ref.
func rowByRef(t *testing.T, r cleanup.Report, k cleanup.Kind, ref string) cleanup.Row {
	t.Helper()
	for _, x := range r.Rows {
		if x.Kind == k && x.Ref == ref {
			return x
		}
	}
	t.Fatalf("no %s row for %s", k, ref)
	return cleanup.Row{}
}

// assertCaseSiblingKept checks a run's outcome and that both spellings are
// still listed.
func assertCaseSiblingKept(t *testing.T, f *fixture, o Outcome) {
	t.Helper()
	if o.Branch != BrRetainedUnknown || !strings.Contains(o.Reason, "differing only in case") || !strings.Contains(o.Reason, "refs/heads/topic") {
		t.Errorf("branch %s, reason %q; want retained_unknown naming refs/heads/topic as a case sibling", o.Branch, o.Reason)
	}
	if o.DeletedTip != "" {
		t.Errorf("DeletedTip %q for a branch that was not deleted", o.DeletedTip)
	}
	for _, ref := range []string{"refs/heads/Topic", "refs/heads/topic"} {
		if _, ok := f.refTip(ref); !ok {
			t.Errorf("%s was deleted; refs now:\n%s", ref, f.git("for-each-ref", "refs/heads"))
		}
	}
}

// TestCaseSiblingBranchItemKept: a branch-only item for Topic while topic
// exists at the same tip is retained_unknown on every filesystem, and the
// preview already says so (it is refused, nothing to do). On APFS the
// compare-and-swap on Topic took topic with it and reported only Topic
// deleted: a branch the user never ticked or saw was lost.
func TestCaseSiblingBranchItemKept(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	caseSiblings(f, false)
	s, r := f.report()
	row := rowByRef(t, r, cleanup.KindBranch, "refs/heads/Topic")
	e := f.engine(s, f.inputs(), Options{})

	pv := e.Preview([]Item{ItemFromRow(row)})
	if pv[0].Approval != ApprovalRefused || !strings.Contains(pv[0].Reason, "differing only in case") {
		t.Errorf("preview %s %q; want refused for the case sibling", pv[0].Approval, pv[0].Reason)
	}
	assertCaseSiblingKept(t, f, one(t, e, approve(itemOf(row), false, true)))
}

// TestCaseSiblingWorktreeItemKeepsBranch: removing a worktree on Topic
// removes the worktree but keeps Topic (and topic), with the preview's kept
// note agreeing with the run.
func TestCaseSiblingWorktreeItemKeepsBranch(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := caseSiblings(f, true)
	s, r := f.report()
	var row cleanup.Row
	for _, x := range r.Rows {
		if x.Kind == cleanup.KindWorktree && cleanup.ResolvePath(x.Path) == cleanup.ResolvePath(p) {
			row = x
		}
	}
	if row.Path == "" {
		t.Fatal("no worktree row for Topic")
	}
	e := f.engine(s, f.inputs(), Options{})
	pv := e.Preview([]Item{ItemFromRow(row)})
	if pv[0].CanDeleteBranch || pv[0].Plan.Branch != BrRetainedUnknown {
		t.Errorf("preview plan branch %s, CanDeleteBranch %v; want retained_unknown and no -D offer", pv[0].Plan.Branch, pv[0].CanDeleteBranch)
	}
	o := one(t, e, approve(itemOf(row), true, true))
	if o.Worktree != WtRemoved {
		t.Fatalf("worktree %s: %s", o.Worktree, o.Reason)
	}
	assertCaseSiblingKept(t, f, o)
}
