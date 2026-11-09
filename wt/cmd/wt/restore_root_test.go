package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
)

// restoreRootApp is a test app with the classification clock an hour ahead
// (so the fixture's seconds-old branches clear the min-age rule) and no
// terminal: rescueApp for `wt restore-root`.
func restoreRootApp(t *testing.T) *app {
	t.Helper()
	a, tmp := newTestApp(t)
	t.Setenv("WT_REGISTRY", filepath.Join(tmp, "local-ai", "registry.toml"))
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(tmp, "config.yaml"))
	stubCleanupGH(t)
	oldNow, oldTTY, oldAfter := removeNow, restoreRootTTY, afterRestoreRootConfirm
	removeNow = func() time.Time { return time.Now().Add(time.Hour) }
	restoreRootTTY = func(io.Reader, io.Writer) bool { return false }
	t.Cleanup(func() { removeNow, restoreRootTTY, afterRestoreRootConfirm = oldNow, oldTTY, oldAfter })
	return a
}

// runRestoreRoot runs `wt restore-root args...` from dir with stdin as the
// input.
func runRestoreRoot(t *testing.T, a *app, dir, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	cmd := restoreRootCmd(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{}, args...))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func decodeRestoreRoot(t *testing.T, stdout string) rootmove.Envelope {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var env rootmove.Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not one envelope: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one document:\n%s", stdout)
	}
	return env
}

// excludeWorktrees makes repo ignore .worktrees/ locally, as the command
// requires.
func excludeWorktrees(t *testing.T, repo string) {
	t.Helper()
	appendExclude(t, repo, ".worktrees/")
}

func appendExclude(t *testing.T, repo, pattern string) {
	t.Helper()
	p := cgit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(pattern + "\n"); err != nil {
		t.Fatal(err)
	}
}

// rootOnTopic puts the root of repo on a new branch "topic" with two
// unpushed commits, a staged new file, an unstaged edit of README and an
// untracked file, and ignores .worktrees/: the state `wt restore-root`
// moves.
func rootOnTopic(t *testing.T, repo string) {
	t.Helper()
	excludeWorktrees(t, repo)
	cgit(t, repo, "switch", "-q", "-c", "topic")
	cleanupCommit(t, repo, "one.txt", "one")
	cleanupCommit(t, repo, "two.txt", "two")
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	cgit(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// postCheckoutHook installs an executable post-checkout hook in repo that
// would create a marker file if it ran, and returns the marker's path.
func postCheckoutHook(t *testing.T, repo string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := cgit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "hooks/post-checkout")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return marker
}

// pushFromSecondClone moves origin/main one commit ahead from another clone,
// leaving repo's origin/main behind until it fetches.
func pushFromSecondClone(t *testing.T, repo string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "second")
	cgit(t, filepath.Dir(repo), "clone", "-q", filepath.Join(filepath.Dir(repo), "origin.git"), other)
	cgit(t, other, "config", "user.email", "t@example.com")
	cgit(t, other, "config", "user.name", "T")
	cgit(t, other, "config", "commit.gpgsign", "false")
	cleanupCommit(t, other, "remote-only.txt", "moved on")
	cgit(t, other, "push", "-q", "origin", "main")
	return cgit(t, other, "rev-parse", "HEAD")
}

// liveClaim claims the root of repo for another live process (a sleep the
// test kills afterwards): a running wt session.
func liveClaim(t *testing.T, repo string) {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Unix()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	dir := filepath.Join(cgit(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=topic\nagent=claude\n", pid, started, cleanup.ResolvePath(repo))
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rootState is sourceState for the root: its HEAD, index bytes, status and
// the hash of every file outside .git. sourceState would walk into the
// root's .git directory, where `wt cleanup`'s merge proof may leave an
// unreachable loose object (git gc collects it); that is no ref, index or
// working file.
func rootState(t *testing.T, repo string) string {
	t.Helper()
	run := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		b, err := c.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return string(b)
	}
	idx, _ := os.ReadFile(filepath.Join(repo, ".git", "index"))
	h := sha256.New()
	filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case !d.IsDir():
			c, _ := os.ReadFile(p)
			fmt.Fprintf(h, "%s %x\n", p, sha256.Sum256(c))
		}
		return nil
	})
	return run("rev-parse", "HEAD") + fmt.Sprintf("%x\n", sha256.Sum256(idx)) + run("status", "--porcelain=v2", "-z") + fmt.Sprintf("%x", h.Sum(nil))
}

