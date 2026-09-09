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
	Task      string    `json:"task,omitempty"`
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
