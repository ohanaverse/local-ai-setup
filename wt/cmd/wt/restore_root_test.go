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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
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
	// Never outside the repository: a core.hooksPath (global or local) would
	// point --git-path at a shared hooks directory.
	gitDir := cgit(t, repo, "rev-parse", "--path-format=absolute", "--git-dir")
	if rel, err := filepath.Rel(gitDir, hook); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("the post-checkout hook path %s is outside %s; refusing to write it", hook, gitDir)
	}
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

// holderClaim spawns a short-lived real process and claims the root of repo
// for it under the repository's wt-sessions — a fixture "other session" that
// reads live under the claim contract's liveness rule (real pid, real start
// time via procStart, never a manufactured timestamp). The child and its
// claim are cleaned up. (Shape: rootclaim's holdlaunch_test, cleanup's
// snapshot_test, rescue's and rootmove's preflight_test.)
func holderClaim(t *testing.T, repo, branch, agent string) sessionclaim.Claim {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	c := sessionclaim.Claim{PID: child.Process.Pid, Started: procStart(t, child.Process.Pid),
		Path: cleanup.ResolvePath(repo), Branch: branch, Agent: agent}
	common := cgit(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	release, err := sessionclaim.Hold(common, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return c
}

// rootState is sourceState for the root: its HEAD, index and .git/config
// bytes, status and the hash of every file outside .git. sourceState would walk into the
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
	conf, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	idx = append(idx, conf...)
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
	// Piped input is never approval: with no terminal even "y" on stdin is
	// approval missing, so `echo y | wt restore-root` changes nothing.
	for name, c := range map[string]struct {
		stdin string
		json  bool
	}{"no terminal": {"", false}, "piped y": {"y\n", false}, "piped y json": {"y\n", true}} {
		t.Run(name, func(t *testing.T) {
			a := restoreRootApp(t)
			dir := cleanupRepo(t)
			rootOnTopic(t, dir)
			world, before := repoWorld(t, dir), rootState(t, dir)
			args := []string{"--no-fetch"}
			if c.json {
				args = append(args, "--json")
			}

			stdout, _, err := runRestoreRoot(t, a, dir, c.stdin, args...)

			if exitOf(err) != 3 || !strings.Contains(err.Error(), "approval missing") || !strings.Contains(err.Error(), "no terminal") {
				t.Fatalf("err = %v (exit %d)", err, exitOf(err))
			}
			if c.json {
				if env := decodeRestoreRoot(t, stdout); env.Error == nil || !strings.Contains(env.Error.Message, "approval missing") {
					t.Errorf("envelope = %+v", env)
				}
			}
			if repoWorld(t, dir) != world || rootState(t, dir) != before || rootBranch(t, dir) != "topic" {
				t.Error("a run without approval changed the repository")
			}
		})
	}
	t.Run("yes", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		restoreRootTTY = func(io.Reader, io.Writer) bool { return true }

		out, errOut, err := runRestoreRoot(t, a, dir, "y\n", "--no-fetch")

		if exitOf(err) != 0 {
			t.Fatalf("exit %d (%v)\nprompt: %s\nout: %s", exitOf(err), err, errOut, out)
		}
		for _, want := range []string{"2 commits, 1 staged, 1 unstaged, 1 untracked file", filepath.Join(".worktrees", "topic"), "main",
			"left behind   0 ignored entries", "fast-forward  no: main is already at origin/main", "Move? [y/N] "} {
			if !strings.Contains(errOut, want) {
				t.Errorf("prompt lacks %q:\n%s", want, errOut)
			}
		}
		if !strings.Contains(out, "moved") || rootBranch(t, dir) != "main" {
			t.Errorf("out %q, root on %s", out, rootBranch(t, dir))
		}
	})
	for name, answer := range map[string]string{"empty": "\n", "n": "n\n", "EOF": "", "yes please": "yes please\n"} {
		t.Run("declined "+name, func(t *testing.T) {
			a := restoreRootApp(t)
			dir := cleanupRepo(t)
			rootOnTopic(t, dir)
			restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
			world, before := repoWorld(t, dir), rootState(t, dir)

			_, _, err := runRestoreRoot(t, a, dir, answer, "--no-fetch")

			if exitOf(err) != 3 || !strings.Contains(err.Error(), "not confirmed; nothing was changed") {
				t.Fatalf("err = %v (exit %d)", err, exitOf(err))
			}
			if repoWorld(t, dir) != world || rootState(t, dir) != before {
				t.Error("a declined run changed the repository")
			}
		})
	}
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
		{"no default branch", func(t *testing.T) string {
			dir := filepath.Join(t.TempDir(), "nodefault")
			if b, err := exec.Command("git", "init", "-q", "-b", "trunk", dir).CombinedOutput(); err != nil {
				t.Fatalf("init: %v\n%s", err, b)
			}
			cgit(t, dir, "config", "user.email", "t@example.com")
			cgit(t, dir, "config", "user.name", "T")
			cgit(t, dir, "config", "commit.gpgsign", "false")
			cleanupCommit(t, dir, "README", "init")
			cgit(t, dir, "switch", "-q", "-c", "topic")
			excludeWorktrees(t, dir)
			return dir
		}, rootmove.CodeNoDefaultBranch},
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
// process while the confirmation is held (or just before --yes runs) and
// requires the move to proceed and complete anyway: exit 0, moved, the root
// on the default and the work carried into the new worktree. Why: a held root
// only rides Preflight's plan as a warning and the user decides — Execute
// reads no session claims, so a claim appearing in the prompt window can
// neither refuse nor fail a move.
func TestRestoreRootCmdClaimWhilePromptHeld(t *testing.T) {
	t.Run("prompted", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
		afterRestoreRootConfirm = func() { liveClaim(t, dir) }

		stdout, _, err := runRestoreRoot(t, a, dir, "y\n", "--json", "--no-fetch")

		if exitOf(err) != 0 {
			t.Fatalf("exit %d\n%s", exitOf(err), stdout)
		}
		env := decodeRestoreRoot(t, stdout)
		if env.Outcome != "moved" || !env.Created {
			t.Errorf("envelope = %+v", env)
		}
		if rootBranch(t, dir) != "main" {
			t.Errorf("root on %s, want main", rootBranch(t, dir))
		}
		if b, _ := os.ReadFile(filepath.Join(env.DestPath, "new.txt")); string(b) != "n" {
			t.Errorf("the new worktree lacks the untracked work: %q", b)
		}
	})
	t.Run("yes", func(t *testing.T) {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		afterRestoreRootConfirm = func() { liveClaim(t, dir) }

		stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

		if exitOf(err) != 0 {
			t.Fatalf("exit %d\n%s", exitOf(err), stdout)
		}
		env := decodeRestoreRoot(t, stdout)
		if env.Outcome != "moved" || !env.Created {
			t.Errorf("envelope = %+v", env)
		}
		if rootBranch(t, dir) != "main" {
			t.Errorf("root on %s, want main", rootBranch(t, dir))
		}
		if b, _ := os.ReadFile(filepath.Join(env.DestPath, "new.txt")); string(b) != "n" {
			t.Errorf("the new worktree lacks the untracked work: %q", b)
		}
	})
}