// rootBranch is the root's current branch, short name.
func rootBranch(t *testing.T, repo string) string {
	t.Helper()
	return cgit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
}

// TestRestoreRootCmdRegisteredOnRoot checks `wt restore-root` is a
// subcommand of the root command. Why: the file defines the command; only
// main wires it.
func TestRestoreRootCmdRegisteredOnRoot(t *testing.T) {
	newTestApp(t)
	c, _, err := rootCmd().Find([]string{"restore-root"})
	if err != nil || c == nil || c.Name() != "restore-root" {
		t.Fatalf("Find(restore-root) = %v, %v", c, err)
	}
}

// TestRestoreRootCmdDryRunChangesNothing checks --dry-run --json: exit 0,
// would_move, the default_ff verdict reported, the no-fetch note, and no
// ref, worktree, index or file changed (origin/main stays stale: no fetch).
// Why: a dry run promises to change nothing, fetch included.
func TestRestoreRootCmdDryRunChangesNothing(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	// origin/main one commit ahead of main locally, so default_ff is true.
	pushFromSecondClone(t, dir)
	cgit(t, dir, "fetch", "-q", "origin")
	world, before := repoWorld(t, dir), rootState(t, dir)

	stdout, _, err := runRestoreRoot(t, a, dir, "", "--dry-run", "--json")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Outcome != "would_move" || !env.DryRun || env.Created || env.Branch != "topic" || env.Default != "main" || !env.DefaultFF {
		t.Fatalf("envelope = %+v", env)
	}
	if !strings.HasSuffix(env.DestPath, "/.worktrees/topic") || exists(env.DestPath) {
		t.Errorf("dest_path %q (exists %v), want an absent .worktrees/topic", env.DestPath, exists(env.DestPath))
	}
	if !strings.Contains(strings.Join(env.Warnings, "\n"), dryRunFetchNote) {
		t.Errorf("warnings %q lack the no-fetch note", env.Warnings)
	}
	if repoWorld(t, dir) != world {
		t.Error("a dry run changed refs or worktrees")
	}
	if after := rootState(t, dir); after != before {
		t.Errorf("a dry run changed the root:\n%s\n--- was ---\n%s", after, before)
	}
}

// TestRestoreRootCmdApproval checks the three approval outcomes: no --yes and
// no terminal is exit 3 with nothing changed; a terminal and "y" moves, after
// a prompt on stderr naming the counts, the destination and `Move?`; a
// terminal and an empty answer is exit 3 with nothing changed. Why: the
// command resets the root, so it asks, and a script must not hang.
func TestRestoreRootCmdApproval(t *testing.T) {
	t.Run("no terminal", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		world, before := repoWorld(t, dir), rootState(t, dir)

		_, _, err := runRestoreRoot(t, a, dir, "", "--no-fetch")

		if exitOf(err) != 3 || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("err = %v (exit %d)", err, exitOf(err))
		}
		if repoWorld(t, dir) != world || rootState(t, dir) != before {
			t.Error("a run without approval changed the repository")
		}
	})
	t.Run("yes", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		restoreRootTTY = func(io.Reader, io.Writer) bool { return true }

		out, errOut, err := runRestoreRoot(t, a, dir, "y\n", "--no-fetch")

		if exitOf(err) != 0 {
			t.Fatalf("exit %d (%v)\nprompt: %s\nout: %s", exitOf(err), err, errOut, out)
		}
		for _, want := range []string{"2 commits, 1 staged, 1 unstaged, 1 untracked file", filepath.Join(".worktrees", "topic"), "main", "Move? [y/N] "} {
			if !strings.Contains(errOut, want) {
				t.Errorf("prompt lacks %q:\n%s", want, errOut)
			}
		}
		if !strings.Contains(out, "moved") || rootBranch(t, dir) != "main" {
			t.Errorf("out %q, root on %s", out, rootBranch(t, dir))
		}
	})
	t.Run("declined", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
		world, before := repoWorld(t, dir), rootState(t, dir)

		_, _, err := runRestoreRoot(t, a, dir, "\n", "--no-fetch")

		if exitOf(err) != 3 || repoWorld(t, dir) != world || rootState(t, dir) != before {
			t.Fatalf("err = %v (exit %d)", err, exitOf(err))
		}
	})
}

