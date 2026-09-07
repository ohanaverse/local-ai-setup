# wt Session Survey — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a lightweight post-session survey (did it work? speed? quality?) so users can see whether a model or an agent×model *combination* is the problem, with stats surfaced after every survey, in the model picker, and via `wt stats`.

**Architecture:** New package `internal/survey` mirrors `internal/usage`'s JSONL-store shape (flock sidecar, prune-on-write, atomic write) but records richer events and exposes pure aggregation functions over `[]Event` keyed by an explicit `asOf time.Time`. A single `PromptRun` implementation is wired into both the non-TUI launch path (`cmd/wt/launch.go`) and the TUI launch path (`internal/tui`, via a capture-then-emit pattern matching the existing post-run summary line). The model picker gets a new trailing segment sourced from `survey.AgentModelStats`. `wt stats` is a new read-only cobra command reading `survey.jsonl` directly — no catalog dependency.

**Tech Stack:** Go (module `github.com/ohanaverse/local-ai-setup/wt`), `github.com/spf13/cobra`, `github.com/charmbracelet/x/term`, `github.com/charmbracelet/bubbletea`.

**Spec:** `docs/superpowers/specs/2026-09-07-wt-session-survey-design.md`

## Global Constraints

- **Worked-percentage semantics:** `worked% = worked / (worked + failed)` over *answered* surveys only. Skips are recorded but excluded from the denominator.
- **Retention:** 30 days (the longest stats window), pruned on every `Record` call — mirrors `internal/usage`.
- **Event JSON shape:** every line carries `{agent, model_id, timestamp}` plus *exactly one* of `{"skipped":true}`, `{"worked":false}`, or `{"worked":true, "speed"?, "quality"?}`. `Worked` is `*bool` (not `bool`) so `omitempty` can distinguish "absent" (skip) from "present but false".
- **Survey guards (silent no-op):** stdin not a TTY, or `m.ID == ""` (command agents like `shell` have no model to survey). Both guards live inside `survey.PromptRun` so both launch paths share one implementation.
- **Ordering everywhere:** summary line → survey prompt → after-survey stats.
- **Picker segment:** appended **last**, after `[tags]`; omitted when `Answered == 0`; `⚠` replaces `✓` when `WorkedPct < 70%` **and** `Answered >= 3`.
- **`survey.jsonl` is wt-owned**; nothing else reads it, and it never touches `usage.jsonl`.
- **No config toggle** — every-exit survey with a one-keypress skip is the intended trade-off (v1 non-goal).

---

### Task 1: `internal/survey` — Event & Store

**Files:**
- Create: `internal/survey/survey.go`
- Test: `internal/survey/survey_test.go`

**Interfaces:**
- Produces: `type Event struct { Agent, ModelID string; Timestamp time.Time; Skipped bool; Worked *bool; Speed, Quality *int }`, `type Store interface { Record(Event) error; Events() []Event }`, `type StoreImpl struct{...}`, `func NewStore() *StoreImpl`, `func NewStoreAt(dir string) *StoreImpl`, unexported `boolPtr(bool) *bool`, `intPtr(int) *int`, package var `now`.

- [ ] **Step 1: Write the failing tests**

Create `internal/survey/survey_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/survey/... -v`
Expected: FAIL — package `survey` does not exist / compile errors (`Event`, `NewStoreAt`, etc. undefined).

- [ ] **Step 3: Implement `internal/survey/survey.go`**

```go
// Package survey records and queries post-session verdicts (did the
// agent×model combo work, how fast, how good) so users can see
// accumulated stats before launching and after each session.
package survey

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// retentionWindow is the longest stats window (30 days). Record prunes
// events older than this on every write, mirroring internal/usage.
const retentionWindow = 30 * 24 * time.Hour

// Event is one line in the survey JSONL file. Exactly one of three shapes
// is populated: Skipped (skip pressed, no verdict), Worked == false (did
// not work, no ratings), or Worked == true with Speed/Quality independently
// optional. Worked is a pointer so json omitempty can distinguish "false"
// (must be written) from "absent" (a skip event).
type Event struct {
	Agent     string    `json:"agent"`
	ModelID   string    `json:"model_id"`
	Timestamp time.Time `json:"timestamp"`
	Skipped   bool      `json:"skipped,omitempty"`
	Worked    *bool     `json:"worked,omitempty"`
	Speed     *int      `json:"speed,omitempty"`
	Quality   *int      `json:"quality,omitempty"`
}

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// Store is an interface for recording and querying survey events.
type Store interface {
	Record(Event) error
	Events() []Event
}

// StoreImpl reads and appends to the survey history file.
type StoreImpl struct {
	dir string
}

func NewStore() *StoreImpl {
	return NewStoreAt(config.Dir())
}

func NewStoreAt(dir string) *StoreImpl {
	return &StoreImpl{dir: dir}
}

func (s *StoreImpl) path() string {
	return filepath.Join(s.dir, "survey.jsonl")
}

// now is overridable for tests.
var now = time.Now

// Record appends one survey event atomically, always stamping Timestamp
// with now() (any caller-supplied Timestamp is overwritten — Record's job
// is to timestamp "now", mirroring internal/usage.Record). It first drops
// any existing events older than retentionWindow.
//
// Concurrency is guarded by a POSIX advisory flock on a sidecar
// survey.jsonl.lock file — see internal/usage.Record for the full
// rationale; the pattern is identical here.
func (s *StoreImpl) Record(e Event) error {
	e.Timestamp = now().UTC()
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := s.path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(filepath.Dir(path), "survey.jsonl.lock")
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	existing, _ := os.ReadFile(path)
	kept, err := pruneOlderThan(existing, now().UTC(), retentionWindow)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, append(kept, line...), 0o600)
}

// pruneOlderThan returns the lines of data whose event timestamp is within
// window of asOf. Lines that fail to parse are dropped along with expired
// ones. A scan error is surfaced (see internal/usage.pruneOlderThan for the
// rationale — the result overwrites the on-disk history in Record).
func pruneOlderThan(data []byte, asOf time.Time, window time.Duration) ([]byte, error) {
	var out []byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if asOf.Sub(ev.Timestamp.UTC()) >= window {
			continue
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Events returns every event in the store, best-effort: a missing file
// returns nil, and malformed lines are skipped rather than aborting the
// scan (mirroring internal/usage.Counts).
func (s *StoreImpl) Events() []Event {
	f, err := os.Open(s.path())
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/survey/... -v`
Expected: PASS for all `TestRecord*` / `TestEvents*` tests.

- [ ] **Step 5: Commit**

```bash
git add internal/survey/survey.go internal/survey/survey_test.go
git commit -m "feat(survey): add Event/Store for session-survey JSONL history

completes plan item #1"
```

---

### Task 2: `internal/survey` — Stats & aggregation

**Files:**
- Create: `internal/survey/stats.go`
- Test: `internal/survey/stats_test.go`

**Interfaces:**
- Consumes: `Event` from Task 1.
- Produces: `type Stats struct{...}` with `WorkedPct() (float64, bool)`, `SpeedAvg() (float64, bool)`, `QualityAvg() (float64, bool)`; `const Window1d, Window7d, Window30d time.Duration`; `func ModelStats(events []Event, window time.Duration, asOf time.Time) map[string]Stats`; `func AgentModelStats(events []Event, agent string, window time.Duration, asOf time.Time) map[string]Stats`; `type ComboRow struct{ Agent, ModelID string; Stats Stats }`; `func AllAgentModelStats(events []Event, window time.Duration, asOf time.Time) []ComboRow`.

- [ ] **Step 1: Write the failing tests**

Create `internal/survey/stats_test.go`:

```go
package survey

import (
	"testing"
	"time"
)

func TestStatsWorkedPctZeroWhenNoAnswered(t *testing.T) {
	var s Stats
	if _, ok := s.WorkedPct(); ok {
		t.Fatal("WorkedPct ok = true, want false for zero Answered")
	}
}

func TestStatsWorkedPct(t *testing.T) {
	s := Stats{Answered: 4, Worked: 3, Failed: 1}
	pct, ok := s.WorkedPct()
	if !ok {
		t.Fatal("WorkedPct ok = false, want true")
	}
	if pct != 75 {
		t.Fatalf("WorkedPct = %v, want 75", pct)
	}
}

func TestStatsSpeedQualityAvg(t *testing.T) {
	s := Stats{RatedSpeed: 2, SpeedSum: 7, RatedQuality: 3, QualitySum: 12}
	speed, ok := s.SpeedAvg()
	if !ok || speed != 3.5 {
		t.Fatalf("SpeedAvg = %v, %v, want 3.5, true", speed, ok)
	}
	quality, ok := s.QualityAvg()
	if !ok || quality != 4 {
		t.Fatalf("QualityAvg = %v, %v, want 4, true", quality, ok)
	}
}

func TestStatsSpeedQualityAvgUnrated(t *testing.T) {
	var s Stats
	if _, ok := s.SpeedAvg(); ok {
		t.Fatal("SpeedAvg ok = true, want false when no rated events")
	}
	if _, ok := s.QualityAvg(); ok {
		t.Fatal("QualityAvg ok = true, want false when no rated events")
	}
}

// TestModelStatsExcludesSkipsFromDenominator verifies the design's core
// semantics: worked% = worked / (worked + failed) over answered surveys
// only. A skip is recorded (visible via Skipped) but must not inflate or
// deflate the denominator.
func TestModelStatsExcludesSkipsFromDenominator(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(false)},
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Skipped: true},
	}
	got := ModelStats(events, Window1d, asOf)["m"]
	if got.Answered != 2 {
		t.Fatalf("Answered = %d, want 2 (skip excluded)", got.Answered)
	}
	if got.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", got.Skipped)
	}
	pct, ok := got.WorkedPct()
	if !ok || pct != 50 {
		t.Fatalf("WorkedPct = %v, %v, want 50, true", pct, ok)
	}
}

// TestModelStatsWindowExcludesOldEvents verifies events outside the
// requested window are dropped from the aggregate, so a 1d query does not
// pick up a 10-day-old event.
func TestModelStatsWindowExcludesOldEvents(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-30 * time.Minute), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-10 * 24 * time.Hour), Worked: boolPtr(true)},
	}
	got := ModelStats(events, Window1d, asOf)["m"]
	if got.Answered != 1 {
		t.Fatalf("Answered = %d, want 1 (10-day-old event excluded from 1d window)", got.Answered)
	}
	got30 := ModelStats(events, Window30d, asOf)["m"]
	if got30.Answered != 2 {
		t.Fatalf("Answered(30d) = %d, want 2", got30.Answered)
	}
}

// TestAgentModelStatsFiltersByAgent verifies AgentModelStats only
// aggregates events for the requested agent — the picker's agent-scoped
// stats must not leak another agent's verdicts for the same model.
func TestAgentModelStatsFiltersByAgent(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(false)},
	}
	got := AgentModelStats(events, "claude", Window1d, asOf)["m"]
	if got.Answered != 1 || got.Worked != 1 {
		t.Fatalf("claude stats = %+v, want Answered=1 Worked=1", got)
	}
	if _, ok := AgentModelStats(events, "claude", Window1d, asOf)["nonexistent"]; ok {
		t.Fatal("expected no entry for a model with no events")
	}
}

// TestAllAgentModelStatsGroupsByCombo verifies every observed (agent,
// model) pair gets its own row, and that two agents sharing a model do
// not merge into one row — this is what makes a bad agent×model combo
// visible in `wt stats` even when the model itself looks fine overall.
func TestAllAgentModelStatsGroupsByCombo(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(false)},
	}
	rows := AllAgentModelStats(events, Window1d, asOf)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	byAgent := map[string]ComboRow{}
	for _, r := range rows {
		byAgent[r.Agent] = r
	}
	if byAgent["claude"].Stats.Worked != 1 {
		t.Fatalf("claude row = %+v, want Worked=1", byAgent["claude"])
	}
	if byAgent["codex"].Stats.Failed != 1 {
		t.Fatalf("codex row = %+v, want Failed=1", byAgent["codex"])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/survey/... -run 'TestStats|TestModelStats|TestAgentModelStats|TestAllAgentModelStats' -v`
Expected: FAIL — `Stats`, `Window1d`, `ModelStats`, etc. undefined (compile error).

- [ ] **Step 3: Implement `internal/survey/stats.go`**

```go
package survey

import "time"

// Window durations for stats aggregation.
const (
	Window1d  = 24 * time.Hour
	Window7d  = 7 * 24 * time.Hour
	Window30d = 30 * 24 * time.Hour
)

// Stats accumulates survey outcomes for one (model) or (agent, model) key
// within a time window. Answered = Worked + Failed; Skipped is tracked
// separately since it is excluded from WorkedPct's denominator (a skip
// records participation but carries no verdict).
type Stats struct {
	Answered     int
	Worked       int
	Failed       int
	Skipped      int
	RatedSpeed   int
	SpeedSum     int
	RatedQuality int
	QualitySum   int
}

// WorkedPct returns the worked percentage (0-100) over answered surveys
// only. ok is false when Answered == 0 (nothing to divide by).
func (s Stats) WorkedPct() (float64, bool) {
	if s.Answered == 0 {
		return 0, false
	}
	return float64(s.Worked) / float64(s.Answered) * 100, true
}

// SpeedAvg returns the average speed rating (1-5). ok is false when no
// worked survey carried a speed rating.
func (s Stats) SpeedAvg() (float64, bool) {
	if s.RatedSpeed == 0 {
		return 0, false
	}
	return float64(s.SpeedSum) / float64(s.RatedSpeed), true
}

// QualityAvg returns the average quality rating (1-5). ok is false when no
// worked survey carried a quality rating.
func (s Stats) QualityAvg() (float64, bool) {
	if s.RatedQuality == 0 {
		return 0, false
	}
	return float64(s.QualitySum) / float64(s.RatedQuality), true
}

// accumulate folds one event into s. Callers filter by window/agent/model
// before calling — accumulate itself does no filtering.
func accumulate(s *Stats, ev Event) {
	if ev.Skipped {
		s.Skipped++
		return
	}
	if ev.Worked == nil {
		return
	}
	s.Answered++
	if !*ev.Worked {
		s.Failed++
		return
	}
	s.Worked++
	if ev.Speed != nil {
		s.RatedSpeed++
		s.SpeedSum += *ev.Speed
	}
	if ev.Quality != nil {
		s.RatedQuality++
		s.QualitySum += *ev.Quality
	}
}

// inWindow reports whether ev happened within window of asOf.
func inWindow(ev Event, window time.Duration, asOf time.Time) bool {
	return asOf.Sub(ev.Timestamp.UTC()) < window
}

// ModelStats aggregates events across all agents, keyed by model id, for
// events within window of asOf. Used by the picker's own-model row and by
// `wt stats`'s per-model "(all)" aggregate row.
func ModelStats(events []Event, window time.Duration, asOf time.Time) map[string]Stats {
	out := map[string]Stats{}
	for _, ev := range events {
		if !inWindow(ev, window, asOf) {
			continue
		}
		s := out[ev.ModelID]
		accumulate(&s, ev)
		out[ev.ModelID] = s
	}
	return out
}

// AgentModelStats aggregates events for one agent, keyed by model id, for
// events within window of asOf. Used by the model picker (agent-scoped
// segment) and the after-survey combo row.
func AgentModelStats(events []Event, agent string, window time.Duration, asOf time.Time) map[string]Stats {
	out := map[string]Stats{}
	for _, ev := range events {
		if ev.Agent != agent || !inWindow(ev, window, asOf) {
			continue
		}
		s := out[ev.ModelID]
		accumulate(&s, ev)
		out[ev.ModelID] = s
	}
	return out
}

// ComboRow is one (agent, model) row for AllAgentModelStats / `wt stats`.
type ComboRow struct {
	Agent   string
	ModelID string
	Stats   Stats
}

// AllAgentModelStats aggregates events for every observed (agent, model)
// pair within window of asOf, for the `wt stats` report command.
func AllAgentModelStats(events []Event, window time.Duration, asOf time.Time) []ComboRow {
	type key struct{ agent, model string }
	acc := map[key]*Stats{}
	var order []key
	for _, ev := range events {
		if !inWindow(ev, window, asOf) {
			continue
		}
		k := key{ev.Agent, ev.ModelID}
		s, ok := acc[k]
		if !ok {
			s = &Stats{}
			acc[k] = s
			order = append(order, k)
		}
		accumulate(s, ev)
	}
	rows := make([]ComboRow, 0, len(order))
	for _, k := range order {
		rows = append(rows, ComboRow{Agent: k.agent, ModelID: k.model, Stats: *acc[k]})
	}
	return rows
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/survey/... -v`
Expected: PASS for all tests, including Task 1's.

- [ ] **Step 5: Commit**

```bash
git add internal/survey/stats.go internal/survey/stats_test.go
git commit -m "feat(survey): add Stats and window aggregation functions

completes plan item #2"
```

---

### Task 3: `internal/survey` — Format functions

**Files:**
- Create: `internal/survey/format.go`
- Test: `internal/survey/format_test.go`

**Interfaces:**
- Consumes: `Stats`, `Event`, `ModelStats`, `AgentModelStats`, `Window1d/7d/30d` from Task 2.
- Produces: `func FormatAfterSurvey(events []Event, agent, modelID string, asOf time.Time) string`, `func FormatPickerSegment(s Stats) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/survey/format_test.go`:

```go
package survey

import (
	"strings"
	"testing"
	"time"
)

func TestFormatPickerSegmentOmittedWhenNoAnswered(t *testing.T) {
	if got := FormatPickerSegment(Stats{}); got != "" {
		t.Fatalf("FormatPickerSegment(zero) = %q, want empty", got)
	}
}

func TestFormatPickerSegmentChecksWorked(t *testing.T) {
	s := Stats{Answered: 12, Worked: 11, Failed: 1, RatedQuality: 10, QualitySum: 42, RatedSpeed: 10, SpeedSum: 39}
	got := FormatPickerSegment(s)
	want := "✓92% q4.2 s3.9 n12"
	if got != want {
		t.Fatalf("FormatPickerSegment = %q, want %q", got, want)
	}
}

// TestFormatPickerSegmentWarnsBelowThreshold verifies the ⚠ swap: it only
// triggers when the combo has enough signal (Answered >= 3) AND is below
// 70% worked — either condition alone must not flag it, so a single bad
// run doesn't scare a user off a combo with too little data to judge.
func TestFormatPickerSegmentWarnsBelowThreshold(t *testing.T) {
	belowThresholdEnoughData := Stats{Answered: 3, Worked: 2, Failed: 1} // 66.7% < 70%, Answered >= 3
	if got := FormatPickerSegment(belowThresholdEnoughData); !strings.HasPrefix(got, "⚠") {
		t.Fatalf("FormatPickerSegment = %q, want ⚠ prefix (67%% < 70%% with 3 answered)", got)
	}

	belowThresholdNotEnoughData := Stats{Answered: 2, Worked: 1, Failed: 1} // 50% < 70%, Answered < 3
	if got := FormatPickerSegment(belowThresholdNotEnoughData); !strings.HasPrefix(got, "✓") {
		t.Fatalf("FormatPickerSegment = %q, want ✓ prefix (only 2 answered, too little signal to warn)", got)
	}

	aboveThreshold := Stats{Answered: 10, Worked: 8, Failed: 2} // 80% >= 70%
	if got := FormatPickerSegment(aboveThreshold); !strings.HasPrefix(got, "✓") {
		t.Fatalf("FormatPickerSegment = %q, want ✓ prefix (80%% >= 70%%)", got)
	}
}

// TestFormatPickerSegmentUnratedShowsDash verifies a worked-but-unrated
// combo still renders a segment (Answered > 0), with a dash in place of
// whichever rating was never given.
func TestFormatPickerSegmentUnratedShowsDash(t *testing.T) {
	s := Stats{Answered: 3, Worked: 3}
	got := FormatPickerSegment(s)
	want := "✓100% q- s- n3"
	if got != want {
		t.Fatalf("FormatPickerSegment = %q, want %q", got, want)
	}
}

// TestFormatAfterSurveyShowsDashForEmptyWindow verifies the combo row
// renders "—" for a window with zero answered surveys, while the model row
// (all agents) still shows real data from an event recorded by a different
// agent — this is the "model looks fine, this agent×model combo has no
// data yet" case from the design's illustrative example.
func TestFormatAfterSurveyShowsDashForEmptyWindow(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
	}
	out := FormatAfterSurvey(events, "codex", "m", asOf)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "✓100%") {
		t.Errorf("model row = %q, want a ✓100%% segment (event is by claude, counted in the all-agents model row)", lines[0])
	}
	if !strings.Contains(lines[1], "—") {
		t.Errorf("combo row = %q, want a — segment (no codex events)", lines[1])
	}
	if !strings.HasPrefix(lines[1], "codex × model") {
		t.Errorf("combo row = %q, want it to start with the agent label", lines[1])
	}
}

// TestFormatAfterSurveyAlignsColumns verifies the two rows' window
// segments are padded to matching column widths: when both rows report
// identical stats (the only event is by the surveyed agent), everything
// after the label must be byte-identical regardless of the two labels'
// different lengths ("model" vs "claude × model").
func TestFormatAfterSurveyAlignsColumns(t *testing.T) {
	asOf := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Agent: "claude", ModelID: "m", Timestamp: asOf.Add(-1 * time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
	}
	out := FormatAfterSurvey(events, "claude", "m", asOf)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	modelBody := lines[0][strings.Index(lines[0], "1d"):]
	comboBody := lines[1][strings.Index(lines[1], "1d"):]
	if modelBody != comboBody {
		t.Fatalf("model body = %q, combo body = %q, want equal (column alignment)", modelBody, comboBody)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/survey/... -run 'TestFormat' -v`
Expected: FAIL — `FormatAfterSurvey`, `FormatPickerSegment` undefined (compile error).

- [ ] **Step 3: Implement `internal/survey/format.go`**

```go
package survey

import (
	"fmt"
	"time"
)

// ratingCell renders one rating average with its one-letter prefix ("q" or
// "s"), or "<prefix>-" when the rating is unrated (RatedX == 0).
func ratingCell(prefix string, avg float64, ok bool) string {
	if !ok {
		return prefix + "-"
	}
	return fmt.Sprintf("%s%.1f", prefix, avg)
}

// FormatPickerSegment renders the compact 30-day survey segment appended
// to a model picker row: "[⚠]✓<pct> q<quality> s<speed> n<answered>".
// Returns "" when Answered == 0 so the caller can omit the segment
// entirely rather than showing a meaningless "✓- q- s- n0".
//
// ⚠ replaces ✓ when the combo looks broken: WorkedPct < 70% with at least
// 3 answered surveys (Answered < 3 is too little signal to flag).
func FormatPickerSegment(s Stats) string {
	pct, ok := s.WorkedPct()
	if !ok {
		return ""
	}
	marker := "✓"
	if pct < 70 && s.Answered >= 3 {
		marker = "⚠"
	}
	quality, qok := s.QualityAvg()
	speed, sok := s.SpeedAvg()
	return fmt.Sprintf("%s%d%% %s %s n%d",
		marker, int(pct+0.5), ratingCell("q", quality, qok), ratingCell("s", speed, sok), s.Answered)
}

// windowSpec pairs a window's display label with its duration, in the
// fixed 1d/7d/30d display order.
type windowSpec struct {
	label string
	dur   time.Duration
}

var afterSurveyWindows = []windowSpec{
	{"1d", Window1d},
	{"7d", Window7d},
	{"30d", Window30d},
}

// windowSegment renders one window's cell: "<label> —" when nothing was
// answered in that window, otherwise "<label> ✓<pct> q<quality> s<speed>
// (<n>)".
func windowSegment(label string, s Stats) string {
	pct, ok := s.WorkedPct()
	if !ok {
		return label + " —"
	}
	quality, qok := s.QualityAvg()
	speed, sok := s.SpeedAvg()
	return fmt.Sprintf("%s ✓%d%% %s %s (%d)",
		label, int(pct+0.5), ratingCell("q", quality, qok), ratingCell("s", speed, sok), s.Answered)
}

// formatStatsRow pads label to labelWidth, then each segment to its
// column's width (colWidth[i]) with 4 trailing spaces — except the final
// segment, which is unpadded. Used by FormatAfterSurvey so its two output
// rows align column-for-column regardless of which window has data.
func formatStatsRow(label string, labelWidth int, segs []string, colWidth []int) string {
	row := fmt.Sprintf("%-*s  ", labelWidth, label)
	for i, seg := range segs {
		if i == len(segs)-1 {
			row += seg
			continue
		}
		row += fmt.Sprintf("%-*s    ", colWidth[i], seg)
	}
	return row
}

// FormatAfterSurvey renders the two-row post-survey stats block: the
// model's own row (all agents) and the agent×model combo row, with each
// window's segment padded so the 1d/7d/30d columns align between the two
// rows regardless of which segments have data.
func FormatAfterSurvey(events []Event, agent, modelID string, asOf time.Time) string {
	modelLabel := "model"
	comboLabel := fmt.Sprintf("%s × model", agent)
	labelWidth := len(modelLabel)
	if len(comboLabel) > labelWidth {
		labelWidth = len(comboLabel)
	}

	modelSegs := make([]string, len(afterSurveyWindows))
	comboSegs := make([]string, len(afterSurveyWindows))
	for i, w := range afterSurveyWindows {
		modelSegs[i] = windowSegment(w.label, ModelStats(events, w.dur, asOf)[modelID])
		comboSegs[i] = windowSegment(w.label, AgentModelStats(events, agent, w.dur, asOf)[modelID])
	}

	colWidth := make([]int, len(afterSurveyWindows))
	for i := range afterSurveyWindows {
		if len(modelSegs[i]) > colWidth[i] {
			colWidth[i] = len(modelSegs[i])
		}
		if len(comboSegs[i]) > colWidth[i] {
			colWidth[i] = len(comboSegs[i])
		}
	}

	return fmt.Sprintf("%s\n%s",
		formatStatsRow(modelLabel, labelWidth, modelSegs, colWidth),
		formatStatsRow(comboLabel, labelWidth, comboSegs, colWidth))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/survey/... -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add internal/survey/format.go internal/survey/format_test.go
git commit -m "feat(survey): add FormatAfterSurvey and FormatPickerSegment

completes plan item #3"
```

---

### Task 4: `internal/survey` — PromptRun

**Files:**
- Create: `internal/survey/prompt.go`
- Test: `internal/survey/prompt_test.go`

**Interfaces:**
- Consumes: `Store`, `Event`, `FormatAfterSurvey`, `now` from Tasks 1-3; `config.Model` from `internal/config`.
- Produces: `func PromptRun(r io.Reader, w io.Writer, store Store, agent string, m config.Model)`, package var `stdinTTY func() bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/survey/prompt_test.go`:

```go
package survey

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func withTTY(t *testing.T, val bool) {
	t.Helper()
	old := stdinTTY
	stdinTTY = func() bool { return val }
	t.Cleanup(func() { stdinTTY = old })
}

// TestPromptRunNoopWhenNotTTY verifies the survey silently does nothing on
// a non-interactive launch (piped stdin, CI) — the design explicitly
// rejects a config toggle in favor of this guard.
func TestPromptRunNoopWhenNotTTY(t *testing.T) {
	withTTY(t, false)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\n"), &out, store, "claude", config.Model{ID: "m"})
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty (non-TTY guard)", out.String())
	}
	if len(store.Events()) != 0 {
		t.Fatalf("Events() = %v, want empty", store.Events())
	}
}

// TestPromptRunNoopForCommandAgent verifies command agents (m.ID == "",
// e.g. shell) are never surveyed — there's no model verdict to record.
func TestPromptRunNoopForCommandAgent(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\n"), &out, store, "shell", config.Model{})
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty (command-agent guard)", out.String())
	}
}

// TestPromptRunYesFlow verifies the full y -> speed -> quality path
// records a worked event with both ratings and prints the after-survey
// stats block.
func TestPromptRunYesFlow(t *testing.T) {
	withTTY(t, true)
	now = func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\n"), &out, store, "claude", config.Model{ID: "ollama/gemma4:9b"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	e := events[0]
	if e.Worked == nil || !*e.Worked {
		t.Fatalf("Worked = %v, want true", e.Worked)
	}
	if e.Speed == nil || *e.Speed != 4 {
		t.Fatalf("Speed = %v, want 4", e.Speed)
	}
	if e.Quality == nil || *e.Quality != 5 {
		t.Fatalf("Quality = %v, want 5", e.Quality)
	}
	if !strings.Contains(out.String(), "survey saved") {
		t.Errorf("output = %q, want it to contain \"survey saved\"", out.String())
	}
	if !strings.Contains(out.String(), "model") {
		t.Errorf("output = %q, want the after-survey stats block", out.String())
	}
}

// TestPromptRunNoFlowStopsAfterQ1 verifies "n" records a failed verdict and
// never prompts for ratings — a failed run has nothing to rate.
func TestPromptRunNoFlowStopsAfterQ1(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("n\n"), &out, store, "claude", config.Model{ID: "m"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	if events[0].Worked == nil || *events[0].Worked {
		t.Fatalf("Worked = %v, want false", events[0].Worked)
	}
	if strings.Contains(out.String(), "speed") {
		t.Errorf("output = %q, should not prompt for speed after \"no\"", out.String())
	}
}

// TestPromptRunSkipFlow verifies both "s" and a bare Enter record a skip
// event with no verdict.
func TestPromptRunSkipFlow(t *testing.T) {
	for _, input := range []string{"s\n", "\n"} {
		withTTY(t, true)
		store := NewStoreAt(t.TempDir())
		var out bytes.Buffer
		PromptRun(strings.NewReader(input), &out, store, "claude", config.Model{ID: "m"})

		events := store.Events()
		if len(events) != 1 {
			t.Fatalf("input %q: Events() = %d, want 1", input, len(events))
		}
		if !events[0].Skipped {
			t.Fatalf("input %q: Skipped = false, want true", input)
		}
		if events[0].Worked != nil {
			t.Fatalf("input %q: Worked = %v, want nil", input, events[0].Worked)
		}
	}
}

// TestPromptRunInvalidQ1Reprompts verifies an unrecognized Q1 answer
// re-prompts instead of silently defaulting, so a mistyped key doesn't
// record the wrong verdict.
func TestPromptRunInvalidQ1Reprompts(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("x\ny\n\n\n"), &out, store, "claude", config.Model{ID: "m"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	if events[0].Worked == nil || !*events[0].Worked {
		t.Fatalf("Worked = %v, want true (recovered after reprompt)", events[0].Worked)
	}
	if !strings.Contains(out.String(), "please answer y, n, s") {
		t.Errorf("output = %q, want a reprompt message for the invalid \"x\"", out.String())
	}
}

// TestPromptRunRatingReprompts verifies an out-of-range rating (e.g. "9")
// re-prompts instead of silently recording a bad value, and that a bare
// Enter still leaves the rating unrated afterward.
func TestPromptRunRatingReprompts(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n9\n3\n\n"), &out, store, "claude", config.Model{ID: "m"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	e := events[0]
	if e.Speed == nil || *e.Speed != 3 {
		t.Fatalf("Speed = %v, want 3 (recovered after reprompt on invalid \"9\")", e.Speed)
	}
	if e.Quality != nil {
		t.Fatalf("Quality = %v, want nil (Enter-skipped)", e.Quality)
	}
	if !strings.Contains(out.String(), "please answer 1-5") {
		t.Errorf("output = %q, want a reprompt message for the invalid \"9\"", out.String())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/survey/... -run 'TestPromptRun' -v`
Expected: FAIL — `PromptRun`, `stdinTTY` undefined (compile error).

- [ ] **Step 3: Implement `internal/survey/prompt.go`**

```go
package survey

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// stdinTTY is a test seam wrapping the real TTY check, mirroring cmd/wt's
// isStdinTTY/stdinTTY pattern. PromptRun's r/w are injected for tests, but
// TTY-ness is a property of the real process's stdin, not of whatever
// io.Reader a test passes in — so it needs its own seam rather than
// inferring TTY-ness from r.
var stdinTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

// answer identifies the Q1 verdict.
type answer int

const (
	answerSkip answer = iota
	answerNo
	answerYes
)

// PromptRun runs the up-to-three-question post-session survey against r/w,
// records the answer via store, and prints the accumulated stats. It is a
// no-op when stdin is not a TTY (non-interactive launch) or when m.ID == ""
// (command agents like shell have no model to survey) — the single guard
// both the TUI and non-TUI launch paths rely on.
func PromptRun(r io.Reader, w io.Writer, store Store, agent string, m config.Model) {
	if !stdinTTY() || m.ID == "" {
		return
	}
	scanner := bufio.NewScanner(r)

	verdict, ok := promptWorked(scanner, w)
	if !ok {
		return // reader exhausted (e.g. closed pipe); nothing to record
	}

	var e Event
	switch verdict {
	case answerSkip:
		e = Event{Agent: agent, ModelID: m.ID, Skipped: true}
	case answerNo:
		e = Event{Agent: agent, ModelID: m.ID, Worked: boolPtr(false)}
	case answerYes:
		speed := promptRating(scanner, w, "speed 1(slow)-5(fast)?")
		quality := promptRating(scanner, w, "quality 1(bad)-5(great)?")
		e = Event{Agent: agent, ModelID: m.ID, Worked: boolPtr(true), Speed: speed, Quality: quality}
	}

	if err := store.Record(e); err != nil {
		fmt.Fprintf(os.Stderr, "wt: survey not saved: %v\n", err)
		return
	}
	fmt.Fprintln(w, "survey saved")
	fmt.Fprintln(w, FormatAfterSurvey(store.Events(), agent, m.ID, now().UTC()))
}

// promptWorked runs Q1's reprompt loop. The bool return is false only when
// the scanner is exhausted without a valid line (e.g. a closed reader in a
// test) — a real TTY session always eventually yields a valid answer or an
// Enter (skip).
func promptWorked(scanner *bufio.Scanner, w io.Writer) (answer, bool) {
	for {
		fmt.Fprint(w, "survey · did it work? [y]es / [n]o / [s]kip (Enter=skip) ")
		if !scanner.Scan() {
			return answerSkip, false
		}
		switch scanner.Text() {
		case "y", "Y":
			return answerYes, true
		case "n", "N":
			return answerNo, true
		case "s", "S", "":
			return answerSkip, true
		default:
			fmt.Fprintln(w, "please answer y, n, s, or Enter to skip")
		}
	}
}

// promptRating runs one Q2/Q3 reprompt loop. Returns nil when the user
// presses Enter (leave unrated) or when the scanner is exhausted.
func promptRating(scanner *bufio.Scanner, w io.Writer, question string) *int {
	for {
		fmt.Fprintf(w, "survey · %s (Enter=skip) ", question)
		if !scanner.Scan() {
			return nil
		}
		text := scanner.Text()
		if text == "" {
			return nil
		}
		if len(text) == 1 && text[0] >= '1' && text[0] <= '5' {
			v := int(text[0] - '0')
			return &v
		}
		fmt.Fprintln(w, "please answer 1-5, or Enter to skip")
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/survey/... -v`
Expected: PASS for all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add internal/survey/prompt.go internal/survey/prompt_test.go
git commit -m "feat(survey): add PromptRun for the post-session survey

completes plan item #4"
```

---

### Task 5: Wire the survey into the non-TUI launch path

**Files:**
- Modify: `cmd/wt/launch.go:163-184` (`runAgentCmd`)
- Test: `cmd/wt/launch_test.go`

**Interfaces:**
- Consumes: `survey.PromptRun`, `survey.NewStore` from Task 4.

- [ ] **Step 1: Write the failing test**

Add to `cmd/wt/launch_test.go` (after `TestRunAgentCmdLeadingNewlineBeforeSummary`):

```go
// TestRunAgentCmdSurveyNoopWithoutTTY verifies runAgentCmd still returns
// normally and prints only the summary line when stdin is not a TTY (the
// state of the test process's stdin) — the post-run survey prompt must
// never hang or error a non-interactive run, and must not print any
// prompt text in that case.
func TestRunAgentCmdSurveyNoopWithoutTTY(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := exec.Command(truePath)
	if err := runAgentCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}); err != nil {
		t.Fatalf("runAgentCmd: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "survey ·") {
		t.Errorf("stdout = %q, should not contain survey prompts when stdin is not a TTY", string(out))
	}
	if !strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("stdout = %q, want the summary line", string(out))
	}
}
```

- [ ] **Step 2: Run the test to verify it currently passes trivially, then confirm it would fail without the wiring**

Run: `go test ./cmd/wt/... -run TestRunAgentCmdSurveyNoopWithoutTTY -v`
Expected: PASS already (nothing to survey yet) — this step exists to confirm the assertions compile and the harness (`true` binary, stdout pipe) works before Step 3 adds the call this test guards against regressing.

- [ ] **Step 3: Wire `survey.PromptRun` into `runAgentCmd`**

In `cmd/wt/launch.go`, add the import:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
```

