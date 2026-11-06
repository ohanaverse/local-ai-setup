package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// addLinkedWorktree adds a linked worktree on a new branch under root and
// returns its resolved path.
func addLinkedWorktree(t *testing.T, root, branch string) string {
	t.Helper()
	linked := filepath.Join(root, ".worktrees", branch)
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", branch, linked).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	return physical(t, linked)
}

// TestRootFlagLaunchesInThePrimaryFromALinkedWorktreeAndASubdir checks --root
// finds the primary checkout by the checkout, not the current directory: run
// from a linked worktree or from a subdirectory of the root, the agent still
// starts in the root. A fresh repo per case keeps the cases independent.
func TestRootFlagLaunchesInThePrimaryFromALinkedWorktreeAndASubdir(t *testing.T) {
	for _, tc := range []string{"linked worktree", "subdirectory", "root itself"} {
		t.Run(tc, func(t *testing.T) {
			root := sharedRootRepo(t)
			from := root
			switch tc {
			case "linked worktree":
				from = addLinkedWorktree(t, root, "feat")
			case "subdirectory":
				from = filepath.Join(root, "sub")
				if err := os.Mkdir(from, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			stubStdinTTY(t, false)
			res := runPinnedLaunch(t, from, []string{"--root"})
			if res.err != nil || res.agentDir != root {
				t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, root)
			}
		})
	}
}

// TestRootFlagIsMutuallyExclusive checks --root, --cwd and -W name where to
// launch, so any two together are a usage error and nothing launches.
func TestRootFlagIsMutuallyExclusive(t *testing.T) {
	for _, where := range [][]string{
		{"--root", "--cwd"},
		{"--root", "-W", "x"},
		{"--cwd", "-W", "x"},
	} {
		t.Run(strings.Join(where, " "), func(t *testing.T) {
			root := sharedRootRepo(t)
			stubStdinTTY(t, false)
			res := runPinnedLaunch(t, root, where)
			if res.err == nil || !strings.Contains(res.err.Error(), "mutually exclusive") {
				t.Fatalf("err = %v; want a mutually exclusive error", res.err)
			}
			if res.agentDir != "" {
				t.Errorf("the agent ran in %q", res.agentDir)
			}
		})
	}
}

// TestRootFlagOutsideARepositoryRefuses checks --root has nothing to find
// outside a repository: it errors rather than launching in the current
// directory.
func TestRootFlagOutsideARepositoryRefuses(t *testing.T) {
	stubStdinTTY(t, false)
	res := runPinnedLaunch(t, t.TempDir(), []string{"--root"})
	if res.err == nil {
		t.Fatal("--root outside a repository succeeded")
	}
	if res.agentDir != "" {
		t.Errorf("the agent ran in %q", res.agentDir)
	}
}

// TestWorktreeFlagNamingTheRootBranchIsRefused checks -W <the root's branch>
// no longer lands in the root silently: it is refused naming --root, nothing
// launches and no claim is written.
func TestWorktreeFlagNamingTheRootBranchIsRefused(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, false)
	res := runPinnedLaunch(t, root, []string{"-W", "main"})
	if res.err == nil || !strings.Contains(res.err.Error(), "--root") {
		t.Fatalf("err = %v; want a refusal naming --root", res.err)
	}
	if res.agentDir != "" || res.claimSeen || fileExists(ownClaim(root)) {
		t.Errorf("launched or claimed: agentDir=%q claimSeen=%v", res.agentDir, res.claimSeen)
	}
}

// TestCwdAtRootWithoutTerminalRefusesNamingRoot checks --cwd standing in the
// root with no terminal to ask on is refused and the message points at --root.
func TestCwdAtRootWithoutTerminalRefusesNamingRoot(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, false)
	res := runCwdLaunch(t, root)
	if res.err == nil || !strings.Contains(res.err.Error(), "--root") {
		t.Fatalf("err = %v; want a refusal naming --root", res.err)
	}
	if res.agentDir != "" {
		t.Errorf("the agent ran in %q", res.agentDir)
	}
}

// TestCwdInASubdirectoryOfRootIsNotGated checks a subdirectory of the root is
// not the root (the documented limit): --cwd there launches with no question
// and no terminal.
func TestCwdInASubdirectoryOfRootIsNotGated(t *testing.T) {
	root := sharedRootRepo(t)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stubStdinTTY(t, false)
	res := runCwdLaunch(t, sub)
	if res.err != nil || res.agentDir != physical(t, sub) {
		t.Fatalf("err=%v agentDir=%q; want a silent launch in %s", res.err, res.agentDir, sub)
	}
}

// TestQuitAtTheRootPromptEndsTheLaunchQuietly checks answering no at the
// root prompt ends the launch with a nil error (wt exits 0, prints nothing),
// runs no agent and leaves no claim file for this process.
func TestQuitAtTheRootPromptEndsTheLaunchQuietly(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, true)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin; r.Close() })

	res := runCwdLaunch(t, root)
	if res.err != nil {
		t.Fatalf("err = %v; want nil", res.err)
	}
	if res.agentDir != "" || res.claimSeen || fileExists(ownClaim(root)) {
		t.Errorf("launched or claimed: agentDir=%q claimSeen=%v", res.agentDir, res.claimSeen)
	}
}