// TestRestoreRootCmdYesJSON checks the whole path with --yes --json: exit 0,
// one document saying moved, with the record and dest_path; the root ends on
// main and clean, the new worktree on topic at the same commit, with the
// work. Why: this is the command's reason to exist.
func TestRestoreRootCmdYesJSON(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	head := cgit(t, dir, "rev-parse", "HEAD")

	stdout, stderr, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Outcome != "moved" || !env.Created || env.Record == nil || env.Record.Ref == "" || env.DestPath == "" {
		t.Fatalf("envelope = %+v", env)
	}
	if rootBranch(t, dir) != "main" || cgit(t, dir, "status", "--porcelain") != "" {
		t.Errorf("root on %s, status %q", rootBranch(t, dir), cgit(t, dir, "status", "--porcelain"))
	}
	if rootBranch(t, env.DestPath) != "topic" || cgit(t, env.DestPath, "rev-parse", "HEAD") != head {
		t.Errorf("dest on %s at %s", rootBranch(t, env.DestPath), cgit(t, env.DestPath, "rev-parse", "HEAD"))
	}
	if b, _ := os.ReadFile(filepath.Join(env.DestPath, "new.txt")); string(b) != "n" {
		t.Errorf("untracked file not carried: %q", b)
	}
}

// TestRestoreRootCmdExitCodes checks each refusal Preflight makes exits 2
// with its code and changes nothing, already_default exits 0 with its
// message, and a usage error exits 1 with the one error document under
// --json. Why: scripts branch on the exit status and the code.
func TestRestoreRootCmdExitCodes(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) string
		code  string
	}{
		{"detached", func(t *testing.T) string {
			dir := cleanupRepo(t)
			excludeWorktrees(t, dir)
			cgit(t, dir, "switch", "-q", "--detach")
			return dir
		}, rootmove.CodeDetachedRoot},
		{"held", func(t *testing.T) string {
			dir := cleanupRepo(t)
			rootOnTopic(t, dir)
			liveClaim(t, dir)
			return dir
		}, rootmove.CodeRootHeld},
		{"default in a worktree", func(t *testing.T) string {
			dir := cleanupRepo(t)
			rootOnTopic(t, dir)
			cgit(t, dir, "worktree", "add", "-q", filepath.Join(dir, ".worktrees", "m"), "main")
			return dir
		}, rootmove.CodeDefaultInWorktree},
		{"no commit", func(t *testing.T) string {
			dir := filepath.Join(t.TempDir(), "empty")
			if b, err := exec.Command("git", "init", "-q", "-b", "topic", dir).CombinedOutput(); err != nil {
				t.Fatalf("init: %v\n%s", err, b)
			}
			excludeWorktrees(t, dir)
			return dir
		}, rootmove.CodeNoCommit},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := restoreRootApp(t)
			dir := c.setup(t)
			world := repoWorld(t, dir)

			stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

			if exitOf(err) != 2 {
				t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
			}
			env := decodeRestoreRoot(t, stdout)
			if env.Outcome != "refused" || env.Code != c.code || env.Created || env.Reason == "" {
				t.Errorf("envelope = %+v", env)
			}
			if repoWorld(t, dir) != world {
				t.Error("a refusal changed the repository")
			}
		})
	}
	t.Run("already default", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		excludeWorktrees(t, dir)
		stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")
		if exitOf(err) != 0 {
			t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
		}
		if env := decodeRestoreRoot(t, stdout); env.Outcome != "refused" || env.Code != rootmove.CodeAlreadyDefault {
			t.Errorf("envelope = %+v", env)
		}
		text, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--no-fetch")
		if err != nil || !strings.Contains(text, "already on the default branch main") {
			t.Errorf("text = %q, %v", text, err)
		}
	})
	t.Run("usage", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		world := repoWorld(t, dir)
		for name, args := range map[string][]string{
			"positional":   {"topic", "--json", "--no-fetch"},
			"unknown flag": {"--json", "--bogus"},
		} {
			stdout, _, err := runRestoreRoot(t, a, dir, "", args...)
			if exitOf(err) != 1 {
				t.Errorf("%s: exit %d", name, exitOf(err))
				continue
			}
			if env := decodeRestoreRoot(t, stdout); env.Error == nil || env.Outcome != "" {
				t.Errorf("%s: envelope = %+v", name, env)
			}
		}
		if repoWorld(t, dir) != world {
			t.Error("a usage error changed the repository")
		}
	})
}

