// Package rotation manages the global last-launched model, the row the
// picker's marker sits on, and computes the next model to present in the
// picker.
package rotation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// Rotation reads and writes two one-line files in the config directory:
// rotation.state, the registry id of the last single-model launch (the
// rotation position), and last-picked.state, the id of the last row launched
// from the picker, which may be a slot profile's (the picker's marker).
type Rotation struct {
	dir   string
	store usage.Store
}

// New returns a Rotation using the default config directory.
func New() *Rotation {
	return NewAt(config.Dir())
}

// NewAt returns a Rotation using dir as the config directory.
func NewAt(dir string) *Rotation {
	r := &Rotation{dir: dir, store: usage.NewStoreAt(dir)}
	_ = r.migrate()
	return r
}

// statePath returns the path to rotation.state.
func (r *Rotation) statePath() string {
	return filepath.Join(r.dir, "rotation.state")
}

// Last returns the most recently launched model ID.
func (r *Rotation) Last() (string, bool) {
	data, err := os.ReadFile(r.statePath())
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", false
	}
	return id, true
}

// pickedPath is <dir>/last-picked.state.
func (r *Rotation) pickedPath() string {
	return filepath.Join(r.dir, "last-picked.state")
}

// LastPicked is the row id the picker's marker sits on: last-picked.state,
// or, when that file is missing or empty, Last(). The fallback keeps state
// written before last-picked.state existed working: there the last-launched
// model is also the last-picked row.
func (r *Rotation) LastPicked() (string, bool) {
	if data, err := os.ReadFile(r.pickedPath()); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, true
		}
	}
	return r.Last()
}

// RecordFor writes modelID as the new last-launched model and as the
// last-picked row, and records a usage event attributed to the agent. Writing
// both files is what moves the picker's marker off a slot profile once a
// single model is launched after it.
func (r *Rotation) RecordFor(agent, modelID string) error {
	path := r.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path, []byte(modelID+"\n"), 0o600); err != nil {
		return fmt.Errorf("rotation.state not saved: %w", err)
	}
	pickedErr := config.WriteFileAtomic(r.pickedPath(), []byte(modelID+"\n"), 0o600)
	// Usage recording is best-effort; do not fail rotation write if usage fails.
	// It runs whether or not last-picked.state was written: the launch goes
	// ahead either way, so it is counted either way.
	_ = r.store.RecordFor(agent, modelID)
	if pickedErr != nil {
		return fmt.Errorf("last-picked.state not saved: %w", pickedErr)
	}
	return nil
}

// RecordProfile records a slot-profile launch: it writes profileID to
// last-picked.state and appends the usage event for agent and profileID
// (best-effort, as RecordFor's is). It never touches rotation.state, so the
// rotation position stays on the last single-model launch: rotation.state
// holds one bare registry id, and a profile id is not one.
func (r *Rotation) RecordProfile(agent, profileID string) error {
	pickedErr := config.WriteFileAtomic(r.pickedPath(), []byte(profileID+"\n"), 0o600)
	_ = r.store.RecordFor(agent, profileID)
	if pickedErr != nil {
		return fmt.Errorf("last-picked.state not saved: %w", pickedErr)
	}
	return nil
}

// NextFromEligible returns the first model after the last launched one, in
// the registry's order, that is in eligible. Callers already hold the
// eligible slice (launchFilteredImpl from resolveModel; the TUI from its
// table rows), so cfg.EligibleModels is not computed again here.
//
// Rotation never launches on its own authority: the non-TUI rotation
// fallback picks only from the launchable list resolveModel returns —
// cloud rows, or local rows the live probe reports as running — so an
// auto-launched rotation pick can never target a server that is not up.
// The TUI calls this only to position the picker cursor, on a slice that
// also includes start rows; Enter there drives the visible start flow, so
// nothing starts unasked. Starting a local model is the user's decision
// (-M pin, or Enter on a start row), never rotation's. So an auto-launch
// caller must pass a slice resolved from live rows, not cfg.EligibleModels,
// which applies exposure and tag/family filters only, with no running check.
func (r *Rotation) NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool) {
	if len(eligible) == 0 || cfg == nil || len(cfg.Models) == 0 {
		return config.Model{}, false
	}
	allowed := map[string]bool{}
	for _, m := range eligible {
		allowed[m.ID] = true
	}

	start := 0
	if last, ok := r.Last(); ok {
		for i, m := range cfg.Models {
			if m.ID == last {
				start = i + 1
				break
			}
		}
	}

	for offset := 0; offset < len(cfg.Models); offset++ {
		idx := (start + offset) % len(cfg.Models)
		m := cfg.Models[idx]
		if allowed[m.ID] {
			return m, true
		}
	}
	return config.Model{}, false
}

// migrate imports old per-slot rotation files into the new global state.
// It runs once: when rotation.state is missing and old files exist.
func (r *Rotation) migrate() error {
	path := r.statePath()
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}

	var bestID string
	var bestTime time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "rotation-") || !strings.HasSuffix(name, ".state") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, name))
		if err != nil {
			continue
		}
		id := lastNonEmptyLine(string(data))
		if id == "" {
			continue
		}
		if bestID == "" || info.ModTime().After(bestTime) {
			bestID = id
			bestTime = info.ModTime()
		}
	}

	if bestID == "" {
		return nil
	}
	if err := config.WriteFileAtomic(path, []byte(bestID+"\n"), 0o600); err != nil {
		return err
	}
	// Delete old files after successful migration.
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "rotation-") && strings.HasSuffix(name, ".state") {
			_ = os.Remove(filepath.Join(r.dir, name))
		}
	}
	return nil
}

// lastNonEmptyLine returns the last non-empty line from s, trimmed.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// FirstAfter returns the model that comes after target in the
// snapshot, wrapping to the first item if target is the last or
// not in the snapshot. Returns (zero, false) if the snapshot is
// empty. Retained for callers that still manage their own ordered
// snapshot.
func FirstAfter(models []config.Model, target config.Model) (config.Model, bool) {
	if len(models) == 0 {
		return config.Model{}, false
	}
	for i, m := range models {
		if m.ID == target.ID {
			if i+1 < len(models) {
				return models[i+1], true
			}
			return models[0], true
		}
	}
	return models[0], true
}
