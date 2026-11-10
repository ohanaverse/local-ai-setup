package rootmove

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// fixture is a real repository: a bare "origin" (a local directory, never a
// real remote) and a clone with main pushed. Linked worktrees live under
// <repo>/.worktrees/ as wt places them. Each test builds its own, so one
// wrong success cannot leave a worktree that masks later cases.
type fixture struct {
	t      *testing.T
	Origin string
	Dir    string
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureExcluding(t, true)
}

// newFixtureExcluding is newFixture, with .worktrees/ excluded only when
// excludeWorktrees is set: a repository that does not ignore its own
// worktree directory is a state a real user can be in. The origin/repo pair
// is this test's clone of the shared gittest template for that lineage (the
// template holds the seed commit and the exclusion — git clone copies
// neither, so Clone carries the lineage's info/exclude into both), and
// Lineage gates the clone before any test body trusts it (Constraint 4).
func newFixtureExcluding(t *testing.T, excludeWorktrees bool) *fixture {
	t.Helper()
	tpl := gittest.Seed(t, gittest.SeedOpts{ExcludeWorktrees: excludeWorktrees})
	origin, repo := tpl.Clone(t)
	f := &fixture{t: t, Origin: origin, Dir: repo}
	gittest.Lineage(t, repo)
	return f
}

func (f *fixture) git(args ...string) string { f.t.Helper(); return gitIn(f.t, f.Dir, args...) }

