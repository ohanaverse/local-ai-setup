package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// rescueApp is a test app with the classification clock an hour ahead (so the
// fixture's seconds-old branches clear the min-age rule) and no terminal.
func rescueApp(t *testing.T) *app {
	t.Helper()
	a, tmp := newTestApp(t)
	t.Setenv("WT_REGISTRY", filepath.Join(tmp, "local-ai", "registry.toml"))
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(tmp, "config.yaml"))
	stubCleanupGH(t)
	oldNow, oldTTY := removeNow, rescueTTY
	removeNow = func() time.Time { return time.Now().Add(time.Hour) }
	rescueTTY = func(io.Reader, io.Writer) bool { return false }
	t.Cleanup(func() { removeNow, rescueTTY = oldNow, oldTTY })
	return a
}

// runRescue runs `wt rescue args...` from dir with stdin as the input.
func runRescue(t *testing.T, a *app, dir, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	cmd := rescueCmd(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{}, args...))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// workWorktree adds a worktree on "spike" with one unpushed commit, a staged
// change, an unstaged change and an untracked file, and returns its path.
func workWorktree(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(dir), "wt-spike")
	cgit(t, dir, "worktree", "add", "-b", "spike", p)
	cleanupCommit(t, p, "spike.txt", "spike work")
	if err := os.WriteFile(filepath.Join(p, "staged.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	cgit(t, p, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(p, "spike.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// sourceState is a worktree's HEAD, index bytes and a status read with
// optional locks off, plus the hash of every file: what must not change.
func sourceState(t *testing.T, dir string) string {
	t.Helper()
	run := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		b, err := c.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(b)
	}
	idx := strings.TrimSpace(run("rev-parse", "--path-format=absolute", "--git-path", "index"))
	b, _ := os.ReadFile(idx)
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(filepath.Base(p), ".git") {
			c, _ := os.ReadFile(p)
			fmt.Fprintf(h, "%s %x\n", p, sha256.Sum256(c))
		}
		return nil
	})
	return run("rev-parse", "HEAD") + fmt.Sprintf("%x\n", sha256.Sum256(b)) + run("status", "--porcelain=v2", "-z") + fmt.Sprintf("%x", h.Sum(nil))
}

// repoWorld is the refs and registered worktrees: what a refused or dry run
// rescue must leave alone.
func repoWorld(t *testing.T, dir string) string {
	t.Helper()
	return cgit(t, dir, "for-each-ref") + "\n" + cgit(t, dir, "worktree", "list", "--porcelain")
}

func decodeRescue(t *testing.T, stdout string) rescue.Envelope {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var env rescue.Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not one envelope: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one document:\n%s", stdout)
	}
	return env
}

