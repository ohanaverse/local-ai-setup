package survey

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRecordAndEvents verifies a recorded "worked" event with both ratings
// round-trips through Events() with all fields intact — the basic contract
// every stats/format consumer builds on.
func TestRecordAndEvents(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)

	fixed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	e := Event{Agent: "claude", ModelID: "ollama/gemma4:9b", Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)}
	if err := store.Record(e); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := store.Events()
	if len(got) != 1 {
		t.Fatalf("Events() = %d events, want 1", len(got))
	}
	if got[0].Agent != "claude" || got[0].ModelID != "ollama/gemma4:9b" {
		t.Fatalf("Events()[0] = %+v, want agent claude, model ollama/gemma4:9b", got[0])
	}
	if got[0].Worked == nil || !*got[0].Worked {
		t.Fatalf("Events()[0].Worked = %v, want true", got[0].Worked)
	}
	if got[0].Speed == nil || *got[0].Speed != 4 {
		t.Fatalf("Events()[0].Speed = %v, want 4", got[0].Speed)
	}
	if got[0].Quality == nil || *got[0].Quality != 5 {
		t.Fatalf("Events()[0].Quality = %v, want 5", got[0].Quality)
	}
	if !got[0].Timestamp.Equal(fixed) {
		t.Fatalf("Events()[0].Timestamp = %v, want %v", got[0].Timestamp, fixed)
	}
}

// TestRecordEventShapes pins the "exactly one of" JSON contract from the
// design spec: a skip event carries only "skipped":true (no "worked" key),
// a failed event carries "worked":false with no rating keys, and a worked
// event omits a rating key entirely when that rating was left unrated
// (Enter-skipped) rather than writing a zero. Getting this wrong would
// make an unrated speed indistinguishable from a literal "0 (unusable)"
// rating downstream.
func TestRecordEventShapes(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	cases := []struct {
		name    string
		event   Event
		wantHas []string
		wantNot []string
	}{
		{
			name:    "skip",
			event:   Event{Agent: "claude", ModelID: "m", Skipped: true},
			wantHas: []string{`"skipped":true`},
			wantNot: []string{`"worked"`, `"speed"`, `"quality"`},
		},
		{
			name:    "failed",
			event:   Event{Agent: "claude", ModelID: "m", Worked: boolPtr(false)},
			wantHas: []string{`"worked":false`},
			wantNot: []string{`"skipped"`, `"speed"`, `"quality"`},
		},
		{
			name:    "worked unrated",
			event:   Event{Agent: "claude", ModelID: "m", Worked: boolPtr(true)},
			wantHas: []string{`"worked":true`},
			wantNot: []string{`"skipped"`, `"speed"`, `"quality"`},
		},
		{
			name:    "worked rated",
			event:   Event{Agent: "claude", ModelID: "m", Worked: boolPtr(true), Speed: intPtr(3), Quality: intPtr(2)},
			wantHas: []string{`"worked":true`, `"speed":3`, `"quality":2`},
			wantNot: []string{`"skipped"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := NewStoreAt(t.TempDir())
			if err := store.Record(c.event); err != nil {
				t.Fatalf("Record: %v", err)
			}
			raw, err := os.ReadFile(store.path())
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			for _, want := range c.wantHas {
				if !strings.Contains(string(raw), want) {
					t.Errorf("line %q missing %q", string(raw), want)
				}
			}
			for _, notWant := range c.wantNot {
				if strings.Contains(string(raw), notWant) {
					t.Errorf("line %q should not contain %q", string(raw), notWant)
				}
			}
		})
	}
}

// TestEventsMissingFile returns nil when the survey file does not exist yet.
func TestEventsMissingFile(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	got := store.Events()
	if got != nil {
		t.Fatalf("Events() = %v, want nil", got)
	}
}

// TestEventsIgnoresBadLines ensures a corrupt JSONL line does not crash the
// scanner or drop valid events around it.
func TestEventsIgnoresBadLines(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	now = func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	if err := os.WriteFile(store.path(), []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Record(Event{Agent: "claude", ModelID: "m", Skipped: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := store.Events()
	if len(got) != 1 {
		t.Fatalf("Events() = %d events, want 1 (bad line skipped)", len(got))
	}
}

// TestRecordPrunesEventsOlderThanRetentionWindow verifies Record drops
// events past the 30-day window on every write, mirroring
// internal/usage.Record — otherwise survey.jsonl grows unbounded across
// the lifetime of the install.
func TestRecordPrunesEventsOlderThanRetentionWindow(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	fixed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	stale := Event{Agent: "claude", ModelID: "old", Timestamp: fixed.Add(-31 * 24 * time.Hour), Skipped: true}
	fresh := Event{Agent: "claude", ModelID: "recent", Timestamp: fixed.Add(-1 * time.Hour), Skipped: true}
	var data []byte
	for _, ev := range []Event{stale, fresh} {
		line, _ := json.Marshal(ev)
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(store.path(), data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.Record(Event{Agent: "claude", ModelID: "new", Skipped: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	raw, err := os.ReadFile(store.path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(raw)
	if strings.Contains(got, `"old"`) {
		t.Errorf("survey file still contains stale event: %q", got)
	}
	if !strings.Contains(got, `"recent"`) {
		t.Errorf("survey file dropped an event still within retentionWindow: %q", got)
	}
	if !strings.Contains(got, `"new"`) {
		t.Errorf("survey file missing the just-recorded event: %q", got)
	}
}

// TestRecordCreatesDirectory verifies Record creates the config directory
// if it does not exist.
func TestRecordCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(filepath.Join(dir, "nested"))
	now = func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()

	if err := store.Record(Event{Agent: "claude", ModelID: "m", Skipped: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := os.Stat(store.path()); err != nil {
		t.Fatalf("survey file missing: %v", err)
	}
}

// TestRecordSerializesConcurrentProcesses verifies the file-lock around the
// read-prune-write critical section prevents lost writes when N goroutines
// call Record at the same time, mirroring internal/usage's equivalent test.
func TestRecordSerializesConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	fixed := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
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
			errs[i] = store.Record(Event{Agent: "claude", ModelID: "model-" + string(rune('a'+i)), Skipped: true})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Record[%d]: %v", i, err)
		}
	}

	got := store.Events()
	seen := map[string]bool{}
	for _, ev := range got {
		if seen[ev.ModelID] {
			t.Errorf("duplicate model id %q in survey file", ev.ModelID)
		}
		seen[ev.ModelID] = true
	}
	if len(got) != n {
		t.Fatalf("survey file has %d events, want %d (lost writes: %v)", len(got), n, seen)
	}
}
