package ollamaconfig

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestParseTags verifies that comma-delimited tag strings are parsed
// into slices with trimming and empty-drop.
func TestParseTags(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"code, design", []string{"code", "design"}},
		{"  code ,  design  ", []string{"code", "design"}},
		{"code", []string{"code"}},
		{"", nil},
		{" , , ", nil},
	}
	for _, c := range cases {
		got := parseTags(c.input)
		if len(got) != len(c.want) {
			t.Errorf("parseTags(%q) = %v, want %v", c.input, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseTags(%q)[%d] = %q, want %q", c.input, i, got[i], c.want[i])
			}
		}
	}
}

// TestTagsToString verifies that a tag slice is joined into a
// comma-delimited string for display in the edit input.
func TestTagsToString(t *testing.T) {
	cases := []struct {
		tags []string
		want string
	}{
		{[]string{"code", "design"}, "code, design"},
		{[]string{"code"}, "code"},
		{nil, ""},
		{[]string{}, ""},
	}
	for _, c := range cases {
		got := tagsToString(c.tags)
		if got != c.want {
			t.Errorf("tagsToString(%v) = %q, want %q", c.tags, got, c.want)
		}
	}
}

// TestToggleLocation verifies that the location toggle cycles between
// local and cloud.
func TestToggleLocation(t *testing.T) {
	if got := toggleLocation(config.LocationLocal); got != config.LocationCloud {
		t.Errorf("toggleLocation(local) = %s, want cloud", got)
	}
	if got := toggleLocation(config.LocationCloud); got != config.LocationLocal {
		t.Errorf("toggleLocation(cloud) = %s, want local", got)
	}
	// Empty string defaults to local, then toggles to cloud.
	if got := toggleLocation(""); got != config.LocationLocal {
		t.Errorf("toggleLocation(\"\") = %s, want local", got)
	}
}

// TestSaveExistingModel verifies that saving an edit to a synced model
// updates the matching entry in cfg.Models and leaves others unchanged.
func TestSaveExistingModel(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}},
			{ID: "ollama/gemma4:27b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:27b", Location: config.LocationLocal, Tags: []string{"code"}},
		},
	}
	updated := config.Model{
		ID:         "ollama/gemma4:9b",
		Family:     "gemma4-renamed",
		ProviderID: "ollama",
		ModelName:  "gemma4:9b",
		Location:   config.LocationCloud,
		Tags:       []string{"code", "design"},
	}
	saveModelToConfig(cfg, updated, false)
	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.Models))
	}
	m := cfg.Models[0]
	if m.Family != "gemma4-renamed" {
		t.Errorf("family = %q, want gemma4-renamed", m.Family)
	}
	if m.Location != config.LocationCloud {
		t.Errorf("location = %s, want cloud", m.Location)
	}
	if len(m.Tags) != 2 || m.Tags[0] != "code" || m.Tags[1] != "design" {
		t.Errorf("tags = %v, want [code design]", m.Tags)
	}
	// Second model unchanged.
	if cfg.Models[1].Family != "gemma4" {
		t.Errorf("second model family changed: %q", cfg.Models[1].Family)
	}
}

// TestSaveNewModel verifies that saving an untracked model appends a new
// entry with the correct auto-generated fields.
func TestSaveNewModel(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}},
		},
	}
	newModel := config.Model{
		ID:         "ollama/llama3.2:3b",
		Family:     "llama3.2",
		ProviderID: "ollama",
		ModelName:  "llama3.2:3b",
		Location:   config.LocationLocal,
		Tags:       []string{"code"},
	}
	saveModelToConfig(cfg, newModel, true)
	if len(cfg.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.Models))
	}
	m := cfg.Models[1]
	if m.ID != "ollama/llama3.2:3b" {
		t.Errorf("id = %q, want ollama/llama3.2:3b", m.ID)
	}
	if m.Source != config.SourceCurated {
		t.Errorf("source = %s, want curated", m.Source)
	}
}

// TestSaveExistingModelNotFoundReturnsFalse verifies that updating a
// model whose ID no longer exists in cfg.Models (e.g. removed by a
// concurrent `wt config ollama` session or a manual edit since the list
// was loaded) reports failure and leaves cfg unchanged, instead of
// silently no-oping.
func TestSaveExistingModelNotFoundReturnsFalse(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b"},
		},
	}
	updated := config.Model{ID: "ollama/removed:model", Family: "x", ProviderID: "ollama", ModelName: "removed:model"}
	if saveModelToConfig(cfg, updated, false) {
		t.Error("expected saveModelToConfig to report false for a missing ID")
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("expected cfg unchanged (1 model), got %d", len(cfg.Models))
	}
}

// TestDeleteModelFromConfig verifies that deleting a model by ID removes
// it from cfg.Models without affecting other entries.
func TestDeleteModelFromConfig(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
			{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud},
		},
	}
	cfg.DeleteModel("ollama/kimi:cloud")
	if len(cfg.Models) != 1 {
		t.Fatalf("expected 1 model after delete, got %d", len(cfg.Models))
	}
	if cfg.Models[0].ID != "ollama/gemma4:9b" {
		t.Errorf("remaining model = %q, want ollama/gemma4:9b", cfg.Models[0].ID)
	}
}

// TestDeleteModelFromConfigNotFound verifies that deleting a non-existent
// ID is a no-op (no error, no change).
func TestDeleteModelFromConfigNotFound(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b"},
		},
	}
	cfg.DeleteModel("ollama/nonexistent")
	if len(cfg.Models) != 1 {
		t.Fatalf("expected 1 model (no-op), got %d", len(cfg.Models))
	}
}