// TestRestoreRootCmdHeldRootWarnsAndMoves checks a root another live wt
// session holds still moves with --yes --json: exit 0, outcome moved, the
// root on the default with the work carried, and the result warnings carry
// BOTH held lines — rescue's own ("another wt session holds this worktree…")
// and rootmove's "root_held: " line naming the holder (pid included) — and
// the Long help lists held as that warning, never a refusal. Why: held must
// warn and never prevent at the command too (R408b keeps both lines riding
// one held plan), and the help is where a user learns the refusals (R409c:
// it must not claim held refuses).
func TestRestoreRootCmdHeldRootWarnsAndMoves(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	holder := holderClaim(t, dir, "topic", "codex")

	stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Outcome != "moved" || !env.Created {
		t.Fatalf("envelope = %+v", env)
	}
	var prefixed []string
	for _, w := range env.Warnings {
		if strings.HasPrefix(w, rootmove.HeldWarningPrefix) {
			prefixed = append(prefixed, w)
		}
	}
	if len(prefixed) != 1 || !strings.Contains(prefixed[0], fmt.Sprintf("(pid %d,", holder.PID)) {
		t.Errorf("warnings = %q, want exactly one %q line naming pid %d", env.Warnings, rootmove.HeldWarningPrefix, holder.PID)
	}
	if got := strings.Count(strings.Join(env.Warnings, "\n"), "another wt session holds this worktree"); got != 1 {
		t.Errorf("warnings = %q, want rescue's held line once (both held lines ride one held plan)", env.Warnings)
	}
	if rootBranch(t, dir) != "main" {
		t.Errorf("root on %s; a held root must still move", rootBranch(t, dir))
	}
	if b, _ := os.ReadFile(filepath.Join(env.DestPath, "new.txt")); string(b) != "n" {
		t.Errorf("the new worktree lacks the untracked work: %q", b)
	}
	long := restoreRootCmd(a).Long
	_, rest, _ := strings.Cut(long, "It refuses")
	refusals, _, _ := strings.Cut(rest, "A failure part-way")
	if refusals == "" || strings.Contains(refusals, "held") {
		t.Errorf("the help's refusals list is not truthful about held:\n%s", refusals)
	}
	if !strings.Contains(long, rootmove.HeldWarningPrefix) {
		t.Errorf("the help does not name the held warning line %q:\n%s", rootmove.HeldWarningPrefix, long)
	}
}

