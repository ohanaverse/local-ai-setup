package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// failReader fails the test if a question is read when none should be asked.
type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Error("a question read input, but none should have been asked")
	return 0, fmt.Errorf("unexpected read")
}

// TestHoldLaunchClaimsTheWorktree checks the early claim side of a launch in
// isolation: a launch in the repository root or a linked worktree writes this
// session's claim for that checkout (path, branch, agent) before any model
// starts and the release removes it again, and a claims directory that cannot be written is one note
// and never a refusal. Why: the held warning other launches show reads these
// claims, so a root launch must be visible exactly while it runs — and a
// broken claims directory must not stop anybody from working.
func TestHoldLaunchClaimsTheWorktree(t *testing.T) {
	t.Run("the root", func(t *testing.T) {
		root := sharedRootRepo(t)
		holdLaunch("claude", root)
		data, err := os.ReadFile(ownClaim(root))
		if err != nil {
			t.Fatalf("no claim written: %v", err)
		}
		for _, want := range []string{"path=" + root + "\n", "branch=main\n", "agent=claude\n"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("claim %q lacks %q", data, want)
			}
		}
		releaseRootClaim()
		if fileExists(ownClaim(root)) {
			t.Error("release left the claim behind")
		}
	})
	t.Run("a linked worktree", func(t *testing.T) {
		root := sharedRootRepo(t)
		linked := addLinkedWorktree(t, root, "feat")
		holdLaunch("claude", linked)
		data, err := os.ReadFile(ownClaim(root))
		if err != nil {
			t.Fatalf("no claim written: %v", err)
		}
		for _, want := range []string{"path=" + linked + "\n", "branch=feat\n"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("claim %q lacks %q", data, want)
			}
		}
		if strings.Contains(string(data), "path="+root+"\n") {
			t.Error("a linked-worktree launch claimed the root")
		}
		releaseRootClaim()
		if fileExists(ownClaim(root)) {
			t.Error("release left the claim behind")
		}
	})
	t.Run("an unwritable claims directory", func(t *testing.T) {
		root := sharedRootRepo(t)
		sessions := filepath.Join(root, ".git", "wt-sessions")
		if err := os.MkdirAll(sessions, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sessions, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sessions, 0o755) })
		if os.Geteuid() == 0 {
			t.Skip("root ignores file modes")
		}
		notes := captureNotes(t)
		holdLaunch("claude", root)
		releaseRootClaim()
		if s := notes.String(); !strings.Contains(s, "note") || strings.Count(s, "\n") != 1 {
			t.Errorf("notes %q; want a one-line note", s)
		}
	})
}

// TestNamedWorktreeLaunchDecidesOnTheResolvedDir checks a launch is judged by
// the directory it resolves to, not the flag that named it: -W naming a linked
// worktree is never warned about even while another session holds the root —
// it claims its own worktree for the agent's run (Task 405) — and a launch in
// the root claims it for the agent's run too: held by another live session it
// warns (naming the holder) and proceeds. Why: the warnings must describe the
// place the agent will actually start; the old shared-root refusal and its
// c/w/q menu are gone.
func TestNamedWorktreeLaunchDecidesOnTheResolvedDir(t *testing.T) {
	t.Run("linked worktree", func(t *testing.T) {
		root := sharedRootRepo(t)
		liveOtherClaim(t, root, "codex")
		linked := addLinkedWorktree(t, root, "feat")
		stubStdinTTY(t, false)
		notes := captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"-W", "feat"})
		if res.err != nil || res.agentDir != linked {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
		}
		if s := notes.String(); strings.Contains(s, "in use by another session") || strings.Contains(s, "is the repository root") {
			t.Errorf("notes = %q; a linked worktree is not the root", s)
		}
		if !res.claimSeen {
			t.Error("the agent did not see the linked worktree's claim while it ran")
		}
		if fileExists(ownClaim(root)) {
			t.Error("the claim outlived the agent")
		}
	})
	t.Run("root, held", func(t *testing.T) {
		root := sharedRootRepo(t)
		pid := liveOtherClaim(t, root, "codex")
		stubStdinTTY(t, false)
		notes := captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"--root"})
		if res.err != nil || res.agentDir != root {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s despite the other session", res.err, res.agentDir, root)
		}
		s := notes.String()
		for _, want := range []string{"in use by another session", "codex", "pid " + strconv.Itoa(pid)} {
			if !strings.Contains(s, want) {
				t.Errorf("notes = %q; want the held warning naming %q", s, want)
			}
		}
		if !res.claimSeen {
			t.Error("the agent did not see the root claim while it ran")
		}
		if fileExists(ownClaim(root)) {
			t.Error("the claim outlived the agent")
		}
	})
	t.Run("root, free", func(t *testing.T) {
		root := sharedRootRepo(t)
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"--root"})
		if res.err != nil || res.agentDir != root {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, root)
		}
		if !res.claimSeen {
			t.Error("the agent did not see the root claim while it ran")
		}
		if fileExists(ownClaim(root)) {
			t.Error("the claim outlived the agent")
		}
	})
}
