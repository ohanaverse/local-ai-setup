package sessionclaim

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestPruneRemovesDeadAndReusedKeepsLiveAndUnknown checks Prune deletes exactly
// the claims scan's prune rule calls stale — a dead pid's leftover, a running
// pid the OS reused (its real start time differs from the claim's by more than
// startTolerance) and a malformed file — while a live claim and a running pid
// whose start time cannot be read survive, and that it reports how many it
// removed. A refresh that tidies claims must never drop a running session's
// hold, nor a claim nobody can verify, and must clear the rest.
func TestPruneRemovesDeadAndReusedKeepsLiveAndUnknown(t *testing.T) {
	// 100 live (start matches the claim), 102 reused (start differs), 103 runs
	// but its start time is unreadable (the override below), 101 absent from the
	// table so it is dead, 104 malformed. None is this process or an ancestor
	// (fakeProcs walks the parent chain to pid 1), so only liveness decides.
	fakeProcs(t, map[int]int64{100: 1000, 102: 1100, 103: 1000})
	base := startTime
	startTime = func(pid int) (int64, error) {
		if pid == 103 {
			return 0, fmt.Errorf("ps failed")
		}
		return base(pid)
	}
	dir := t.TempDir()
	live := claimFile(t, dir, 100, body(100, 1000, root))
	dead := claimFile(t, dir, 101, body(101, 1000, root))
	reused := claimFile(t, dir, 102, body(102, 1000, root))
	unknown := claimFile(t, dir, 103, body(103, 1000, root))
	malformed := claimFile(t, dir, 104, "not a claim\n")

	removed, err := Prune(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Errorf("Prune removed %d, want 3 (dead + reused + malformed)", removed)
	}
	for _, p := range []string{dead, reused, malformed} {
		if exists(p) {
			t.Errorf("%s kept; it is stale and must be pruned", p)
		}
	}
	for _, p := range []string{live, unknown} {
		if !exists(p) {
			t.Errorf("%s pruned; it must be kept", p)
		}
	}
}

// TestPruneTakesTheLock checks Prune holds wt-sessions' exclusive flock while it
// deletes: while another holder has the lock, Prune must still be waiting — its
// stale file survives — and once the lock is released Prune runs and prunes it.
// A prune that skipped the lock could delete a claim a concurrent Hold or Join
// had just written (those take the same flock via withLock).
func TestPruneTakesTheLock(t *testing.T) {
	fakeProcs(t, nil) // pid 100 is absent from the table, so its claim is stale
	dir := t.TempDir()
	stale := claimFile(t, dir, 100, body(100, 1000, root))
	lock, err := os.OpenFile(filepath.Join(dir, "wt-sessions", ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close() // closing the fd releases the flock on any exit path
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		n, err := Prune(dir)
		if err != nil {
			t.Error(err)
		}
		done <- n
	}()
	// While this test holds the lock, Prune must still be blocked on it: a
	// non-blocking prune would have finished and pruned within this window.
	select {
	case <-done:
		t.Fatal("Prune finished while the lock was held elsewhere; it does not take the lock")
	case <-time.After(200 * time.Millisecond):
	}
	if !exists(stale) {
		t.Fatal("Prune pruned while another holder had the lock")
	}
	// Hand the lock over: Prune now proceeds and prunes.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("Prune removed %d, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prune never finished after the lock was released")
	}
	if exists(stale) {
		t.Fatal("stale claim survived the prune")
	}
}

// TestPruneMissingDirIsZeroNil checks a checkout nobody has claimed (no
// wt-sessions directory) is nothing to prune and no error, and — like the
// lock-free readers — the call creates no wt-sessions directory or lock file: a
// read that tidies up must not fabricate state where there was none (withLock
// would MkdirAll the dir and create .lock, so the missing case must be handled
// before the lock is taken).
func TestPruneMissingDirIsZeroNil(t *testing.T) {
	fakeProcs(t, nil)
	dir := t.TempDir()
	removed, err := Prune(dir)
	if err != nil || removed != 0 {
		t.Fatalf("Prune = %d, %v; want 0, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wt-sessions")); !os.IsNotExist(err) {
		t.Errorf("Prune created wt-sessions: %v", err)
	}
}
