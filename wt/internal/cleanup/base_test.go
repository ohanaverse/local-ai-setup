package cleanup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// TestResolveBaseUsesOriginHEAD checks that origin/HEAD, not the local main,
// names the default and the base is the remote-tracking ref. Why: the remote's
// idea of the default is authoritative; origin/HEAD points at develop while a
// local main also exists, so skipping that step picks the wrong branch.
func TestResolveBaseUsesOriginHEAD(t *testing.T) {
	f := newFixture(t)
	f.Branch("develop")
	f.Push("develop")
	f.git("remote", "set-head", "origin", "develop")
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "develop" || b.Ref != "origin/develop" || b.Fallback {
		t.Fatalf("got %+v", b)
	}
}

// TestResolveBaseDanglingOriginHEAD checks that an origin/HEAD naming a
// deleted remote-tracking branch is skipped, not trusted. Why: a stale symref
// must fall through the chain rather than yield a base that does not exist.
func TestResolveBaseDanglingOriginHEAD(t *testing.T) {
	f := newFixture(t)
	f.Branch("develop")
	f.Push("develop")
	f.git("remote", "set-head", "origin", "develop")
	f.git("update-ref", "-d", "refs/remotes/origin/develop")
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "main" || b.Ref != "origin/main" || b.Fallback {
		t.Fatalf("got %+v", b)
	}
}

// TestResolveBaseRemoteOnly checks that with no local main or master but an
// origin/main, the remote branch is the base. Why: a clone that checked out
// only a topic branch still has a default to compare against.
func TestResolveBaseRemoteOnly(t *testing.T) {
	f := newFixture(t)
	f.git("checkout", "-b", "topic")
	f.git("branch", "-D", "main")
	f.git("remote", "set-head", "origin", "-d")
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "main" || b.Ref != "origin/main" || b.Fallback {
		t.Fatalf("got %+v", b)
	}
}