Change `runAgentCmd` (currently `cmd/wt/launch.go:165-184`):

```go
// runAgentCmd wires stdio through to the agent, runs it, prints the post-run
// summary line, runs the post-session survey, and propagates the agent's
// exit code to the caller. The survey call sits between the summary Println
// and the os.Exit(ExitCode) branch so a non-zero agent exit is still
// surveyed — a crashed session is exactly a "did it work? no" data point.
func runAgentCmd(cmd *exec.Cmd, agent string, m config.Model) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start := time.Now()
	err := cmd.Run()
	// Leading "\n" guards against the agent's last byte being non-newline
	// (e.g. a bare prompt or a SIGINT-truncated line) — without it the
	// summary would glue to that partial output. Println adds the trailing
	// newline itself, so the line is always self-terminated.
	fmt.Println("\n" + agents.Summary(agent, m, time.Since(start)))
	survey.PromptRun(os.Stdin, os.Stdout, survey.NewStore(), agent, m)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wt/... -run 'TestRunAgentCmd' -v`
Expected: PASS — including the pre-existing `TestRunAgentCmdPrintsSummary` and `TestRunAgentCmdLeadingNewlineBeforeSummary`, which must still pass unchanged.

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "feat(wt): survey the non-TUI launch path after every exit

completes plan item #5"
```

---

### Task 6: Wire the survey into the TUI launch path

**Files:**
- Create: `internal/tui/survey.go`
- Modify: `internal/tui/launch.go:16-71` (`pendingSurvey` type/var, stash in `runAndWaitCmd`)
- Modify: `internal/tui/app.go:907-947` (`Run`, extract `printPendingSummaryAndSurvey`)
- Modify: `internal/tui/launch_test.go`
- Modify: `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `survey.PromptRun`, `survey.NewStore`, `survey.Store` from Task 4.
- Produces: `var newSurveyStore func() survey.Store`, `var runSurvey func(agent string, m config.Model)`, `type pendingSurvey struct{ agent string; m config.Model }`, `var pendingSurveyState pendingSurvey`, `func printPendingSummaryAndSurvey()`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/survey.go` seams first (needed for the tests to compile — this file has no logic to TDD beyond the two seam vars, so it is written directly rather than test-first):

```go
package tui

import (
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// newSurveyStore is a seam for tests: production uses realNewSurveyStore
// (the default config dir); tests swap it to isolate from the real
// survey.jsonl, mirroring newUsageStore in model_list.go.
var newSurveyStore = realNewSurveyStore

func realNewSurveyStore() survey.Store { return survey.NewStore() }

// runSurvey is a seam for tests: production runs the real post-session
// survey prompt against the parent terminal; tests swap it to assert it
// was invoked with the right agent/model without needing a TTY.
var runSurvey = realRunSurvey

func realRunSurvey(agent string, m config.Model) {
	survey.PromptRun(os.Stdin, os.Stdout, newSurveyStore(), agent, m)
}
```

Add to `internal/tui/launch_test.go` (after `TestRunAndWaitCmdCapturesSummaryOnFailure`):

```go
// TestRunAndWaitCmdCapturesPendingSurvey verifies runAndWaitCmd stashes the
// agent/model into pendingSurveyState next to pendingSummary, so Run() can
// invoke the post-session survey after the alt-screen tears down — the
// same capture-then-emit pattern the summary line uses.
func TestRunAndWaitCmdCapturesPendingSurvey(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	prev := pendingSurveyState
	pendingSurveyState = pendingSurvey{}
	t.Cleanup(func() { pendingSurveyState = prev })

	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "claude", config.Model{ID: "claude/sonnet"})()
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if pendingSurveyState.agent != "claude" || pendingSurveyState.m.ID != "claude/sonnet" {
		t.Fatalf("pendingSurveyState = %+v, want agent=claude model=claude/sonnet", pendingSurveyState)
	}
}
```

Add to `internal/tui/app_test.go` (anywhere at top level):

```go
// TestPrintPendingSummaryAndSurveyOrder verifies Run()'s post-p.Run() logic
// prints the summary before invoking the survey (matching the non-TUI
// path's summary → survey ordering) and clears both package vars so a
// later launch in the same process doesn't replay stale state.
func TestPrintPendingSummaryAndSurveyOrder(t *testing.T) {
	prevSummary, prevSurvey, prevRunSurvey := pendingSummary, pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSummary = prevSummary
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
	})

	pendingSummary = "wt: claude · claude/sonnet · 1s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "claude/sonnet"}}

	var calledWith pendingSurvey
	called := false
	runSurvey = func(agent string, m config.Model) {
		called = true
		calledWith = pendingSurvey{agent: agent, m: m}
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if !strings.Contains(string(out), "wt: claude · claude/sonnet · 1s") {
		t.Errorf("stdout = %q, want the summary line", string(out))
	}
	if !called {
		t.Fatal("runSurvey was not invoked")
	}
	if calledWith.agent != "claude" || calledWith.m.ID != "claude/sonnet" {
		t.Fatalf("runSurvey called with %+v, want agent=claude model=claude/sonnet", calledWith)
	}
	if pendingSummary != "" {
		t.Errorf("pendingSummary = %q, want cleared", pendingSummary)
	}
	if pendingSurveyState.agent != "" {
		t.Errorf("pendingSurveyState = %+v, want cleared", pendingSurveyState)
	}
}

// TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch verifies runSurvey is
// never invoked when no launch happened (pendingSurveyState.agent == ""),
// e.g. the user quit from the worktree picker without launching anything.
func TestPrintPendingSummaryAndSurveySkipsWhenNoLaunch(t *testing.T) {
	prevSurvey, prevRunSurvey := pendingSurveyState, runSurvey
	t.Cleanup(func() {
		pendingSurveyState = prevSurvey
		runSurvey = prevRunSurvey
	})
	pendingSurveyState = pendingSurvey{}
	called := false
	runSurvey = func(agent string, m config.Model) { called = true }

	printPendingSummaryAndSurvey()
	if called {
		t.Fatal("runSurvey was invoked with no pending launch")
	}
}
```

Check `internal/tui/app_test.go`'s existing imports include `"io"`, `"os"`, `"strings"`, and `"github.com/ohanaverse/local-ai-setup/wt/internal/config"`; add any that are missing.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestRunAndWaitCmdCapturesPendingSurvey|TestPrintPendingSummaryAndSurvey' -v`
Expected: FAIL — `pendingSurveyState`, `pendingSurvey`, `printPendingSummaryAndSurvey` undefined (compile error).

- [ ] **Step 3: Implement the wiring**

In `internal/tui/launch.go`, add after the existing `pendingSummary` declaration (currently ending at line 31):

```go
// pendingSurvey holds the agent/model to survey once the alt-screen tears
// down, mirroring pendingSummary's capture-then-emit pattern (printing
// here would land inside the discarded alt-screen buffer).
type pendingSurvey struct {
	agent string
	m     config.Model
}

// pendingSurveyState is populated by runAndWaitCmd next to pendingSummary.
// Run() invokes the survey through it after printing the summary. Reset by
// Run() before launch.
var pendingSurveyState pendingSurvey
```

In `runAndWaitCmd`, change:

```go
		pendingSummary = agents.Summary(agent, m, time.Since(start))
		return launchDoneMsg{err: err}
```

to:

```go
		pendingSummary = agents.Summary(agent, m, time.Since(start))
		pendingSurveyState = pendingSurvey{agent: agent, m: m}
		return launchDoneMsg{err: err}
```

In `internal/tui/app.go`, change `Run` (currently lines 907-947):

```go
	currentProgram = p
	// Reset any summary captured by a previous run (e.g. from a test
	// invocation sharing the process).
	pendingSummary = ""
	_, err := p.Run()
	// Print the post-run summary line on the parent terminal. The
	// alt-screen is now torn down (p.Run() has returned and bubbletea
	// has called exitAltScreen), so stdout reaches the user's terminal
	// rather than a discarded buffer.
	if pendingSummary != "" {
		// Leading "\n" guards against the agent's last byte being
		// non-newline so the summary always lands on a fresh line.
		// Println adds the trailing newline itself.
		fmt.Println("\n" + pendingSummary)
		pendingSummary = ""
	}
	return err
}
```

to:

