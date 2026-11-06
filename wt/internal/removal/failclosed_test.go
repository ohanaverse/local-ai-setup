package removal

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// failProbe makes the probe whose argv starts with first fail, as an error
// (asErr) or a non-zero exit, for the rest of the test; others run.
func failProbe(t *testing.T, first, second string, asErr bool) {
	t.Helper()
	old := probeGit
	probeGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		if len(args) > 1 && args[0] == first && args[1] == second {
			if asErr {
				return cleanup.Out{}, errors.New("probe failed")
			}
			return cleanup.Out{Code: 128, Stderr: "fatal: probe failed"}, nil
		}
		return old(g, args...)
	}
	t.Cleanup(func() { probeGit = old })
}

// TestCaseSiblingCheckFailsClosed: when the branch list the case-sibling
// check reads cannot be read, the branch is kept (retained_unknown), never
// taken as "no sibling" and deleted.
func TestCaseSiblingCheckFailsClosed(t *testing.T) {
	for _, asErr := range []bool{true, false} {
		noGh(t)
		f := newFixture(t)
		f.Branch("topic")
		s, r := f.report()
		row := f.row(r, cleanup.KindBranch, "topic")
		failProbe(t, "for-each-ref", "--format=%(refname)", asErr)
		o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
		if o.Branch != BrRetainedUnknown || !strings.Contains(o.Reason, "could not list branches") {
			t.Errorf("asErr=%v: branch %s reason %q; want retained_unknown", asErr, o.Branch, o.Reason)
		}
		if _, ok := f.refTip("refs/heads/topic"); !ok {
			t.Errorf("asErr=%v: topic was deleted", asErr)
		}
	}
}

// TestSubmoduleCheckFailsClosed: when the forced worktree's git dir (or its
// submodule status) cannot be read, the force path is refused, never taken
// as "no submodules".
func TestSubmoduleCheckFailsClosed(t *testing.T) {
	for _, c := range []struct {
		first, second string
		asErr         bool
	}{
		{"rev-parse", "--path-format=absolute", true},
		{"rev-parse", "--path-format=absolute", false},
		{"submodule", "status", true},
		{"submodule", "status", false},
	} {
		noGh(t)
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Dirty(p, "wip.txt")
		s, r := f.report()
		row := f.row(r, cleanup.KindWorktree, "topic")
		failProbe(t, c.first, c.second, c.asErr)
		o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), true, false))
		if o.Worktree != WtRefused || !strings.Contains(o.Reason, "could not check for submodules") {
			t.Errorf("%s %s asErr=%v: worktree %s reason %q; want refused", c.first, c.second, c.asErr, o.Worktree, o.Reason)
		}
		if !exists(t, p) {
			t.Errorf("%s %s asErr=%v: worktree removed", c.first, c.second, c.asErr)
		}
	}
}

// TestForceRefusesEmbeddedSubmodule: a submodule added in place from an
// existing clone keeps its repository inside the worktree (no admin-dir
// modules/), so only `git submodule status` sees it; --force would delete
// that repository and a commit only it has. Refused.
func TestForceRefusesEmbeddedSubmodule(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	sub := filepath.Join(t.TempDir(), "sub")
	gitIn(t, filepath.Dir(sub), "init", "-q", "-b", "main", sub)
	gitIn(t, sub, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	p := f.Worktree("topic")
	gitIn(t, p, "clone", "-q", sub, "emb")
	gitIn(t, filepath.Join(p, "emb"), "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "only here")
	gitIn(t, p, "-c", "protocol.file.allow=always", "submodule", "add", sub, "emb")
	admin := gitIn(t, p, "rev-parse", "--path-format=absolute", "--git-dir")
	if exists(t, filepath.Join(admin, "modules")) {
		t.Fatal("fixture: an admin-dir modules/ exists; the test must reach the submodule status check")
	}
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	o := one(t, f.engine(s, f.inputs(), Options{}), approve(itemOf(row), true, false))
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "contains submodules") {
		t.Errorf("worktree %s reason %q; want refused: contains submodules", o.Worktree, o.Reason)
	}
	if !exists(t, filepath.Join(p, "emb", ".git")) {
		t.Error("the embedded submodule's repository was removed")
	}
}
