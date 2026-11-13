package sessionclaim

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// dirSnapshot names every file in dir with its contents, so a test can pin
// that a reader changed nothing on disk.
func dirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	snap := make(map[string]string, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snap[e.Name()] = string(data)
	}
	return snap
}

// TestHoldersGroupsLiveClaimsByPath checks Holders keys its map by worktree
// path and lists every live claim on each one, whole and sorted by pid: the
// cleanup list and the launch warnings show "who else holds" each checkout
// from one read, and a claim on one path must not surface under another. The
// pids are a lexical-vs-numeric trap on purpose — the claim files' names sort
// as "10" < "100" < "9", so without Holders' pid sort root's slice reads
// [100, 9] and the comparison below fails.
func TestHoldersGroupsLiveClaimsByPath(t *testing.T) {
	fakeProcs(t, map[int]int64{9: 1000, 10: 1000, 100: 1000})
	dir := t.TempDir()
	claimFile(t, dir, 9, body(9, 1000, root))
	claimFile(t, dir, 100, body(100, 1000, root))
	claimFile(t, dir, 10, body(10, 1000, "/elsewhere"))
	got, err := Holders(dir, self())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Claim{
		root: {
			{PID: 9, Started: 1000, Path: root, Branch: "main", Agent: "claude"},
			{PID: 100, Started: 1000, Path: root, Branch: "main", Agent: "claude"},
		},
		"/elsewhere": {
			{PID: 10, Started: 1000, Path: "/elsewhere", Branch: "main", Agent: "claude"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Holders = %v; want %v", got, want)
	}
}

// TestHoldersExcludesSelfAndAncestors checks the caller's own claim and a
// claim held by one of its parents are never holders: a nested wt runs inside
// the session that already holds the checkout, and reporting the person's own
// session as "another holder" would warn them about themselves. All three
// claims are live in the fake table, so only the exclusion rules can drop the
// first two (fakeProcs walks this process's parents to pid 1, so a claim named
// pid 1 is an ancestor's and pid 100 a stranger's).
func TestHoldersExcludesSelfAndAncestors(t *testing.T) {
	fakeProcs(t, map[int]int64{1: 1000, 100: 1000, 900: 5000})
	dir := t.TempDir()
	claimFile(t, dir, 900, body(900, 5000, root)) // self's own claim
	claimFile(t, dir, 1, body(1, 1000, root))     // an ancestor's claim
	claimFile(t, dir, 100, body(100, 1000, root)) // a real other session
	got, err := Holders(dir, self())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[root]) != 1 || got[root][0].PID != 100 {
		t.Fatalf("Holders = %v; want only pid 100 (self and ancestors excluded)", got)
	}
}

// TestHoldersReusedPidIsNotAHolder checks the contract's liveness rule and its
// 5-second boundary exactly: a running pid whose real start time differs from
// the claim's by strictly more than 5 s is the OS's reused pid — the file is a
// dead session's leftover and holds nothing — while 5 s or less, the skew
// between wt's and the bash reader's start-time measurement, is still the
// claimed process and does hold.
func TestHoldersReusedPidIsNotAHolder(t *testing.T) {
	for _, tc := range []struct {
		real   int64
		holder bool
	}{
		{1000, true}, // exactly as claimed
		{1005, true}, // 5 s off either way is still the claimed process
		{995, true},
		{1006, false}, // strictly more than 5 s off is a reused pid
		{994, false},
		{1100, false},
	} {
		fakeProcs(t, map[int]int64{100: tc.real})
		dir := t.TempDir()
		claimFile(t, dir, 100, body(100, 1000, root))
		got, err := Holders(dir, self())
		if err != nil {
			t.Fatal(err)
		}
		isHolder := len(got) == 1 && len(got[root]) == 1
		if isHolder != tc.holder {
			t.Errorf("real start %d (claimed 1000): holder=%v, want %v", tc.real, isHolder, tc.holder)
		}
	}
}

// TestHoldersIsReadOnly checks Holders changes nothing on disk — no claim is
// pruned (the stale ones remain for Prune and the writers), no lock file is
// taken, and every byte is as it was. The cleanup list and the launch warnings
// read while other sessions run, so a read that tidied up could delete a claim
// a concurrent Hold just wrote.
func TestHoldersIsReadOnly(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1000, 102: 1100})
	dir := t.TempDir()
	dead := claimFile(t, dir, 101, body(101, 1000, root))
	reused := claimFile(t, dir, 102, body(102, 1000, root))
	malformed := claimFile(t, dir, 103, "not a claim\n")
	claimFile(t, dir, 100, body(100, 1000, root))
	claimsDir := filepath.Join(dir, "wt-sessions")
	before := dirSnapshot(t, claimsDir)
	got, err := Holders(dir, self())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[root]) != 1 || got[root][0].PID != 100 {
		t.Fatalf("Holders = %v; want only the live pid 100 (stale is ignored, not deleted)", got)
	}
	if after := dirSnapshot(t, claimsDir); !reflect.DeepEqual(before, after) {
		t.Errorf("Holders changed the directory:\nbefore %v\nafter  %v", before, after)
	}
	for _, p := range []string{dead, reused, malformed} {
		if !exists(p) {
			t.Errorf("%s was deleted; Holders must never prune", p)
		}
	}
}

// TestHoldersNoDirIsEmptyNil checks a repository nobody has claimed (no
// wt-sessions directory) has no holders and no error — the common case — and
// the read does not create the directory: Holders never writes anything.
func TestHoldersNoDirIsEmptyNil(t *testing.T) {
	fakeProcs(t, nil)
	dir := t.TempDir()
	got, err := Holders(dir, self())
	if err != nil || len(got) != 0 {
		t.Fatalf("Holders = %v, %v; want empty, nil", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wt-sessions")); !os.IsNotExist(err) {
		t.Errorf("Holders created wt-sessions: %v", err)
	}
}

// TestHoldersMalformedIgnored checks a garbage file and files missing pid,
// started or path hold nothing and fail nothing: a half-written or foreign
// file must not hide the real holders nor break the read. (Hold prunes such
// files; this reader only ignores them.)
func TestHoldersMalformedIgnored(t *testing.T) {
	fakeProcs(t, map[int]int64{100: 1000, 101: 1000, 102: 1000, 103: 1000, 104: 1000})
	dir := t.TempDir()
	claimFile(t, dir, 101, "not a claim\n")
	claimFile(t, dir, 102, "started=1000\npath=/repo\n")
	claimFile(t, dir, 103, "pid=103\npath=/repo\n")
	claimFile(t, dir, 104, "pid=104\nstarted=1000\n")
	claimFile(t, dir, 100, body(100, 1000, root)) // the real holder
	got, err := Holders(dir, self())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[root]) != 1 || got[root][0].PID != 100 {
		t.Fatalf("Holders = %v; want only pid 100 on %s", got, root)
	}
}
