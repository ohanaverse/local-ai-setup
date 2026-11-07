package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// answerOnStdin makes os.Stdin a pipe that holds answer and ends, until the
// test ends: what a person typing one key on a terminal would send.
func answerOnStdin(t *testing.T, answer string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(answer); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

// captureNotes points osStderr at a buffer, where the question is printed.
func captureNotes(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := osStderr
	osStderr = &b
	t.Cleanup(func() { osStderr = old })
	return &b
}

// wantNothingCreated fails when name left a branch or a .worktrees/<name>
// directory in root.
func wantNothingCreated(t *testing.T, root, name string) {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "branch", "--list", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch %s exists: %s", name, out)
	}
	if fileExists(filepath.Join(root, ".worktrees", name)) {
		t.Errorf(".worktrees/%s exists", name)
	}
}

// TestWorktreeFlagUnknownNameWithoutTerminalRefusesNamingNew checks -W with a
// name that exists nowhere, and no terminal to ask on, refuses naming --new,
// so a typo in a script cannot create a branch; nothing is created or claimed.
func TestWorktreeFlagUnknownNameWithoutTerminalRefusesNamingNew(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, false)
	res := runPinnedLaunch(t, root, []string{"-W", "typo"})
	if res.err == nil || !strings.Contains(res.err.Error(), "--new") {
		t.Fatalf("err = %v; want a refusal naming --new", res.err)
	}
	if res.agentDir != "" || fileExists(ownClaim(root)) {
		t.Errorf("launched or claimed: agentDir=%q", res.agentDir)
	}
	wantNothingCreated(t, root, "typo")
}

// TestWorktreeFlagUnknownNameAskedOnATerminal checks on a terminal the
// question names the typed name and the base, no leaves nothing behind and
// ends quietly, and y creates the branch and worktree and launches in it.
func TestWorktreeFlagUnknownNameAskedOnATerminal(t *testing.T) {
	t.Run("no", func(t *testing.T) {
		root := sharedRootRepo(t)
		stubStdinTTY(t, true)
		answerOnStdin(t, "n")
		notes := captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"-W", "typo"})
		if res.err != nil {
			t.Fatalf("err = %v; want nil", res.err)
		}
		if !strings.Contains(notes.String(), `no branch or worktree named "typo"; create it from main?`) {
			t.Errorf("question = %q", notes.String())
		}
		if res.agentDir != "" || res.claimSeen || fileExists(ownClaim(root)) {
			t.Errorf("launched or claimed: agentDir=%q", res.agentDir)
		}
		wantNothingCreated(t, root, "typo")
	})
	t.Run("yes", func(t *testing.T) {
		root := sharedRootRepo(t)
		stubStdinTTY(t, true)
		answerOnStdin(t, "y")
		captureNotes(t)
		res := runPinnedLaunch(t, root, []string{"-W", "typo"})
		want := physical(t, filepath.Join(root, ".worktrees", "typo"))
		if res.err != nil || res.agentDir != want {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
		}
	})
}

// TestWorktreeFlagNewCreatesWithoutAsking checks --new is the answer already
// given: with an unknown name and a terminal available it creates and
// launches printing no question. Stdin is at end of input, so a question asked
// anyway would answer no and fail the launch check below, instead of reading
// the terminal this test runs from.
func TestWorktreeFlagNewCreatesWithoutAsking(t *testing.T) {
	root := sharedRootRepo(t)
	stubStdinTTY(t, true)
	answerOnStdin(t, "")
	notes := captureNotes(t)
	res := runPinnedLaunch(t, root, []string{"-W", "fresh", "--new"})
	want := physical(t, filepath.Join(root, ".worktrees", "fresh"))
	if res.err != nil || res.agentDir != want {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
	}
	if strings.Contains(notes.String(), "[y/N]") {
		t.Errorf("a question was printed: %q", notes.String())
	}
}

// TestWorktreeFlagExistingNamesNeverAsk checks the names that are not a
// creation (an existing local branch with no worktree, an existing worktree)
// launch with no question and no terminal, and the root's branch is still
// refused with the --root remedy rather than the creation question.
func TestWorktreeFlagExistingNamesNeverAsk(t *testing.T) {
	t.Run("local branch without a worktree", func(t *testing.T) {
		root := sharedRootRepo(t)
		if out, err := exec.Command("git", "-C", root, "branch", "topic").CombinedOutput(); err != nil {
			t.Fatalf("branch: %v\n%s", err, out)
		}
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "topic"})
		want := physical(t, filepath.Join(root, ".worktrees", "topic"))
		if res.err != nil || res.agentDir != want {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, want)
		}
	})
	t.Run("existing worktree", func(t *testing.T) {
		root := sharedRootRepo(t)
		linked := addLinkedWorktree(t, root, "feat")
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "feat"})
		if res.err != nil || res.agentDir != linked {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
		}
	})
	t.Run("root branch", func(t *testing.T) {
		root := sharedRootRepo(t)
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "main"})
		if res.err == nil || !strings.Contains(res.err.Error(), "--root") || strings.Contains(res.err.Error(), "--new") {
			t.Fatalf("err = %v; want the --root refusal", res.err)
		}
	})
}