// TestResolveBasePrefersMainOverMaster checks that a repo with both local
// branches and no origin/HEAD resolves main. Why: the chain order is the
// spec's and must be deterministic.
func TestResolveBasePrefersMainOverMaster(t *testing.T) {
	f := newFixture(t)
	f.Branch("master")
	f.git("remote", "set-head", "origin", "-d")
	b, err := ResolveBase(Git{Dir: f.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if b.Default != "main" || b.Ref != "origin/main" || b.Fallback {
		t.Fatalf("got %+v", b)
	}
}

// TestResolveBaseGitErrorIsNotNoDefault checks that a fatal git failure (not a
// repository) is returned as its own error. Why: reading it as "no default
// branch" would blame the repo layout for a broken or missing repository.
func TestResolveBaseGitErrorIsNotNoDefault(t *testing.T) {
	_, err := ResolveBase(Git{Dir: t.TempDir()})
	if err == nil || errors.Is(err, ErrNoDefaultBranch) {
		t.Fatalf("err = %v", err)
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

// advanceOrigin pushes one new commit to f's origin main from a separate
// clone and returns it, so f's origin/main is stale until f fetches origin.
func advanceOrigin(t *testing.T, f *fixture) string {
	t.Helper()
	c := filepath.Join(t.TempDir(), "other-clone")
	gitIn(t, filepath.Dir(c), "clone", f.Origin, c)
	gitIn(t, c, "config", "user.email", "t@example.com")
	gitIn(t, c, "config", "user.name", "T")
	gitIn(t, c, "config", "commit.gpgsign", "false")
	gitIn(t, c, "commit", "--allow-empty", "-m", "upstream moved")
	gitIn(t, c, "push", "origin", "main")
	return gitIn(t, c, "rev-parse", "HEAD")
}

// TestFetchNamesOrigin checks out a branch whose upstream is a second remote
// and fetches. Why: a bare `git fetch --prune` fetches the current branch's
// upstream remote, not origin, so origin/<default> (the base) stayed stale
// while the header said "fetch: ok".
func TestFetchNamesOrigin(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(t.TempDir(), "other.git")
	gitIn(t, filepath.Dir(other), "init", "--bare", "-b", "main", other)
	f.git("remote", "add", "other", other)
	f.git("checkout", "-b", "topic")
	f.git("push", "-u", "other", "topic")
	moved := advanceOrigin(t, f)

	if r := Fetch(Git{Dir: f.Dir}); !r.OK {
		t.Fatalf("fetch: %+v", r)
	}
	if got := f.git("rev-parse", "refs/remotes/origin/main"); got != moved {
		t.Fatalf("origin/main = %s after Fetch, want %s (origin was not fetched)", got, moved)
	}
}

// TestFetchWithoutOriginIsSkipped removes the only (origin) remote and fetches. Why:
// the base is origin's; with no origin there is nothing to fetch, and
// "FAILED (fatal: no remote ...)" on every run would read as a broken fetch.
// It is a skip with its cause, never ok and never a failure.
func TestFetchWithoutOriginIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	r := Fetch(Git{Dir: f.Dir})
	if r.OK || r.Err != "" || r.Skipped != "no remotes" || !r.At.IsZero() {
		t.Fatalf("got %+v, want skipped (no remotes), not ok, no error, no time", r)
	}
}

// TestFetchTimeoutIsNamed gives the fetch a timeout it cannot meet. Why: a
// timed-out fetch must say so in the FAILED text ("timed out after ..."),
// not a bare "context deadline exceeded".
func TestFetchTimeoutIsNamed(t *testing.T) {
	f := newFixture(t)
	// An upload-pack that sleeps holds the fetch past the timeout.
	f.git("config", "remote.origin.uploadpack", "sleep 5; git-upload-pack")
	r := Fetch(Git{Dir: f.Dir, Timeout: 200 * time.Millisecond})
	if r.OK || r.Err != "git fetch --prune origin timed out after 200ms" {
		t.Fatalf("got %+v, want a failure naming the timeout", r)
	}
	// The remote listing has its own floor: the 200ms must not bind it.
	if len(r.Remotes) == 0 {
		t.Fatalf("got %+v, want the remotes listed despite the short timeout", r)
	}
}

// TestFetchListingNotBoundByFetchTimeout gives a healthy repo a 1ns timeout.
// Why: the one timeout is meant for the fetch; a tiny value must not make
// `git remote` fail, so the failure names the fetch and the remotes are
// still listed.
func TestFetchListingNotBoundByFetchTimeout(t *testing.T) {
	f := newFixture(t)
	r := Fetch(Git{Dir: f.Dir, Timeout: time.Nanosecond})
	if len(r.Remotes) == 0 || r.Remotes[0].Name != "origin" {
		t.Fatalf("got %+v, want origin listed", r)
	}
	if strings.Contains(r.Err, "git remote") || !strings.Contains(r.Err, "git fetch --prune origin") {
		t.Fatalf("got Err %q, want the failure to name the fetch", r.Err)
	}
}

// TestRemoteListErrTextNamesTimeout feeds the listing's deadline error to
// the formatter Fetch uses. Why: the 30s floor cannot be lowered in a test,
// so the branch is reached here; dropping it would print a bare
// "context deadline exceeded".
func TestRemoteListErrTextNamesTimeout(t *testing.T) {
	got := remoteListErrText(fmt.Errorf("run: %w", context.DeadlineExceeded), remoteListTimeout)
	if got != "git remote timed out after 30s" {
		t.Fatalf("got %q, want the timeout named", got)
	}
	if got := remoteListErrText(errors.New("boom"), remoteListTimeout); strings.Contains(got, "timed out") {
		t.Fatalf("got %q, a non-timeout error must not claim a timeout", got)
	}
}

func TestTimedOutText(t *testing.T) {
	for _, c := range []struct {
		args string
		d    time.Duration
		want string
	}{
		{"remote", 30 * time.Second, "git remote timed out after 30s"},
		{"fetch --prune origin", 200 * time.Millisecond, "git fetch --prune origin timed out after 200ms"},
	} {
		if got := timedOutText(c.args, c.d); got != c.want {
			t.Errorf("timedOutText(%q, %s) = %q, want %q", c.args, c.d, got, c.want)
		}
	}
}

// addBareRemote adds a bare repository as remote name of f and returns its
// path (a branch "topic" pushed to it when push is set).
func addBareRemote(t *testing.T, f *fixture, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".git")
	gitIn(t, filepath.Dir(p), "init", "--bare", "-b", "main", p)
	f.git("remote", "add", name, p)
	f.git("push", name, "main")
	return p
}

// TestFetchEveryRemote fetches a repo with origin and a second remote that
// both moved. What: both tracking refs advance and the result lists both.
// Why: the spec says "fetch --all"; the "pushed" test reads every
// refs/remotes/*, so a stale second remote made unpushed counts wrong, and
// the header must say which remotes were fetched.
func TestFetchEveryRemote(t *testing.T) {
	f := newFixture(t)
	other := addBareRemote(t, f, "upstream")
	c := filepath.Join(t.TempDir(), "c")
	gitIn(t, filepath.Dir(c), "clone", other, c)
	gitIn(t, c, "config", "user.email", "t@example.com")
	gitIn(t, c, "config", "user.name", "T")
	gitIn(t, c, "commit", "--allow-empty", "-m", "x")
	gitIn(t, c, "push", "origin", "main")
	moved := gitIn(t, c, "rev-parse", "HEAD")
	advanceOrigin(t, f)

	r := Fetch(Git{Dir: f.Dir})
	if !r.OK || len(r.Remotes) != 2 || !r.Remotes[0].OK || !r.Remotes[1].OK {
		t.Fatalf("got %+v, want both remotes ok", r)
	}
	if got := f.git("rev-parse", "refs/remotes/upstream/main"); got != moved {
		t.Fatalf("upstream/main = %s, want %s (second remote not fetched)", got, moved)
	}
}

// TestFetchOneRemoteFailing breaks the second remote. What: origin is still
// fetched and Fetch.OK stays true, while Remotes names the failure. Why: a
// partial failure must be reported truthfully, and must not hide the remote
// the base and the origin rows depend on.
func TestFetchOneRemoteFailing(t *testing.T) {
	f := newFixture(t)
	addBareRemote(t, f, "upstream")
	f.git("remote", "set-url", "upstream", filepath.Join(t.TempDir(), "missing.git"))
	r := Fetch(Git{Dir: f.Dir})
	if !r.OK {
		t.Fatalf("origin fetched fine, OK should hold: %+v", r)
	}
	var bad *RemoteFetch
	for i := range r.Remotes {
		if r.Remotes[i].Name == "upstream" {
			bad = &r.Remotes[i]
		}
	}
	if bad == nil || bad.OK || bad.Err == "" {
		t.Fatalf("upstream should be a named failure: %+v", r.Remotes)
	}
}

// TestFetchOriginFailingOtherOK breaks origin only. What: OK is false though
// the other remote fetched. Why: Fetch.OK is origin's freshness; the base and
// the origin rows depend on it.
func TestFetchOriginFailingOtherOK(t *testing.T) {
	f := newFixture(t)
	addBareRemote(t, f, "upstream")
	f.git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	r := Fetch(Git{Dir: f.Dir})
	if r.OK || r.Err == "" {
		t.Fatalf("got %+v, want origin failure", r)
	}
	for _, rm := range r.Remotes {
		if rm.Name == "upstream" && !rm.OK {
			t.Fatalf("upstream should still be fetched: %+v", rm)
		}
	}
}

// TestFetchSkipFetchAllRemote marks a remote skipFetchAll. What: it is listed
// as skipped and not fetched. Why: the user opted that remote out of `fetch
// --all`; honouring it keeps "every remote" the same set git means.
func TestFetchSkipFetchAllRemote(t *testing.T) {
	f := newFixture(t)
	addBareRemote(t, f, "upstream")
	f.git("config", "remote.upstream.skipFetchAll", "true")
	r := Fetch(Git{Dir: f.Dir})
	for _, rm := range r.Remotes {
		if rm.Name == "upstream" && !rm.Skipped {
			t.Fatalf("upstream should be skipped: %+v", rm)
		}
	}
	if !r.OK {
		t.Fatalf("%+v", r)
	}
}

// TestFetchOriginIgnoresSkipFetchAll marks origin skipFetchAll and moves
// origin. What: Fetch still fetches origin (OK, no Skipped) and the tracking
// ref follows. Why: a plain `git fetch origin` ignores skipFetchAll, and the
// base and every origin row are origin's; skipping it would make all of them
// unverifiable for a setting that only means "leave me out of --all".
func TestFetchOriginIgnoresSkipFetchAll(t *testing.T) {
	f := newFixture(t)
	f.git("config", "remote.origin.skipFetchAll", "true")
	moved := advanceOrigin(t, f)
	r := Fetch(Git{Dir: f.Dir})
	if !r.OK || r.Skipped != "" {
		t.Fatalf("origin should be fetched: %+v", r)
	}
	if got := f.git("rev-parse", "refs/remotes/origin/main"); got != moved {
		t.Fatalf("origin/main = %s, want %s", got, moved)
	}
}

// TestFetchWithoutAnyRemote has no remote at all. What: Skipped says so.
// Why: nothing to fetch is not a failure.
func TestFetchWithoutAnyRemote(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	if r := Fetch(Git{Dir: f.Dir}); r.Skipped != "no remotes" || r.OK {
		t.Fatalf("got %+v", r)
	}
}

// TestFetchNoOriginStillFetchesOthers has a remote but no origin. What: the
// other is fetched and Skipped names the missing origin. Why: the header must
// still say which remotes were fetched.
func TestFetchNoOriginStillFetchesOthers(t *testing.T) {
	f := newFixture(t)
	addBareRemote(t, f, "upstream")
	f.git("remote", "remove", "origin")
	r := Fetch(Git{Dir: f.Dir})
	if r.Skipped != "no origin remote" || r.OK || len(r.Remotes) != 1 || !r.Remotes[0].OK {
		t.Fatalf("got %+v", r)
	}
}

// TestFetchOtherRemotesRunConcurrentlyWithAShorterTimeout adds two remotes
// whose upload-pack sleeps. What: with the non-origin timeout at 300ms both
// are reported as timed out after 300ms, origin is fetched OK, the run takes
// well under the sum of two sleeps, and Remotes keeps `git remote` order.
// Why: origin alone drives classification; a slow fork or upstream must not
// hold the command for origin's full window, nor serially.
func TestFetchOtherRemotesRunConcurrentlyWithAShorterTimeout(t *testing.T) {
	old := otherFetchTimeout
	otherFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { otherFetchTimeout = old })
	f := newFixture(t)
	for _, n := range []string{"slow1", "slow2"} {
		addBareRemote(t, f, n)
		f.git("config", "remote."+n+".uploadpack", "sleep 5; git-upload-pack")
	}
	start := time.Now()
	r := Fetch(Git{Dir: f.Dir})
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s: other remotes did not time out early", d)
	}
	if !r.OK || len(r.Remotes) != 3 || r.Remotes[0].Name != "origin" || !r.Remotes[0].OK {
		t.Fatalf("got %+v, want origin ok first", r)
	}
	for _, rm := range r.Remotes[1:] {
		if rm.OK || !strings.Contains(rm.Err, "timed out after 300ms") {
			t.Fatalf("%s: %+v, want a 300ms timeout", rm.Name, rm)
		}
	}
	if r.Remotes[1].Name != "slow1" || r.Remotes[2].Name != "slow2" {
		t.Fatalf("order %+v", r.Remotes)
	}
}

