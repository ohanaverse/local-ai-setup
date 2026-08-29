package main

import (
	"errors"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestResolveModel covers the non-TUI model resolution path used after
// -W/--cwd has resolved the worktree and -A has resolved the agent.
// This is the gate that catches config errors and -M mismatches before
// launching an agent.
func TestResolveModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "ollama", ModelName: "sonnet", Family: "sonnet", Tags: []string{"design"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"claude", "ollama"}},
		},
	}

	// resolveModel errors on any ambiguous eligible list rather than
	// falling back to defaultModel. launch.go calls resolveModel and
	// handles the error via rotation.

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		pinned  string
		wantID  string
		wantErr bool
	}{
		{"single match", "claude", "", "", "", "claude/opus", false},
		{"pinned in eligible", "pi", "", "", "claude/opus", "claude/opus", false},
		{"pinned not in eligible", "pi", "", "", "ollama/missing", "", true},
		{"pinned wrong provider for agent", "claude", "", "", "ollama/gemma4:9b", "", true},
		{"multiple no pin errors", "pi", "", "", "", "", true},
		{"tag filter narrows to one", "pi", "code", "gemma4", "", "ollama/gemma4:9b", false},
		{"empty eligible errors", "claude", "design", "", "", "", true},
		{"unknown agent", "nope", "", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, err := resolveModel(tc.agent, cfg, tc.tags, tc.family, tc.pinned)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if m.ID != tc.wantID {
				t.Errorf("got ID %q, want %q", m.ID, tc.wantID)
			}
		})
	}

	// A command agent returns the errCommandAgent sentinel specifically, so
	// launchFiltered's errors.Is(err, errCommandAgent) dispatch matches. This
	// locks the sentinel identity the dispatch depends on.
	if _, _, err := resolveModel("shell", cfg, "", "", ""); !errors.Is(err, errCommandAgent) {
		t.Errorf("resolveModel(shell) err = %v, want errCommandAgent", err)
	}
}

// TestResolveModelReturnsEligible verifies resolveModel returns the full
// eligible list even when it returns an error, so callers can reuse it
// instead of calling cfg.EligibleModels a second time.
func TestResolveModelReturnsEligible(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "ollama/code", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/design", ProviderID: "ollama", Tags: []string{"design"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}

	m, eligible, err := resolveModel("claude", cfg, "", "", "")
	if err == nil {
		t.Fatal("expected multiple-models error")
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero", m.ID)
	}
	if len(eligible) != 2 {
		t.Fatalf("eligible = %d, want 2", len(eligible))
	}
}
