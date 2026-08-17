package rotation

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// Rotation cycles through a tag group's models, advancing each Next() call.
type Rotation struct {
	mu       sync.Mutex
	tag      string
	models   []config.Model
	stateDir string
}

// New builds a Rotation for a tag group from the given models. stateDir is
// where rotation-<tag>.state lives; pass "" to use the default (~/.config/agent-wt).
func New(tag string, models []config.Model, stateDir string) *Rotation {
	return &Rotation{tag: tag, models: models, stateDir: stateDir}
}

// ForTag returns a Rotation over cfg's models tagged with tag, using the
// default state directory.
func ForTag(cfg *config.Config, tag string) *Rotation {
	return New(tag, cfg.ModelsWithTag(tag), "")
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

// Next returns the next model in rotation. otherTag is the tag group to
// cross-skip against (pass "" to disable). Returns false if the group is
// empty or no model is usable. Advances the on-disk state.
func (r *Rotation) Next(otherTag string) (config.Model, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.models) == 0 {
		return config.Model{}, false
	}

	stateDir := r.StateDir()

	// What did the other group last use? Avoid it.
	otherLast := ""
	if otherTag != "" {
		otherLast = config.LastSelected(stateDir, otherTag)
	}

	// Load current index from disk.
	idx := r.loadIndex(stateDir)
	if idx < 0 || idx >= len(r.models) {
		idx = 0
	}

	n := len(r.models)
	// Walk forward from idx, skipping any candidate the other group just used.
	for i := 0; i < n; i++ {
		cand := r.models[(idx+i)%n]
		if otherLast != "" && cand.ID == otherLast {
			continue
		}
		// Persist next index (one past this pick) and the model we just chose.
		nextIdx := (idx + i + 1) % n
		if err := r.saveState(stateDir, nextIdx, cand.ID); err != nil {
			// State write failed; still return the pick so callers can proceed.
			return cand, true
		}
		return cand, true
	}

	// All candidates were cross-skipped. Fall back to the index-position pick
	// so we always make progress even when the other group just used the
	// only model in this group.
	selected := r.models[idx]
	_ = r.saveState(stateDir, (idx+1)%n, selected.ID)
	return selected, true
}

// loadIndex reads the saved index (first line) from the state file.
// Returns -1 if the file is missing or malformed.
func (r *Rotation) loadIndex(dir string) int {
	data, err := os.ReadFile(StateFile(dir, r.tag))
	if err != nil {
		return -1
	}
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(parts) == 0 {
		return -1
	}
	idx, err := strconv.Atoi(parts[0])
	if err != nil {
		return -1
	}
	return idx
}

// saveState writes "<index>\n<last>\n" atomically via temp file + rename.
func (r *Rotation) saveState(dir string, idx int, last string) error {
	content := fmt.Sprintf("%d\n%s\n", idx, last)
	return config.WriteFileAtomic(StateFile(dir, r.tag), []byte(content), 0o600)
}
