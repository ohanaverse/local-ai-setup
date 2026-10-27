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
// changes nothing on disk: the picker asks before anything is written.
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

// TestMenuListsOthersAndKeys checks the menu text the plain terminal and the
// picker share: the root, one line per other session, and the three keys.
func TestMenuListsOthersAndKeys(t *testing.T) {
	others := []Claim{
		{PID: 1, Started: 1000, Branch: "main", Agent: "codex"},
		{PID: 2, Started: 1100, Branch: "feat", Agent: "claude"},
	}
	got := Menu("/repo", others, time.Unix(1200, 0))
	want := "the repository root /repo is in use by another session:\n" +
		"  codex (pid 1, running 3m20s, started on main)\n" +
		"  claude (pid 2, running 1m40s, started on feat)\n" +
		"  c  continue here    w  new worktree    q  quit (Esc)\n"
	if got != want {
		t.Fatalf("Menu =\n%q\nwant\n%q", got, want)
	}
}

// TestCheckFailedNote checks the one-line note both paths print when the
// claims cannot be read or written, which never stops the launch.
func TestCheckFailedNote(t *testing.T) {
	got := CheckFailed("/repo", errors.New("permission denied"))
	if want := "could not check other sessions in /repo (permission denied); launching anyway"; got != want {
		t.Fatalf("CheckFailed = %q; want %q", got, want)
	}
}

// TestNewWorktreeName checks the generated name for the "new worktree"
// choice: session, pid and time, never a default branch name.
func TestNewWorktreeName(t *testing.T) {
	got := NewWorktreeName(42, time.Date(2026, 10, 27, 9, 5, 7, 0, time.Local))
	if want := "session-42-090507"; got != want {
		t.Fatalf("NewWorktreeName = %q; want %q", got, want)
	}
}
