package rotation

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Slot identifies the rotation state slot for an (agent, tag, family)
// combination. Tag and Family are normalized: empty values become "-"
// and special characters (commas, dots) are escaped to underscores.
// The normalization guarantees a predictable state-file name.
type Slot struct {
	Agent, Tag, Family string
}

// SlotFromFlags builds a Slot from the agent name and resolved
// tag/family. Empty tag/family values become "-"; commas and dots are
// escaped to underscores to keep state-file names safe.
func SlotFromFlags(agent, tag, family string) Slot {
	return Slot{
		Agent:  agent,
		Tag:    escapeComponent(tag),
		Family: escapeComponent(family),
	}
}

// escapeComponent normalizes a single slot component: empty -> "-",
// replace commas and dots with underscores.
func escapeComponent(s string) string {
	if s == "" {
		return "-"
	}
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}

// defaultStateDir returns ~/.config/agent-wt (or $XDG_CONFIG_HOME/agent-wt).
func defaultStateDir() string {
	return config.Dir()
}

// Rotation remembers which model was last launched in a rotation slot.
// The model set is fixed at construction time and must match the
// picker's filtered snapshot. Rotation is driven by the launch
// action: RecordLaunch persists the pick, and LastLaunched +
// FirstAfter compute the cursor position for the next picker entry.
type Rotation struct {
	mu       sync.Mutex
	slot     Slot
	models   []config.Model
	stateDir string
}

// NewForSlot builds a Rotation for the given slot from the given
// models. stateDir is where the per-slot state file lives; pass ""
// to use the default (~/.config/agent-wt). This is the canonical
// constructor; the Slot is the (agent, tag, family) identifier.
func NewForSlot(slot Slot, models []config.Model, stateDir string) *Rotation {
	return &Rotation{slot: slot, models: models, stateDir: stateDir}
}

// Slot returns the slot this Rotation serves.
func (r *Rotation) Slot() Slot { return r.slot }

// StateDir returns the resolved state directory for this Rotation.
func (r *Rotation) StateDir() string {
	if r.stateDir != "" {
		return r.stateDir
	}
	return defaultStateDir()
}

// StateFileForSlot returns the per-slot state path.
// Format: rotation-<agent>-<tag>-<family>.state. Empty or "-" values
// for Agent or Family are rendered as "_" so the filename stays free
// of consecutive dashes regardless of which components a caller left
// blank.
func StateFileForSlot(stateDir string, slot Slot) string {
	agent := slot.Agent
	if agent == "" || agent == "-" {
		agent = "_"
	}
	family := slot.Family
	if family == "" || family == "-" {
		family = "_"
	}
	return filepath.Join(stateDir, "rotation-"+agent+"-"+slot.Tag+"-"+family+".state")
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
	// Try the per-slot file first.
	data, err := os.ReadFile(StateFileForSlot(r.StateDir(), r.slot))
	if err == nil {
		return matchLastLaunched(string(data), r.models)
	}
	// Back-compat: fall back to the legacy rotation-<tag>.state.
	// The legacy public StateFile() helper was removed in PR 3b; the
	// path is inlined here so a downgrade or shared config dir keeps
	// the old state readable.
	data, err = os.ReadFile(filepath.Join(r.StateDir(), "rotation-"+r.slot.Tag+".state"))
	if err == nil {
		return matchLastLaunched(string(data), r.models)
	}
	return config.Model{}, false
}

// matchLastLaunched parses a state-file body and returns the first
// model ID that matches one in the snapshot. The legacy 2-line format
// is supported by reading the last non-empty line.
func matchLastLaunched(body string, models []config.Model) (config.Model, bool) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		id := strings.TrimSpace(lines[i])
		if id == "" {
			continue
		}
		for _, m := range models {
			if m.ID == id {
				return m, true
			}
		}
	}
	return config.Model{}, false
}

// RecordLaunch writes the given model as the new last-launched.
// The write is atomic (temp file + rename), targeted at the per-slot
// state file. Errors are returned to the caller; the picker proceeds
// with the launch even if the write fails (the next picker entry
// falls back to index 0). The legacy rotation-<tag>.state file is
// NOT touched — LastLaunched still reads it for backward
// compatibility, so a downgrade or shared config dir keeps the old
// state intact.
func (r *Rotation) RecordLaunch(m config.Model) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return config.WriteFileAtomic(
		StateFileForSlot(r.StateDir(), r.slot),
		[]byte(m.ID+"\n"),
		0o600,
	)
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
