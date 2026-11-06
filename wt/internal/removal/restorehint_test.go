package removal

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// restoreArgv is the git argv of the restore line in reason (after
// "restore: git ", up to the closing parenthesis), with a `""` word read as
// the empty argument a shell would pass.
func restoreArgv(t *testing.T, reason string) []string {
	t.Helper()
	_, hint, ok := strings.Cut(reason, "restore: git ")
	if !ok {
		t.Fatalf("no restore line in %q", reason)
	}
	hint = strings.TrimSuffix(hint, ")")
	var argv []string
	for _, w := range strings.Fields(hint) {
		if w == `""` {
			w = ""
		}
		argv = append(argv, w)
	}
	return argv
}

// TestRestoreHintForFlagLikeBranchName: a branch named like a flag (-D)
// cannot be restored with `git branch -D <tip>` (git refuses the name, and
// the line reads as a delete); its restore line creates the ref through
// update-ref with an empty old value, still after "(was <full sha>)".
func TestRestoreHintForFlagLikeBranchName(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.git("update-ref", "refs/heads/-D", "HEAD")
	s, r := f.report()
	row := rowByRef(t, r, cleanup.KindBranch, "refs/heads/-D")
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	want := "deleted refs/heads/-D (was " + row.Tip + `; restore: git update-ref refs/heads/-D ` + row.Tip + ` "")`
	if o.Branch != BrDeleted || o.Reason != want {
		t.Errorf("branch %s reason %q, want deleted with %q", o.Branch, o.Reason, want)
	}
}

// TestRestoreHintIsCreateOnly runs the printed restore line twice: the first
// run puts the deleted branch back; after someone re-creates the branch at
// another commit, the second run must FAIL and leave that commit alone (a
// plain `update-ref <ref> <sha>` exits 0 and silently moves the branch
// back, losing the new commit).
func TestRestoreHintIsCreateOnly(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.git("update-ref", "refs/heads/-D", "HEAD")
	s, r := f.report()
	row := rowByRef(t, r, cleanup.KindBranch, "refs/heads/-D")
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Branch != BrDeleted {
		t.Fatalf("branch %s: %s", o.Branch, o.Reason)
	}
	argv := restoreArgv(t, o.Reason)
	run := func() error {
		return exec.Command("git", append([]string{"-C", f.Dir}, argv...)...).Run()
	}
	if err := run(); err != nil {
		t.Fatalf("first restore %q failed: %v", argv, err)
	}
	if tip, ok := f.refTip("refs/heads/-D"); !ok || tip != row.Tip {
		t.Fatalf("first restore left refs/heads/-D at %q (exists %v)", tip, ok)
	}
	other := f.Commit(f.Dir, "other.txt", "other", "other")
	f.git("update-ref", "refs/heads/-D", other)
	if err := run(); err == nil {
		t.Error("second restore over an existing branch exited 0")
	}
	if tip, _ := f.refTip("refs/heads/-D"); tip != other {
		t.Errorf("refs/heads/-D is at %s, want the re-created commit %s", tip, other)
	}
}
