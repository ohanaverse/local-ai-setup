package ollamaconfig

import (
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// parseTags splits a comma-delimited tag string, trimming whitespace and
// dropping empty entries. Returns nil for empty/whitespace-only input.
// Delegates to config.ParseFilterList so tag parsing stays in sync with
// the -T/--tags filter parsing used elsewhere.
func parseTags(s string) []string {
	return config.ParseFilterList(s)
}

// tagsToString joins a tag slice into a comma-delimited display string.
// Returns "" for nil or empty slices.
func tagsToString(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ", ")
}

// toggleLocation cycles between local and cloud. An empty location
// defaults to local (the first press on a fresh entry sets it to local).
func toggleLocation(loc config.Location) config.Location {
	switch loc {
	case config.LocationLocal:
		return config.LocationCloud
	case config.LocationCloud:
		return config.LocationLocal
	default:
		return config.LocationLocal
	}
}

// saveModelToConfig writes m into cfg.Models. If isNew is true, m is
// appended and this always succeeds. If isNew is false, the existing
// entry with matching ID is updated in place; it reports false if no
// entry with that ID exists (e.g. removed concurrently since the model
// was loaded), leaving cfg unchanged. Source is set to curated for new
// models.
func saveModelToConfig(cfg *config.Config, m config.Model, isNew bool) bool {
	if isNew {
		m.Source = config.SourceCurated
		cfg.Models = append(cfg.Models, m)
		return true
	}
	for i := range cfg.Models {
		if cfg.Models[i].ID == m.ID {
			cfg.Models[i] = m
			return true
		}
	}
	return false
}

// deleteModelFromConfig removes the model with the given ID from
// cfg.Models. No-op if the ID is not found.
func deleteModelFromConfig(cfg *config.Config, id string) {
	for i, m := range cfg.Models {
		if m.ID == id {
			cfg.Models = append(cfg.Models[:i], cfg.Models[i+1:]...)
			return
		}
	}
}
