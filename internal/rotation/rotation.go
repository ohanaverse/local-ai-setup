package rotation

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// StateFile returns the state path for a tag group under the config dir.
func StateFile(cfgDir, tag string) string {
	return filepath.Join(cfgDir, "rotation-"+tag+".state")
}

// defaultStateDir returns ~/.config/agent-wt (or $XDG_CONFIG_HOME/agent-wt).
func defaultStateDir() string {
	return config.Dir()
}

// Rotation remembers which model was last launched in a tag group.
// The model set is fixed at construction time and must match the
// picker's filtered snapshot. Rotation is driven by the launch
// action: RecordLaunch persists the pick, and LastLaunched +
// FirstAfter compute the cursor position for the next picker entry.
type Rotation struct {
	mu       sync.Mutex
	tag      string
	models   []config.Model
	stateDir string
}

// New builds a Rotation for a tag group from the given models.
// stateDir is where rotation-<tag>.state lives; pass "" to use the
// default (~/.config/agent-wt).
func New(tag string, models []config.Model, stateDir string) *Rotation {
	return &Rotation{tag: tag, models: models, stateDir: stateDir}
}

// Tag returns the tag group this Rotation serves.
func (r *Rotation) Tag() string { return r.tag }

// StateDir returns the resolved state directory for this Rotation.
func (r *Rotation) StateDir() string {
	if r.stateDir != "" {
		return r.stateDir
	}
	return defaultStateDir()
}

// LastLaunched returns the model most recently recorded for this
// rotation, or (zero, false) if the state file is missing, empty,
// or references a model no longer in the snapshot. Backward-
// compatible with the legacy 2-line format ("<index>\n<id>\n") —
// the last non-empty line is treated as the model ID.
func (r *Rotation) LastLaunched() (config.Model, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastLaunchedLocked()
}

func (r *Rotation) lastLaunchedLocked() (config.Model, bool) {
	data, err := os.ReadFile(StateFile(r.StateDir(), r.tag))
	if err != nil {
		return config.Model{}, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		id := strings.TrimSpace(lines[i])
		if id == "" {
			continue
		}
		for _, m := range r.models {
			if m.ID == id {
				return m, true
			}
		}
	}
	return config.Model{}, false
}

// RecordLaunch writes the given model as the new last-launched.
// The write is atomic (temp file + rename). Errors are returned to
// the caller; the picker proceeds with the launch even if the write
// fails (the next picker entry falls back to index 0).
func (r *Rotation) RecordLaunch(m config.Model) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return config.WriteFileAtomic(StateFile(r.StateDir(), r.tag), []byte(m.ID+"\n"), 0o600)
}

// FirstAfter returns the model that comes after target in the
// snapshot, wrapping to the first item if target is the last or
// not in the snapshot. Returns (zero, false) if the snapshot is
// empty. This is the "what to show on next picker entry" calculation.
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
