package cleanup

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestResolveBaseUsesOriginHEAD checks that origin/HEAD names the default and
// the comparison base is the remote-tracking ref. Why: a local main with
// unpushed commits must not make work look merged.
func TestResolveBaseUsesOriginHEAD(t *testing.T) {
	f := newFixture(t)
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "main" || b.Ref != "origin/main" || b.Fallback {
		t.Fatalf("got %+v", b)
	}
}

// TestResolveBaseFallbackChain checks that without origin/HEAD the local
// main, then master, still resolve to an origin/<default> base. Why: a clone
// without origin/HEAD is common and must not fail the whole cleanup.
func TestResolveBaseFallbackChain(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "set-head", "origin", "-d")
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "main" || b.Ref != "origin/main" || b.Fallback {
		t.Fatalf("no origin/HEAD: got %+v", b)
	}

	g := newFixture(t)
	g.git("branch", "-m", "main", "master")
	g.git("push", "-u", "origin", "master")
	gitIn(t, g.Origin, "symbolic-ref", "HEAD", "refs/heads/master")
	g.git("push", "origin", "--delete", "main")
	g.git("fetch", "--prune")
	// Newer git recreates origin/HEAD on fetch; delete it after.
	g.git("remote", "set-head", "origin", "-d")
	b, err = ResolveBase(Git{Dir: g.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "master" || b.Ref != "origin/master" || b.Fallback {
		t.Fatalf("master: got %+v", b)
	}
}

// TestResolveBaseLocalOnlyIsFallback checks that a repo with no remote falls
// back to the local default and says so. Why: callers must know the base is
// not the published state.
func TestResolveBaseLocalOnlyIsFallback(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "main" || b.Ref != "main" || !b.Fallback {
		t.Fatalf("got %+v", b)
	}
}

// TestResolveBaseNoneIsErrNoDefaultBranch checks that a repo with no default
// branch candidate fails closed. Why: an empty default would make downstream
// guards silently skip.
func TestResolveBaseNoneIsErrNoDefaultBranch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "r")
	gitIn(t, filepath.Dir(dir), "init", "-b", "topic", dir)
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "T")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	gitIn(t, dir, "commit", "--allow-empty", "-m", "x")
	_, err := ResolveBase(Git{Dir: dir})
	if !errors.Is(err, ErrNoDefaultBranch) {
		t.Fatalf("err = %v", err)
	}
}

// TestFetchFailureIsReported checks that a failing fetch comes back as a
// value with a message. Why: a stale base must be visible, never swallowed.
func TestFetchFailureIsReported(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	r := Fetch(Git{Dir: f.Dir})
	if r.OK || r.Err == "" {
		t.Fatalf("got %+v", r)
	}

	ok := Fetch(Git{Dir: newFixture(t).Dir})
	if !ok.OK || ok.At.IsZero() {
		t.Fatalf("good fetch: %+v", ok)
	}
}
