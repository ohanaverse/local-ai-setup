package config

import (
	"testing"
)

// TestUpsertAgent_AddRenameDelete verifies the full lifecycle: add an agent,
// rename it, and delete it. Renaming must not collide with existing agents.
func TestUpsertAgent_AddRenameDelete(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "p1"}},
	}

	// Add.
	if err := cfg.UpsertAgent(Agent{Name: "a1", SupportedProviders: []string{"p1"}}, ""); err != nil {
		t.Fatalf("add agent: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(cfg.Agents))
	}

	// Rename.
	if err := cfg.UpsertAgent(Agent{Name: "a2", SupportedProviders: []string{"p1"}}, "a1"); err != nil {
		t.Fatalf("rename agent: %v", err)
	}
	if cfg.Agents[0].Name != "a2" {
		t.Errorf("name = %q, want a2", cfg.Agents[0].Name)
	}

	// Rename collision.
	cfg.Agents = append(cfg.Agents, Agent{Name: "a3", SupportedProviders: []string{"p1"}})
	if err := cfg.UpsertAgent(Agent{Name: "a3", SupportedProviders: []string{"p1"}}, "a2"); err == nil {
		t.Fatal("expected error renaming agent to existing name")
	}

	// Delete.
	cfg.DeleteAgent("a2")
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "a3" {
		t.Fatalf("expected only a3 after delete, got %v", cfg.Agents)
	}
}

// TestUpsertAgent_Validation verifies UpsertAgent enforces required fields,
// existing providers, and default-provider membership.
func TestUpsertAgent_Validation(t *testing.T) {
	cfg := &Config{Providers: []Provider{{ID: "p1"}}}

	if err := cfg.UpsertAgent(Agent{Name: "a1"}, ""); err == nil {
		t.Fatal("expected error for empty supported providers")
	}
	if err := cfg.UpsertAgent(Agent{Name: "a1", SupportedProviders: []string{"missing"}}, ""); err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if err := cfg.UpsertAgent(Agent{Name: "a1", SupportedProviders: []string{"p1"}, DefaultProvider: "missing"}, ""); err == nil {
		t.Fatal("expected error for default provider not in supported list")
	}
	if err := cfg.UpsertAgent(Agent{Name: "a1", SupportedProviders: []string{"p1"}, DefaultProvider: "p1"}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