```go
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingSurveyState = pendingSurvey{}
	_, err := p.Run()
	// The alt-screen is now torn down (p.Run() has returned and bubbletea
	// has called exitAltScreen), so stdout reaches the user's terminal
	// rather than a discarded buffer.
	printPendingSummaryAndSurvey()
	return err
}

// printPendingSummaryAndSurvey prints the captured post-run summary (if
// any) and then runs the post-session survey (if a launch happened),
// matching the summary → survey ordering used by the non-TUI path. It is
// extracted from Run() so this ordering is unit-testable without a real
// tea.Program/TTY.
func printPendingSummaryAndSurvey() {
	if pendingSummary != "" {
		// Leading "\n" guards against the agent's last byte being
		// non-newline so the summary always lands on a fresh line.
		// Println adds the trailing newline itself.
		fmt.Println("\n" + pendingSummary)
		pendingSummary = ""
	}
	if pendingSurveyState.agent != "" {
		runSurvey(pendingSurveyState.agent, pendingSurveyState.m)
		pendingSurveyState = pendingSurvey{}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS for all tests, including the pre-existing `TestRunAndWaitCmdCapturesSummaryOnSuccess`/`OnFailure`.

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/survey.go internal/tui/launch.go internal/tui/app.go internal/tui/launch_test.go internal/tui/app_test.go
git commit -m "feat(wt): survey the TUI launch path after every exit

completes plan item #6"
```

---

### Task 7: Model picker survey segment

**Files:**
- Modify: `internal/tui/model_list.go:174-251` (`buildModelItems` signature + segment)
- Modify: `internal/tui/app.go:669-750` (`enterModelPhase`)
- Modify: `internal/tui/model_list_test.go` (5 call sites)
- Modify: `internal/tui/model_line_test.go` (1 call site)
- Modify: `internal/tui/model_family_test.go` (4 call sites)
- Modify: `internal/tui/agent_model_test.go` (1 call site)
- Modify: `internal/tui/testhelpers_test.go` (1 call site)

**Interfaces:**
- Consumes: `survey.Stats`, `survey.FormatPickerSegment`, `survey.AgentModelStats`, `survey.Window30d` from Tasks 2-3; `newSurveyStore` from Task 6.
- Produces: `buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, lastID string, stats map[string]survey.Stats) []*modelItem` (new trailing `stats` parameter).

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/model_list_test.go` (after `TestBuildModelItemsNoMarkerWithoutLastLaunched`):

```go
// TestBuildModelItemsAppendsSurveySegment verifies the survey stats
// segment is appended last on the line — after the usage counts, pricing,
// and [tags] — so it never shifts any existing column.
func TestBuildModelItemsAppendsSurveySegment(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal, Tags: []string{"code"}},
	}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	stats := map[string]survey.Stats{
		"ollama/gemma4:9b": {Answered: 12, Worked: 11, Failed: 1, RatedQuality: 10, QualitySum: 42, RatedSpeed: 10, SpeedSum: 39},
	}
	items := buildModelItems(models, familyOf, store, "", stats)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	line := items[0].line
	wantSeg := "✓92% q4.2 s3.9 n12"
	if !strings.HasSuffix(line, wantSeg) {
		t.Fatalf("line = %q, want it to end with %q", line, wantSeg)
	}
	if idx := strings.Index(line, "[code]"); idx == -1 || idx > strings.Index(line, wantSeg) {
		t.Fatalf("line = %q, want the survey segment after [tags]", line)
	}
}

// TestBuildModelItemsOmitsSurveySegmentWhenNoAnswered verifies a model
// with zero answered surveys renders no segment at all — existing lines
// (models never surveyed) stay byte-identical whether stats is nil or an
// explicit zero-value entry.
func TestBuildModelItemsOmitsSurveySegmentWhenNoAnswered(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	withoutStats := buildModelItems(models, familyOf, store, "", nil)
	withZeroStats := buildModelItems(models, familyOf, store, "", map[string]survey.Stats{"ollama/gemma4:9b": {}})
	if withoutStats[0].line != withZeroStats[0].line {
		t.Fatalf("nil stats map produced %q, zero-value stats entry produced %q, want identical", withoutStats[0].line, withZeroStats[0].line)
	}
	if strings.Contains(withoutStats[0].line, "✓") || strings.Contains(withoutStats[0].line, "⚠") {
		t.Errorf("line = %q, want no survey segment when Answered == 0", withoutStats[0].line)
	}
}
```

Add `"github.com/ohanaverse/local-ai-setup/wt/internal/survey"` to `internal/tui/model_list_test.go`'s imports.

Update every other `buildModelItems(...)` call site to pass a trailing `nil` (these tests don't exercise survey stats):

In `internal/tui/model_list_test.go`:

```go
	}, map[string]string{"ollama/gemma4:9b": "gemma4"}, store, "")
```
→
```go
	}, map[string]string{"ollama/gemma4:9b": "gemma4"}, store, "", nil)
