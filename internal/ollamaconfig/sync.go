package ollamaconfig

import (
	"sort"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// syncEntry represents one row in the union list. It tracks whether the
// model appears in config.toml, in ollama list, or both.
type syncEntry struct {
	model  config.Model // config.toml model (zero value if not in config)
	ollama bool         // true if model appears in ollama list
	config bool         // true if model appears in config.toml
}

// Status returns the human-readable status string for this entry.
func (e syncEntry) Status() string {
	switch {
	case e.config && e.ollama:
		return "synced"
	case e.config && !e.ollama:
		return "missing"
	default:
		return "untracked"
	}
}

// computeUnion builds the union of config.toml ollama models and
// ollama-discovered models, keyed by ModelName. Non-ollama config models
// are excluded. The result is sorted by family, then model_name.
func computeUnion(curated, discovered []config.Model) []syncEntry {
	// Build a map of config ollama models keyed by ModelName.
	byName := map[string]syncEntry{}
	for _, m := range curated {
		if m.ProviderID != "ollama" {
			continue
		}
		byName[m.ModelName] = syncEntry{
			model:  m,
			config: true,
		}
	}
	// Merge in discovered models.
	for _, m := range discovered {
		entry, exists := byName[m.ModelName]
		if !exists {
			byName[m.ModelName] = syncEntry{
				model:  m,
				ollama: true,
			}
			continue
		}
		entry.ollama = true
		byName[m.ModelName] = entry
	}
	// Collect and sort.
	entries := make([]syncEntry, 0, len(byName))
	for _, e := range byName {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].model.Family != entries[j].model.Family {
			return entries[i].model.Family < entries[j].model.Family
		}
		return entries[i].model.ModelName < entries[j].model.ModelName
	})
	return entries
}