func (f *fixture) exclude(pattern string) {
	f.t.Helper()
	ex := f.git("rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		f.t.Fatal(err)
	}
	fh, err := os.OpenFile(ex, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(pattern + "\n"); err != nil {
		f.t.Fatal(err)
	}
}

// Commit writes file in dir, commits it on dir's HEAD and returns the commit.
func (f *fixture) Commit(dir, file, content, msg string) string {
	f.t.Helper()
	f.write(dir, file, content)
	gitIn(f.t, dir, "add", file)
	gitIn(f.t, dir, "commit", "-m", msg)
	return gitIn(f.t, dir, "rev-parse", "HEAD")
}

// Worktree adds a linked worktree on a new branch under .worktrees/.
func (f *fixture) Worktree(branch string) string {
	f.t.Helper()
	p := filepath.Join(f.Dir, ".worktrees", strings.ReplaceAll(branch, "/", "-"))
	f.git("worktree", "add", "-b", branch, p)
	return p
}

// Detached adds a detached linked worktree at HEAD under .worktrees/name.
func (f *fixture) Detached(name string) string {
	f.t.Helper()
	p := filepath.Join(f.Dir, ".worktrees", name)
	f.git("worktree", "add", "--detach", p)
	return p
}

func (f *fixture) write(dir, file, content string) {
	f.t.Helper()
	p := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// later is a clock an hour ahead so the min-age rule stays out of the way.
func later() time.Time { return time.Now().Add(time.Hour) }

// The helpers below are rootmove's own (rescue's engine, snapshot and snap
// are rescue-shaped or read its unexported fingerprint).

// engine gathers and classifies the repository as `wt cleanup` does (no
// fetch), standing in the root, and returns a root-mode Engine. A repository
// with no default branch still gathers (cleanup returns the snapshot with
// ErrNoDefaultBranch), so Preflight can refuse it itself. The gh seam is
// answered by this call's own PRList hook (an empty listing), never a
// package stub; TestMain's poison is the forgetting-to-hook fallback.
func (f *fixture) engine() Engine {
	f.t.Helper()
	in := cleanup.GatherInputs{
		CWD:      f.Dir,
		OwnRepo:  "o/r",
		Parallel: 2,
		PRList:   func(string, string) ([]byte, error) { return []byte("[]"), nil },
	}
	snap, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, in)
	if err != nil && !errors.Is(err, cleanup.ErrNoDefaultBranch) {
		f.t.Fatalf("Gather: %v", err)
	}
	return Engine{Rescue: rescue.Engine{G: cleanup.Git{Dir: f.Dir}, Prior: snap, In: in, Classify: cleanup.Options{Now: later()}, ForRoot: true}}
}

// onBranch switches the root to a new branch name and commits one file on
// it (unpushed): the root state `wt restore-root` moves.
func (f *fixture) onBranch(name string) {
	f.t.Helper()
	f.git("switch", "-q", "-c", name)
	f.Commit(f.Dir, "work-"+strings.ReplaceAll(name, "/", "-")+".txt", "work", "work on "+name)
}

// dirty gives the root a staged new file, an unstaged edit of README and an
// untracked new.txt.
func (f *fixture) dirty() {
	f.t.Helper()
	f.write(f.Dir, "staged.txt", "s")
	f.git("add", "staged.txt")
	f.write(f.Dir, "README", "edited")
	f.write(f.Dir, "new.txt", "new")
}

// originAhead pushes n commits to origin/main from a second clone, then
// fetches them into the root, so origin/main is n commits ahead of main.
func (f *fixture) originAhead(n int) {
	f.t.Helper()
	other := filepath.Join(f.t.TempDir(), "other")
	if b, err := exec.Command("git", "clone", "-q", f.Origin, other).CombinedOutput(); err != nil {
		f.t.Fatalf("clone: %v\n%s", err, b)
	}
	gitIn(f.t, other, "config", "user.email", "o@example.com")
	gitIn(f.t, other, "config", "user.name", "O")
	gitIn(f.t, other, "config", "commit.gpgsign", "false")
	for i := range n {
		f.Commit(other, fmt.Sprintf("upstream-%d.txt", i), "u", fmt.Sprintf("upstream %d", i))
	}
	gitIn(f.t, other, "push", "-q", "origin", "HEAD:main")
	f.git("fetch", "-q", "origin")
}

// rootState is everything a refused Preflight must leave as it was: the
// refs, the registered worktrees and the names in .worktrees (rescue's
// world()), HEAD, the full status, and the raw index file bytes (a status
// that refreshed the stat cache would rewrite them).
// The raw index is read through gittest's probe, armed first — it guards the
// flake history of internal/cleanup/dirt_test.go's
// TestDirtCoveredRacyCleanEntry. A root with no index file (an unborn
// branch) arms nothing: "absent" staying "absent" already sees any write,
// which would create the file.
func (f *fixture) rootState() string {
	f.t.Helper()
	ents, _ := os.ReadDir(filepath.Join(f.Dir, ".worktrees"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	// Every read runs with GIT_OPTIONAL_LOCKS=0: a status that refreshed the
	// stat cache would rewrite the raw index this snapshot compares.
	soft := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", f.Dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		b, _ := cmd.CombinedOutput()
		return string(b)
	}
	raw := gittest.RawIndex(f.t, f.Dir)
	if raw != "absent" {
		gittest.ArmIndexProbe(f.t, f.Dir)
		gittest.AssertProbeArmed(f.t, f.Dir)
		raw = gittest.RawIndex(f.t, f.Dir)
	}
	return strings.Join([]string{
		soft("for-each-ref"),
		soft("worktree", "list", "--porcelain"),
		strings.Join(names, ","),
		soft("symbolic-ref", "-q", "HEAD"),
		soft("rev-parse", "-q", "--verify", "HEAD"),
		soft("status", "--porcelain=v2", "-z", "--untracked-files=all"),
		raw,
	}, "\n--\n")
}

// hook installs an executable hooks/<name> in the root's repository that
// touches marker when it runs. The file is the one hand-built .git shape in
// this package, so it round-trips through the real git read the engine makes
// (rescue's postCheckoutHookWarning resolves `rev-parse --git-path
// hooks/<name>` and stats the path): a shape git resolves elsewhere would make
// the warning tests exercise git's error path instead of the engine's read.
func (f *fixture) hook(name, marker string) {
	f.t.Helper()
	p := f.git("rev-parse", "--path-format=absolute", "--git-path", "hooks/"+name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	gittest.RoundTrip(f.t, f.Dir, p+"\n", "rev-parse", "--path-format=absolute", "--git-path", "hooks/"+name)
}
