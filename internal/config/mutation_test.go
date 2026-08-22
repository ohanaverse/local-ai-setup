package config

import (
	"testing"
)

// TestProviderInUse_ReportsModelsAndAgents verifies that ProviderInUse lists
// every model and agent that references the provider. This is the FK check
// backing the config editor's delete confirmation.
func TestProviderInUse_ReportsModelsAndAgents(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "p1"}, {ID: "p2"}},
		Models:    []Model{{ID: "m1", ProviderID: "p1"}, {ID: "m2", ProviderID: "p2"}},
		Agents:    []Agent{{Name: "a1", SupportedProviders: []string{"p1"}}, {Name: "a2", SupportedProviders: []string{"p1", "p2"}}},
	}

	refs := cfg.ProviderInUse("p1")
	if len(refs) != 3 {
		t.Fatalf("expected 3 references to p1, got %v", refs)
	}
	want := map[string]bool{"model:m1": true, "agent:a1": true, "agent:a2": true}
	for _, r := range refs {
		if !want[r] {
			t.Errorf("unexpected reference %q", r)
		}
	}

	if len(cfg.ProviderInUse("p2")) != 2 {
		t.Fatalf("expected 2 references to p2, got %v", cfg.ProviderInUse("p2"))
	}
}

// TestUpsertProvider_AddThenUpdate verifies that UpsertProvider appends a new
// provider and updates an existing one by ID.
func TestUpsertProvider_AddThenUpdate(t *testing.T) {
	cfg := &Config{}

	if err := cfg.UpsertProvider(Provider{ID: "p1", Name: "P1"}, true); err != nil {
		t.Fatalf("add provider: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(cfg.Providers))
	}

	if err := cfg.UpsertProvider(Provider{ID: "p1", Name: "P1-new"}, false); err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if cfg.Providers[0].Name != "P1-new" {
		t.Errorf("name = %q, want P1-new", cfg.Providers[0].Name)
	}
}

// TestUpsertProvider_DuplicateAddBlocked verifies that adding a provider
// with an existing ID is rejected, preventing accidental overwrites.
func TestUpsertProvider_DuplicateAddBlocked(t *testing.T) {
	cfg := &Config{Providers: []Provider{{ID: "p1"}}}
	if err := cfg.UpsertProvider(Provider{ID: "p1"}, true); err == nil {
		t.Fatal("expected error adding duplicate provider")
	}
}

// TestUpsertModel_AddThenUpdate verifies that UpsertModel appends a new
// model and updates an existing one by ID.
func TestUpsertModel_AddThenUpdate(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "p1"}},
	}

	if err := cfg.UpsertModel(Model{ID: "m1", ProviderID: "p1"}, true); err != nil {
		t.Fatalf("add model: %v", err)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(cfg.Models))
	}

	if err := cfg.UpsertModel(Model{ID: "m1", ProviderID: "p1", Family: "f1"}, false); err != nil {
		t.Fatalf("update model: %v", err)
	}
	if cfg.Models[0].Family != "f1" {
		t.Errorf("family = %q, want f1", cfg.Models[0].Family)
	}
}

// TestUpsertModel_DuplicateAddBlocked verifies that adding a model with an
// existing ID is rejected. Duplicate IDs break model lookups and rotation.
func TestUpsertModel_DuplicateAddBlocked(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "p1"}},
		Models:    []Model{{ID: "m1", ProviderID: "p1"}},
	}
	if err := cfg.UpsertModel(Model{ID: "m1", ProviderID: "p1"}, true); err == nil {
		t.Fatal("expected error adding duplicate model")
	}
}

// TestUpsertModel_UnknownProviderBlocked verifies that a model cannot point
// to a provider that does not exist. This catches typos early.
func TestUpsertModel_UnknownProviderBlocked(t *testing.T) {
	cfg := &Config{}
	if err := cfg.UpsertModel(Model{ID: "m1", ProviderID: "missing"}, true); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

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
