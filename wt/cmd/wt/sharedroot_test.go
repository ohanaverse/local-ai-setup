package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
)

// sharedRootRepo makes a repository with one commit on main and returns its
// root with symlinks resolved, the spelling claims are written with.
func sharedRootRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitInit(t, dir)
	for _, args := range [][]string{
		{"symbolic-ref", "HEAD", "refs/heads/main"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// liveOtherClaim writes a claim on root for a real, running process (a sleep
// child killed on cleanup), so the liveness check sees a live session.
func liveOtherClaim(t *testing.T, root, agent string) int {
	t.Helper()
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Unix()
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	dir := filepath.Join(root, ".git", "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=%s\n", pid, started, root, agent)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return pid
}

// ownClaim is the path of this process's claim file on root.
func ownClaim(root string) string {
	return filepath.Join(root, ".git", "wt-sessions", strconv.Itoa(os.Getpid()))
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// failReader fails the test if the menu is read when it must not be shown.
type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Error("the menu read input, but no question should have been asked")
	return 0, errors.New("unexpected read")
}

// TestConfirmNoOthersProceeds checks a root nobody else holds launches with no
// prompt and records this session, so the common case costs nothing.
func TestConfirmNoOthersProceeds(t *testing.T) {
	root := sharedRootRepo(t)
	var out bytes.Buffer
	dir, release, err := confirmSharedRoot(root, "claude", false, failReader{t}, &out, true)
	if err != nil || dir != root {
		t.Fatalf("dir=%q err=%v; want %q, nil", dir, err, root)
	}
	if out.Len() != 0 {
		t.Errorf("unexpected output: %q", out.String())
	}
	data, err := os.ReadFile(ownClaim(root))
	if err != nil {
		t.Fatalf("no claim written: %v", err)
	}
	for _, want := range []string{"path=" + root + "\n", "branch=main\n", "agent=claude\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("claim %q lacks %q", data, want)
		}
	}
	release()
	if fileExists(ownClaim(root)) {
		t.Error("release left the claim behind")
	}
}

// TestConfirmContinueKeepsDir checks "c" launches in the root after naming the
// other session, and records this session too.
func TestConfirmContinueKeepsDir(t *testing.T) {
	root := sharedRootRepo(t)
	pid := liveOtherClaim(t, root, "codex")
	var out bytes.Buffer
	dir, release, err := confirmSharedRoot(root, "claude", false, strings.NewReader("c"), &out, true)
	if err != nil || dir != root {
		t.Fatalf("dir=%q err=%v; want %q, nil", dir, err, root)
	}
	for _, want := range []string{"codex", "pid " + strconv.Itoa(pid), "running", "main", "continue", "new worktree", "quit"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("menu %q does not name %q", out.String(), want)
		}
	}
	if !fileExists(ownClaim(root)) {
		t.Fatal("continue wrote no claim")
	}
	release()
	if fileExists(ownClaim(root)) {
		t.Error("release left the claim behind")
	}
}

// TestConfirmNewWorktreeReturnsLinkedPath checks "w" moves the launch into a
// new linked worktree, not the default branch, and claims nothing.
func TestConfirmNewWorktreeReturnsLinkedPath(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	var out bytes.Buffer
	dir, release, err := confirmSharedRoot(root, "claude", false, strings.NewReader("w"), &out, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !strings.HasPrefix(dir, filepath.Join(root, ".worktrees")+string(filepath.Separator)) {
		t.Fatalf("dir = %q; want a path under %s/.worktrees/", dir, root)
	}
	b, err := exec.Command("git", "-C", dir, "branch", "--show-current").Output()
	if err != nil {
		t.Fatalf("new worktree is not a checkout: %v", err)
	}
	if br := strings.TrimSpace(string(b)); br == "" || br == "main" {
		t.Errorf("new worktree is on %q; want a new branch, never the default", br)
	}
	if fileExists(ownClaim(root)) {
		t.Error("a new-worktree launch claimed the root")
	}
}

// TestConfirmQuit checks "q" cancels the launch and claims nothing.
func TestConfirmQuit(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	_, _, err := confirmSharedRoot(root, "claude", false, strings.NewReader("q"), &bytes.Buffer{}, true)
	if !errors.Is(err, errCancelled) {
		t.Fatalf("err = %v; want errCancelled", err)
	}
	if fileExists(ownClaim(root)) {
		t.Error("quit claimed the root")
	}
}

// TestConfirmEsc checks Esc is quit, as every other wt prompt treats it.
func TestConfirmEsc(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	_, _, err := confirmSharedRoot(root, "claude", false, strings.NewReader("\x1b"), &bytes.Buffer{}, true)
	if !errors.Is(err, errCancelled) {
		t.Fatalf("err = %v; want errCancelled", err)
	}
	if fileExists(ownClaim(root)) {
		t.Error("Esc claimed the root")
	}
}

// TestConfirmNonRootSkipsEverything checks a launch in a linked worktree is
// never questioned and writes no claim, even when the root is held.
func TestConfirmNonRootSkipsEverything(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	linked := filepath.Join(root, ".worktrees", "feat")
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "feat", linked).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	var out bytes.Buffer
	dir, release, err := confirmSharedRoot(linked, "claude", false, failReader{t}, &out, false)
	if err != nil || dir != linked {
		t.Fatalf("dir=%q err=%v; want %q, nil", dir, err, linked)
	}
	release()
	if out.Len() != 0 {
		t.Errorf("unexpected output: %q", out.String())
	}
	if fileExists(ownClaim(root)) {
		t.Error("a linked-worktree launch claimed the root")
	}
}

