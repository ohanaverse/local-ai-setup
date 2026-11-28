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
	if err := cfg.UpsertAgent(Agent{Name: "a1"}, ""); err != nil {
		t.Fatalf("add agent: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(cfg.Agents))
	}

	// Rename.
	if err := cfg.UpsertAgent(Agent{Name: "a2"}, "a1"); err != nil {
		t.Fatalf("rename agent: %v", err)
	}
	if cfg.Agents[0].Name != "a2" {
		t.Errorf("name = %q, want a2", cfg.Agents[0].Name)
	}

	// Rename collision.
	cfg.Agents = append(cfg.Agents, Agent{Name: "a3"})
	if err := cfg.UpsertAgent(Agent{Name: "a3"}, "a2"); err == nil {
		t.Fatal("expected error renaming agent to existing name")
	}

	// Delete.
	cfg.DeleteAgent("a2")
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "a3" {
		t.Fatalf("expected only a3 after delete, got %v", cfg.Agents)
	}
}

// TestUpsertAgent_Validation verifies UpsertAgent still enforces the one
// required field, the name, and a well-formed entry still saves.
func TestUpsertAgent_Validation(t *testing.T) {
	cfg := &Config{Providers: []Provider{{ID: "p1"}}}

	if err := cfg.UpsertAgent(Agent{}, ""); err == nil {
		t.Fatal("expected error for an empty name")
	}
	if err := cfg.UpsertAgent(Agent{Name: "a1"}, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestUpsertAgentNeedsOnlyANameAndNoCollision proves UpsertAgent checks the
// name and the rename collision and nothing else: with no providers loaded (a
// session config reads no registry) agents are saved, while an empty name and
// a rename onto an existing name still fail. Why: the editor saves on a
// session machine, where a provider-existence check could never pass, and an
// agent names no provider.
func TestUpsertAgentNeedsOnlyANameAndNoCollision(t *testing.T) {
	cfg := &Config{}
	if err := cfg.UpsertAgent(Agent{Name: "a1"}, ""); err != nil {
		t.Fatalf("a named agent refused: %v", err)
	}
	if err := cfg.UpsertAgent(Agent{Name: "a2"}, ""); err != nil {
		t.Fatalf("a second named agent refused: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("agents = %+v, want both saved", cfg.Agents)
	}
	if err := cfg.UpsertAgent(Agent{}, ""); err == nil {
		t.Error("an empty name was accepted")
	}
	if err := cfg.UpsertAgent(Agent{Name: "a2"}, "a1"); err == nil {
		t.Error("a rename onto an existing name was accepted")
	}
}
