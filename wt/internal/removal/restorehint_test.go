package removal

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// TestRestoreHintForFlagLikeBranchName: a branch named like a flag (-D)
// cannot be restored with `git branch -D <tip>` (git refuses the name, and
// the line reads as a delete); its restore line creates the ref through
// update-ref instead, still after "(was <full sha>)", and running it puts
// the branch back.
func TestRestoreHintForFlagLikeBranchName(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.git("update-ref", "refs/heads/-D", "HEAD")
	s, r := f.report()
	row := rowByRef(t, r, cleanup.KindBranch, "refs/heads/-D")
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	want := "deleted refs/heads/-D (was " + row.Tip + "; restore: git update-ref refs/heads/-D " + row.Tip + ")"
	if o.Branch != BrDeleted || o.Reason != want {
		t.Errorf("branch %s reason %q, want deleted with %q", o.Branch, o.Reason, want)
	}
	f.git("update-ref", "refs/heads/-D", row.Tip)
	if tip, ok := f.refTip("refs/heads/-D"); !ok || tip != row.Tip {
		t.Error("the restore line did not restore the branch")
	}
}
