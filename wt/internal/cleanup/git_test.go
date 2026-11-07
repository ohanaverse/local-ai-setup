package cleanup

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestRunReportsExitCodeWithoutError checks that a non-zero git exit lands in
// Out.Code with a nil error: the evidence ladder reads exit 1 from
// merge-base --is-ancestor as an answer, not a failure.
func TestRunReportsExitCodeWithoutError(t *testing.T) {
	f := newFixture(t)
	base := f.Commit("a.txt", "a", "base")
	f.Branch("topic")
	f.Checkout("topic")
	topic := f.Commit("b.txt", "b", "topic work")
	g := Git{Dir: f.Dir}

	out, err := g.Run("merge-base", "--is-ancestor", topic, base)
	if err != nil || out.Code != 1 {
		t.Fatalf("non-ancestor: Code=%d err=%v, want 1 and nil", out.Code, err)
	}
	out, err = g.Run("merge-base", "--is-ancestor", base, topic)
	if err != nil || out.Code != 0 {
		t.Fatalf("ancestor: Code=%d err=%v, want 0 and nil", out.Code, err)
	}
	out, err = g.Run("merge-base", "--is-ancestor", "no-such-rev", base)
	if err != nil || out.Code != 128 {
		t.Fatalf("bad revision: Code=%d err=%v, want 128 and nil", out.Code, err)
	}
}

// TestRunTimeoutIsAnError checks that a git process outliving Timeout is an
// error, not an exit code, so a hung git can never read as an answer.
func TestRunTimeoutIsAnError(t *testing.T) {
	f := newFixture(t)
	f.Commit("a.txt", "a", "base")
	g := Git{Dir: f.Dir, Timeout: 50 * time.Millisecond}
	if _, err := g.Run("-c", "alias.nap=!sleep 5", "nap"); err == nil {
		t.Fatal("want an error from a timed-out git, got nil")
	}
}

// TestGitRunTurnsOptionalLocksOff checks every git call the classifier makes
// carries GIT_OPTIONAL_LOCKS=0 (observed through a shell alias, which sees git's
// environment). Why: the package promises to write no index; without this
// `git status` may refresh the index's stat cache in the worktree it inspects,
// which `wt rescue` found by comparing a source's index bytes before and after.
func TestGitRunTurnsOptionalLocksOff(t *testing.T) {
	f := newFixture(t)
	o, err := Git{Dir: f.Dir}.Run("-c", "alias.optlocks=!echo $GIT_OPTIONAL_LOCKS", "optlocks")
	if err != nil || o.Code != 0 {
		t.Fatalf("run: %v %+v", err, o)
	}
	if got := strings.TrimSpace(o.Stdout); got != "0" {
		t.Errorf("GIT_OPTIONAL_LOCKS = %q, want 0", got)
	}
}

// TestGitConfigAndEnvApplyToEveryCall checks Git.Config becomes `-c k=v`
// options before the subcommand and Git.Env is added to the child's
// environment (observed through git config and a shell alias). Why: the
// rescue engine reads a source it must not change and needs
// core.fsmonitor=false and its own environment on every call, through this
// one runner instead of a second copy of it.
func TestGitConfigAndEnvApplyToEveryCall(t *testing.T) {
	f := newFixture(t)
	f.Commit("a.txt", "a", "base")
	g := Git{Dir: f.Dir, Config: []string{"core.fsmonitor=false", "rescue.probe=yes"}, Env: []string{"RESCUE_PROBE=seen"}}

	out, err := g.Run("config", "--get", "rescue.probe")
	if err != nil || out.Code != 0 || strings.TrimSpace(out.Stdout) != "yes" {
		t.Fatalf("config probe: %+v err=%v, want yes", out, err)
	}
	out, err = g.Run("-c", "alias.env=!echo $RESCUE_PROBE", "env")
	if err != nil || strings.TrimSpace(out.Stdout) != "seen" {
		t.Fatalf("env probe: %+v err=%v, want seen", out, err)
	}
	if _, err := (Git{Dir: f.Dir}).Run("config", "--get", "rescue.probe"); err != nil {
		t.Fatal(err)
	}
}

// TestNonInteractiveEnvReachesGit runs a git alias that prints the prompt
// variable through Git{Env: NonInteractive(f.Dir)}. What: the env reaches the
// child git. Why: the spec and `wt cleanup --help` promise a fetch that never
// prompts (GIT_TERMINAL_PROMPT=0), but Git.Run set nothing, so a credential
// prompt could reach /dev/tty; the push added in PR C must not prompt either.
func TestNonInteractiveEnvReachesGit(t *testing.T) {
	f := newFixture(t)
	o, err := Git{Dir: f.Dir, Env: NonInteractive(f.Dir)}.Run("-c", `alias.e=!echo "$GIT_TERMINAL_PROMPT $GCM_INTERACTIVE"`, "e")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(o.Stdout); len(got) != 2 || got[0] != "0" || got[1] != "never" {
		t.Fatalf("got %q, want 0 and never", o.Stdout)
	}
}

// TestNonInteractiveSSHBatchMode checks the ssh half of the no-prompt env.
// What: with neither GIT_SSH_COMMAND nor core.sshCommand set, git's ssh runs
// with BatchMode=yes (no passphrase or host-key prompt); a user's own
// GIT_SSH_COMMAND or core.sshCommand (repository config) is left alone.
// Why: GIT_TERMINAL_PROMPT=0 does not stop ssh asking on /dev/tty, and
// overriding a user's ssh command would break their setup.
func TestNonInteractiveSSHBatchMode(t *testing.T) {
	has := func(env []string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, "GIT_SSH_COMMAND=") && strings.Contains(e, "BatchMode=yes") {
				return true
			}
		}
		return false
	}
	t.Run("unset", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "")
		os.Unsetenv("GIT_SSH_COMMAND")
		f := newFixture(t)
		if !has(NonInteractive(f.Dir)) {
			t.Fatalf("no BatchMode in %v", NonInteractive(f.Dir))
		}
	})
	t.Run("env set", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "ssh -i mykey")
		f := newFixture(t)
		for _, e := range NonInteractive(f.Dir) {
			if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
				t.Fatalf("overrides the user's GIT_SSH_COMMAND: %s", e)
			}
		}
	})
	t.Run("core.sshCommand set", func(t *testing.T) {
		t.Setenv("GIT_SSH_COMMAND", "")
		os.Unsetenv("GIT_SSH_COMMAND")
		f := newFixture(t)
		f.git("config", "core.sshCommand", "ssh -i mykey")
		for _, e := range NonInteractive(f.Dir) {
			if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
				t.Fatalf("overrides core.sshCommand: %s", e)
			}
		}
	})
}
