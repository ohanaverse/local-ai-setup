package ollamaconfig

import (
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
// Delegates to config.TagsToString so tag formatting stays consistent.
func tagsToString(tags []string) string {
	return config.TagsToString(tags)
}

// toggleLocation cycles between local and cloud. Delegates to
// config.ToggleLocation so both ollamaconfig and configeditor use the same
// behavior.
func toggleLocation(loc config.Location) config.Location {
	return config.ToggleLocation(loc)
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