// TestRestoreRootCmdHeldRootPromptNamesHolder checks the y/N prompt of a held
// root names the holder — sessionclaim.Describe's entry (agent, pid, running,
// branch) in the "root_held: " warning line — before the confirm, and that a
// "y" answer moves. Why: the prompt is the user's one chance to end the other
// session first, so the holder must be named before the decision, and held
// must not silently block the move either.
func TestRestoreRootCmdHeldRootPromptNamesHolder(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	holder := holderClaim(t, dir, "topic", "codex")
	restoreRootTTY = func(io.Reader, io.Writer) bool { return true }

	out, errOut, err := runRestoreRoot(t, a, dir, "y\n", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\nprompt: %s\nout: %s", exitOf(err), err, errOut, out)
	}
	frag := fmt.Sprintf("codex (pid %d, running", holder.PID)
	at, confirm := strings.Index(errOut, frag), strings.Index(errOut, "Move? [y/N] ")
	switch {
	case at < 0:
		t.Errorf("the prompt does not name the holder (%q):\n%s", frag, errOut)
	case confirm < 0:
		t.Errorf("the prompt has no confirm:\n%s", errOut)
	case at > confirm:
		t.Errorf("the holder is named at %d, after the confirm at %d:\n%s", at, confirm, errOut)
	}
	if !strings.Contains(errOut, rootmove.HeldWarningPrefix) {
		t.Errorf("the prompt carries no %q warning line:\n%s", rootmove.HeldWarningPrefix, errOut)
	}
	if !strings.Contains(out, "moved") || rootBranch(t, dir) != "main" {
		t.Errorf("out %q, root on %s; a yes answer must move a held root", out, rootBranch(t, dir))
	}
}

// TestRestoreRootCmdHeldRootNoTerminalNoYesIsExit3 checks a held root with no
// terminal and no --yes is exit 3 (approval missing) with nothing changed.
// Why: warn-but-never-prevent means held never refuses a move — it never
// waives the confirmation either; the root is reset only on approval.
func TestRestoreRootCmdHeldRootNoTerminalNoYesIsExit3(t *testing.T) {
	a := restoreRootApp(t) // restoreRootTTY answers false: no terminal to ask
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	holderClaim(t, dir, "topic", "codex")
	world, before := repoWorld(t, dir), rootState(t, dir)

	stdout, _, err := runRestoreRoot(t, a, dir, "", "--json", "--no-fetch")

	if exitOf(err) != 3 || !strings.Contains(err.Error(), "approval missing") {
		t.Fatalf("err = %v (exit %d)\n%s", err, exitOf(err), stdout)
	}
	if env := decodeRestoreRoot(t, stdout); env.Error == nil || !strings.Contains(env.Error.Message, "approval missing") {
		t.Errorf("envelope = %+v", env)
	}
	if repoWorld(t, dir) != world || rootState(t, dir) != before || rootBranch(t, dir) != "topic" {
		t.Error("an unapproved run changed the repository")
	}
}

