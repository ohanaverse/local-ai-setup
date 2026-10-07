// Package usage records and queries global model-launch history.
package usage

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

// retentionWindow is the longest count window Counts reports (30 days).
// RecordFor prunes events older than this on every write so usage.jsonl
// stays bounded by launch frequency instead of growing across the
// lifetime of the install.
const retentionWindow = 30 * 24 * time.Hour

// UsageCounts holds launch counts for the last 1, 7, and 30 days.
type UsageCounts struct {
	OneDay    int
	SevenDay  int
	ThirtyDay int
}

// event is one line in the usage JSONL file.
type event struct {
	ModelID   string    `json:"model_id"`
	Agent     string    `json:"agent,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Store is an interface for recording and querying model-launch history.
type Store interface {
	Record(modelID string) error
	RecordFor(agent, modelID string) error
	Counts(modelIDs []string) map[string]UsageCounts
	CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts
}

// StoreImpl reads and appends to the usage history file.
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
	return filepath.Join(s.dir, "usage.jsonl")
}

// now is overridable for tests.
var now = time.Now

// Record appends a launch event with no agent attribution. Prefer RecordFor;
// Record remains for callers that only know the model.
func (s *StoreImpl) Record(modelID string) error { return s.RecordFor("", modelID) }

// RecordFor appends one launch event for agent+modelID atomically, first dropping
// any existing events older than retentionWindow so the file doesn't grow
// unbounded — only the trailing 30-day window is ever read by Counts/CountsForAgent.
//
// Concurrency: the read-prune-write critical section is guarded by a POSIX
// advisory flock on a sidecar usage.jsonl.lock file in the same directory.
// This serializes concurrent wt processes (e.g. two terminal launches in
// the same config dir) so neither's just-recorded event is silently dropped
// by the other's later rename. The sidecar pattern keeps the lock attached
// to the file's location while the target file is renamed freely.
func (s *StoreImpl) RecordFor(agent, modelID string) error {
	e := event{ModelID: modelID, Agent: agent, Timestamp: now().UTC()}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := s.path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(filepath.Dir(path), "usage.jsonl.lock")
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
// window of asOf. Lines that fail to parse as an event are dropped along
// with expired ones. A scan error (e.g. a corrupt line exceeding bufio's
// token limit) is surfaced rather than silently truncating the pruned
// output, since the result overwrites the on-disk history in Record.
func pruneOlderThan(data []byte, asOf time.Time, window time.Duration) ([]byte, error) {
	var out []byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		var ev event
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

// Counts returns 1d/7d/30d counts per model across all agents (including
// legacy agent-less events). See countsWhere for semantics.
func (s *StoreImpl) Counts(modelIDs []string) map[string]UsageCounts {
	return s.countsWhere(modelIDs, func(event) bool { return true })
}

// CountsForAgent is Counts scoped to one agent. Events without an agent
// (legacy lines, or Record) never match, and an empty agent matches nothing.
func (s *StoreImpl) CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts {
	return s.countsWhere(modelIDs, func(ev event) bool { return agent != "" && ev.Agent == agent })
}

// AllCounts returns 1d/7d/30d counts for every model that has a launch in
// the file within the last 30 days. Unlike Counts it takes no id list, so
// it also reports models that have since left the registry — `wt stats`
// has no other way to learn which models were launched. An empty agent
// counts every launch, legacy agent-less lines included (the Counts rule);
// a named agent counts only launches recorded for it (the CountsForAgent
// rule). A model whose only events are older than 30 days is left out, so a
// missing file and a stale one both read as an empty map.
func (s *StoreImpl) AllCounts(agent string) map[string]UsageCounts {
	out := map[string]UsageCounts{}
	s.scan(func(ev event, age time.Duration) {
		if agent != "" && ev.Agent != agent {
			return
		}
		if age >= retentionWindow {
			return
		}
		out[ev.ModelID] = out[ev.ModelID].add(age)
	})
	return out
}

// add returns c with one launch of the given age counted in every window
// it falls inside.
func (c UsageCounts) add(age time.Duration) UsageCounts {
	if age < 24*time.Hour {
		c.OneDay++
	}
	if age < 7*24*time.Hour {
		c.SevenDay++
	}
	if age < retentionWindow {
		c.ThirtyDay++
	}
	return c
}

// countsWhere returns 1d/7d/30d counts for each model in modelIDs, zero-filling
// every requested ID (a missing file or a model with no recent events reads
// as zero counts).
func (s *StoreImpl) countsWhere(modelIDs []string, match func(event) bool) map[string]UsageCounts {
	out := map[string]UsageCounts{}
	for _, id := range modelIDs {
		out[id] = UsageCounts{}
	}
	s.scan(func(ev event, age time.Duration) {
		if _, want := out[ev.ModelID]; !want || !match(ev) {
			return
		}
		out[ev.ModelID] = out[ev.ModelID].add(age)
	})
	return out
}

// scan calls fn once per parseable event in the file, with the event's age
// as of now. A missing file yields no calls.
//
// Best-effort for display: if the scan aborts partway (e.g. a single corrupt
// line exceeding bufio.Scanner's token limit), the events seen so far have
// been reported and no error is returned — a truncated read only skews
// displayed numbers. RecordFor's prune path is where scan errors are
// surfaced, because there the result overwrites the on-disk history.
func (s *StoreImpl) scan(fn func(ev event, age time.Duration)) {
	f, err := os.Open(s.path())
	if err != nil {
		return
	}
	defer f.Close()

	today := now().UTC()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		fn(ev, today.Sub(ev.Timestamp.UTC()))
	}
}