// TestRecordFetchNilHookFetchesPerRemote runs RecordFetch with no hook against
// the fixture's real origin. What: ok, At stamped, origin in Remotes, and
// worktree.LastFetched at the root moves. Why: the one fetch mechanism is
// Fetch; the nil hook must not be a second, weaker one (#586).
func TestRecordFetchNilHookFetchesPerRemote(t *testing.T) {
	f := newFixture(t)
	r := RecordFetch(nil, f.Dir)
	if !r.OK || r.Err != "" || r.At.IsZero() || len(r.Remotes) != 1 || r.Remotes[0].Name != "origin" || !r.Remotes[0].OK {
		t.Fatalf("got %+v, want ok with origin's per-remote result", r)
	}
	got, ok := worktree.LastFetched(f.Dir)
	if !ok || time.Since(got) > time.Minute {
		t.Fatalf("LastFetched = %v, %v; want a stamp from the fetch that just ran", got, ok)
	}
}

// TestRecordFetchNilHookDeadRemote: a dead origin is a value (Err, At set),
// never a panic or an error return.
func TestRecordFetchNilHookDeadRemote(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	r := RecordFetch(nil, f.Dir)
	if r.OK || r.Err == "" || r.At.IsZero() || strings.Contains(r.Err, "\n") {
		t.Fatalf("got %+v, want a one-line Err and a time", r)
	}
}

