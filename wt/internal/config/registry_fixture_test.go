package config

import "testing"

// TestLoadRegistryMatchesSharedFixture guards wt's registry.toml decoding
// against the schema modelman actually writes. The fixture at
// docs/contracts/registry.sample.toml is also read by modelman's
// tests/contracts/test_registry_fixture.py — if a schema change isn't
// reflected in both tests, both CI jobs fail in the same PR instead of
// drifting silently.
func TestLoadRegistryMatchesSharedFixture(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}

	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(providers))
	}
	ollama, openrouter := providers[0], providers[1]
	if ollama.ID != "ollama" || ollama.Auth.Type != "none" || ollama.Auth.BaseURL != "http://localhost:11434" {
		t.Errorf("ollama provider decoded wrong: %+v", ollama)
	}
	if openrouter.ID != "openrouter" || openrouter.Auth.Type != "api_key" || openrouter.Auth.SecretRef != "OPENROUTER_API_KEY" {
		t.Errorf("openrouter provider decoded wrong: %+v", openrouter)
	}

	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	cloud := models[1]
	if cloud.ID != "openrouter/contract-fixture:cloud" || cloud.Location != "cloud" || cloud.ProviderID != "openrouter" {
		t.Errorf("cloud model decoded wrong: %+v", cloud)
	}
}
