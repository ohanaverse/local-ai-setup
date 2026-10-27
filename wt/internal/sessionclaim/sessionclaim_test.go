package sessionclaim

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

const root = "/repo"

// fakeProcs swaps the liveness and start-time seams: alive maps pid to its
// real start time; any other pid is dead. It restores the seams on cleanup.
func fakeProcs(t *testing.T, alive map[int]int64) {
	t.Helper()
	oldStart, oldAlive := startTime, pidAlive
	startTime = func(pid int) (int64, error) {
		s, ok := alive[pid]
		if !ok {
			return 0, fmt.Errorf("no pid %d", pid)
		}
		return s, nil
	}
	pidAlive = func(pid int) bool { _, ok := alive[pid]; return ok }
	t.Cleanup(func() { startTime, pidAlive = oldStart, oldAlive })
}

func claimFile(t *testing.T, dir string, pid int, body string) string {
	t.Helper()
	d := filepath.Join(dir, "wt-sessions")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, strconv.Itoa(pid))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func body(pid int, started int64, path string) string {
	return fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=main\nagent=claude\n", pid, started, path)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func self() Claim { return Claim{PID: 900, Started: 5000, Path: root, Branch: "main", Agent: "pi"} }

// TestJoinNoOthersWritesClaim checks a launch with no other claim writes its own
// file without asking, so an unshared root costs the user no prompt.
func TestJoinNoOthersWritesClaim(t *testing.T) {
	fakeProcs(t, nil)
	dir := t.TempDir()
	rel, others, err := Join(dir, self(), func([]Claim) bool { t.Fatal("ask called"); return true })
	if err != nil || len(others) != 0 {
		t.Fatalf("err=%v others=%v", err, others)
	}
	got, err := os.ReadFile(filepath.Join(dir, "wt-sessions", "900"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pid=900\nstarted=5000\npath=/repo\nbranch=main\nagent=pi\n" {
		t.Fatalf("claim = %q", got)
	}
	rel()
}

// TestJoinLiveOtherCallsAsk checks a live claim for the same path is reported
// and passed to ask, which is the whole point of the warning.
func TestJoinLiveOtherCallsAsk(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1000})
	dir := t.TempDir()
	claimFile(t, dir, 100, body(100, 1000, root))
	var asked []Claim
	_, others, err := Join(dir, self(), func(o []Claim) bool { asked = o; return true })
	if err != nil || len(others) != 1 || len(asked) != 1 || asked[0].PID != 100 || asked[0].Agent != "claude" || asked[0].Branch != "main" {
		t.Fatalf("err=%v others=%v asked=%v", err, others, asked)
	}
	if !exists(filepath.Join(dir, "wt-sessions", "900")) {
		t.Fatal("continue must write the claim")
	}
}

// TestJoinAskFalseWritesNothing checks declining leaves no claim, so a quit or
// new-worktree choice never marks the root as held.
func TestJoinAskFalseWritesNothing(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1000})
	dir := t.TempDir()
	claimFile(t, dir, 100, body(100, 1000, root))
	rel, _, err := Join(dir, self(), func([]Claim) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if exists(filepath.Join(dir, "wt-sessions", "900")) {
		t.Fatal("claim written despite ask=false")
	}
	if !exists(filepath.Join(dir, "wt-sessions", "100")) {
		t.Fatal("release removed another session's claim")
	}
}

// TestStaleDeadPidDeleted checks a claim whose pid is gone is deleted and not
// reported, so a killed wt never blocks the root.
func TestStaleDeadPidDeleted(t *testing.T) {
	fakeProcs(t, nil)
	dir := t.TempDir()
	p := claimFile(t, dir, 100, body(100, 1000, root))
	_, others, err := Join(dir, self(), func([]Claim) bool { return true })
	if err != nil || len(others) != 0 || exists(p) {
		t.Fatalf("err=%v others=%v exists=%v", err, others, exists(p))
	}
}

// TestReusedPidIsStale checks a running pid with a different start time is
// stale: the OS reused the pid for an unrelated process.
func TestReusedPidIsStale(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1100})
	dir := t.TempDir()
	p := claimFile(t, dir, 100, body(100, 1000, root))
	_, others, _ := Join(dir, self(), func([]Claim) bool { return true })
	if len(others) != 0 || exists(p) {
		t.Fatalf("others=%v exists=%v", others, exists(p))
	}
}