// TestRecordFetchNilHookNoRemotes: an explicit fetch with no remotes is
// Skipped "no remotes", At is stamped (the user did ask), never ok or Err.
func TestRecordFetchNilHookNoRemotes(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "remove", "origin")
	r := RecordFetch(nil, f.Dir)
	if r.OK || r.Err != "" || r.Skipped != "no remotes" || r.At.IsZero() {
		t.Fatalf("got %+v, want skipped (no remotes) with a time", r)
	}
	if hdr := strings.Join(HeaderLines(Report{Repo: f.Dir, Fetch: r}, time.Now()), "\n"); !strings.Contains(hdr, "fetch: skipped (no remotes)") {
		t.Fatalf("the header does not say the fetch was skipped:\n%s", hdr)
	}
}

// TestRecordFetchHookKeepsItsWording: a non-nil hook is what runs, with the
// ok/Err wording it always had.
func TestRecordFetchHookKeepsItsWording(t *testing.T) {
	if r := RecordFetch(func(string) error { return nil }, "x"); !r.OK || r.At.IsZero() {
		t.Fatalf("ok hook: %+v", r)
	}
	if r := RecordFetch(func(string) error { return errors.New("boom") }, "x"); r.OK || r.Err != "boom" {
		t.Fatalf("failing hook: %+v", r)
	}
}