// TestRestoreRootCmdHeldRootDryRunReportsWarning checks a dry run of a held
// root reports the "root_held: " warning naming the holder and moves nothing.
// Why: --dry-run is where the user reads what a real run would do; held must
// be visible there, and a dry run must change nothing while saying it.
func TestRestoreRootCmdHeldRootDryRunReportsWarning(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	holder := holderClaim(t, dir, "topic", "codex")
	world, before := repoWorld(t, dir), rootState(t, dir)

	stdout, _, err := runRestoreRoot(t, a, dir, "", "--dry-run", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d\n%s", exitOf(err), stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Outcome != "would_move" || env.Created {
		t.Fatalf("envelope = %+v", env)
	}
	if w := strings.Join(env.Warnings, "\n"); !strings.Contains(w, rootmove.HeldWarningPrefix) || !strings.Contains(w, fmt.Sprintf("(pid %d,", holder.PID)) {
		t.Errorf("warnings = %q, want the %q line naming pid %d", env.Warnings, rootmove.HeldWarningPrefix, holder.PID)
	}
	if repoWorld(t, dir) != world || rootState(t, dir) != before || exists(filepath.Join(dir, ".worktrees", "topic")) {
		t.Error("a dry run changed the repository")
	}
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

// TestRestoreRootCmdRemedyPrintedWhole checks the worktrees_not_ignored
// refusal prints its one-line remedy whole, in the text and in the JSON
// reason, and says "nothing was changed" once. Why: the reason is longer
// than OneLine's 200-rune cap, which cut the remedy off mid-command.
func TestRestoreRootCmdRemedyPrintedWhole(t *testing.T) {
	const remedy = `echo '.worktrees/' >> "$(git rev-parse --git-path info/exclude)"`
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	// The developer's global excludes file may ignore .worktrees/; this
	// repository must not.
	cgit(t, dir, "config", "core.excludesFile", os.DevNull)
	cgit(t, dir, "switch", "-q", "-c", "topic")

	text, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--no-fetch")
	if exitOf(err) != 2 || !strings.Contains(text, remedy) || strings.Count(text, "nothing was") != 1 {
		t.Errorf("exit %d, text:\n%s", exitOf(err), text)
	}
	stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")
	env := decodeRestoreRoot(t, stdout)
	if exitOf(err) != 2 || env.Code != rootmove.CodeWorktreesNotIgnored || !strings.HasSuffix(env.Reason, remedy) {
		t.Errorf("exit %d, envelope = %+v", exitOf(err), env)
	}
	dry, _, err := runRestoreRoot(t, a, dir, "", "--dry-run", "--no-fetch")
	if exitOf(err) != 2 || strings.Count(dry, "nothing was changed") != 1 {
		t.Errorf("dry-run refusal says nothing was changed %d times:\n%s", strings.Count(dry, "nothing was changed"), dry)
	}
}

// TestRestoreRootCmdRecoveryPrintedWhole fails a --yes run at the release's
// first gate (after the confirmation the default branch becomes checked out
// in another worktree) and checks the recovery sentence, which names the copy
// and the record, reaches the text and the JSON reason whole although the
// reason is over 200 runes, and that the text does not also say nothing was
// changed. Why: the recovery table's row is how the user finds the work after
// a failure.
func TestRestoreRootCmdRecoveryPrintedWhole(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		afterRestoreRootConfirm = func() {
			cgit(t, dir, "worktree", "add", "-q", filepath.Join(dir, ".worktrees", "m"), "main")
		}
		args := []string{"--yes", "--no-fetch"}
		if asJSON {
			args = append(args, "--json")
		}

		out, _, err := runRestoreRoot(t, a, dir, "", args...)

		if exitOf(err) != 2 {
			t.Fatalf("exit %d\n%s", exitOf(err), out)
		}
		refs := cgit(t, dir, "for-each-ref", "--format=%(refname)", "refs/wt/rescued/")
		closing := ", recorded at " + refs + "."
		reason := out
		if asJSON {
			reason = decodeRestoreRoot(t, out).Reason
			if len([]rune(reason)) <= 200 {
				t.Fatalf("the reason is only %d runes; the test proves nothing", len([]rune(reason)))
			}
		}
		if !strings.Contains(reason, "In the root nothing was reset or removed") || !strings.Contains(reason, closing) {
			t.Errorf("json=%v: the recovery sentence is cut:\n%s", asJSON, reason)
		}
		if !asJSON && (strings.Contains(out, "nothing was changed") || strings.Contains(out, " carries ")) {
			t.Errorf("a failed run's text claims nothing changed or uses the dry-run verb:\n%s", out)
		}
	}
}

