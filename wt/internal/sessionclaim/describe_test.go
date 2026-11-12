package sessionclaim

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOthersReadsLiveClaimsWithoutWriting checks Others reports the live
// claims on the path, skips this process's own and another path's, and
// changes nothing on disk: the ask before Join's write is the launch
// confirmation (rootclaim.AskContinue via launchwarn.Check) on whichever
// launch path runs.
func TestOthersReadsLiveClaimsWithoutWriting(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1000, 101: 1000, 900: 5000})
	dir := t.TempDir()
	claimFile(t, dir, 100, body(100, 1000, root))
	claimFile(t, dir, 101, body(101, 1000, "/elsewhere"))
	claimFile(t, dir, 900, body(900, 5000, root))
	stale := claimFile(t, dir, 102, body(102, 1000, root))
	others, err := Others(dir, self())
	if err != nil || len(others) != 1 || others[0].PID != 100 {
		t.Fatalf("others=%v err=%v; want pid 100 only", others, err)
	}
	if !exists(stale) {
		t.Error("Others deleted a stale claim; it must not write")
	}
	if exists(filepath.Join(dir, "wt-sessions", ".lock")) {
		t.Error("Others took the lock; it must not write")
	}
}

// TestOthersNoDirectoryIsNone checks a repository nobody has claimed (no
// wt-sessions directory) has no others and no error, the common case.
func TestOthersNoDirectoryIsNone(t *testing.T) {
	fakeProcs(t, nil)
	dir := t.TempDir()
	others, err := Others(dir, self())
	if err != nil || len(others) != 0 {
		t.Fatalf("others=%v err=%v", others, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wt-sessions")); !os.IsNotExist(err) {
		t.Errorf("Others created wt-sessions: %v", err)
	}
}

// TestDescribeNamesTheSession checks the one-line description both launch
// paths show: agent, pid, how long it has run and the branch it started on.
func TestDescribeNamesTheSession(t *testing.T) {
	c := Claim{PID: 123, Started: 1000, Path: root, Branch: "main", Agent: "codex"}
	got := Describe(c, time.Unix(1250, 0))
	if want := "codex (pid 123, running 4m10s, started on main)"; got != want {
		t.Fatalf("Describe = %q; want %q", got, want)
	}
}

// TestCheckFailedNote checks the one-line note the root-claim step prints
// when the claims cannot be read or written, which never stops the launch
// (cmd/wt's holdRoot and the TUI's claimRoot both render it as `wt: note:`).
func TestCheckFailedNote(t *testing.T) {
	got := CheckFailed("/repo", errors.New("permission denied"))
	if want := "could not check other sessions in /repo (permission denied); launching anyway"; got != want {
		t.Fatalf("CheckFailed = %q; want %q", got, want)
	}
}

// TestDescribeAllJoinsDescriptions checks the one-line rendering of a path's
// holders — the cleanup list's held reason and the launch warnings name every
// session on a checkout: Describe's entry per claim, in the order given,
// joined with "; " (the join the held warning already uses). One claim reads
// exactly as Describe, and none gives "".
func TestDescribeAllJoinsDescriptions(t *testing.T) {
	now := time.Unix(1250, 0)
	cs := []Claim{
		{PID: 123, Started: 1000, Path: root, Branch: "main", Agent: "codex"},
		{PID: 456, Started: 1200, Path: root, Branch: "topic", Agent: "claude"},
	}
	got := DescribeAll(cs, now)
	if want := "codex (pid 123, running 4m10s, started on main); claude (pid 456, running 50s, started on topic)"; got != want {
		t.Fatalf("DescribeAll = %q; want %q", got, want)
	}
	if one := DescribeAll(cs[:1], now); one != Describe(cs[0], now) {
		t.Errorf("DescribeAll of one claim = %q; want Describe's %q", one, Describe(cs[0], now))
	}
	if none := DescribeAll(nil, now); none != "" {
		t.Errorf("DescribeAll of none = %q; want %q", none, "")
	}
}