// TestWorktreeFlagInvalidNameErrorsBeforeTheQuestion checks a name
// EnsureForName rejects fails with that error on a terminal without being
// asked first, so "y" is never the way to learn the name was bad.
func TestWorktreeFlagInvalidNameErrorsBeforeTheQuestion(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{name: "origin/main", want: "default branch"},
		{name: strings.Repeat("a", 300), want: "too long"},
	} {
		t.Run(tc.name[:min(len(tc.name), 12)], func(t *testing.T) {
			root := sharedRootRepo(t)
			// origin/main is a default-branch form only when origin exists,
			// has a remote-tracking main, and origin/HEAD names it: the
			// refusal is gated on DefaultBranch, which reads origin/HEAD.
			for _, args := range [][]string{
				{"remote", "add", "origin", root},
				{"update-ref", "refs/remotes/origin/main", "HEAD"},
				{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
			} {
				if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			stubStdinTTY(t, true)
			notes := captureNotes(t)
			res := runPinnedLaunch(t, root, []string{"-W", tc.name})
			if res.err == nil {
				t.Fatal("the launch succeeded; want the name rejected")
			}
			if !strings.Contains(res.err.Error(), tc.want) {
				t.Errorf("err = %v; want it to say %q", res.err, tc.want)
			}
			if strings.Contains(res.err.Error(), "--new") || strings.Contains(notes.String(), "[y/N]") {
				t.Errorf("asked before rejecting: err=%v notes=%q", res.err, notes.String())
			}
		})
	}
}

// TestNewFlagUsage checks --new is only meaningful with -W: alone, with --cwd
// or with --root it is a usage error and nothing launches; with an existing
// name it changes nothing.
func TestNewFlagUsage(t *testing.T) {
	for _, where := range [][]string{{"--new"}, {"--new", "--cwd"}, {"--new", "--root"}} {
		t.Run(strings.Join(where, " "), func(t *testing.T) {
			root := sharedRootRepo(t)
			stubStdinTTY(t, false)
			res := runPinnedLaunch(t, root, where)
			if res.err == nil || !strings.Contains(res.err.Error(), "--new") {
				t.Fatalf("err = %v; want a usage error naming --new", res.err)
			}
			if res.agentDir != "" {
				t.Errorf("the agent ran in %q", res.agentDir)
			}
		})
	}
	t.Run("existing name is a no-op", func(t *testing.T) {
		root := sharedRootRepo(t)
		linked := addLinkedWorktree(t, root, "feat")
		stubStdinTTY(t, false)
		res := runPinnedLaunch(t, root, []string{"-W", "feat", "--new"})
		if res.err != nil || res.agentDir != linked {
			t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
		}
	})
}

// TestConfirmCreate checks the question itself: --new and y say yes, n is
// errCancelled, and with no terminal and no --new the error names --new
// without reading input.
func TestConfirmCreate(t *testing.T) {
	var out bytes.Buffer
	if err := confirmCreate("x", "from main", true, failReader{t}, &out, false); err != nil || out.Len() != 0 {
		t.Errorf("--new: err=%v out=%q", err, out.String())
	}
	if err := confirmCreate("x", "from main", false, strings.NewReader("y"), &out, true); err != nil {
		t.Errorf("y: err=%v", err)
	}
	if err := confirmCreate("x", "from main", false, strings.NewReader("n"), &out, true); err != errCancelled {
		t.Errorf("n: err=%v; want errCancelled", err)
	}
	err := confirmCreate("x", "from main", false, failReader{t}, &out, false)
	if err == nil || !strings.Contains(err.Error(), `no branch or worktree named "x"; pass --new to create it`) {
		t.Errorf("no terminal: err=%v", err)
	}
}

// TestOtherCreationPathsDoNotAsk checks the shared-root "w" choice creates its
// worktree with no creation question: it already is an explicit choice, and
// its generated name could never be a typo. (The picker's new-worktree prompt
// calls EnsureForName directly, which has no question to ask.)
func TestOtherCreationPathsDoNotAsk(t *testing.T) {
	root := sharedRootRepo(t)
	liveOtherClaim(t, root, "codex")
	var out bytes.Buffer
	dir, release, err := confirmSharedRoot(root, "claude", false, strings.NewReader("w"), &out, true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !fileExists(dir) || strings.Contains(out.String(), "no branch or worktree named") {
		t.Errorf("dir=%q out=%q; want a created worktree and no creation question", dir, out.String())
	}
}

// TestConfirmCreateRendersTheStartPhrase checks the question joins "create
// it" with worktree.WouldCreate's start phrase verbatim, so an unborn
// repository is asked about "an empty orphan branch with no shared history" —
// never "from <branch>", which git would not use there (it infers --orphan).
func TestConfirmCreateRendersTheStartPhrase(t *testing.T) {
	for _, c := range []struct{ start, want string }{
		{start: "from main", want: `no branch or worktree named "x"; create it from main?`},
		{start: "as an empty orphan branch with no shared history (this repository has no commits yet)",
			want: `no branch or worktree named "x"; create it as an empty orphan branch with no shared history (this repository has no commits yet)?`},
	} {
		t.Run(c.start, func(t *testing.T) {
			var out bytes.Buffer
			if err := confirmCreate("x", c.start, false, strings.NewReader("n"), &out, true); err != errCancelled {
				t.Fatalf("err = %v; want errCancelled", err)
			}
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("question = %q; want it to contain %q", out.String(), c.want)
			}
		})
	}
}