// TestRescueCmdCopiesTheWorkAndLeavesTheSource checks the whole path with
// --yes: exit 0, a new branch and worktree at the source's HEAD carrying the
// staged, unstaged and untracked work, the JSON says moved, and the source's
// HEAD, index and files are byte-identical afterwards. Why: this is the
// command's reason to exist, and "source untouched" is its promise.
func TestRescueCmdCopiesTheWorkAndLeavesTheSource(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	src := workWorktree(t, dir)
	head := cgit(t, src, "rev-parse", "HEAD")
	before := sourceState(t, src)

	stdout, stderr, err := runRescue(t, a, dir, "", "spike", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	env := decodeRescue(t, stdout)
	if env.Outcome != "moved" || !env.Created || env.Destination == nil || env.Destination.Branch != "rescued/spike" {
		t.Fatalf("envelope = %+v", env)
	}
	dest := env.Destination.Path
	if cgit(t, dest, "rev-parse", "HEAD") != head || env.Destination.Start != head {
		t.Errorf("destination HEAD %s, want %s", cgit(t, dest, "rev-parse", "HEAD"), head)
	}
	for file, want := range map[string]string{"spike.txt": "edited", "staged.txt": "s", "new.txt": "n"} {
		if b, _ := os.ReadFile(filepath.Join(dest, file)); string(b) != want {
			t.Errorf("%s = %q, want %q", file, b, want)
		}
	}
	if c := env.Carried; c == nil || c.Commits != 1 || c.Staged != 1 || c.Unstaged != 1 || c.Untracked != 1 {
		t.Errorf("carried = %+v", env.Carried)
	}
	if after := sourceState(t, src); after != before {
		t.Error("the source changed")
	}
	if s := cgit(t, src, "symbolic-ref", "HEAD"); s != "refs/heads/spike" {
		t.Errorf("the source's branch is now %s", s)
	}
}

// TestRescueCmdDryRunChangesNothing checks --dry-run needs no --yes and no
// terminal, prints would_move with the plan and exits 0, and creates no ref,
// worktree or file. Why: it is the preview a user (or script) runs first.
func TestRescueCmdDryRunChangesNothing(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	src := workWorktree(t, dir)
	world, before := repoWorld(t, dir), sourceState(t, src)

	stdout, _, err := runRescue(t, a, dir, "", "spike", "--dry-run", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRescue(t, stdout)
	if env.Outcome != "would_move" || !env.DryRun || env.Created || env.Destination == nil {
		t.Fatalf("envelope = %+v", env)
	}
	if repoWorld(t, dir) != world || sourceState(t, src) != before {
		t.Error("a dry run changed the repository")
	}
	if exists(env.Destination.Path) {
		t.Error("a dry run created the destination")
	}
	text, _, err := runRescue(t, a, dir, "", "spike", "--dry-run", "--no-fetch")
	if err != nil || !strings.Contains(text, "dry run: nothing was changed") || !strings.Contains(text, "rescued/spike") {
		t.Errorf("text dry run = %q, %v", text, err)
	}
}

// TestRescueCmdApproval checks the three approval outcomes: no --yes and no
// terminal is exit 3 with nothing created; a terminal and "y" copies; a
// terminal and anything else is exit 3 with nothing created. The prompt lists
// the branch, start and counts. Why: the command creates a branch and a
// directory the user did not name, so it asks, and a script must not hang.
func TestRescueCmdApproval(t *testing.T) {
	t.Run("no terminal", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		workWorktree(t, dir)
		world := repoWorld(t, dir)

		_, _, err := runRescue(t, a, dir, "", "spike", "--no-fetch")

		if exitOf(err) != 3 || !strings.Contains(err.Error(), "nothing was created") || repoWorld(t, dir) != world {
			t.Fatalf("err = %v (exit %d)", err, exitOf(err))
		}
	})
	t.Run("yes", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		workWorktree(t, dir)
		rescueTTY = func(io.Reader, io.Writer) bool { return true }

		out, errOut, err := runRescue(t, a, dir, "y\n", "spike", "--no-fetch")

		if exitOf(err) != 0 || !strings.Contains(errOut, "rescued/spike") || !strings.Contains(errOut, "1 commit, 1 staged, 1 unstaged, 1 untracked file") || !strings.Contains(out, "copied") {
			t.Fatalf("exit %d\nprompt: %s\nout: %s", exitOf(err), errOut, out)
		}
		if !hasBranch(t, dir, "rescued/spike") {
			t.Error("the branch was not created")
		}
	})
	t.Run("declined", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		workWorktree(t, dir)
		rescueTTY = func(io.Reader, io.Writer) bool { return true }
		world := repoWorld(t, dir)

		_, _, err := runRescue(t, a, dir, "\n", "spike", "--no-fetch")

		if exitOf(err) != 3 || repoWorld(t, dir) != world {
			t.Fatalf("err = %v (exit %d)", err, exitOf(err))
		}
	})
}

