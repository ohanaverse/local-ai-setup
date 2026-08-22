package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
