package registry

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Merge combines curated models with discovered ones. Curated entries win on
// id collisions; discovered entries are appended otherwise.
func Merge(curated []config.Model, discovered []config.Model) []config.Model {
	byID := make(map[string]config.Model, len(curated))
	for _, m := range curated {
		byID[m.ID] = m
	}
	for _, d := range discovered {
		if _, exists := byID[d.ID]; !exists {
			byID[d.ID] = d
		}
	}
	out := make([]config.Model, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	return out
}

// Discover runs each connected discoverer and returns the merged registry.
func Discover(cfg *config.Config) []config.Model {
	var discovered []config.Model
	discoverers := []Discoverer{Ollama{}, OpenRouter{}}
	for _, d := range discoverers {
		models, err := d.Discover()
		if err != nil {
			// Discovery failures are non-fatal — fall back to curated only.
			continue
		}
		discovered = append(discovered, models...)
	}
	return Merge(cfg.Models, discovered)
}
