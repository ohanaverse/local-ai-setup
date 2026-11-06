package removal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// dirtySubmoduleWorktree is a dirty worktree on topic holding an initialised
// submodule with a commit only the submodule's repository has (under the
// worktree's admin dir, <common>/worktrees/topic/modules/sub). It returns the
// worktree path and the submodule's git dir.
func dirtySubmoduleWorktree(t *testing.T, f *fixture) (string, string) {
	t.Helper()
	sub := filepath.Join(t.TempDir(), "sub")
	gitIn(t, filepath.Dir(sub), "init", "-q", "-b", "main", sub)
	gitIn(t, sub, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	f.git("-c", "protocol.file.allow=always", "submodule", "add", sub, "sub")
	f.git("commit", "-m", "add sub")
	f.Push("main")
	p := f.Worktree("topic")
	gitIn(t, p, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
	gitIn(t, filepath.Join(p, "sub"), "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "only in submodule")
	subGitDir := gitIn(t, filepath.Join(p, "sub"), "rev-parse", "--path-format=absolute", "--git-dir")
	return p, subGitDir
}

// TestForceRefusesWorktreeWithSubmodules: git skips its "contains
// submodules" check under --force, so a dirty worktree removed with Force
// lost an initialised submodule's repository, and with it a commit the
// submodule never pushed (never shown as a loss). The force path is refused
// instead, in the preview and the run alike, and nothing is touched.
func TestForceRefusesWorktreeWithSubmodules(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p, subGitDir := dirtySubmoduleWorktree(t, f)
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if !row.Losses.Dirty {
		t.Fatalf("fixture: worktree not dirty: %+v", row.Losses)
	}
	e := f.engine(s, f.inputs(), Options{})
	pv := e.Preview([]Item{ItemFromRow(row)})
	if pv[0].Approval != ApprovalRefused || !strings.Contains(pv[0].Reason, "contains submodules") {
		t.Errorf("preview %s %q; want refused: contains submodules", pv[0].Approval, pv[0].Reason)
	}
	o := one(t, e, approve(itemOf(row), true, false))
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "contains submodules; remove by hand") {
		t.Errorf("worktree %s, reason %q; want refused: contains submodules", o.Worktree, o.Reason)
	}
	if !exists(t, p) || !exists(t, subGitDir) {
		t.Errorf("worktree (%v) or submodule repository (%v) was removed", exists(t, p), exists(t, subGitDir))
	}
}

// TestForceRefusesWorktreeWithSubmoduleRepoOnly: a submodule repository left
// in the worktree's admin dir (modules/) refuses the force path even when
// `submodule status` would not list it (deinitialised, .gitmodules gone).
func TestForceRefusesWorktreeWithSubmoduleRepoOnly(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	admin := gitIn(t, p, "rev-parse", "--path-format=absolute", "--git-dir")
	if err := os.MkdirAll(filepath.Join(admin, "modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Dirty(p, "scratch.txt")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), true, false))
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "contains submodules") {
		t.Errorf("worktree %s, reason %q; want refused: contains submodules", o.Worktree, o.Reason)
	}
}