// TestConfirmNoTTYRefuses checks a script launching into a held root stops
// with an error naming the other session and the flag that lets it through,
// instead of hanging on a question nobody can answer.
func TestConfirmNoTTYRefuses(t *testing.T) {
	root := sharedRootRepo(t)
	pid := liveOtherClaim(t, root, "codex")
	_, _, err := confirmSharedRoot(root, "claude", false, failReader{t}, &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("no error; want a refusal")
	}
	for _, want := range []string{"codex", "pid " + strconv.Itoa(pid), "--allow-shared-root"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if fileExists(ownClaim(root)) {
		t.Error("a refused launch claimed the root")
	}
}

// TestConfirmNoTTYAllowFlagProceeds checks --allow-shared-root lets a script
// launch into a held root, and that it is still recorded.
func TestConfirmNoTTYAllowFlagProceeds(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	dir, release, err := confirmSharedRoot(root, "claude", true, failReader{t}, &bytes.Buffer{}, false)
	if err != nil || dir != root {
		t.Fatalf("dir=%q err=%v; want %q, nil", dir, err, root)
	}
	if !fileExists(ownClaim(root)) {
		t.Fatal("an allowed launch wrote no claim")
	}
	release()
}

// TestConfirmClaimDirUnwritableProceedsWithNote checks a claims directory wt
// cannot write never blocks a launch: it says so in one line and goes on.
func TestConfirmClaimDirUnwritableProceedsWithNote(t *testing.T) {
	root := sharedRootRepo(t)
	sessions := filepath.Join(root, ".git", "wt-sessions")
	if err := os.MkdirAll(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessions, 0o755) })
	var out bytes.Buffer
	dir, release, err := confirmSharedRoot(root, "claude", false, failReader{t}, &out, false)
	if err != nil || dir != root {
		t.Fatalf("dir=%q err=%v; want %q, nil", dir, err, root)
	}
	release()
	if s := out.String(); !strings.Contains(s, "note") || strings.Count(s, "\n") != 1 {
		t.Errorf("output %q; want a one-line note", s)
	}
}

// TestCwdLaunchAtHeldRootRefusesWithoutTTY checks the check is on the real
// --cwd launch path: a script launching in a held root starts no agent and
// fails naming the flag.
func TestCwdLaunchAtHeldRootRefusesWithoutTTY(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	stubStdinTTY(t, false)
	res := runCwdLaunch(t, root)
	if res.err == nil || !strings.Contains(res.err.Error(), "--allow-shared-root") {
		t.Fatalf("err = %v; want the shared-root refusal", res.err)
	}
	if res.agentDir != "" {
		t.Errorf("the agent ran in %q despite the refusal", res.agentDir)
	}
}

// TestCwdLaunchAllowSharedRootReleasesClaim checks --allow-shared-root
// launches in the held root and that the claim is gone once the agent exits.
func TestCwdLaunchAllowSharedRootReleasesClaim(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	stubStdinTTY(t, false)
	res := runCwdLaunch(t, root, "--allow-shared-root")
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.agentDir != root {
		t.Errorf("the agent ran in %q, want %q", res.agentDir, root)
	}
	if fileExists(ownClaim(root)) {
		t.Error("the claim outlived the agent")
	}
}

// pickerRootCheck launches through the picker (tuiRun stubbed) with args and
// returns the shared-root mode the TUI was handed.
func pickerRootCheck(t *testing.T, args ...string) tui.RootCheck {
	t.Helper()
	dir := initTestRepo(t)
	t.Chdir(dir)
	writeConfiguredAgent(t, t.TempDir(), "pi", "ollama")
	t.Cleanup(func() { tui.SharedRoot = tui.RootCheck{} })
	var got tui.RootCheck
	called := false
	old := tuiRun
	tuiRun = func(bool, bool, string, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier, tui.SlotProfiles) error {
		got, called = tui.SharedRoot, true
		return nil
	}
	t.Cleanup(func() { tuiRun = old })
	stubStdinTTY(t, true)
	if _, err := runWT(t, args...); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("the launch did not reach the TUI")
	}
	return got
}

// TestPickerLaunchHandsTheTUIItsRootCheck checks the TUI gets the same rules
// as the launch without the picker: -W is never checked (Skip), and
// --allow-shared-root continues without the screen (Allow).
func TestPickerLaunchHandsTheTUIItsRootCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want tui.RootCheck
	}{
		{"cwd", []string{"--cwd", "-A", "pi"}, tui.RootCheck{}},
		{"cwd allow", []string{"--cwd", "-A", "pi", "--allow-shared-root"}, tui.RootCheck{Allow: true}},
		{"named worktree", []string{"-W", "my-feature", "-A", "pi"}, tui.RootCheck{Skip: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickerRootCheck(t, tc.args...); got != tc.want {
				t.Errorf("RootCheck = %+v; want %+v", got, tc.want)
			}
		})
	}
}