// TestRescueCmdRefusalsExit2 checks a refusal prints its code, creates nothing
// and exits 2: a clean worktree, a branch with no worktree, and a taken name
// (which names --to). Why: scripts branch on the exit status and the code.
func TestRescueCmdRefusalsExit2(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, dir string) (target string, extra []string)
		code  string
	}{
		{"nothing to keep", func(t *testing.T, dir string) (string, []string) {
			p := filepath.Join(filepath.Dir(dir), "wt-clean")
			cgit(t, dir, "worktree", "add", "-b", "clean", p)
			return "clean", nil
		}, "nothing_to_keep"},
		{"branch without worktree", func(t *testing.T, dir string) (string, []string) {
			uniqueBranch(t, dir, "lone")
			return "lone", nil
		}, "not_worktree"},
		{"name taken", func(t *testing.T, dir string) (string, []string) {
			workWorktree(t, dir)
			cgit(t, dir, "branch", "rescued/spike")
			return "spike", nil
		}, "name_unavailable"},
		{"primary", func(t *testing.T, dir string) (string, []string) {
			if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir, nil
		}, "primary"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := rescueApp(t)
			dir := cleanupRepo(t)
			target, _ := c.setup(t, dir)
			world := repoWorld(t, dir)

			stdout, _, err := runRescue(t, a, dir, "", target, "--yes", "--json", "--no-fetch")

			if exitOf(err) != 2 {
				t.Fatalf("exit %d\n%s", exitOf(err), stdout)
			}
			env := decodeRescue(t, stdout)
			if env.Outcome != "refused" || env.Code != c.code || env.Created || !strings.Contains(env.Reason, "nothing was created") {
				t.Errorf("envelope = %+v", env)
			}
			if c.code == "name_unavailable" && !strings.Contains(env.Reason, "--to") {
				t.Errorf("reason does not name --to: %s", env.Reason)
			}
			if repoWorld(t, dir) != world {
				t.Error("a refusal changed the repository")
			}
		})
	}
}

