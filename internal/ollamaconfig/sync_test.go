package ollamaconfig

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestComputeUnionAllSynced verifies that models present in both config
// and ollama list are marked as synced, using the config model's values.
func TestComputeUnionAllSynced(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal, Tags: []string{"code"}},
	}
	discovered := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if !e.config || !e.ollama {
		t.Errorf("entry should be synced (config=%v, ollama=%v)", e.config, e.ollama)
	}
	// Synced entries use the config model (which has tags).
	if len(e.model.Tags) != 1 || e.model.Tags[0] != "code" {
		t.Errorf("expected tags from config model, got %v", e.model.Tags)
	}
}

// TestComputeUnionMissingModel verifies that a model in config but not in
// ollama list is marked as missing.
func TestComputeUnionMissingModel(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud},
	}
	discovered := []config.Model{}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].config && !entries[0].ollama {
		return // correct: missing
	}
	t.Errorf("expected missing (config=true, ollama=false), got (config=%v, ollama=%v)", entries[0].config, entries[0].ollama)
}

// TestComputeUnionUntrackedModel verifies that a model in ollama list but
// not in config is marked as untracked, using the discovered model.
func TestComputeUnionUntrackedModel(t *testing.T) {
	curated := []config.Model{}
	discovered := []config.Model{
		{ID: "ollama/llama3.2:3b", Family: "llama3.2:3b", ProviderID: "ollama", ModelName: "llama3.2:3b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.config || !e.ollama {
		t.Errorf("expected untracked (config=false, ollama=true), got (config=%v, ollama=%v)", e.config, e.ollama)
	}
	if e.model.ModelName != "llama3.2:3b" {
		t.Errorf("expected model_name llama3.2:3b, got %q", e.model.ModelName)
	}
}

// TestComputeUnionAllThreeStates verifies that synced, missing, and
// untracked entries all appear in the same union.
func TestComputeUnionAllThreeStates(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
		{ID: "ollama/kimi:cloud", Family: "kimi", ProviderID: "ollama", ModelName: "kimi:cloud", Location: config.LocationCloud},
	}
	discovered := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
		{ID: "ollama/llama3.2:3b", Family: "llama3.2:3b", ProviderID: "ollama", ModelName: "llama3.2:3b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// gemma4:9b → synced, kimi:cloud → missing, llama3.2:3b → untracked
	byName := map[string]syncEntry{}
	for _, e := range entries {
		byName[e.model.ModelName] = e
	}
	if e := byName["gemma4:9b"]; !e.config || !e.ollama {
		t.Errorf("gemma4:9b should be synced, got config=%v ollama=%v", e.config, e.ollama)
	}
	if e := byName["kimi:cloud"]; !e.config || e.ollama {
		t.Errorf("kimi:cloud should be missing, got config=%v ollama=%v", e.config, e.ollama)
	}
	if e := byName["llama3.2:3b"]; e.config || !e.ollama {
		t.Errorf("llama3.2:3b should be untracked, got config=%v ollama=%v", e.config, e.ollama)
	}
}

// TestComputeUnionExcludesNonOllama verifies that non-ollama config models
// (e.g. openrouter) are excluded from the union.
func TestComputeUnionExcludesNonOllama(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
		{ID: "openrouter/google/gemma-4-9b", Family: "gemma4", ProviderID: "openrouter", ModelName: "google/gemma-4-9b"},
	}
	discovered := []config.Model{
		{ID: "ollama/gemma4:9b", Family: "gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Location: config.LocationLocal},
	}
	entries := computeUnion(curated, discovered)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (openrouter excluded), got %d", len(entries))
	}
	if entries[0].model.ModelName != "gemma4:9b" {
		t.Errorf("expected gemma4:9b, got %q", entries[0].model.ModelName)
	}
}

// TestComputeUnionSorting verifies entries are sorted by family, then
// model_name.
func TestComputeUnionSorting(t *testing.T) {
	curated := []config.Model{
		{ID: "ollama/zzz:1b", Family: "zzz", ProviderID: "ollama", ModelName: "zzz:1b", Location: config.LocationLocal},
		{ID: "ollama/aaa:1b", Family: "aaa", ProviderID: "ollama", ModelName: "aaa:1b", Location: config.LocationLocal},
		{ID: "ollama/aaa:3b", Family: "aaa", ProviderID: "ollama", ModelName: "aaa:3b", Location: config.LocationLocal},
	}
	discovered := []config.Model{}
	entries := computeUnion(curated, discovered)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	want := []string{"aaa:1b", "aaa:3b", "zzz:1b"}
	for i, w := range want {
		if entries[i].model.ModelName != w {
			t.Errorf("entry[%d] = %q, want %q", i, entries[i].model.ModelName, w)
		}
	}
}

// TestComputeUnionEmptyBoth verifies that empty inputs produce an empty
// result (no panic).
func TestComputeUnionEmptyBoth(t *testing.T) {
	entries := computeUnion(nil, nil)
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// TestSyncEntryStatus verifies the Status() method returns the correct
// string for each state.
func TestSyncEntryStatus(t *testing.T) {
	cases := []struct {
		entry  syncEntry
		status string
	}{
		{syncEntry{config: true, ollama: true}, "synced"},
		{syncEntry{config: true, ollama: false}, "missing"},
		{syncEntry{config: false, ollama: true}, "untracked"},
	}
	for _, c := range cases {
		if got := c.entry.Status(); got != c.status {
			t.Errorf("Status() = %q, want %q", got, c.status)
		}
	}
}
