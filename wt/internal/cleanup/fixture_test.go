package cleanup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a real repository: a bare "origin", a clone with main pushed and
// origin/HEAD set. Each test builds its own.
type fixture struct {
	t      *testing.T
	Origin string
	Dir    string
	n      int
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

func newFixture(t *testing.T) *fixture {
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
	f.Commit("README", "init", "init")
	f.git("push", "-u", "origin", "main")
	f.git("remote", "set-head", "origin", "main")
	return f
}

func (f *fixture) git(args ...string) string { f.t.Helper(); return gitIn(f.t, f.Dir, args...) }

// Commit writes file, commits it on the current branch and returns the sha.
func (f *fixture) Commit(file, content, msg string) string {
	f.t.Helper()
	p := filepath.Join(f.Dir, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", file)
	f.git("commit", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

// Branch creates name at HEAD without switching.
func (f *fixture) Branch(name string) { f.t.Helper(); f.git("branch", name) }

// Checkout switches the primary checkout to name.
func (f *fixture) Checkout(name string) { f.t.Helper(); f.git("checkout", name) }

// Push pushes ref to origin, setting upstream.
func (f *fixture) Push(ref string) { f.t.Helper(); f.git("push", "-u", "origin", ref) }

// Squash squash-merges branch into main as one new commit and pushes main.
func (f *fixture) Squash(branch string) {
	f.t.Helper()
	f.git("checkout", "main")
	f.git("merge", "--squash", branch)
	f.git("commit", "-m", "squash "+branch)
	f.git("push", "origin", "main")
}

// Revert reverts sha on the current branch.
func (f *fixture) Revert(sha string) { f.t.Helper(); f.git("revert", "--no-edit", sha) }

// Worktree adds a linked worktree on a new branch and returns its path.
func (f *fixture) Worktree(branch string) string {
	f.t.Helper()
	f.n++
	p := filepath.Join(filepath.Dir(f.Dir), "wt-"+strings.ReplaceAll(branch, "/", "-"))
	f.git("worktree", "add", "-b", branch, p)
	return p
}

// Dirty leaves an uncommitted untracked file at path (relative to the repo).
func (f *fixture) Dirty(path string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, path), []byte("dirty"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// Ignored creates a git-ignored file at path, ignored through info/exclude.
func (f *fixture) Ignored(path string) {
	f.t.Helper()
	ex := filepath.Join(f.Dir, ".git", "info", "exclude")
	fh, err := os.OpenFile(ex, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(path + "\n"); err != nil {
		f.t.Fatal(err)
	}
	f.Dirty(path)
}
