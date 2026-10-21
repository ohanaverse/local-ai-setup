package gitenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain isolates git for this package's own tests, which build
// repositories in temp directories.
func TestMain(m *testing.M) {
	IsolateForTest()
	os.Exit(m.Run())
}

// gitInit makes dir a git repository.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

// topLevel returns the work tree git finds from dir, and whether it found one.
func topLevel(dir string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// TestIsolateForTestUnsetsTheRepositoryVariables checks that IsolateForTest
// removes every variable that names a repository, and removes it rather than
// emptying it. One left set means a suite run from a git hook runs its
// `git init` and `git commit` against the developer's repository; one left
// empty makes every git command in the suite fail (an empty GIT_DIR is fatal
// to git).
func TestIsolateForTestUnsetsTheRepositoryVariables(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other.git")
	for _, name := range repoVars {
		t.Setenv(name, other)
	}
	t.Setenv(ceilingVar, os.Getenv(ceilingVar)) // IsolateForTest sets it again
	IsolateForTest()

	for _, name := range repoVars {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s is still set, to %q; want it unset", name, value)
		}
	}
	dir := t.TempDir()
	gitInit(t, dir)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("git init %s did not make a repository there: %v", dir, err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Errorf("git init ran against the inherited GIT_DIR %s", other)
	}
}

// TestSetenvOnPurposeSurvivesAndIsUndone checks the two halves of what a
// process-wide scrub means for a test that sets a repository variable itself
// (internal/smoke has two): the test sees its own value, and once it is over
// the variable is unset again, not left at the value or at the empty string.
// A leftover would point every later test in the binary at that repository.
func TestSetenvOnPurposeSurvivesAndIsUndone(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		t.Setenv("GIT_DIR", "/caller/.git")
		if got := os.Getenv("GIT_DIR"); got != "/caller/.git" {
			t.Fatalf("GIT_DIR = %q inside the test that set it", got)
		}
	})
	if value, set := os.LookupEnv("GIT_DIR"); set {
		t.Errorf("GIT_DIR is %q after the test that set it; want it unset", value)
	}
}