// TestRescueCmdToNamesTheBranch checks --to is used as the full branch name
// and the worktree directory is derived from it. Why: the default name can
// collide or be unwanted, and --to is the only way out of a name_unavailable.
func TestRescueCmdToNamesTheBranch(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	workWorktree(t, dir)

	stdout, _, err := runRescue(t, a, dir, "", "spike", "--to", "keep/spike", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRescue(t, stdout)
	if env.Destination.Branch != "keep/spike" || filepath.Base(env.Destination.Path) != "keep-spike" {
		t.Errorf("destination = %+v", env.Destination)
	}
}

// TestRescueCmdDetachedWorktree checks a detached worktree with commits is
// named by its path, copied to rescued/<directory> and the branch holds the
// commits. Why: detached work has no branch name to find it by, so this is
// the case the whole feature serves.
func TestRescueCmdDetachedWorktree(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	p := filepath.Join(filepath.Dir(dir), "scratch")
	cgit(t, dir, "worktree", "add", "--detach", p)
	cleanupCommit(t, p, "a.txt", "a")
	cleanupCommit(t, p, "b.txt", "b")
	head := cgit(t, p, "rev-parse", "HEAD")

	stdout, _, err := runRescue(t, a, dir, "", p, "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRescue(t, stdout)
	if env.Destination.Branch != "rescued/scratch" || cgit(t, dir, "rev-parse", "refs/heads/rescued/scratch") != head || env.Source.Ref != "" {
		t.Errorf("envelope = %+v", env)
	}
}

// TestRescueCmdUsageErrorsExit1 checks the argument errors: none, two, no
// match, a bad --min-age, each exit 1 with nothing created and, under --json,
// the one error document. Why: "could not start" must read differently from
// "refused" to a script.
func TestRescueCmdUsageErrorsExit1(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	workWorktree(t, dir)
	world := repoWorld(t, dir)
	for name, args := range map[string][]string{
		"no target":    {"--json"},
		"two targets":  {"spike", "other", "--json"},
		"no match":     {"nowhere", "--json", "--no-fetch"},
		"bad min-age":  {"spike", "--min-age", "5s", "--json", "--no-fetch"},
		"empty target": {"", "--json", "--no-fetch"},
	} {
		stdout, _, err := runRescue(t, a, dir, "", args...)
		if exitOf(err) != 1 {
			t.Errorf("%s: exit %d", name, exitOf(err))
			continue
		}
		if env := decodeRescue(t, stdout); env.Error == nil || env.Outcome != "" {
			t.Errorf("%s: envelope = %+v", name, env)
		}
	}
	if repoWorld(t, dir) != world {
		t.Error("a usage error changed the repository")
	}
}

// TestRescueCmdRegisteredOnRoot checks `wt rescue` is a subcommand of the
// root command. Why: the file defines the command; only main wires it.
func TestRescueCmdRegisteredOnRoot(t *testing.T) {
	newTestApp(t)
	c, _, err := rootCmd().Find([]string{"rescue"})
	if err != nil || c.Name() != "rescue" {
		t.Fatalf("Find(rescue) = %v, %v", c, err)
	}
}

// TestRescueContainsNoDeletion checks neither the rescue engine nor its
// command holds a deletion shape (the same banned substrings as the cleanup
// check). Why: the rescue only creates; internal/removal is the only deleter
// and TestDeletionStringsOnlyInRemovalPackage also reads these files.
func TestRescueContainsNoDeletion(t *testing.T) {
	files, _ := filepath.Glob("../../internal/rescue/*.go")
	more, _ := filepath.Glob("rescue*.go")
	files = append(files, more...)
	banned := []string{"worktree remove", `"worktree", "remove"`, `"worktree", "prune"`, "update-ref", `branch", "-d`, `branch", "-D`, "--delete", "os.RemoveAll", "os.Remove(", "os.Rename(",
		`":"+`, `":" +`, `"push"`, "-X DELETE"}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range banned {
			if strings.Contains(string(b), s) {
				t.Errorf("%s contains %q", f, s)
			}
		}
	}
	if scanned < 6 {
		t.Errorf("scanned only %d sources", scanned)
	}
}

// TestCleanupListGetsTheRescue checks the tick list is handed a Rescue over
// this repository's snapshot, also when removal is off for want of a registry:
// called with a listed row it asks on the given streams, copies the work and
// prints the result. Why: `r` must reach the same executor as `wt rescue`,
// and a registry problem (which only matters for removing) must not take it away.
func TestCleanupListGetsTheRescue(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	workWorktree(t, dir)
	oldTTY, oldList, oldStreams := isTerminalWriter, runCleanupList, rescueStreams
	isTerminalWriter = func(io.Writer) bool { return true }
	var got cleanup.Report
	var rm tui.RemoveFuncs
	runCleanupList = func(r cleanup.Report, _ themes.Theme, f tui.RemoveFuncs) error { got, rm = r, f; return nil }
	var out, errOut bytes.Buffer
	rescueStreams = func() (io.Reader, io.Writer, io.Writer) { return strings.NewReader("y\n"), &out, &errOut }
	t.Cleanup(func() { isTerminalWriter, runCleanupList, rescueStreams = oldTTY, oldList, oldStreams })
	if _, _, err := runCleanup(t, a, dir, "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	if rm.Rescue == nil {
		t.Fatal("the list was not handed a Rescue")
	}
	if off := cleanupRemoveFuncs(nil, cleanup.Snapshot{}, cleanup.GatherInputs{}, 0, errRegistryUnavailable); off.Rescue == nil {
		t.Error("removal off took the rescue away")
	}
	var row cleanup.Row
	for _, r := range got.Rows {
		if r.Kind == cleanup.KindWorktree && r.Name == "spike" {
			row = r
		}
	}
	rescueTTY = func(io.Reader, io.Writer) bool { return true }

	if err := rm.Rescue(row); err != nil {
		t.Fatalf("Rescue: %v\n%s", err, errOut.String())
	}

	if !hasBranch(t, dir, "rescued/spike") || !strings.Contains(out.String(), "copied") || !strings.Contains(errOut.String(), "Copy it? [y/N]") {
		t.Errorf("out %q err %q", out.String(), errOut.String())
	}
}
