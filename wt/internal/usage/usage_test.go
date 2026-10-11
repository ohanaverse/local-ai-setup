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

	if err := store.RecordFor("", "ollama/gemma4:9b"); err != nil {
		t.Fatalf("RecordFor: %v", err)
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
		{"m", fixed.Add(-30 * time.Minute)},    // 1d, 7d, 30d
		{"m", fixed.Add(-26 * time.Hour)},      // 7d, 30d
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
	store.RecordFor("", "a")

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

// TestRecordPrunesEventsOlderThanRetentionWindow verifies RecordFor drops
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

	if err := store.RecordFor("", "new"); err != nil {
		t.Fatalf("RecordFor: %v", err)
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

// TestRecordCreatesDirectory verifies RecordFor creates the config directory if
// it does not exist.
func TestRecordCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "nested"))

	now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	if err := store.RecordFor("", "x"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	if _, err := os.Stat(store.path()); err != nil {
		t.Fatalf("usage file missing: %v", err)
	}
}

// TestRecordSerializesConcurrentProcesses verifies the file-lock around the
// read-prune-write critical section prevents lost writes when N goroutines
// (in-process stand-in for N concurrent wt processes) call RecordFor at the
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
			errs[i] = store.RecordFor("", "model-"+string(rune('a'+i)))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("RecordFor[%d]: %v", i, err)
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

