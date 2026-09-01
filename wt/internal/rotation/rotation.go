// Package rotation manages the global last-launched model and computes the
// next model to present in the picker.
package rotation

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// Rotation reads and writes the single global rotation.state file.
type Rotation struct {
	dir   string
	store *usage.Store
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

// Record writes modelID as the new last-launched model and records a usage event.
func (r *Rotation) Record(modelID string) error {
	path := r.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := config.WriteFileAtomic(path, []byte(modelID+"\n"), 0o600); err != nil {
		return err
	}
	// Usage recording is best-effort; do not fail rotation write if usage fails.
	_ = r.store.Record(modelID)
	return nil
}

// cfgHasModels reports whether cfg is safe to rotate against, i.e. non-nil
// with at least one model. Shared by Next and NextFromEligible so the guard
// can't drift between the two copies.
func cfgHasModels(cfg *config.Config) bool {
	return cfg != nil && len(cfg.Models) > 0
}

// Next returns the first model after the last launched one that is eligible
// for agent under the given tags/family filters. It computes the eligible
// list and delegates to NextFromEligible.
func (r *Rotation) Next(cfg *config.Config, agent, tags, family string) (config.Model, bool) {
	if !cfgHasModels(cfg) {
		return config.Model{}, false
	}
	eligible, err := cfg.EligibleModels(agent, tags, family)
	if err != nil || len(eligible) == 0 {
		return config.Model{}, false
	}
	return r.NextFromEligible(eligible, cfg)
}

// NextFromEligible is the rotation core without the expensive
// cfg.EligibleModels call. The caller (launchFilteredImpl) already has the
// eligible slice from resolveModel, so this avoids computing it twice.
func (r *Rotation) NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool) {
	if len(eligible) == 0 || !cfgHasModels(cfg) {
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
		// Skip the new global file if it somehow matches.
		if name == "rotation.state" {
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
		if strings.HasPrefix(name, "rotation-") && strings.HasSuffix(name, ".state") && name != "rotation.state" {
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
