package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRecordAndCounts verifies that a recorded launch appears in all three
// windows when it happened within the last day.
func TestRecordAndCounts(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	if err := store.Record("ollama/gemma4:9b"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := store.Counts([]string{"ollama/gemma4:9b"})
	want := UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}
	if got["ollama/gemma4:9b"] != want {
		t.Fatalf("Counts = %+v, want %+v", got, want)
	}
}

// TestCountsSlidingWindows verifies events are bucketed into the correct
// 1d/7d/30d windows relative to the current time.
func TestCountsSlidingWindows(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	events := []struct {
		id   string
		when time.Time
	}{
		{"m", fixed.Add(-30 * time.Minute)},     // 1d, 7d, 30d
		{"m", fixed.Add(-26 * time.Hour)},     // 7d, 30d
		{"m", fixed.Add(-10 * 24 * time.Hour)}, // 30d
		{"m", fixed.Add(-40 * 24 * time.Hour)}, // none
	}
	var data []byte
	for _, e := range events {
		line, _ := json.Marshal(event{ModelID: e.id, Timestamp: e.when})
		data = append(data, append(line, '\n')...)
	}
	_ = os.WriteFile(store.path(), data, 0o600)

	got := store.Counts([]string{"m"})
	want := UsageCounts{OneDay: 1, SevenDay: 2, ThirtyDay: 3}
	if got["m"] != want {
		t.Fatalf("Counts = %+v, want %+v", got, want)
	}
}

// TestCountsIgnoresUnknownModels verifies Counts only returns entries for the
// requested model IDs.
func TestCountsIgnoresUnknownModels(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	line, _ := json.Marshal(event{ModelID: "other", Timestamp: fixed})
	_ = os.WriteFile(store.path(), append(line, '\n'), 0o600)

	got := store.Counts([]string{"wanted"})
	if _, ok := got["wanted"]; !ok {
		t.Fatalf("wanted not in result: %v", got)
	}
	if got["wanted"] != (UsageCounts{}) {
		t.Fatalf("wanted counts = %+v, want zero", got["wanted"])
	}
}

// TestCountsIgnoresBadLines ensures a corrupt JSONL line does not crash the
// scanner or affect counts for valid lines.
func TestCountsIgnoresBadLines(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	_ = os.WriteFile(store.path(), []byte("not json\n"), 0o600)
	store.Record("a")

	got := store.Counts([]string{"a"})
	if got["a"].OneDay != 1 {
		t.Fatalf("OneDay = %d, want 1", got["a"].OneDay)
	}
}

// TestCountsMissingFile returns zero counts when the usage file does not
// exist yet.
func TestCountsMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	got := store.Counts([]string{"x"})
	if got["x"] != (UsageCounts{}) {
		t.Fatalf("Counts = %+v, want zero", got["x"])
	}
}

// TestRecordPrunesEventsOlderThanRetentionWindow verifies Record drops
// events past the 30-day window on every write, so usage.jsonl stays
// bounded by launch frequency instead of growing across the lifetime of
// the install — only the trailing 30-day window is ever read by Counts.
// Flagged in PR #82 review: appendAtomic previously read-and-rewrote the
// whole file on every launch with no pruning.
func TestRecordPrunesEventsOlderThanRetentionWindow(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	stale := event{ModelID: "old", Timestamp: fixed.Add(-31 * 24 * time.Hour)}
	fresh := event{ModelID: "recent", Timestamp: fixed.Add(-1 * time.Hour)}
	var data []byte
	for _, ev := range []event{stale, fresh} {
		line, _ := json.Marshal(ev)
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(store.path(), data, 0o600); err != nil {
		t.Fatalf("seed usage file: %v", err)
	}

	if err := store.Record("new"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read usage file: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, `"old"`) {
		t.Errorf("usage file still contains stale event past retentionWindow: %q", got)
	}
	if !strings.Contains(got, `"recent"`) {
		t.Errorf("usage file dropped an event still within retentionWindow: %q", got)
	}
	if !strings.Contains(got, `"new"`) {
		t.Errorf("usage file missing the just-recorded event: %q", got)
	}
}

// TestRecordCreatesDirectory verifies Record creates the config directory if
// it does not exist.
func TestRecordCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "nested"))

	now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	if err := store.Record("x"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := os.Stat(store.path()); err != nil {
		t.Fatalf("usage file missing: %v", err)
	}
}

