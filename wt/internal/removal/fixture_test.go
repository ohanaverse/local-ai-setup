package removal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// fixture is a real repository: a bare "origin" and a clone with main pushed
// and origin/HEAD set. Linked worktrees go under <repo>/.worktrees/, as wt
// places them; .worktrees/ is in info/exclude so the primary checkout stays
// clean. Each test builds its own (a wrong success leaves a worktree behind
// that would mask every later case).
//
// It is a slim copy of internal/cleanup's fixture (plan call 9: duplicate,
// extract later), holding only what the removal tests need.
type fixture struct {
	t      *testing.T
	Origin string
	Dir    string
}

// gitIn runs git in dir and fails the test on a non-zero exit.
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
	f.exclude(".worktrees/")
	f.Commit(f.Dir, "README", "init", "init")
	f.git("push", "-u", "origin", "main")
	f.git("remote", "set-head", "origin", "main")
	return f
}

func (f *fixture) git(args ...string) string { f.t.Helper(); return gitIn(f.t, f.Dir, args...) }

// exclude appends pattern to the repo's info/exclude, which every worktree of
// the repo shares.
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

// Commit writes file in dir (the primary checkout or a linked worktree),
// commits it on dir's current HEAD and returns the commit id.
func (f *fixture) Commit(dir, file, content, msg string) string {
	f.t.Helper()
	f.write(dir, file, content)
	gitIn(f.t, dir, "add", file)
	gitIn(f.t, dir, "commit", "-m", msg)
	return gitIn(f.t, dir, "rev-parse", "HEAD")
}

// Branch creates name at the primary checkout's HEAD without switching.
func (f *fixture) Branch(name string) { f.t.Helper(); f.git("branch", name) }

// Push pushes branch to origin from the primary repository, setting upstream.
func (f *fixture) Push(branch string) { f.t.Helper(); f.git("push", "-u", "origin", branch) }

// Worktree adds a linked worktree on a new branch at <repo>/.worktrees/<flat
// branch name> and returns its path.
func (f *fixture) Worktree(branch string) string {
	f.t.Helper()
	p := filepath.Join(f.Dir, ".worktrees", strings.ReplaceAll(branch, "/", "-"))
	f.git("worktree", "add", "-b", branch, p)
	return p
}

// Dirty leaves an untracked file at file relative to dir.
func (f *fixture) Dirty(dir, file string) { f.t.Helper(); f.write(dir, file, "dirty") }

// Ignored creates a git-ignored file at file relative to dir: the pattern goes
// to info/exclude (a gitignore entry every worktree shares, so the entry
// itself is not an untracked file).
func (f *fixture) Ignored(dir, file string) {
	f.t.Helper()
	f.exclude(file)
	f.write(dir, file, "ignored")
}

// Lock locks the linked worktree at path.
func (f *fixture) Lock(path string) {
	f.t.Helper()
	f.git("worktree", "lock", "--reason", "fixture", path)
}

// Switch switches the checkout at dir to the existing branch.
func (f *fixture) Switch(dir, branch string) { f.t.Helper(); gitIn(f.t, dir, "switch", branch) }

// SquashMerge squash-merges branch into main as one new commit in the primary
// checkout and pushes main, as a forge's squash merge does.
func (f *fixture) SquashMerge(branch string) string {
	f.t.Helper()
	f.git("checkout", "main")
	f.git("merge", "--squash", branch)
	f.git("commit", "-m", "squash "+branch)
	f.git("push", "origin", "main")
	return f.git("rev-parse", "HEAD")
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

// later is a clock an hour ahead: every branch the fixture made is older than
// cleanup.DefaultMinAge by then, so the min-age rule stays out of a case's way.
func later() time.Time { return time.Now().Add(time.Hour) }

// noGh stubs the gh pr list seam with an answered, empty listing for every
// branch, for the rest of the test.
func noGh(t *testing.T) {
	t.Helper()
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
}

// inputs are the GatherInputs a test's Engine and report use: the primary
// checkout as the caller's directory, own repo o/r, no fetch.
func (f *fixture) inputs() cleanup.GatherInputs {
	return cleanup.GatherInputs{CWD: f.Dir, OwnRepo: "o/r", Parallel: 2, OriginRows: true}
}

// report gathers and classifies the repository the way `wt cleanup` does
// (Gather, then Classify) with the clock at later(): what the user saw. The
// caller stubs gh first.
func (f *fixture) report() (cleanup.Snapshot, cleanup.Report) {
	f.t.Helper()
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, f.inputs())
	if err != nil {
		f.t.Fatalf("Gather: %v", err)
	}
	return s, cleanup.Classify(s, cleanup.Options{Now: later()})
}

// row returns the report's one row of kind k named name.
func (f *fixture) row(r cleanup.Report, k cleanup.Kind, name string) cleanup.Row {
	f.t.Helper()
	var got []cleanup.Row
	var all []string
	for _, row := range r.Rows {
		all = append(all, fmt.Sprintf("%s %q status=%s", row.Kind, row.Name, row.Status))
		if row.Kind == k && row.Name == name {
			got = append(got, row)
		}
	}
	if len(got) != 1 {
		f.t.Fatalf("%d rows of kind %s named %q, want 1; rows: %v", len(got), k, name, all)
	}
	return got[0]
}

// itemOf is the Item a user ticking row and confirming it would hand the
// engine: what they saw (status, and the tip: the branch tip, or HEAD for a
// detached worktree) and the typed confirmation (Approved.Confirm), no
// flag. Tests of the confirmation itself build Approved explicitly.
func itemOf(row cleanup.Row) Item {
	tip := row.Tip
	if tip == "" {
		tip = row.Head
	}
	return seal(Item{Kind: row.Kind, Path: row.Path, Ref: row.Ref, Name: row.Name, Saw: row.Status, SawTip: tip, SawLosses: row.Losses,
		Approved: Needs{Confirm: true}})
}

// approve is it confirmed with this item's own --force and -D approvals
// (Ruling K: the flags the old run-wide Options carried), re-sealed.
func approve(it Item, force, del bool) Item {
	it.Approved = Needs{Confirm: true, Force: force, Delete: del}
	return seal(it)
}

// seal is it sealed as it stands, as Confirmed seals what it builds. Tests
// use it to hand the engine items Confirmed would not build (to test the
// engine's own checks behind the seal); production code can only seal
// through ItemPreview.Confirmed. Tests of the seal itself use the real
// Preview -> Confirmed -> Run path.
func seal(it Item) Item {
	it.seal = sealOf(it)
	return it
}
