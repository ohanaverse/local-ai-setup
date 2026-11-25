package launchwarn

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// prRepo is a cloned repository on a github.com origin with branch topic one
// commit past main; it returns the root, topic's tip and main's tip.
func prRepo(t *testing.T, origin string) (root, tip, base string) {
	t.Helper()
	root = cloneRepo(t)
	git(t, root, "remote", "set-url", "origin", origin)
	git(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	base = revParse(t, root, "HEAD")
	git(t, root, "switch", "-q", "-c", "topic")
	git(t, root, "commit", "-q", "--allow-empty", "-m", "work")
	tip = revParse(t, root, "HEAD")
	return root, tip, base
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func prJSON(rows ...string) []byte { return []byte("[" + strings.Join(rows, ",") + "]") }

func prRow(n int, state, head string) string {
	return fmt.Sprintf(`{"number":%d,"state":%q,"headRefOid":%q,"baseRefName":"main","mergeCommit":null,`+
		`"headRepository":{"name":"r","nameWithOwner":"o/r"},"headRepositoryOwner":{"login":"o"},"isCrossRepository":false}`, n, state, head)
}

const ghOrigin = "https://github.com/o/r.git"

func resolveWith(t *testing.T, dir, branch string, list func(string, string) ([]byte, error)) Target {
	t.Helper()
	return StartPRLookup(dir, branch, list).Resolve(Target{Dir: dir, Branch: branch}, 5*time.Second)
}

// TestPRLookupMergedAnswerWarns checks that a merged PR whose head is the
// branch tip comes back as PRState merged with its number, so Collect warns
// "PR #N is merged" (#585). Why: a launch by flag has no picker row to carry
// the fact, and this is the whole feature.
func TestPRLookupMergedAnswerWarns(t *testing.T) {
	root, tip, _ := prRepo(t, ghOrigin)
	got := resolveWith(t, root, "topic", func(string, string) ([]byte, error) { return prJSON(prRow(7, "MERGED", tip)), nil })
	if got.PRState != "merged" || got.PRNumber != 7 {
		t.Fatalf("PRState=%q PRNumber=%d, want merged #7", got.PRState, got.PRNumber)
	}
	if w, _ := Collect(got); len(w) != 1 || !strings.Contains(w[0].Text, "PR #7 is merged") {
		t.Errorf("warnings = %+v, want the pr_merged warning", w)
	}
}

// TestPRLookupMergedHeadInHistoryWarns checks a merged PR whose head is an
// ancestor of the tip (more commits since) still warns. Why: the branch
// carries the PR's work, so it is the PR's branch.
func TestPRLookupMergedHeadInHistoryWarns(t *testing.T) {
	root, tip, _ := prRepo(t, ghOrigin)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "more")
	got := resolveWith(t, root, "topic", func(string, string) ([]byte, error) { return prJSON(prRow(7, "MERGED", tip)), nil })
	if got.PRState != "merged" {
		t.Errorf("PRState=%q, want merged", got.PRState)
	}
}

// TestPRLookupReusedNameDoesNotWarn checks a merged PR whose head is
// neither the tip nor in its history (the name was reused after the PR,
// #389/#451) leaves the state unknown. Why: warning would tell someone
// their fresh branch is already merged.
func TestPRLookupReusedNameDoesNotWarn(t *testing.T) {
	root, _, base := prRepo(t, ghOrigin)
	git(t, root, "switch", "-q", "--detach", base)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "old pr work")
	old := revParse(t, root, "HEAD")
	git(t, root, "switch", "-q", "topic")
	got := resolveWith(t, root, "topic", func(string, string) ([]byte, error) { return prJSON(prRow(7, "MERGED", old)), nil })
	if got.PRState != "unknown" || got.PRNumber != 0 {
		t.Errorf("PRState=%q PRNumber=%d, want unknown/0", got.PRState, got.PRNumber)
	}
}

// TestPRLookupOpenPRWins checks an open PR for the name beats a merged one
// (no warning). Why: the branch is live work again.
func TestPRLookupOpenPRWins(t *testing.T) {
	root, tip, _ := prRepo(t, ghOrigin)
	got := resolveWith(t, root, "topic", func(string, string) ([]byte, error) {
		return prJSON(prRow(7, "MERGED", tip), prRow(9, "OPEN", tip)), nil
	})
	if got.PRState == "merged" {
		t.Errorf("PRState=%q, want no merged warning with an open PR", got.PRState)
	}
}

// TestPRLookupFailureIsUnknown checks a gh error and unusable output add
// nothing. Why: gh is optional; a launch is never blocked or errored by it.
func TestPRLookupFailureIsUnknown(t *testing.T) {
	root, _, _ := prRepo(t, ghOrigin)
	for name, list := range map[string]func(string, string) ([]byte, error){
		"error": func(string, string) ([]byte, error) { return nil, errors.New("gh: not installed") },
		"junk":  func(string, string) ([]byte, error) { return []byte("not json"), nil },
		"none":  func(string, string) ([]byte, error) { return []byte("[]"), nil },
	} {
		if got := resolveWith(t, root, "topic", list); got.PRState != "unknown" {
			t.Errorf("%s: PRState=%q, want unknown", name, got.PRState)
		}
	}
}

// TestPRLookupSlowAnswerIsDropped checks a hook slower than the budget leaves
// the target alone and Resolve returns promptly. Why: the launch must never
// wait on gh past the budget.
func TestPRLookupSlowAnswerIsDropped(t *testing.T) {
	root, tip, _ := prRepo(t, ghOrigin)
	release := make(chan struct{})
	defer close(release)
	l := StartPRLookup(root, "topic", func(string, string) ([]byte, error) {
		<-release
		return prJSON(prRow(7, "MERGED", tip)), nil
	})
	start := time.Now()
	got := l.Resolve(Target{Dir: root, Branch: "topic"}, 50*time.Millisecond)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Resolve took %v with a 50ms budget", d)
	}
	if got.PRState != "unknown" {
		t.Errorf("PRState=%q, want unknown", got.PRState)
	}
}

// TestPRLookupSkips checks the lookups that must not run: an empty branch,
// the default branch, a non-github origin, and a nil handle. Why: a brand-new
// or default branch has no PR of its own, and a non-GitHub repo has no gh PRs.
func TestPRLookupSkips(t *testing.T) {
	called := false
	list := func(string, string) ([]byte, error) { called = true; return []byte("[]"), nil }
	root, _, _ := prRepo(t, ghOrigin)
	other, _, _ := prRepo(t, "git@gitlab.example.com:o/r.git")
	for name, got := range map[string]Target{
		"empty branch":   StartPRLookup(root, "", list).Resolve(Target{}, time.Second),
		"default branch": StartPRLookup(root, "main", list).Resolve(Target{}, time.Second),
		"non-github":     StartPRLookup(other, "topic", list).Resolve(Target{}, time.Second),
		"nil handle":     (*PRLookup)(nil).Resolve(Target{}, time.Second),
	} {
		if got.PRState != "unknown" {
			t.Errorf("%s: PRState=%q, want unknown", name, got.PRState)
		}
	}
	if called {
		t.Error("gh was asked for a lookup that must be skipped")
	}
}
