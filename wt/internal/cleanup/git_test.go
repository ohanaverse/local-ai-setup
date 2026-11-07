package cleanup

import (
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