// TestFetchNewAndPrunedBranches pins Fetch's ref refresh against a real
// remote: a branch a teammate pushed becomes a remote-tracking ref, and one
// deleted upstream is pruned instead of lingering as a stale row.
func TestFetchNewAndPrunedBranches(t *testing.T) {
	f := newFixture(t)
	f.git("checkout", "-b", "gone-soon")
	f.git("push", "origin", "gone-soon")
	f.git("checkout", "main")
	f.git("branch", "-D", "gone-soon")
	gitIn(t, f.Origin, "branch", "-D", "gone-soon")
	c := filepath.Join(t.TempDir(), "mate")
	gitIn(t, filepath.Dir(c), "clone", f.Origin, c)
	gitIn(t, c, "checkout", "-b", "teammate-feature")
	gitIn(t, c, "push", "origin", "teammate-feature")

	if f.git("for-each-ref", "refs/remotes/origin/gone-soon") == "" {
		t.Fatal("origin/gone-soon missing before the fetch; setup drifted, nothing to prune")
	}
	if r := Fetch(Git{Dir: f.Dir}); !r.OK {
		t.Fatalf("fetch: %+v", r)
	}
	if f.git("for-each-ref", "refs/remotes/origin/teammate-feature") == "" {
		t.Fatal("origin/teammate-feature missing after the fetch")
	}
	if got := f.git("for-each-ref", "refs/remotes/origin/gone-soon"); got != "" {
		t.Fatalf("origin/gone-soon should have been pruned: %s", got)
	}
}

// TestLastFetchedPerRoot: a fetch at a root advances that root's stamp only;
// a fresh linked worktree has none until it fetches (LastFetched is per
// worktree, FETCH_HEAD being one per worktree).
func TestLastFetchedPerRoot(t *testing.T) {
	f := newFixture(t)
	linked := filepath.Join(t.TempDir(), "linked")
	f.git("worktree", "add", "-b", "linked-stamp", linked)
	if got, ok := worktree.LastFetched(linked); ok {
		t.Fatalf("LastFetched at a fresh linked root = %v, true; want false", got)
	}
	if r := RecordFetch(nil, linked); !r.OK {
		t.Fatalf("fetch at linked: %+v", r)
	}
	if _, ok := worktree.LastFetched(linked); !ok {
		t.Fatal("LastFetched after a fetch at the linked root = false; want true")
	}
}
