package refcount

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRecordAndCounts verifies a recorded pid+model pair is reflected in
// Counts: the core "one session, one model, one count" path the model
// picker's ref column depends on. Counts is a pure read — liveness is
// Sweep's job — so no pidAlive stub is needed here.
func TestRecordAndCounts(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := store.Counts([]string{"ollama/gemma4:9b"})
	if got["ollama/gemma4:9b"] != 1 {
		t.Fatalf("Counts = %d, want 1", got["ollama/gemma4:9b"])
	}
}

// TestCountsZeroFillsRequestedIDs verifies every requested model ID appears
// in the result even with no recorded sessions, so the picker's ref column
// renders blank (not a missing map key) for an unused model.
func TestCountsZeroFillsRequestedIDs(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	got := store.Counts([]string{"unused"})
	if v, ok := got["unused"]; !ok || v != 0 {
		t.Fatalf("Counts[unused] = %d, ok=%v, want 0, true", v, ok)
	}
}

// TestCountsIgnoresUnrequestedModels verifies a recorded entry for a model
// outside the requested set never leaks into the result.
func TestCountsIgnoresUnrequestedModels(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "other/model"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := store.Counts([]string{"wanted"})
	if len(got) != 1 || got["wanted"] != 0 {
		t.Fatalf("Counts = %v, want only {wanted: 0}", got)
	}
}

// TestCountsMultipleSessionsSameModel verifies two different pids recorded
// against the same model both count — this is what lets a second concurrent
// wt session show "2" instead of clobbering the first session's entry.
func TestCountsMultipleSessionsSameModel(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record pid 111: %v", err)
	}
	if err := store.Record(222, "ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record pid 222: %v", err)
	}
	got := store.Counts([]string{"ollama/gemma4:9b"})
	if got["ollama/gemma4:9b"] != 2 {
		t.Fatalf("Counts = %d, want 2", got["ollama/gemma4:9b"])
	}
}

// TestSweepDropsDeadPids verifies Sweep removes an entry whose pid is
// reported dead by the injected pidAlive seam, and keeps a live one — the
// mechanism that fixes stale "in use" counts after a crashed or killed wt
// session, per the design's launch-time sweep.
func TestSweepDropsDeadPids(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	old := pidAlive
	defer func() { pidAlive = old }()
	pidAlive = func(pid int) bool { return pid != 999 }

	seedRefcountFile(t, dir, entry{Pid: 999, ModelID: "dead/model"}, entry{Pid: 111, ModelID: "live/model"})

	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, "dead/model") {
		t.Errorf("state still contains dead pid's entry: %q", got)
	}
	if !strings.Contains(got, "live/model") {
		t.Errorf("state dropped the live pid's entry: %q", got)
	}
}

// TestSweepIgnoresCorruptLines verifies a corrupt JSONL line is dropped
// rather than aborting the sweep or crashing the scanner, matching Sweep's
// best-effort contract (a stale-count risk, never a blocked launch).
func TestSweepIgnoresCorruptLines(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	old := pidAlive
	defer func() { pidAlive = old }()
	pidAlive = func(int) bool { return true }

	if err := os.WriteFile(store.path(), []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt line: %v", err)
	}
	if err := store.Record(111, "ok/model"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got := store.Counts([]string{"ok/model"})
	if got["ok/model"] != 1 {
		t.Fatalf("Counts = %d, want 1 (valid entry survives a corrupt sibling line)", got["ok/model"])
	}
}

// TestSweepMissingFileIsNoop verifies Sweep succeeds with no state file yet
// (fresh install) instead of erroring — the launch-time sweep call in
// runLaunchPath is unconditional and must never fail a launch.
func TestSweepMissingFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Sweep(); err != nil {
		t.Fatalf("Sweep on missing file: %v", err)
	}
}

// TestPidAliveDefaultDetectsCurrentProcess sanity-checks the real
// (unstubbed) pidAlive wrapper against the test's own pid, which is always
// alive during the test run — a cheap, deterministic check that the
// syscall.Kill(pid, 0) wrapper isn't inverted.
func TestPidAliveDefaultDetectsCurrentProcess(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("pidAlive(os.Getpid()) = false, want true (the running test process is alive)")
	}
}

// TestRecordLockFileCreated verifies Record creates the sidecar lock file
// in the same directory as refcount.jsonl, mirroring usage.Record's
// concurrency contract.
func TestRecordLockFileCreated(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	if err := store.Record(111, "x"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	lockPath := filepath.Join(dir, "refcount.jsonl.lock")
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file missing at %s: %v", lockPath, err)
	}
	if info.IsDir() {
		t.Fatalf("lock path is a directory, want a file: %s", lockPath)
	}
}

// TestRecordSerializesConcurrentProcesses verifies the flock around Record's
// read-modify-write critical section prevents lost writes when N goroutines
// (in-process stand-in for N concurrent wt processes) call Record at the
// same time — without it, two goroutines could read the same starting state
// and each rewrite the file, silently dropping the other's entry.
func TestRecordSerializesConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = store.Record(1000+i, "model-x")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if lines != n {
		t.Fatalf("state file has %d lines, want %d (lost writes)", lines, n)
	}
}

// seedRefcountFile writes entries directly to dir/refcount.jsonl, bypassing
// Record, so Sweep tests can set up state without depending on Record.
func seedRefcountFile(t *testing.T, dir string, entries ...entry) {
	t.Helper()
	var data []byte
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal seed entry: %v", err)
		}
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "refcount.jsonl"), data, 0o600); err != nil {
		t.Fatalf("seed refcount file: %v", err)
	}
}