// TestRecordLockFileCreated verifies RecordFor creates the sidecar lock file
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

	if err := store.RecordFor("", "x"); err != nil {
		t.Fatalf("RecordFor: %v", err)
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

// TestRecordForScopesCountsToAgent verifies CountsForAgent counts only
// events recorded for that agent, while Counts still totals every agent
// (and legacy agent-less lines). This is what lets the selector show
// per-pair launch counts without losing model-level totals.
func TestRecordForScopesCountsToAgent(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	for _, rec := range [][2]string{{"claude", "m"}, {"claude", "m"}, {"codex", "m"}, {"", "m"}} {
		if err := store.RecordFor(rec[0], rec[1]); err != nil {
			t.Fatalf("RecordFor(%q): %v", rec[0], err)
		}
	}

	if got := store.CountsForAgent("claude", []string{"m"})["m"]; got != (UsageCounts{2, 2, 2}) {
		t.Errorf("claude pair = %+v, want {2 2 2}", got)
	}
	if got := store.CountsForAgent("codex", []string{"m"})["m"]; got != (UsageCounts{1, 1, 1}) {
		t.Errorf("codex pair = %+v, want {1 1 1}", got)
	}
	if got := store.Counts([]string{"m"})["m"]; got != (UsageCounts{4, 4, 4}) {
		t.Errorf("model-level = %+v, want {4 4 4} (all agents + agentless)", got)
	}
}

// TestCountsForAgentIgnoresLegacyLinesAndEmptyAgent verifies a usage line
// written before the agent field existed (and an empty agent argument)
// never count toward any pair. Guards against inflating a pair's numbers
// with history that cannot be attributed to it.
func TestCountsForAgentIgnoresLegacyLinesAndEmptyAgent(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	legacy := `{"model_id":"m","timestamp":"2026-09-19T11:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.CountsForAgent("claude", []string{"m"})["m"]; got != (UsageCounts{}) {
		t.Errorf("claude = %+v, want zero (legacy line has no agent)", got)
	}
	if got := store.CountsForAgent("", []string{"m"})["m"]; got != (UsageCounts{}) {
		t.Errorf("empty agent = %+v, want zero", got)
	}
	if got := store.Counts([]string{"m"})["m"]; got != (UsageCounts{1, 1, 1}) {
		t.Errorf("model-level = %+v, want {1 1 1} (legacy still counts)", got)
	}
}

// TestRecordForUnregisteredModelAndPrune verifies a model id that exists in
// no registry (a discovered on-disk model) records, reads back, and is
// pruned at the 30-day window like any other id — the store must never
// depend on registry membership.
func TestRecordForUnregisteredModelAndPrune(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	const id = "omlx/Qwen3.8-27B-4bit"

	old := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return old }
	defer func() { now = time.Now }()
	if err := store.RecordFor("claude", id); err != nil {
		t.Fatal(err)
	}
	fresh := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) // 49 days later
	now = func() time.Time { return fresh }
	if err := store.RecordFor("claude", id); err != nil {
		t.Fatal(err)
	}

	if got := store.CountsForAgent("claude", []string{id})[id]; got != (UsageCounts{1, 1, 1}) {
		t.Errorf("counts = %+v, want {1 1 1} (49-day-old event pruned)", got)
	}
}

// TestStoreInterfaceIncludesCountsForAgent is a compile-time guard that the
// Store interface exposes per-agent counts, so the selector can take a Store
// (and tests a mock) instead of the concrete StoreImpl.
func TestStoreInterfaceIncludesCountsForAgent(t *testing.T) {
	var s Store = NewStoreAt(t.TempDir())
	if got := s.CountsForAgent("claude", []string{"m"}); got["m"] != (UsageCounts{}) {
		t.Errorf("got %+v, want zero", got["m"])
	}
}

// writeEvents replaces the store's usage.jsonl with the given events, plus
// any raw lines appended verbatim (for malformed-line cases).
func writeEvents(t *testing.T, store *StoreImpl, events []event, raw ...string) {
	t.Helper()
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	for _, r := range raw {
		data = append(data, []byte(r+"\n")...)
	}
	if err := os.WriteFile(store.path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAllCountsEnumeratesEveryModelInTheFile verifies AllCounts needs no id
// list: it reports every model with a launch in the last 30 days, in the
// same 1d/7d/30d buckets Counts uses, and skips unparseable lines. `wt
// stats` builds its launch column from it; a model missing here is a model
// whose launches the usage table silently leaves out.
func TestAllCountsEnumeratesEveryModelInTheFile(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	writeEvents(t, store, []event{
		{ModelID: "ollama/a:1", Agent: "claude", Timestamp: fixed.Add(-30 * time.Minute)},
		{ModelID: "ollama/a:1", Agent: "codex", Timestamp: fixed.Add(-26 * time.Hour)},
		{ModelID: "ollama/a:1", Timestamp: fixed.Add(-10 * 24 * time.Hour)}, // legacy, no agent
		{ModelID: "removed/from-registry", Agent: "claude", Timestamp: fixed.Add(-2 * time.Hour)},
		{ModelID: "ollama/stale", Agent: "claude", Timestamp: fixed.Add(-40 * 24 * time.Hour)},
	}, "not json", `{"model_id":"ollama/a:1","timestamp":"garbage"}`)

	got := store.AllCounts("", fixed)
	want := map[string]UsageCounts{
		"ollama/a:1":            {OneDay: 1, SevenDay: 2, ThirtyDay: 3},
		"removed/from-registry": {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("AllCounts(\"\") = %+v, want exactly %+v (a model with no launch in 30 days is left out)", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("AllCounts(\"\")[%q] = %+v, want %+v", id, got[id], w)
		}
	}
}

// TestAllCountsScopesToOneAgent verifies a named agent counts only the
// launches recorded for it: legacy agent-less lines and other agents'
// launches are left out, and a model that agent never launched does not
// appear at all. `wt stats --agent codex` would otherwise show every
// agent's launches under a filter that claims to narrow them.
func TestAllCountsScopesToOneAgent(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	writeEvents(t, store, []event{
		{ModelID: "ollama/a:1", Agent: "claude", Timestamp: fixed.Add(-1 * time.Hour)},
		{ModelID: "ollama/a:1", Agent: "codex", Timestamp: fixed.Add(-2 * time.Hour)},
		{ModelID: "ollama/a:1", Timestamp: fixed.Add(-3 * time.Hour)},
		{ModelID: "ollama/b:1", Agent: "claude", Timestamp: fixed.Add(-4 * time.Hour)},
	})

	got := store.AllCounts("codex", fixed)
	if len(got) != 1 || got["ollama/a:1"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
		t.Fatalf("AllCounts(\"codex\") = %+v, want only ollama/a:1 with one launch", got)
	}
	if got := store.AllCounts("nobody", fixed); len(got) != 0 {
		t.Errorf("AllCounts(\"nobody\") = %+v, want an empty map", got)
	}
}

// TestAllCountsMissingFileIsAnEmptyMap verifies that a missing usage.jsonl
// reads as an empty, non-nil map: `wt stats` on a fresh install must print
// "no usage data", not crash. The bucket edges themselves — each bucket
// (asOf - window, asOf], which is also what the picker's 1d/7d/30d columns
// and the spend query use — are pinned by
// TestAllCountsWindowIsOpenAtItsStartAndClosedAtItsEnd.
func TestAllCountsMissingFileIsAnEmptyMap(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	if got := store.AllCounts("", fixed); got == nil || len(got) != 0 {
		t.Fatalf("AllCounts on a missing file = %#v, want an empty non-nil map", got)
	}
}

// TestAllCountsWindowIsOpenAtItsStartAndClosedAtItsEnd pins each bucket as
// (asOf - window, asOf] to the nanosecond: a launch dated exactly one window
// before the instant is outside that bucket, one a nanosecond younger is
// inside it, and one dated exactly at the instant is inside every bucket.
// This is the rule `wt stats`' spend query follows (spend.InWindow, #298);
// if the two disagree at an edge, a model shows a request with no launch
// there, which the usage guide tells the reader is traffic wt did not send.
func TestAllCountsWindowIsOpenAtItsStartAndClosedAtItsEnd(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	asOf := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	writeEvents(t, store, []event{
		{ModelID: "day/on-the-edge", Timestamp: asOf.Add(-day)},
		{ModelID: "day/inside", Timestamp: asOf.Add(-day + time.Nanosecond)},
		{ModelID: "week/on-the-edge", Timestamp: asOf.Add(-7 * day)},
		{ModelID: "week/inside", Timestamp: asOf.Add(-7*day + time.Nanosecond)},
		{ModelID: "month/on-the-edge", Timestamp: asOf.Add(-30 * day)},
		{ModelID: "month/inside", Timestamp: asOf.Add(-30*day + time.Nanosecond)},
		{ModelID: "at-the-instant", Timestamp: asOf},
		{ModelID: "after-the-instant", Timestamp: asOf.Add(time.Nanosecond)},
	})
	got := store.AllCounts("", asOf)
	want := map[string]UsageCounts{
		"day/on-the-edge":  {SevenDay: 1, ThirtyDay: 1},
		"day/inside":       {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
		"week/on-the-edge": {ThirtyDay: 1},
		"week/inside":      {SevenDay: 1, ThirtyDay: 1},
		// month/on-the-edge is in no bucket, so it has no entry at all.
		"month/inside":   {ThirtyDay: 1},
		"at-the-instant": {OneDay: 1, SevenDay: 1, ThirtyDay: 1},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("AllCounts(\"\")[%q] = %+v, want %+v", id, got[id], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("AllCounts(\"\") = %+v, want exactly the %d models above (none 30 days old or dated after the instant)", got, len(want))
	}
}

// TestAllCountsSkipsALineWithNoModelID verifies a parseable line with a
// recent timestamp and no model id (or an empty one) is not counted under
// the key "". wt never writes such a line, but usage.jsonl is a plain file
// people edit; counted, it becomes a `wt stats` row with a blank MODEL cell
// that no --model value can name.
func TestAllCountsSkipsALineWithNoModelID(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	writeEvents(t, store, []event{
		{ModelID: "", Agent: "claude", Timestamp: fixed.Add(-time.Hour)},
		{ModelID: "ollama/a:1", Agent: "claude", Timestamp: fixed.Add(-time.Hour)},
	}, `{"agent":"claude","timestamp":"2026-10-07T11:00:00Z"}`)

	for _, agent := range []string{"", "claude"} {
		got := store.AllCounts(agent, fixed)
		if _, nameless := got[""]; nameless || len(got) != 1 || got["ollama/a:1"] != (UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}) {
			t.Errorf("AllCounts(%q) = %+v, want only ollama/a:1 with one launch and no \"\" key", agent, got)
		}
	}
}

// TestAllCountsAgentFilterAndFutureDatedLines pins two corners the other
// AllCounts tests leave open. Under a named agent, a legacy agent-less line
// and that agent's own out-of-window launch are both left out in the same
// file, so the model does not appear at all. And an event dated after the
// instant the caller passes (a clock that was wrong when the line was
// written, or a launch recorded after the report's instant was read) is not
// counted: `wt stats` reports a window that ends at that instant, and its
// spend half cannot see past it either. Counts, the picker's rule, has no
// such end and still counts the line in all three buckets.
func TestAllCountsAgentFilterAndFutureDatedLines(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	writeEvents(t, store, []event{
		{ModelID: "ollama/a:1", Timestamp: fixed.Add(-time.Hour)},                           // legacy, no agent
		{ModelID: "ollama/a:1", Agent: "codex", Timestamp: fixed.Add(-31 * 24 * time.Hour)}, // codex, too old
		{ModelID: "ollama/future", Agent: "codex", Timestamp: fixed.Add(2 * time.Hour)},     // clock skew
		{ModelID: "ollama/at", Agent: "codex", Timestamp: fixed},                            // the instant itself
	})

	got := store.AllCounts("codex", fixed)
	if _, listed := got["ollama/a:1"]; listed {
		t.Errorf("AllCounts(\"codex\") = %+v, want no ollama/a:1 (one line has no agent, the other is too old)", got)
	}
	if _, listed := got["ollama/future"]; listed {
		t.Errorf("AllCounts(\"codex\") = %+v, want no ollama/future (dated after the instant asked about)", got)
	}
	want := UsageCounts{OneDay: 1, SevenDay: 1, ThirtyDay: 1}
	if got["ollama/at"] != want {
		t.Errorf("AllCounts(\"codex\")[ollama/at] = %+v, want %+v (the window includes its end)", got["ollama/at"], want)
	}
	if c := store.Counts([]string{"ollama/future"})["ollama/future"]; c != want {
		t.Errorf("Counts()[ollama/future] = %+v, want %+v (the picker's rule is unchanged)", c, want)
	}
}

// TestAllCountsMeasuresAgainstTheInstantItIsGiven verifies the buckets are
// measured from the caller's instant and from nothing else: with the
// package clock set years away in either direction, the same file and the
// same instant give the same counts, and moving the instant moves the
// buckets. `wt stats` passes the instant its spend query ends at; if
// AllCounts consulted its own clock, the launch column would cover a
// different window from the spend columns beside it (#287).
func TestAllCountsMeasuresAgainstTheInstantItIsGiven(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	asOf := time.Date(2026, 10, 7, 0, 0, 5, 0, time.UTC)
	defer func() { now = time.Now }()

	writeEvents(t, store, []event{
		{ModelID: "m", Timestamp: asOf.Add(-time.Hour)},                  // 1d, 7d, 30d
		{ModelID: "m", Timestamp: asOf.Add(-24*time.Hour + time.Second)}, // 1d, by a second
		{ModelID: "m", Timestamp: asOf.Add(-24*time.Hour - time.Second)}, // 7d, 30d
		{ModelID: "m", Timestamp: asOf.Add(time.Second)},                 // after asOf: none
	})
	want := UsageCounts{OneDay: 2, SevenDay: 3, ThirtyDay: 3}
	for _, clock := range []time.Time{
		time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
		asOf.Add(3 * time.Second),
		time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		now = func() time.Time { return clock }
		if got := store.AllCounts("", asOf)["m"]; got != want {
			t.Errorf("package clock at %s: AllCounts(\"\", %s)[m] = %+v, want %+v", clock.Format(time.RFC3339), asOf.Format(time.RFC3339), got, want)
		}
	}
	// Two seconds later the same file reads differently: the instant decides.
	later := asOf.Add(2 * time.Second)
	if got, want := store.AllCounts("", later)["m"], (UsageCounts{OneDay: 2, SevenDay: 4, ThirtyDay: 4}); got != want {
		t.Errorf("AllCounts(\"\", %s)[m] = %+v, want %+v", later.Format(time.RFC3339), got, want)
	}
}