// TestRestoreRootTextAndJSONFlattenControls feeds a reason, warning and
// step holding a newline and ESC bytes, and a tail past 200 runes, through
// the text and the JSON: no raw ESC or newline inside a line, and the tail
// kept. Why: reasons quote git and the file system; they must not drive the
// terminal, and must not be cut either.
func TestRestoreRootTextAndJSONFlattenControls(t *testing.T) {
	evil := "bad\nline \x1b]0;title\x07 \x1b[31mred " + strings.Repeat("y", 250) + " TAIL"
	o := rootmove.Outcome{Status: "failed", Code: "switch_failed", Reason: evil, Completed: []string{evil},
		Plan: rootmove.Plan{Branch: "topic", Default: "main", Warnings: []string{evil}}, Created: true}
	var b bytes.Buffer
	if err := printRestoreRootText(&b, rootmove.Meta{}, o); err != nil {
		t.Fatal(err)
	}
	enc, err := rootmove.EncodeResult(o, rootmove.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	env := decodeRestoreRoot(t, string(enc))
	for name, s := range map[string]string{"text": b.String(), "reason": env.Reason, "warning": env.Warnings[0], "completed": env.Completed[0]} {
		if strings.ContainsAny(s, "\x1b\x07") {
			t.Errorf("%s holds a control byte: %q", name, s)
		}
		if strings.Count(s, " TAIL") != map[bool]int{true: 3, false: 1}[name == "text"] {
			t.Errorf("%s lost the tail: %q", name, s)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if strings.HasPrefix(line, "line ") {
			t.Errorf("an embedded newline split a line: %q", line)
		}
	}
}

// TestRestoreRootCmdWarnsOnAFailedFetch makes one remote unreachable (origin,
// then another remote): a real run still moves and its warnings say a fetch
// failed and the fast-forward check used the last fetched state. Why: a
// silent failed fetch would make the fast-forward verdict look current.
func TestRestoreRootCmdWarnsOnAFailedFetch(t *testing.T) {
	for _, remote := range []string{"origin", "other"} {
		a := restoreRootApp(t)
		dir := cleanupRepo(t)
		rootOnTopic(t, dir)
		missing := filepath.Join(t.TempDir(), "gone.git")
		if remote == "origin" {
			cgit(t, dir, "remote", "set-url", "origin", missing)
		} else {
			cgit(t, dir, "remote", "add", "other", missing)
		}

		stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json")

		if exitOf(err) != 0 {
			t.Fatalf("%s: exit %d\n%s", remote, exitOf(err), stdout)
		}
		w := strings.Join(decodeRestoreRoot(t, stdout).Warnings, "\n")
		if !strings.Contains(w, "a fetch failed ("+remote+":") || !strings.Contains(w, "the fast-forward check used the last fetched state") {
			t.Errorf("%s: warnings = %q", remote, w)
		}
	}
}

// TestRestoreRootCmdRejectsBadToNames checks --to refuses, as a usage error
// (exit 1, the error document under --json, nothing created), a name that is
// empty or only whitespace, with leading or trailing whitespace, "." or "..",
// or .git or .worktrees in any case.
// Why: such a name made a directory literally called " " or
// .worktrees/.worktrees, or would aim at the repository's own metadata.
func TestRestoreRootCmdRejectsBadToNames(t *testing.T) {
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	world, before := repoWorld(t, dir), rootState(t, dir)
	for _, name := range []string{"", " ", "\t", ".", "..", ".git", ".GIT", ".worktrees", ".Worktrees", " .git ", " spike", "spike ", "\tspike", "spike\n"} {
		stdout, _, err := runRestoreRoot(t, a, dir, "", "--to", name, "--yes", "--json", "--no-fetch")
		if exitOf(err) != 1 {
			t.Errorf("--to %q: exit %d\n%s", name, exitOf(err), stdout)
			continue
		}
		if env := decodeRestoreRoot(t, stdout); env.Error == nil || !strings.Contains(env.Error.Message, "--to") {
			t.Errorf("--to %q: envelope = %+v", name, env)
		}
	}
	if repoWorld(t, dir) != world || rootState(t, dir) != before {
		t.Error("a bad --to changed the repository")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".worktrees"))
	if len(entries) != 0 {
		t.Errorf(".worktrees holds %v", entries)
	}
}

// TestRestoreRootCmdSignalDuringTheMoveDoesNotStopIt sends SIGINT to the
// process after the confirmation, as the move starts: the move still runs to
// the end, its result is printed, the exit code is the result's, and stderr
// says the signal arrived and did not stop it. The test keeps its own
// SIGINT handler for the duration so the test binary survives either way.
// Why: a move killed part-way leaves a half-built copy or a partly reset
// root with no word on which step was reached.
func TestRestoreRootCmdSignalDuringTheMoveDoesNotStopIt(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	t.Cleanup(func() { signal.Stop(guard) })
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	afterRestoreRootConfirm = func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select { // delivered to every registered channel at once
		case <-guard:
		case <-time.After(5 * time.Second):
			t.Fatal("SIGINT was not delivered")
		}
	}

	stdout, stderr, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s\n%s", exitOf(err), err, stdout, stderr)
	}
	if env := decodeRestoreRoot(t, stdout); env.Outcome != "moved" {
		t.Errorf("envelope = %+v", env)
	}
	if !strings.Contains(stderr, "interrupted (signal interrupt): the move was not stopped; see the result above") {
		t.Errorf("stderr = %q", stderr)
	}
	if rootBranch(t, dir) != "main" {
		t.Errorf("root on %s", rootBranch(t, dir))
	}
}