// TestRecordSerializesConcurrentProcesses verifies the file-lock around the
// read-prune-write critical section prevents lost writes when N goroutines
// (in-process stand-in for N concurrent wt processes) call Record at the
// same time on the same usage.jsonl. Without the lock, two goroutines
// read the same starting state and each rewrite the file, silently
// dropping the other's just-recorded event — a regression a user would
// notice as a model that never appears in Counts despite being launched.
func TestRecordSerializesConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = store.Record("model-" + string(rune('a'+i)))
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
		t.Fatalf("read usage file: %v", err)
	}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	lines := 0
	for scanner.Scan() {
		lines++
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("parse line %q: %v", scanner.Text(), err)
		}
		if seen[ev.ModelID] {
			t.Errorf("duplicate model id %q in usage file", ev.ModelID)
		}
		seen[ev.ModelID] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan usage file: %v", err)
	}
	if lines != n {
		t.Fatalf("usage file has %d lines, want %d (lost writes: %v)", lines, n, seen)
	}
	if len(seen) != n {
		t.Fatalf("usage file has %d distinct model ids, want %d (seen: %v)", len(seen), n, seen)
	}
}

// TestRecordLockFileCreated verifies Record creates the sidecar lock file
// in the same directory as usage.jsonl so the advisory flock is attached
// to the file's location regardless of rename of the target file. The
// sidecar's presence is what allows an external operator to inspect
// concurrent-wt-process state and confirms the lock is being taken in
// the expected path.
func TestRecordLockFileCreated(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	if err := store.Record("x"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	lockPath := filepath.Join(dir, "usage.jsonl.lock")
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file missing at %s: %v", lockPath, err)
	}
	if info.IsDir() {
		t.Fatalf("lock path is a directory, want a file: %s", lockPath)
	}
}

// TestCompositeScore verifies the recency weights: recent launches dominate.
// Formula: 3*OneDay + SevenDay + 2*ThirtyDay.
func TestCompositeScore(t *testing.T) {
	c := UsageCounts{OneDay: 2, SevenDay: 5, ThirtyDay: 10}
	got := CompositeScore(c)
	if got != 31 { // 3*2 + 5 + 2*10 = 31
		t.Fatalf("CompositeScore = %d, want 31", got)
	}
	older := UsageCounts{OneDay: 1, SevenDay: 5, ThirtyDay: 10}
	if got <= CompositeScore(older) {
		t.Fatal("more recent launches should score strictly higher")
	}
}