// fetchesTraced reads a GIT_TRACE2_EVENT file and returns the argv of every
// git process that ran `fetch`.
func fetchesTraced(t *testing.T, file string) []string {
	t.Helper()
	fh, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var ev struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Event != "start" {
			continue
		}
		for i := 1; i < len(ev.Argv); i++ {
			a := ev.Argv[i]
			if a == "-C" || a == "-c" {
				i++
				continue
			}
			if a == "fetch" {
				out = append(out, strings.Join(ev.Argv[i:], " "))
			}
			break
		}
	}
	return out
}

// TestRestoreRootCmdFetchesOnceOnRealRunsOnly checks --no-fetch and
// --dry-run leave origin/main stale and run no fetch, while a real run
// updates origin/main and runs exactly one fetch per remote (every remote,
// as `wt cleanup` fetches). Why: the help promises the dry run and
// --no-fetch never fetch, and the re-checks after Gather must not fetch
// again.
func TestRestoreRootCmdFetchesOnceOnRealRunsOnly(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	cgit(t, dir, "remote", "add", "mirror", filepath.Join(filepath.Dir(dir), "origin.git"))
	stale := cgit(t, dir, "rev-parse", "refs/remotes/origin/main")
	moved := pushFromSecondClone(t, dir)

	for _, args := range [][]string{{"--no-fetch", "--dry-run", "--json"}, {"--dry-run", "--json"}, {"--no-fetch", "--json"}} {
		trace := filepath.Join(t.TempDir(), "trace2.json")
		t.Setenv("GIT_TRACE2_EVENT", trace)
		stdout, _, _ := runRestoreRoot(t, a, dir, "", args...)
		if got := cgit(t, dir, "rev-parse", "refs/remotes/origin/main"); got != stale {
			t.Errorf("%v fetched: origin/main %s, want %s\n%s", args, got, stale, stdout)
		}
		if f := fetchesTraced(t, trace); len(f) != 0 {
			t.Errorf("%v ran fetch: %q", args, f)
		}
	}

	trace := filepath.Join(t.TempDir(), "trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json")
	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
	}
	if got := cgit(t, dir, "rev-parse", "refs/remotes/origin/main"); got != moved {
		t.Errorf("a real run did not fetch: origin/main %s, want %s", got, moved)
	}
	f := fetchesTraced(t, trace)
	if len(f) != 2 || !strings.Contains(strings.Join(f, "\n"), "origin") || !strings.Contains(strings.Join(f, "\n"), "mirror") {
		t.Errorf("fetches = %q, want one of origin and one of mirror", f)
	}
}

