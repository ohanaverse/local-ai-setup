package rescue

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
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
// worktree directory is a state a real user can be in.
func newFixtureExcluding(t *testing.T, excludeWorktrees bool) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, Origin: filepath.Join(root, "origin.git"), Dir: filepath.Join(root, "repo")}
	if b, err := exec.Command("git", "init", "--bare", "-b", "main", f.Origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "clone", f.Origin, f.Dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
	f.git("config", "user.email", "t@example.com")
	f.git("config", "user.name", "T")
	f.git("config", "commit.gpgsign", "false")
	f.git("checkout", "-B", "main")
	if excludeWorktrees {
		f.exclude(".worktrees/")
	}
	f.Commit(f.Dir, "README", "init", "init")
	f.git("push", "-u", "origin", "main")
	f.git("remote", "set-head", "origin", "main")
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

// noGh stubs the gh seam with an answered, empty listing.
func noGh(t *testing.T) {
	t.Helper()
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
}

// engine gathers and classifies the repository as `wt cleanup` does (no
// fetch) and returns an Engine and a function that finds the worktree row at
// path. cwd is the directory the caller stands in.
func (f *fixture) engine(cwd string) (Engine, func(path string) cleanup.Row) {
	f.t.Helper()
	noGh(f.t)
	in := cleanup.GatherInputs{CWD: cwd, OwnRepo: "o/r", Parallel: 2}
	snap, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, in)
	if err != nil {
		f.t.Fatalf("Gather: %v", err)
	}
	opts := cleanup.Options{Now: later()}
	rep := cleanup.Classify(snap, opts)
	eng := Engine{G: cleanup.Git{Dir: f.Dir}, Prior: snap, In: in, Classify: opts}
	return eng, func(path string) cleanup.Row {
		f.t.Helper()
		for _, r := range rep.Rows {
			if r.Kind == cleanup.KindWorktree && cleanup.ResolvePath(r.Path) == cleanup.ResolvePath(path) {
				return r
			}
		}
		f.t.Fatalf("no worktree row for %s", path)
		return cleanup.Row{}
	}
}

// rootOnTopic switches the primary checkout to a new branch "topic" and
// commits work.txt on it (unpushed): the root state `wt restore-root` moves.
func (f *fixture) rootOnTopic() {
	f.t.Helper()
	f.git("switch", "-q", "-c", "topic")
	f.Commit(f.Dir, "work.txt", "work", "work")
}

// rootEngine is engine(f.Dir) in root mode, with the primary checkout's row.
func (f *fixture) rootEngine() (Engine, cleanup.Row) {
	f.t.Helper()
	eng, find := f.engine(f.Dir)
	eng.ForRoot = true
	return eng, find(f.Dir)
}

// rootInto is root mode up to the copy: the plan Preflight makes for the
// primary checkout and the detached destination worktree.CreateDetachedAt
// makes for it, as rootmove will call them.
func (f *fixture) rootInto() (Engine, *Plan, string) {
	f.t.Helper()
	eng, row := f.rootEngine()
	plan, err := eng.Preflight(row, "")
	if err != nil {
		f.t.Fatalf("Preflight: %v", err)
	}
	dest, err := worktree.CreateDetachedAt(f.Dir, plan.DirName, plan.Branch, plan.Start)
	if err != nil {
		f.t.Fatalf("CreateDetachedAt: %v", err)
	}
	return eng, plan, dest
}

// dirtyRoot gives the root (on topic) a staged new file, an unstaged edit, a
// tracked file made executable, an untracked executable script and a nested
// untracked file.
func (f *fixture) dirtyRoot() {
	f.t.Helper()
	f.write(f.Dir, "staged.txt", "s")
	f.git("add", "staged.txt")
	f.write(f.Dir, "README", "edited")
	if err := os.Chmod(filepath.Join(f.Dir, "work.txt"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.write(f.Dir, "run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(f.Dir, "run.sh"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.write(f.Dir, "deep/er/note.txt", "note")
}

// world is everything outside the source that a failed or refused rescue
// must leave alone: the refs, the registered worktrees and the files in
// .worktrees.
func (f *fixture) world() string {
	f.t.Helper()
	ents, _ := os.ReadDir(filepath.Join(f.Dir, ".worktrees"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return f.git("for-each-ref") + "\n" + f.git("worktree", "list", "--porcelain") + "\n" + strings.Join(names, ",")
}

// snap is a fingerprint plus the index file's raw bytes, which the engine's
// own fingerprint ignores (a stat-cache refresh rewrites them) but the engine
// itself must never cause to change.
type snap struct {
	fingerprint
	raw string
}

// snapshot is the source's fingerprint, taken the way the engine takes it,
// with the raw index bytes beside it.
func snapshot(t *testing.T, src string) snap {
	t.Helper()
	fp, err := takeFingerprint(sgit{dir: src})
	if err != nil {
		t.Fatal(err)
	}
	raw := "absent"
	if idx := gitIn(t, src, "rev-parse", "--path-format=absolute", "--git-path", "index"); idx != "" {
		if b, err := os.ReadFile(idx); err == nil {
			raw = string(b)
		}
	}
	return snap{fp, raw}
}

// diff names what differs ("" none), including the raw index bytes.
func (a snap) diff(b snap) string {
	if d := a.fingerprint.diff(b.fingerprint); d != "" {
		return d
	}
	if a.raw != b.raw {
		return "the raw index file"
	}
	return ""
}

// execGit is a git command in dir, for tests that feed stdin or expect failure.
func execGit(dir string, args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", dir}, args...)...)
}