// signalOnWrite is a writer that sends SIGINT to the process on its first
// write and waits until guard has it, then keeps the bytes.
type signalOnWrite struct {
	t     *testing.T
	guard chan os.Signal
	sent  bool
	buf   bytes.Buffer
}

func (w *signalOnWrite) Write(p []byte) (int, error) {
	if !w.sent {
		w.sent = true
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			w.t.Fatal(err)
		}
		select {
		case <-w.guard:
		case <-time.After(5 * time.Second):
			w.t.Fatal("SIGINT was not delivered")
		}
	}
	return w.buf.Write(p)
}

// TestRestoreRootCmdSignalWhileWritingTheResultIsHeld sends SIGINT while the
// result is being written to stdout: the signal is still held (and reported
// after the result), so the output is complete. The test keeps its own
// SIGINT handler so the test binary survives either way. Why: released
// before the write, a signal then would end wt with the result cut short.
func TestRestoreRootCmdSignalWhileWritingTheResultIsHeld(t *testing.T) {
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	t.Cleanup(func() { signal.Stop(guard) })
	a := restoreRootApp(t)
	dir := cleanupRepo(t)
	rootOnTopic(t, dir)
	t.Chdir(dir)
	cmd := restoreRootCmd(a)
	out := &signalOnWrite{t: t, guard: guard}
	var errOut bytes.Buffer
	cmd.SetOut(out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--yes", "--json", "--no-fetch"})

	err := cmd.Execute()

	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, out.buf.String())
	}
	if env := decodeRestoreRoot(t, out.buf.String()); env.Outcome != "moved" {
		t.Errorf("envelope = %+v", env)
	}
	if strings.Count(errOut.String(), "interrupted (signal interrupt)") != 1 {
		t.Errorf("stderr = %q, want the interrupted line once", errOut.String())
	}
}