// TestRestoreRootCmdRunsFromALinkedWorktree runs the command from inside a
// dirty linked worktree: the root's branch moves, the root ends clean on
// main, and the linked worktree's files and status are untouched. Why: the
// command runs from anywhere in the repository and must never act on the
// caller's own checkout.
func TestRestoreRootCmdRunsFromALinkedWorktree(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	other := filepath.Join(dir, ".worktrees", "other")
	cgit(t, dir, "worktree", "add", "-q", "-b", "other", other, "main")
	if err := os.WriteFile(filepath.Join(other, "README"), []byte("other edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "mine.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, other)

	stdout, _, err := runRestoreRoot(t, a, other, "", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
	}
	if env := decodeRestoreRoot(t, stdout); env.Outcome != "moved" || env.Branch != "topic" {
		t.Errorf("envelope = %+v", env)
	}
	if sourceState(t, other) != before {
		t.Error("the linked worktree the command ran from changed")
	}
	if rootBranch(t, dir) != "main" || cgit(t, dir, "status", "--porcelain") != "" {
		t.Errorf("root on %s, status %q", rootBranch(t, dir), cgit(t, dir, "status", "--porcelain"))
	}
}

// TestRestoreRootCmdDryRunText checks the text of a dry run names the
// branch, the counts, the ignored entries left behind, the destination and
// the fast-forward verdict. Why: this is what a user reads before deciding.
func TestRestoreRootCmdDryRunText(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	appendExclude(t, dir, "*.log")
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, _, err := runRestoreRoot(t, a, dir, "", "--dry-run", "--no-fetch")

	if err != nil {
		t.Fatalf("err %v\n%s", err, text)
	}
	for _, want := range []string{"dry run: nothing was changed", "topic", "2 commits, 1 staged, 1 unstaged, 1 untracked file",
		"1 ignored entry", filepath.Join(dir, ".worktrees", "topic"), "fast-forward"} {
		if !strings.Contains(text, want) {
			t.Errorf("dry-run text lacks %q:\n%s", want, text)
		}
	}
}

// TestRestoreRootCmdClaimWhilePromptHeld claims the root for another live
// process while the confirmation is held. Prompted, Execute's start re-check
// refuses root_held with nothing created; with --yes (no start re-check) the
// claim is caught just before the release: failed root_held, the root still
// on topic with its work and the new worktree kept with the copy. Why: the
// claim is checked again exactly where a running agent would lose work.
func TestRestoreRootCmdClaimWhilePromptHeld(t *testing.T) {
	t.Run("prompted", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
		afterRestoreRootConfirm = func() { liveClaim(t, dir) }
		world := repoWorld(t, dir)

		stdout, _, err := runRestoreRoot(t, a, dir, "y\n", "--json", "--no-fetch")

		if exitOf(err) != 2 {
			t.Fatalf("exit %d\n%s", exitOf(err), stdout)
		}
		if env := decodeRestoreRoot(t, stdout); env.Code != rootmove.CodeRootHeld || env.Created {
			t.Errorf("envelope = %+v", env)
		}
		if rootBranch(t, dir) != "topic" || repoWorld(t, dir) != world {
			t.Error("the repository changed")
		}
	})
	t.Run("yes", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		afterRestoreRootConfirm = func() { liveClaim(t, dir) }

		stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

		if exitOf(err) != 2 {
			t.Fatalf("exit %d\n%s", exitOf(err), stdout)
		}
		env := decodeRestoreRoot(t, stdout)
		if env.Outcome != "failed" || env.Code != rootmove.CodeRootHeld || !env.Created {
			t.Fatalf("envelope = %+v", env)
		}
		if rootBranch(t, dir) != "topic" {
			t.Errorf("root on %s, want topic", rootBranch(t, dir))
		}
		for _, d := range []string{dir, env.DestPath} {
			if b, _ := os.ReadFile(filepath.Join(d, "new.txt")); string(b) != "n" {
				t.Errorf("%s lacks the untracked work: %q", d, b)
			}
		}
	})
}

