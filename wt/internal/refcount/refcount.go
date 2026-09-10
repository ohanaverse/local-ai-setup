// Package refcount tracks how many live wt sessions are currently using
// each model, for the model picker's "in use" display (issue #73).
package refcount

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// entry is one line in the refcount JSONL file: one live wt process pid
// paired with the model it launched. Unlike usage.jsonl (30-day retained
// history), refcount.jsonl has no retention window — an entry lives until
// Sweep finds its pid dead.
type entry struct {
	Pid     int    `json:"pid"`
	ModelID string `json:"model_id"`
}

// Store is an interface for recording and querying live-session model usage.
type Store interface {
	Record(pid int, modelID string) error
	Sweep() error
	Counts(modelIDs []string) map[string]int
}

// StoreImpl reads and appends to the refcount state file.
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
	return filepath.Join(s.dir, "refcount.jsonl")
}

func (s *StoreImpl) lockPath() string {
	return filepath.Join(s.dir, "refcount.jsonl.lock")
}

// pidAlive reports whether pid is a live process. nil or EPERM (process
// exists but is owned by another user) means alive; ESRCH — or any other
// error — means dead. Package-level var so tests can inject live/dead pids
// without spawning real processes.
var pidAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// withLock runs fn while holding an exclusive POSIX flock on a sidecar
// refcount.jsonl.lock file, serializing Record/Sweep's read-modify-write
// critical sections across concurrent wt processes. Mirrors usage.Record.
func (s *StoreImpl) withLock(fn func() error) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// Record appends one live-session entry for pid+modelID. Best-effort at the
// call site (see cmd/wt/launch.go and internal/tui/app.go): a failure here
// means a stale/undercounted "in use" column, never a blocked launch.
func (s *StoreImpl) Record(pid int, modelID string) error {
	e := entry{Pid: pid, ModelID: modelID}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	return s.withLock(func() error {
		existing, err := os.ReadFile(s.path())
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return config.WriteFileAtomic(s.path(), append(existing, line...), 0o600)
	})
}

// Sweep drops every entry whose pid is no longer alive and rewrites the
// state file. Called once at the top of every launch (runLaunchPath) so
// counts stay accurate after a crashed or killed wt session. A corrupt line
// is dropped along with dead entries — Sweep favors a clean file over
// surfacing a parse error, since callers treat it as best-effort.
func (s *StoreImpl) Sweep() error {
	return s.withLock(func() error {
		data, err := os.ReadFile(s.path())
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		var out []byte
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := scanner.Bytes()
			var e entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if !pidAlive(e.Pid) {
				continue
			}
			out = append(out, line...)
			out = append(out, '\n')
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return config.WriteFileAtomic(s.path(), out, 0o600)
	})
}

// Counts returns the live-session count per model in modelIDs, zero-filling
// every requested ID. It is a pure read: liveness is only re-checked by
// Sweep, not here, so a caller that wants fresh counts must have swept
// first (runLaunchPath does, before the picker is built).
func (s *StoreImpl) Counts(modelIDs []string) map[string]int {
	out := make(map[string]int, len(modelIDs))
	want := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		out[id] = 0
		want[id] = true
	}

	f, err := os.Open(s.path())
	if err != nil {
		return out
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if !want[e.ModelID] {
			continue
		}
		out[e.ModelID]++
	}
	return out
}