```

```go
	}, store, "")
	if len(items) != 2 {
```
→
```go
	}, store, "", nil)
	if len(items) != 2 {
```

```go
	items := buildModelItems(models, map[string]string{"partial": "test"}, store, "")
```
→
```go
	items := buildModelItems(models, map[string]string{"partial": "test"}, store, "", nil)
```

```go
	items := buildModelItems(models, familyOf, store, "ollama/gemma4:14b")
```
→
```go
	items := buildModelItems(models, familyOf, store, "ollama/gemma4:14b", nil)
```

```go
		items := buildModelItems(models, familyOf, store, lastID)
```
→
```go
		items := buildModelItems(models, familyOf, store, lastID, nil)
```

In `internal/tui/model_line_test.go`:

```go
			items := buildModelItems(tt.models, familyOf, store, "")
```
→
```go
			items := buildModelItems(tt.models, familyOf, store, "", nil)
```

In `internal/tui/model_family_test.go` (4 occurrences — 3 identical `buildModelItems(models, familyOfFor(), store, "")` calls at lines 193, 227, 252, plus 1 at 158):

```go
	items := buildModelItems(modelFamilies(), familyOfFor(), newUsageStore(), "")
```
→
```go
	items := buildModelItems(modelFamilies(), familyOfFor(), newUsageStore(), "", nil)
```

For each of the three `buildModelItems(models, familyOfFor(), store, "")` calls (in `TestBuildModelItemsFamilyColumnShowsFamilyTotal`, `TestBuildModelItemsFamilyCountsUseFullCatalog`, and the `buildModelItems(modelFamilies(), familyOfFor(), store, "")` in `TestBuildModelItemsEmptyFamilyShowsAggregate`), append `, nil` before the closing `)`, e.g.:

```go
	items := buildModelItems(models, familyOfFor(), store, "")
	if len(items) != 2 {
```
→
```go
	items := buildModelItems(models, familyOfFor(), store, "", nil)
	if len(items) != 2 {
```

(apply the equivalent change at each of the three call sites, matched by the surrounding function body — each occurs exactly once per test function, so the preceding `store := stubUsageStore(t)` / function name makes each edit unique)

In `internal/tui/agent_model_test.go`:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), lastID)
```
→
```go
	items := buildModelItems(models, familyOf, newUsageStore(), lastID, nil)
```

In `internal/tui/testhelpers_test.go`:

```go
	items := buildModelItems(models, familyOf, newUsageStore(), "")
```
→
```go
	items := buildModelItems(models, familyOf, newUsageStore(), "", nil)
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go build ./... 2>&1 | head -50`
Expected: compile errors — every unmodified `buildModelItems` call site (Step 1's `nil`-appended edits not yet applied to `buildModelItems` itself) has the wrong argument count once the signature changes in Step 3. Confirm the two new tests fail to compile before Step 3.

- [ ] **Step 3: Implement the signature change and segment**

In `internal/tui/model_list.go`, add `"github.com/ohanaverse/local-ai-setup/wt/internal/survey"` to the imports. Change the function signature and the `[tags]` block (currently `internal/tui/model_list.go:174` and `:240-242`):

```go
func buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, lastID string) []*modelItem {
```
→
```go
func buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store, lastID string, stats map[string]survey.Stats) []*modelItem {
```

```go
		if len(m.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(m.Tags, ","))
		}

		items = append(items, &modelItem{
```
→
```go
		if len(m.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(m.Tags, ","))
		}
		// Survey segment always appended last so it never shifts any
		// existing column; omitted entirely when there's nothing answered
		// (FormatPickerSegment returns "" in that case).
		if st, ok := stats[m.ID]; ok {
			if seg := survey.FormatPickerSegment(st); seg != "" {
				line += " " + seg
			}
		}

		items = append(items, &modelItem{
```

In `internal/tui/app.go`, add `"time"` and `"github.com/ohanaverse/local-ai-setup/wt/internal/survey"` to the imports. Change `enterModelPhase` (currently `internal/tui/app.go:695-702`):

```go
	// One Rotation for both the ▶ marker and the cursor: each New() runs
	// migrate() (os.Stat + os.ReadDir over the config dir), so constructing
	// twice per picker entry doubles that scan. A missing/unreadable
	// rotation.state yields "" and leaves every row unmarked.
	rot := rotation.New()
	lastID, _ := rot.Last()
	// Build the sorted, compact model list.
	items := buildModelItems(models, familyOf, newUsageStore(), lastID)
```
→
```go
	// One Rotation for both the ▶ marker and the cursor: each New() runs
	// migrate() (os.Stat + os.ReadDir over the config dir), so constructing
	// twice per picker entry doubles that scan. A missing/unreadable
	// rotation.state yields "" and leaves every row unmarked.
	rot := rotation.New()
	lastID, _ := rot.Last()
	// Agent-scoped 30-day survey stats, so a bad agent×model combo is
	// visible before launching (design requirement 5). One Events() read
	// feeds the whole picker, mirroring the single usage Counts() pass.
	surveyStats := survey.AgentModelStats(newSurveyStore().Events(), agent, survey.Window30d, time.Now().UTC())
	// Build the sorted, compact model list.
	items := buildModelItems(models, familyOf, newUsageStore(), lastID, surveyStats)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go build ./... && go vet ./...`
Expected: no errors.

Run: `go test ./internal/tui/... -v`
Expected: PASS for all tests, including every pre-existing `TestBuildModelItems*`/`TestAdjacentModelsShareFamilyColumn`/`TestSortModelsByUsage*` test.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model_list.go internal/tui/app.go internal/tui/model_list_test.go internal/tui/model_line_test.go internal/tui/model_family_test.go internal/tui/agent_model_test.go internal/tui/testhelpers_test.go
git commit -m "feat(wt): show agent-scoped 30-day survey stats in the model picker

completes plan item #7"
```

---

### Task 8: `wt stats` command

**Files:**
- Create: `cmd/wt/stats.go`
- Modify: `cmd/wt/main.go:362` (`cmd.AddCommand`)
- Test: `cmd/wt/stats_test.go`

**Interfaces:**
- Consumes: `survey.NewStore`, `survey.Event`, `survey.Stats`, `survey.ModelStats`, `survey.AllAgentModelStats`, `survey.Window1d/7d/30d` from Tasks 1-2; `renderTable`, `mustGetString` from `cmd/wt/helpers.go`; `newTestApp` (test helper already defined in `cmd/wt/commands_config_test.go`, same package).
- Produces: `func statsCmd(a *app) *cobra.Command`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/wt/stats_test.go`:

```go
// Tests for `wt stats`. Verifies window selection, --model/--agent
// filters, and the empty-store message. Seeds survey.jsonl directly
// (rather than through survey.Record) so timestamps can be placed at
// precise offsets from "now" without reaching into the survey package's
// internal now() seam.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

func boolPtr(b bool) *bool { return &b }
func intPtr(i int) *int    { return &i }

// seedSurveyEvents writes raw JSONL lines directly to <tmp>/agent-wt/survey.jsonl.
func seedSurveyEvents(t *testing.T, tmp string, events []survey.Event) {
	t.Helper()
	dir := filepath.Join(tmp, "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "survey.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStatsCmdEmptyStorePrintsMessage verifies `wt stats` never errors on
// a fresh install (no survey.jsonl yet) — it reports "no survey data"
// instead.
func TestStatsCmdEmptyStorePrintsMessage(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(out.String()) != "no survey data" {
		t.Fatalf("output = %q, want \"no survey data\"", out.String())
	}
}

// TestStatsCmdReportsAllAgentAndComboRows verifies the default (30d, no
// filters) report includes both the per-model "(all)" aggregate row and
// per-(agent,model) combo rows.
func TestStatsCmdReportsAllAgentAndComboRows(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
		{Agent: "codex", ModelID: "ollama/gemma4:9b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(false)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "(all)") {
		t.Errorf("output = %q, want a (all) aggregate row", got)
	}
	if !strings.Contains(got, "claude") || !strings.Contains(got, "codex") {
		t.Errorf("output = %q, want both agent combo rows", got)
	}
	if !strings.Contains(got, "ollama/gemma4:9b") {
		t.Errorf("output = %q, want the model id", got)
	}
}

// TestStatsCmdWindowFilter verifies --window 1d excludes an event outside
// the 1-day window that a wider window would include.
func TestStatsCmdWindowFilter(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "old-model", Timestamp: now.Add(-10 * 24 * time.Hour), Worked: boolPtr(true)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--window", "1d"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.TrimSpace(out.String()) != "no survey data" {
		t.Errorf("output = %q, want \"no survey data\" (10-day-old event excluded from 1d window)", out.String())
	}

	cmd30 := statsCmd(a)
	var out30 bytes.Buffer
	cmd30.SetOut(&out30)
	cmd30.SetArgs([]string{"--window", "30d"})
	if err := cmd30.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out30.String(), "old-model") {
		t.Errorf("30d output = %q, want old-model included", out30.String())
	}
}

// TestStatsCmdModelAndAgentFilters verifies --model and --agent narrow the
// report to matching rows only.
func TestStatsCmdModelAndAgentFilters(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "model-b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--model", "model-a"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "model-a") || strings.Contains(out.String(), "model-b") {
		t.Errorf("output = %q, want only model-a rows", out.String())
	}

	cmd2 := statsCmd(a)
	var out2 bytes.Buffer
	cmd2.SetOut(&out2)
	cmd2.SetArgs([]string{"--agent", "codex"})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out2.String(), "codex") || strings.Contains(out2.String(), "claude") {
		t.Errorf("output = %q, want only codex rows", out2.String())
	}
}

// TestStatsCmdInvalidWindow verifies an unrecognized --window value
// returns a clear error rather than silently falling back to a default.
func TestStatsCmdInvalidWindow(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := statsCmd(a)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--window", "5d"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for an invalid --window value")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wt/... -run TestStatsCmd -v`
Expected: FAIL — `statsCmd` undefined (compile error).

- [ ] **Step 3: Implement `cmd/wt/stats.go`**

```go
// wt stats — a read-only report over survey.jsonl (wt collects, wt
// reports). Never touches the modelman catalog: model ids are read
// straight from the survey events, so the command works even for a model
// that has since been removed from registry.toml.
package main

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/spf13/cobra"
)

// statsAllAgents labels the per-model aggregate row (all agents combined),
// distinct from any real agent name.
const statsAllAgents = "(all)"

// statsRow is one rendered row of `wt stats`: either a per-model "(all)"
// aggregate (Agent == statsAllAgents) or one (agent, model) combo.
type statsRow struct {
	ModelID string
	Agent   string
	Stats   survey.Stats
}

// statsCmd returns the `wt stats` command. It never affects the exit code
// on a missing/empty store — only a malformed --window value is an error.
func statsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report accumulated session survey stats",
		Long: "Report accumulated post-session survey stats (worked%, quality, speed)\n" +
			"per model and per agent×model combo, collected by the post-session\n" +
			"survey prompt.",
		RunE: func(cmd *cobra.Command, args []string) error {
			window, err := parseStatsWindow(mustGetString(cmd, "window"))
			if err != nil {
				return err
			}
			modelFilter := mustGetString(cmd, "model")
			agentFilter := mustGetString(cmd, "agent")

			events := survey.NewStore().Events()
			asOf := time.Now().UTC()
			rows := buildStatsRows(events, window, asOf, modelFilter, agentFilter)

			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no survey data")
				return nil
			}

			headers := []string{"MODEL", "AGENT", "WORKED%", "QUALITY", "SPEED", "N", "SKIPPED"}
			tableRows := make([][]string, 0, len(rows))
			for _, r := range rows {
				quality, qok := r.Stats.QualityAvg()
				speed, sok := r.Stats.SpeedAvg()
				tableRows = append(tableRows, []string{
					r.ModelID,
					r.Agent,
					formatPctCell(r.Stats),
					formatAvgCell(quality, qok),
					formatAvgCell(speed, sok),
					strconv.Itoa(r.Stats.Answered),
					strconv.Itoa(r.Stats.Skipped),
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), renderTable(headers, tableRows, a.theme))
			return nil
		},
	}
	cmd.Flags().String("window", "30d", "Stats window: 1d, 7d, or 30d")
	cmd.Flags().String("model", "", "Filter to one model id")
	cmd.Flags().String("agent", "", "Filter to one agent")
	return cmd
}

// parseStatsWindow maps a --window flag value to its duration.
func parseStatsWindow(s string) (time.Duration, error) {
	switch s {
	case "1d":
		return survey.Window1d, nil
	case "7d":
		return survey.Window7d, nil
	case "30d", "":
		return survey.Window30d, nil
	}
	return 0, fmt.Errorf("invalid --window %q: want 1d, 7d, or 30d", s)
}

// buildStatsRows merges the per-model "(all)" aggregate with every
// per-(agent,model) combo into one row list, applies the --model/--agent
// filters, drops rows with zero answered and zero skipped in the window,
// and sorts by model id (the "(all)" row first within a model, then agent
// name).
func buildStatsRows(events []survey.Event, window time.Duration, asOf time.Time, modelFilter, agentFilter string) []statsRow {
	modelStats := survey.ModelStats(events, window, asOf)
	combos := survey.AllAgentModelStats(events, window, asOf)

	modelIDs := make(map[string]bool, len(modelStats))
	for id := range modelStats {
		modelIDs[id] = true
	}
	for _, c := range combos {
		modelIDs[c.ModelID] = true
	}

	var rows []statsRow
	for id := range modelIDs {
		if modelFilter != "" && id != modelFilter {
			continue
		}
		if agentFilter == "" {
			if s := modelStats[id]; s.Answered > 0 || s.Skipped > 0 {
				rows = append(rows, statsRow{ModelID: id, Agent: statsAllAgents, Stats: s})
			}
		}
	}
	for _, c := range combos {
		if modelFilter != "" && c.ModelID != modelFilter {
			continue
		}
		if agentFilter != "" && c.Agent != agentFilter {
			continue
		}
		if c.Stats.Answered == 0 && c.Stats.Skipped == 0 {
			continue
		}
		rows = append(rows, statsRow{ModelID: c.ModelID, Agent: c.Agent, Stats: c.Stats})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ModelID != rows[j].ModelID {
			return rows[i].ModelID < rows[j].ModelID
		}
		if rows[i].Agent == statsAllAgents {
			return true
		}
		if rows[j].Agent == statsAllAgents {
			return false
		}
		return rows[i].Agent < rows[j].Agent
	})
	return rows
}

// formatPctCell renders WorkedPct as "N%", or "-" when unanswered.
func formatPctCell(s survey.Stats) string {
	pct, ok := s.WorkedPct()
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%d%%", int(pct+0.5))
}

// formatAvgCell renders a rating average to one decimal, or "-" when unrated.
func formatAvgCell(avg float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.1f", avg)
}
```

In `cmd/wt/main.go`, change (currently line 362):

```go
	cmd.AddCommand(rotateCmd(a), configCmd(a))
```
→
```go
	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wt/... -v`
Expected: PASS for all tests, including every pre-existing `cmd/wt` test.

Run: `go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/stats.go cmd/wt/stats_test.go cmd/wt/main.go
git commit -m "feat(wt): add \`wt stats\` command

completes plan item #8"
```

---

### Task 9: Docs

**Files:**
- Modify: `wt/CLAUDE.md`
- Modify: `docs/wt-agents/README.md`
- Create: `docs/wt-stats.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Add the `internal/survey` package row and a Session survey section to `wt/CLAUDE.md`**

In the package table (after the `internal/usage/` row):

```
| `internal/usage/` | append-only JSONL launch history with 1d/7d/30d counts, shared by `wt rotate`, the model picker's per-row usage columns, and the rotation module |
```
→
```
| `internal/usage/` | append-only JSONL launch history with 1d/7d/30d counts, shared by `wt rotate`, the model picker's per-row usage columns, and the rotation module |
| `internal/survey/` | post-session survey: append-only JSONL verdicts (worked/speed/quality), 1d/7d/30d stats per model and per agent×model combo — used by the post-run prompt, the model picker's survey segment, and `wt stats` |
```

In the "Post-run summary line" section, append a sentence:

```
After the launched subprocess exits, both the TUI and non-TUI paths print a single `wt: <agent> · <model-id> · <duration>` line to stdout (model segment omitted for command agents like `shell`). Emitted on success and non-zero exit; never affects the exit code. The formatter lives in `internal/agents.Summary` and is the single source of truth for both paths. See `docs/wt-agents/README.md#post-run-summary-line`.
```
→
```
After the launched subprocess exits, both the TUI and non-TUI paths print a single `wt: <agent> · <model-id> · <duration>` line to stdout (model segment omitted for command agents like `shell`). Emitted on success and non-zero exit; never affects the exit code. The formatter lives in `internal/agents.Summary` and is the single source of truth for both paths. See `docs/wt-agents/README.md#post-run-summary-line`.

Immediately after the summary, a post-session survey prompts up to three questions (did it work? speed? quality?) on the parent terminal — see [Session survey](#session-survey-go) below.
```

Add a new section after "## Post-run summary line" (before "### Legacy bash flags" if that heading exists in this file, otherwise at the end of the summary-line discussion — insert directly after the paragraph just edited):

```markdown
## Session survey (Go)

`internal/survey` records a post-session verdict for every agent launch with
a model (skipped for command agents like `shell`, whose `m.ID == ""`).
`survey.PromptRun` is the single implementation wired into both the non-TUI
path (`cmd/wt/launch.go runAgentCmd`, after the summary line, before the
exit-code propagation) and the TUI path (`internal/tui`, via the same
capture-then-emit pattern the summary line uses). It silently no-ops when
stdin is not a TTY.

Order on every launch: **summary → survey prompts → after-survey stats**.

- **Store:** `~/.config/agent-wt/survey.jsonl`, wt-owned, 30-day retention,
  pruned on every write (mirrors `internal/usage`).
- **Worked% semantics:** `worked / (worked + failed)` over *answered*
  surveys only; skips are recorded but excluded from the denominator.
- **Model picker:** each row gets a trailing `✓<pct> q<quality> s<speed>
  n<answered>` segment sourced from the agent-scoped 30-day stats (omitted
  when nothing has been answered yet); `⚠` replaces `✓` when
  `WorkedPct < 70%` and `Answered >= 3`.
- **`wt stats`** — see `docs/wt-stats.md`.
```

- [ ] **Step 2: Add a survey note to `docs/wt-agents/README.md`**

In the "Post-run summary line" section, after the paragraph ending "...so the line lands on a clean line in the parent terminal rather than inside the Bubble Tea frame." add:

```markdown
Immediately after the summary line, a post-session survey prompts up to
three questions on the parent terminal (did it work? speed 1-5? quality
1-5?), each answerable with Enter to skip. It silently does nothing when
stdin is not a TTY or when the launch had no model (command agents like
`shell`). See `docs/wt-stats.md` for how the collected data is reported.
```

- [ ] **Step 3: Create `docs/wt-stats.md`**

```markdown
# `wt stats`

Reports accumulated post-session survey stats: did an agent×model combo
work, how fast, how good. wt collects this data via the post-session
survey prompt (see [wt-agents/README.md#post-run-summary-line](./wt-agents/README.md#post-run-summary-line));
`wt stats` reports it.

```bash
wt stats [--window 1d|7d|30d] [--model <id>] [--agent <name>]
```

- `--window` — defaults to `30d`.
- `--model` — narrow to one model id.
- `--agent` — narrow to one agent.

## Output

One row per model's `(all)`-agents aggregate, plus one row per observed
(agent, model) combo — sorted by model id, `(all)` first, then agent name.
A combo with zero answered and zero skipped surveys in the window is
omitted. An empty store (or a filter matching nothing) prints
`no survey data` and always exits 0 — `wt stats` is a report, never a
gate.

```
$ wt stats
┌────────────────────┬────────┬─────────┬─────────┬───────┬────┬─────────┐
│ MODEL               │ AGENT  │ WORKED% │ QUALITY │ SPEED │ N  │ SKIPPED │
├────────────────────┼────────┼─────────┼─────────┼───────┼────┼─────────┤
│ ollama/gemma4:9b    │ (all)  │ 96%     │ 4.2     │ 3.9   │ 12 │ 2       │
│ ollama/gemma4:9b    │ claude │ 95%     │ 4.3     │ 4.0   │ 8  │ 1       │
│ ollama/gemma4:9b    │ codex  │ 100%    │ 3.8     │ 3.5   │ 4  │ 1       │
└────────────────────┴────────┴─────────┴─────────┴───────┴────┴─────────┘
```

`WORKED%`, `QUALITY`, and `SPEED` render `-` when there is nothing to
average (zero answered surveys, or zero rated surveys for that column).
`WORKED%` is `worked / (worked + failed)` over *answered* surveys only —
skips are tracked in the `SKIPPED` column but excluded from that
percentage.

## Where the data comes from

Every model-driven agent launch (TUI or non-TUI) prompts up to three
questions immediately after the agent exits:

1. **Did it work?** `[y]es / [n]o / [s]kip (Enter=skip)` — `n` records a
   failure and stops; `s`/Enter records a skip and stops.
2. **Speed 1(slow)-5(fast)?** (Enter=skip) — only asked after `y`.
3. **Quality 1(bad)-5(great)?** (Enter=skip) — only asked after `y`.

The prompt is silent (no output at all) when stdin is not a TTY, or when
the launch had no model (command agents like `shell`). There is no config
toggle to disable it — every-exit with a one-keypress skip is the intended
trade-off.

Right after answering, the same accumulated stats `wt stats` reports are
printed for the current model (all agents) and the current agent×model
combo, at the 1d/7d/30d windows.
```

- [ ] **Step 4: Verify links**

Run: `make check-links` (from the monorepo root, `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/session-survey`)
Expected: no broken links.

- [ ] **Step 5: Commit**

```bash
git add wt/CLAUDE.md wt/docs/wt-agents/README.md wt/docs/wt-stats.md
git commit -m "docs(wt): document the session survey and wt stats

completes plan item #9"
```

---

## Final verification

After all 9 tasks:

```bash
cd wt
go build ./...
go vet ./...
go test ./...
make check   # shellcheck/shfmt, if any shell scripts were touched (none in this plan)
```

From the monorepo root:

```bash
make test-all
```

Manual smoke test (needs a real TTY):

```bash
wt --cwd -A claude   # launch, exit the agent, answer the survey, confirm stats print
wt stats             # confirm the just-recorded event shows up
wt                   # open the picker, confirm the model row shows the new segment
```