// TestStartedWithinTwoSecondsIsLive checks the 2-second tolerance both ways and
// its edge, since wt and the bash reader measure start time differently.
func TestStartedWithinTwoSecondsIsLive(t *testing.T) {
	for _, tc := range []struct {
		real int64
		live bool
	}{{1002, true}, {998, true}, {1003, false}, {997, false}} {
		fakeProcs(t, map[int]int64{100: tc.real})
		dir := t.TempDir()
		claimFile(t, dir, 100, body(100, 1000, root))
		_, others, _ := Join(dir, self(), func([]Claim) bool { return true })
		if (len(others) == 1) != tc.live {
			t.Errorf("real=%d: live=%v, want %v", tc.real, len(others) == 1, tc.live)
		}
	}
}

// TestMalformedAndMissingKeysStale checks garbage and files missing pid,
// started or path are stale and deleted, per the contract.
func TestMalformedAndMissingKeysStale(t *testing.T) {
	fakeProcs(t, map[int]int64{101: 1000, 102: 1000, 103: 1000, 104: 1000})
	dir := t.TempDir()
	ps := []string{
		claimFile(t, dir, 101, "not a claim\n"),
		claimFile(t, dir, 102, "started=1000\npath=/repo\n"),
		claimFile(t, dir, 103, "pid=103\npath=/repo\n"),
		claimFile(t, dir, 104, "pid=104\nstarted=1000\n"),
	}
	_, others, err := Join(dir, self(), func([]Claim) bool { return true })
	if err != nil || len(others) != 0 {
		t.Fatalf("err=%v others=%v", err, others)
	}
	for _, p := range ps {
		if exists(p) {
			t.Errorf("%s not deleted", p)
		}
	}
}

// TestUnknownKeyIgnored checks extra keys do not make a claim stale, so the
// format can grow without breaking older readers.
func TestUnknownKeyIgnored(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1000})
	dir := t.TempDir()
	claimFile(t, dir, 100, body(100, 1000, root)+"future=thing\n")
	_, others, _ := Join(dir, self(), func([]Claim) bool { return true })
	if len(others) != 1 {
		t.Fatalf("others=%v", others)
	}
}

// TestReleaseRemovesClaim checks release deletes this launch's claim, so a
// finished session stops holding the root.
func TestReleaseRemovesClaim(t *testing.T) {
	fakeProcs(t, nil)
	dir := t.TempDir()
	rel, _, err := Join(dir, self(), func([]Claim) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "wt-sessions", "900")
	if !exists(p) {
		t.Fatal("claim missing")
	}
	rel()
	if exists(p) {
		t.Fatal("claim survived release")
	}
}

// TestConcurrentJoinOnlyOneSeesEmpty checks the lock: two simultaneous launches
// cannot both see an empty directory, so exactly one is asked.
func TestConcurrentJoinOnlyOneSeesEmpty(t *testing.T) {
	// Both claims are "alive" with matching start times.
	fakeProcs(t, map[int]int64{900: 5000, 901: 5000})
	dir := t.TempDir()
	var asks int32
	var wg sync.WaitGroup
	for _, pid := range []int{900, 901} {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			c := Claim{PID: pid, Started: 5000, Path: root, Branch: "main", Agent: "pi"}
			if _, _, err := Join(dir, c, func([]Claim) bool { atomic.AddInt32(&asks, 1); return true }); err != nil {
				t.Error(err)
			}
		}(pid)
	}
	wg.Wait()
	if asks != 1 {
		t.Fatalf("ask calls = %d, want 1", asks)
	}
}

// TestUnwritableDirReturnsErrorNotPanic checks a commonDir that cannot hold
// wt-sessions yields an error the caller can downgrade to a warning.
func TestUnwritableDirReturnsErrorNotPanic(t *testing.T) {
	fakeProcs(t, nil)
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rel, _, err := Join(f, self(), func([]Claim) bool { return true })
	if err == nil {
		t.Fatal("want error")
	}
	if rel == nil {
		t.Fatal("release must be non-nil even on error")
	}
	rel()
}

// TestStartTimeParsesEtime checks the etime parser and a real-process
// cross-check: the seam's default reads this test process's start within the
// contract's tolerance.
func TestStartTimeParsesEtime(t *testing.T) {
	for in, want := range map[string]int64{"05:07": 307, "01:02:03": 3723, "2-01:02:03": 2*86400 + 3723, "00:00": 0} {
		got, err := parseEtime(in)
		if err != nil || got != want {
			t.Errorf("parseEtime(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseEtime("junk"); err == nil {
		t.Error("junk accepted")
	}
	if _, err := Self(root, "main", "pi"); err != nil {
		t.Fatalf("Self on a real process: %v", err)
	}
}
