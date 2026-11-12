package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// TestWorktreeFlagNamingTheRootBranchLaunchesWithWarning checks -W <the root's
// branch> is an ordinary reuse: it resolves to the root through the reuse loop
// and launches there — but never silently, the root warning is printed and the
// root is claimed for the agent's run. Why: naming the root by its branch is
// natural, and a silent root launch is the one thing the warnings exist to
// prevent; the old refusal just sent people to --root for no gain.
func TestWorktreeFlagNamingTheRootBranchLaunchesWithWarning(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, false)
	notes := captureNotes(t)
	res := runPinnedLaunch(t, root, []string{"-W", "main"})
	if res.err != nil || res.agentDir != root {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, root)
	}
	if !strings.Contains(notes.String(), "is the repository root") {
		t.Errorf("notes = %q; want the root warning", notes.String())
	}
	if !res.claimSeen {
		t.Error("the agent did not see the root claim while it ran")
	}
	if fileExists(ownClaim(root)) {
		t.Error("the claim outlived the agent")
	}
}

// TestCwdAtRootWarnsAndProceeds checks --cwd standing in the repository root
// is a warning, not a refusal: with no terminal the root warning is printed
// and the launch happens in the root. Why: the person's directory choice is
// already explicit, so refusing a script here and naming --root (the old
// behavior) answered a question nobody asked.
func TestCwdAtRootWarnsAndProceeds(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, false)
	notes := captureNotes(t)
	res := runCwdLaunch(t, root)
	if res.err != nil || res.agentDir != root {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, root)
	}
	if !strings.Contains(notes.String(), "is the repository root") {
		t.Errorf("notes = %q; want the root warning", notes.String())
	}
}

// TestRootFlagIsConsentForTheRootWarningButNotForHeld checks what --root
// waives: the root warning alone. A free root launches with no question at
// all; a root another live session holds still asks (the held warning names
// the holder), and a no ends the launch quietly with nothing claimed. Why:
// --root answers "yes, work in the root"; it says nothing about working
// beside somebody else's session.
func TestRootFlagIsConsentForTheRootWarningButNotForHeld(t *testing.T) {
	t.Run("free root does not ask", func(t *testing.T) {
		root := sharedRootRepo(t)
		stubStdinTTY(t, true)
		answerOnStdin(t, "")
		notes := captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"--root"})
		if res.err != nil || res.agentDir != root {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, root)
		}
		if s := notes.String(); strings.Contains(s, "is the repository root") || strings.Contains(s, "continue?") {
			t.Errorf("notes = %q; --root consents to the root warning and nothing asks", s)
		}
	})
	t.Run("held root still asks", func(t *testing.T) {
		root := sharedRootRepo(t)
		pid := liveOtherClaim(t, root, "codex")
		stubStdinTTY(t, true)
		answerOnStdin(t, "\n") // the default is yes
		notes := captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"--root"})
		if res.err != nil || res.agentDir != root {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, root)
		}
		s := notes.String()
		for _, want := range []string{"in use by another session", "codex", "pid " + strconv.Itoa(pid), "continue? [Y/n]"} {
			if !strings.Contains(s, want) {
				t.Errorf("notes = %q; want the held warning and the question naming %q", s, want)
			}
		}
		if strings.Contains(s, "is the repository root") {
			t.Errorf("notes = %q; --root consents to the root warning", s)
		}
	})
	t.Run("no cancels quietly", func(t *testing.T) {
		root := sharedRootRepo(t)
		liveOtherClaim(t, root, "codex")
		stubStdinTTY(t, true)
		answerOnStdin(t, "n\n")
		notes := captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"--root"})
		if res.err != nil {
			t.Fatalf("err = %v; a no is a choice, not a failure", res.err)
		}
		if !strings.Contains(notes.String(), "continue? [Y/n]") {
			t.Errorf("notes = %q; want the question", notes.String())
		}
		if res.agentDir != "" || res.claimSeen || fileExists(ownClaim(root)) {
			t.Errorf("launched or claimed: agentDir=%q claimSeen=%v", res.agentDir, res.claimSeen)
		}
	})
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
// launch warnings' one question ends the launch with a nil error (wt exits 0),
// runs no agent and leaves no claim file for this process: declining is a
// choice, not a failure.
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

// TestRootLaunchUsageErrorsPrintNoUsageBlock checks the refusals of --root and
// -W (two places at once, --root outside a repository) are not usage mistakes
// cobra should answer with its command list: the block would bury the one
// "wt:" line main prints for the error.
func TestRootLaunchUsageErrorsPrintNoUsageBlock(t *testing.T) {
	cases := map[string]struct {
		dir   func(t *testing.T) string
		where []string
	}{
		"root with cwd":     {sharedRootRepo, []string{"--root", "--cwd"}},
		"root with -W":      {sharedRootRepo, []string{"--root", "-W", "x"}},
		"root outside repo": {func(t *testing.T) string { return t.TempDir() }, []string{"--root"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stubStdinTTY(t, false)
			res := runPinnedLaunch(t, tc.dir(t), tc.where)
			if res.err == nil {
				t.Fatal("the launch succeeded; want a refusal")
			}
			if strings.Contains(res.output, "Usage:") || strings.Contains(res.output, "Available Commands") {
				t.Errorf("output carries cobra's usage block:\n%s", res.output)
			}
		})
	}
}