// TestIsolateForTestStopsAtEveryTempRoot checks that after IsolateForTest a
// fresh temp directory is outside every git repository even when the temp
// roots are inside a checkout: TMPDIR, where os.MkdirTemp("", ...) puts a
// directory, and GOTMPDIR, where t.TempDir() puts one when it is set. A root
// left out of the ceiling list means a developer who runs the suite with that
// variable pointing into a checkout gets the wt commit hook, a branch and a
// worktree written into that checkout by tests that believe they are outside
// a repository.
func TestIsolateForTestStopsAtEveryTempRoot(t *testing.T) {
	checkout := t.TempDir()
	gitInit(t, checkout)
	if resolved, err := filepath.EvalSymlinks(checkout); err == nil {
		checkout = resolved
	}
	inCheckout := func(t *testing.T, name string) string {
		t.Helper()
		dir := filepath.Join(checkout, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	for _, tc := range []struct {
		name             string
		tmpdir, gotmpdir bool // which variables point into the checkout
	}{
		{name: "TMPDIR", tmpdir: true},
		{name: "GOTMPDIR", gotmpdir: true},
		{name: "both", tmpdir: true, gotmpdir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOTMPDIR", "") // empty is unset, to os.MkdirTemp as to tempRoots
			if tc.tmpdir {
				t.Setenv("TMPDIR", inCheckout(t, tc.name+"-tmpdir"))
			}
			if tc.gotmpdir {
				t.Setenv("GOTMPDIR", inCheckout(t, tc.name+"-gotmpdir"))
			}

			fromTesting := t.TempDir()
			fromOS, err := os.MkdirTemp("", "wt-gitenv-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(fromOS) })

			// The case is only a test of the ceilings if the directories
			// really are below the checkout, and found there without them.
			if !strings.HasPrefix(fromTesting, checkout+string(os.PathSeparator)) {
				t.Fatalf("t.TempDir() = %s, want it below the checkout %s", fromTesting, checkout)
			}
			if tc.tmpdir && !strings.HasPrefix(fromOS, checkout+string(os.PathSeparator)) {
				t.Fatalf("os.MkdirTemp = %s, want it below the checkout %s", fromOS, checkout)
			}
			if _, found := topLevel(fromTesting); !found {
				t.Fatalf("before the ceilings moved, git in %s found no repository; the case proves nothing", fromTesting)
			}

			t.Setenv(ceilingVar, os.Getenv(ceilingVar)) // registers the restore
			IsolateForTest()

			for _, dir := range []string{fromTesting, fromOS} {
				if top, found := topLevel(dir); found {
					t.Errorf("git in %s found the repository %s; want none", dir, top)
				}
			}
			// A repository a test makes below a temp root is still found.
			own := filepath.Join(fromTesting, "own")
			gitInit(t, own)
			if top, found := topLevel(own); !found || top != own {
				t.Errorf("git in %s found %q (found = %v); want the repository made there", own, top, found)
			}
		})
	}
}

// TestIsolateForTestSetsTheNetworkGuards checks that IsolateForTest sets
// GIT_SSH_COMMAND to "false" and GIT_TERMINAL_PROMPT to "0", over whatever the
// developer's shell exported. Without them a test whose fixture names a real
// ssh remote (worktree.Enumerate fetches every remote) reaches the network
// with the developer's ssh keys, and one that hits a credential question
// waits on the terminal for an answer nobody gives.
func TestIsolateForTestSetsTheNetworkGuards(t *testing.T) {
	want := map[string]string{"GIT_SSH_COMMAND": "false", "GIT_TERMINAL_PROMPT": "0"}
	for name := range want {
		t.Setenv(name, "from-the-shell")
	}
	t.Setenv(ceilingVar, os.Getenv(ceilingVar)) // IsolateForTest sets it again
	IsolateForTest()

	for name, want := range want {
		if got, set := os.LookupEnv(name); !set || got != want {
			t.Errorf("%s = %q (set = %v); want %q", name, got, set, want)
		}
	}
}

// TestIsolateForTestStopsGitStartingSSH checks what the GIT_SSH_COMMAND guard
// is for: after IsolateForTest, a git command given an ssh URL fails at once
// and never runs ssh. A fake ssh first on PATH records each run, and the
// unguarded case shows git does run it for that URL, so the guarded case
// passing is the guard's doing and not the URL's. The fake exits without
// connecting, and the developer's global and system git config are switched
// off for the test so no url.insteadOf rewrites the URL to https.
func TestIsolateForTestStopsGitStartingSSH(t *testing.T) {
	bin := t.TempDir()
	ran := filepath.Join(t.TempDir(), "ssh-ran")
	fakeSSH := "#!/bin/sh\necho \"$@\" >> " + ran + "\nexit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	gitInit(t, repo)

	// lsRemote runs `git ls-remote` against an ssh URL on a host that cannot
	// resolve, and reports whether it failed and whether the fake ssh ran.
	lsRemote := func(t *testing.T) (failed, sshRan bool) {
		t.Helper()
		_ = os.Remove(ran)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		start := time.Now()
		err := exec.CommandContext(ctx, "git", "-C", repo, "ls-remote", "git@wt-gitenv-test.invalid:x/y.git").Run()
		if ctx.Err() != nil {
			t.Fatalf("git ls-remote was still running after %v; it must fail at once", time.Since(start))
		}
		_, statErr := os.Stat(ran)
		return err != nil, statErr == nil
	}

	t.Run("unguarded", func(t *testing.T) {
		// A developer's own ssh selectors must not decide this control: git
		// reads GIT_SSH_COMMAND, then GIT_SSH, before it looks at PATH.
		for _, v := range []string{"GIT_SSH_COMMAND", "GIT_SSH"} {
			t.Setenv(v, os.Getenv(v)) // registers the restore
			if err := os.Unsetenv(v); err != nil {
				t.Fatal(err)
			}
		}
		if _, sshRan := lsRemote(t); !sshRan {
			t.Fatal("without the guard git did not run the ssh on PATH; the guarded case proves nothing")
		}
	})

	t.Setenv(ceilingVar, os.Getenv(ceilingVar)) // IsolateForTest sets it again
	IsolateForTest()
	failed, sshRan := lsRemote(t)
	if !failed {
		t.Error("git ls-remote of an ssh URL succeeded under IsolateForTest; want it to fail")
	}
	if sshRan {
		t.Error("git ran ssh under IsolateForTest; GIT_SSH_COMMAND should have replaced it with false")
	}
}