// TestRestoreRootCmdEditWhilePromptHeld edits the root while the
// confirmation is held: exit 2, source_changed, nothing created. Why: the
// prompt is a window in which the root can change; what was shown must be
// what moves.
func TestRestoreRootCmdEditWhilePromptHeld(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
	afterRestoreRootConfirm = func() {
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte("edited while asking"), 0o644); err != nil {
			t.Error(err)
		}
	}
	world := repoWorld(t, dir)

	stdout, _, err := runRestoreRoot(t, a, dir, "y\n", "--json", "--no-fetch")

	if exitOf(err) != 2 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Code != "source_changed" || env.Created {
		t.Errorf("envelope = %+v", env)
	}
	if repoWorld(t, dir) != world || exists(filepath.Join(dir, ".worktrees", "topic")) {
		t.Error("something was created")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "README")); string(b) != "edited while asking" {
		t.Errorf("the edit was lost: %q", b)
	}
}

// TestRestoreRootCmdToNamesTheDirectory checks --to names the new
// worktree's directory (the branch keeps its name), and that --to naming a
// directory that exists refuses name_unavailable. Why: --to is the way out
// of a taken directory.
func TestRestoreRootCmdToNamesTheDirectory(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	taken := filepath.Join(dir, ".worktrees", "taken")
	if err := os.MkdirAll(taken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taken, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runRestoreRoot(t, a, dir, "", "--to", "taken", "--yes", "--json", "--no-fetch")
	if exitOf(err) != 2 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	if env := decodeRestoreRoot(t, stdout); env.Code != "name_unavailable" {
		t.Errorf("envelope = %+v", env)
	}

	stdout, _, err = runRestoreRoot(t, a, dir, "", "--to", "spike", "--yes", "--json", "--no-fetch")
	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if !strings.HasSuffix(env.DestPath, "/.worktrees/spike") || env.Branch != "topic" {
		t.Errorf("envelope = %+v", env)
	}
	if got := rootBranch(t, filepath.Join(dir, ".worktrees", "spike")); got != "topic" {
		t.Errorf("the new worktree is on %s, want topic", got)
	}
}

// TestRestoreRootCmdPostCheckoutWarningInTextAndJSON installs a
// post-checkout hook: the dry-run text, the real-run text and the real-run
// --json warnings each carry the post-checkout warning, and the hook never
// ran. Why: hooks are off for every checkout the move makes, so the user
// must be told to run what the hook sets up.
func TestRestoreRootCmdPostCheckoutWarningInTextAndJSON(t *testing.T) {
	a := restoreRootApp(t)

	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	marker := postCheckoutHook(t, dir)
	text, _, err := runRestoreRoot(t, a, dir, "", "--dry-run", "--no-fetch")
	if err != nil || !strings.Contains(text, "warning: a post-checkout hook") {
		t.Errorf("dry-run text (%v):\n%s", err, text)
	}
	text, _, err = runRestoreRoot(t, a, dir, "", "--yes", "--no-fetch")
	if err != nil || !strings.Contains(text, "warning: a post-checkout hook") {
		t.Errorf("real-run text (%v):\n%s", err, text)
	}
	if exists(marker) {
		t.Error("the post-checkout hook ran")
	}

	dir2 := cleanupRepo(t)
	rootOnTopic(t, dir2)
	marker2 := postCheckoutHook(t, dir2)
	stdout, _, err := runRestoreRoot(t, a, dir2, "", "--yes", "--json", "--no-fetch")
	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	if env := decodeRestoreRoot(t, stdout); !strings.Contains(strings.Join(env.Warnings, "\n"), "post-checkout") {
		t.Errorf("warnings = %q", env.Warnings)
	}
	if exists(marker2) {
		t.Error("the post-checkout hook ran")
	}
}