// TestAggregateByFamilySumsModelCounts verifies the pure per-family
// aggregation the model picker runs after its single Counts pass: per-model
// counts are summed into their family bucket, and counts for model IDs
// absent from familyOf contribute nothing. This replaces the aggregation
// half of the deleted Store.FamilyCounts, whose separate file scan duplicated
// Counts' scan/bucket loop and made every picker entry read usage.jsonl
// twice.
func TestAggregateByFamilySumsModelCounts(t *testing.T) {
	familyOf := map[string]string{
		"ollama/gemma4:9b":   "gemma4",
		"ollama/gemma4:14b":  "gemma4",
		"ollama/qwen3.8:27b": "qwen3.8",
	}
	counts := map[string]UsageCounts{
		"ollama/gemma4:9b":    {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
		"ollama/gemma4:14b":   {SevenDay: 1, ThirtyDay: 1},
		"ollama/qwen3.8:27b":  {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
		"ollama/notincatalog": {OneDay: 5}, // absent from familyOf: must be ignored
	}
	got := AggregateByFamily(familyOf, counts)
	if got["gemma4"] != (UsageCounts{OneDay: 1, SevenDay: 2, ThirtyDay: 2}) {
		t.Fatalf("gemma4 = %+v, want {1,2,2}", got["gemma4"])
	}
	if got["qwen3.8"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
		t.Fatalf("qwen3.8 = %+v, want {1,1,1}", got["qwen3.8"])
	}
	if _, ok := got["notincatalog"]; ok {
		t.Fatalf("family for a model outside the catalog should be skipped, got %v", got["notincatalog"])
	}
}

// TestAggregateByFamilyDoesNotPreSeedZeroFamilies verifies the family
// aggregation contract carried over from the deleted Store.FamilyCounts:
// families are not pre-seeded — a family whose every model has zero counts
// (no events within the retention window) is absent from the result, so a
// missing key reads as zero usage at the call site. The zero entry for the
// silent family's model mirrors what Counts zero-fills for every requested
// catalog ID.
func TestAggregateByFamilyDoesNotPreSeedZeroFamilies(t *testing.T) {
	familyOf := map[string]string{
		"ollama/used:1":   "used",
		"ollama/silent:1": "silent",
	}
	counts := map[string]UsageCounts{
		"ollama/used:1":   {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
		"ollama/silent:1": {},
	}
	got := AggregateByFamily(familyOf, counts)
	if got["used"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
		t.Fatalf("used = %+v, want {1,1,1}", got["used"])
	}
	if _, ok := got["silent"]; ok {
		t.Fatalf("silent family should be absent from result: %v", got["silent"])
	}
}

// TestCountsThenAggregateByFamily verifies the picker's single-scan family
// aggregation end to end: ONE Counts call over the FULL catalog's IDs (not
// just a filtered eligible subset) followed by AggregateByFamily produces
// the per-family totals the deleted Store.FamilyCounts computed with its own
// second scan of usage.jsonl. Events for models absent from familyOf must
// not leak into any family total.
func TestCountsThenAggregateByFamily(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	fixed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	var data []byte
	for _, e := range []event{
		{ModelID: "ollama/gemma4:9b", Timestamp: fixed.Add(-30 * time.Minute)},
		{ModelID: "ollama/gemma4:14b", Timestamp: fixed.Add(-2 * 24 * time.Hour)},
		{ModelID: "ollama/qwen3.8:27b", Timestamp: fixed.Add(-30 * time.Minute)},
		{ModelID: "ollama/notincatalog:1", Timestamp: fixed.Add(-30 * time.Minute)},
	} {
		line, _ := json.Marshal(e)
		data = append(data, append(line, '\n')...)
	}
	_ = os.WriteFile(store.path(), data, 0o600)

	familyOf := map[string]string{
		"ollama/gemma4:9b":   "gemma4",
		"ollama/gemma4:14b":  "gemma4",
		"ollama/qwen3.8:27b": "qwen3.8",
	}
	ids := make([]string, 0, len(familyOf))
	for id := range familyOf {
		ids = append(ids, id)
	}
	counts := store.Counts(ids)
	got := AggregateByFamily(familyOf, counts)
	if got["gemma4"] != (UsageCounts{OneDay: 1, SevenDay: 2, ThirtyDay: 2}) {
		t.Fatalf("gemma4 = %+v, want {1,2,2}", got["gemma4"])
	}
	if got["qwen3.8"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
		t.Fatalf("qwen3.8 = %+v, want {1,1,1}", got["qwen3.8"])
	}
	if _, ok := got["notincatalog"]; ok {
		t.Fatalf("family for a model outside the catalog should be skipped, got %v", got["notincatalog"])
	}
}

// TestFamilyAggregationMissingFileIsEmpty verifies the picker's family
// aggregation stays empty when usage.jsonl does not exist yet (fresh
// install): Counts zero-fills the catalog IDs and AggregateByFamily emits no
// family keys for all-zero counts. Replaces TestFamilyCountsMissingFile from
// the deleted Store.FamilyCounts.
func TestFamilyAggregationMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	familyOf := map[string]string{"a": "fam"}
	ids := make([]string, 0, len(familyOf))
	for id := range familyOf {
		ids = append(ids, id)
	}
	counts := store.Counts(ids)
	got := AggregateByFamily(familyOf, counts)
	if len(got) != 0 {
		t.Fatalf("family aggregation = %v, want empty", got)
	}
}
