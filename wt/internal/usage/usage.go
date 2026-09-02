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
// Record prunes events older than this on every write so usage.jsonl
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
	Timestamp time.Time `json:"timestamp"`
}

// Store is an interface for recording and querying model-launch history.
type Store interface {
	Record(modelID string) error
	Counts(modelIDs []string) map[string]UsageCounts
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

// Record appends one launch event for modelID atomically, first dropping
// any existing events older than retentionWindow so the file doesn't grow
// unbounded — only the trailing 30-day window is ever read by Counts.
//
// Concurrency: the read-prune-write critical section is guarded by a POSIX
// advisory flock on a sidecar usage.jsonl.lock file in the same directory.
// This serializes concurrent wt processes (e.g. two terminal launches in
// the same config dir) so neither's just-recorded event is silently dropped
// by the other's later rename. The sidecar pattern keeps the lock attached
// to the file's location while the target file is renamed freely.
func (s *StoreImpl) Record(modelID string) error {
	e := event{ModelID: modelID, Timestamp: now().UTC()}
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

// Counts returns 1d/7d/30d counts for each model in modelIDs, zero-filling
// every requested ID (a missing file or a model with no recent events reads
// as zero counts).
//
// Best-effort for display: if the scan aborts partway (e.g. a single corrupt
// line exceeding bufio.Scanner's token limit), the counts accumulated so far
// are returned with no error — a truncated read only skews displayed
// numbers. Record's prune path is where scan errors are surfaced, because
// there the result overwrites the on-disk history.
func (s *StoreImpl) Counts(modelIDs []string) map[string]UsageCounts {
	want := map[string]bool{}
	for _, id := range modelIDs {
		want[id] = true
	}
	out := map[string]UsageCounts{}
	for _, id := range modelIDs {
		out[id] = UsageCounts{}
	}

	f, err := os.Open(s.path())
	if err != nil {
		return out
	}
	defer f.Close()

	today := now().UTC()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if !want[ev.ModelID] {
			continue
		}
		age := today.Sub(ev.Timestamp.UTC())
		c := out[ev.ModelID]
		if age < 24*time.Hour {
			c.OneDay++
		}
		if age < 7*24*time.Hour {
			c.SevenDay++
		}
		if age < retentionWindow {
			c.ThirtyDay++
		}
		out[ev.ModelID] = c
	}
	return out
}

// CompositeScore is a recency-weighted sort key for a UsageCounts bucket.
// Each launch is counted exactly once, weighted by how fresh it is:
// today's launches ≈3x, 1-7 days ≈1.5x, 8-30 days ≈1x. Expressed as
// integer math (x2), 6*OneDay + 3*(SevenDay-OneDay) + 2*(ThirtyDay-SevenDay)
// simplifies to 3*OneDay + SevenDay + 2*ThirtyDay.
func CompositeScore(c UsageCounts) int {
	return 3*c.OneDay + c.SevenDay + 2*c.ThirtyDay
}

// AggregateByFamily sums per-model usage counts into per-family buckets.
// familyOf maps the FULL model catalog's IDs to their families (built from
// the registry, not the currently-eligible subset, so a family's total
// usage is accurate even when a tag/family filter exposes only some of its
// models); counts are the per-model counts Store.Counts returned for those
// same IDs. Counts entries for IDs absent from familyOf are ignored.
//
// Families are not pre-seeded: only families with at least one model whose
// counts are non-zero appear as keys, so a missing family key means zero
// usage. (Counts zero-fills every requested model ID, which is why the
// zero-count skip is what enforces the contract here.)
func AggregateByFamily(familyOf map[string]string, counts map[string]UsageCounts) map[string]UsageCounts {
	out := map[string]UsageCounts{}
	for id, family := range familyOf {
		c := counts[id]
		if c == (UsageCounts{}) {
			continue
		}
		cur := out[family]
		cur.OneDay += c.OneDay
		cur.SevenDay += c.SevenDay
		cur.ThirtyDay += c.ThirtyDay
		out[family] = cur
	}
	return out
}
