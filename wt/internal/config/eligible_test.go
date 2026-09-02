package config

import (
	"testing"
)

// TestEligibleModels covers the eligible-model function used by both the
// non-TUI launch path and (eventually) the model picker. This is the
// single source of truth for "which models can this user launch right now",
// so its filter semantics must be locked down here.
func TestEligibleModels(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers: []Provider{
			{ID: "claude", Location: LocationCloud, Auth: AuthConfig{Type: "native"}},
			{ID: "ollama", Location: LocationLocal, Auth: AuthConfig{Type: "none"}},
		},
		Models: []Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Location: LocationCloud, Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Family: "sonnet", Location: LocationCloud, Tags: []string{"code", "design"}},
			{ID: "claude/native", ProviderID: "claude", ModelName: "native", Location: LocationCloud, Tags: []string{"code"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Location: LocationLocal, Tags: []string{"code"}},
			{ID: "ollama/llama3", ProviderID: "ollama", ModelName: "llama3", Family: "llama3", Location: LocationLocal, Tags: []string{"design"}},
		},
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"ollama", "claude"}},
		},
		exposed: map[string]bool{
			"ollama/gemma4:9b": true,
			"ollama/llama3":    true,
		},
	}
	deriveNative(cfg)

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		wantIDs []string
		wantErr bool
	}{
		{"claude no filters", "claude", "", "", []string{"claude/opus", "claude/sonnet", "claude/native"}, false},
		{"pi no filters", "pi", "", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b", "ollama/llama3"}, false},
		{"tag code only", "pi", "code", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b"}, false},
		{"tag multi", "pi", "code,design", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b", "ollama/llama3"}, false},
		{"family gemma4", "pi", "", "gemma4", []string{"ollama/gemma4:9b"}, false},
		{"tag+family AND", "pi", "code", "gemma4", []string{"ollama/gemma4:9b"}, false},
		{"tag+family AND no overlap", "pi", "design", "gemma4", nil, false},
		{"unknown agent", "nope", "", "", nil, true},
		{"tag with whitespace", "pi", " code , design ", "", []string{"claude/opus", "claude/sonnet", "claude/native", "ollama/gemma4:9b", "ollama/llama3"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cfg.EligibleModels(tc.agent, tc.tags, tc.family)
			if (err != nil) != tc.wantErr {
				t.Fatalf("EligibleModels(%q, %q, %q) err = %v, wantErr = %v", tc.agent, tc.tags, tc.family, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			gotIDs := make([]string, len(got))
			for i, m := range got {
				gotIDs[i] = m.ID
			}
			if len(gotIDs) != len(tc.wantIDs) {
				t.Fatalf("got %d models (%v), want %d (%v)", len(gotIDs), gotIDs, len(tc.wantIDs), tc.wantIDs)
			}
			for i, id := range tc.wantIDs {
				if gotIDs[i] != id {
					t.Errorf("model[%d] = %q, want %q", i, gotIDs[i], id)
				}
			}
		})
	}
}

// TestEligibleModelsInMatchesEligibleModels pins the shared-filter helper used
// by the TUI model picker to avoid a second full-catalog traversal: it must
// return exactly what EligibleModels returns when handed the same full catalog
// directly. The two must never drift — if they did, the picker's eligible
// slice would disagree with the non-TUI launch path.
func TestEligibleModelsInMatchesEligibleModels(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers: []Provider{
			{ID: "claude", Location: LocationCloud, Auth: AuthConfig{Type: "native"}},
			{ID: "ollama", Location: LocationLocal, Auth: AuthConfig{Type: "none"}},
		},
		Models: []Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Location: LocationCloud, Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Family: "sonnet", Location: LocationCloud, Tags: []string{"code", "design"}},
			{ID: "claude/native", ProviderID: "claude", ModelName: "native", Location: LocationCloud, Tags: []string{"code"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Location: LocationLocal, Tags: []string{"code"}},
			{ID: "ollama/llama3", ProviderID: "ollama", ModelName: "llama3", Family: "llama3", Location: LocationLocal, Tags: []string{"design"}},
		},
		Agents: []Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"ollama", "claude"}},
		},
		exposed: map[string]bool{
			"ollama/gemma4:9b": true,
			"ollama/llama3":    true,
		},
	}
	deriveNative(cfg)

	catalog, err := cfg.ModelsForAgent("pi")
	if err != nil {
		t.Fatalf("ModelsForAgent: %v", err)
	}
	// A precomputed full catalog must filter identically to the eager path.
	got, err := cfg.EligibleModelsIn("pi", catalog, "code", "gemma4")
	if err != nil {
		t.Fatalf("EligibleModelsIn with precomputed catalog: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ollama/gemma4:9b" {
		t.Fatalf("EligibleModelsIn(catalog) = %v, want [ollama/gemma4:9b]", idsOf(got))
	}

	want, err := cfg.EligibleModels("pi", "code", "gemma4")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("EligibleModelsIn = %v, EligibleModels = %v; lengths differ", idsOf(got), idsOf(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Errorf("model[%d]: EligibleModelsIn = %q, EligibleModels = %q", i, got[i].ID, want[i].ID)
		}
	}
}

// idsOf returns the IDs of a model slice, for comparison in tests.
func idsOf(ms []Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
