package registry

import "github.com/ohanaverse/agent-worktree/internal/config"

// FilterByTag returns models whose Tags include tag. An empty tag is a
// no-op and returns the input slice unchanged so the no-filter path is
// allocation-free.
func FilterByTag(models []config.Model, tag string) []config.Model {
	if tag == "" {
		return models
	}
	var out []config.Model
	for _, m := range models {
		if m.HasTag(tag) {
			out = append(out, m)
		}
	}
	return out
}

// FilterBySource returns models whose Source field equals source. An
// empty source is a no-op (returns input unchanged).
func FilterBySource(models []config.Model, source config.Source) []config.Model {
	if source == "" {
		return models
	}
	var out []config.Model
	for _, m := range models {
		if m.Source == source {
			out = append(out, m)
		}
	}
	return out
}
