package config

import (
	"os"
	"testing"

	"github.com/BurntSushi/toml"
)

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

	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	cloud := models[1]
	if cloud.ID != "openrouter/contract-fixture:cloud" || cloud.Location != "cloud" || cloud.ProviderID != "openrouter" {
		t.Errorf("cloud model decoded wrong: %+v", cloud)
	}
}

// TestRegistryFixtureCost verifies that the shared fixture's full pricing
// model decodes with the new flat per-token + subscription fields. This test
// uses a local decode struct so it keeps working before the production
// Model.Cost field is added in the next task.
func TestRegistryFixtureCost(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	path := RegistryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	type fixtureCost struct {
		InputPricePerMillion  float64 `toml:"input_price_per_million"`
		CachePricePerMillion  float64 `toml:"cache_price_per_million"`
		OutputPricePerMillion float64 `toml:"output_price_per_million"`
		SubscriptionPrice     float64 `toml:"subscription_price"`
		SubscriptionPeriod    string  `toml:"subscription_period"`
	}
	type fixtureModel struct {
		ID   string     `toml:"id"`
		Cost fixtureCost `toml:"cost"`
	}
	var fixture struct {
		Models []fixtureModel `toml:"models"`
	}
	if _, err := toml.Decode(string(data), &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	var cloud *fixtureModel
	for i := range fixture.Models {
		if fixture.Models[i].ID == "openrouter/contract-fixture:cloud" {
			cloud = &fixture.Models[i]
			break
		}
	}
	if cloud == nil {
		t.Fatal("missing priced cloud model in fixture")
	}

	if cloud.Cost.InputPricePerMillion != 0.50 {
		t.Errorf("input price = %v, want 0.50", cloud.Cost.InputPricePerMillion)
	}
	if cloud.Cost.CachePricePerMillion != 0.25 {
		t.Errorf("cache price = %v, want 0.25", cloud.Cost.CachePricePerMillion)
	}
	if cloud.Cost.OutputPricePerMillion != 1.00 {
		t.Errorf("output price = %v, want 1.00", cloud.Cost.OutputPricePerMillion)
	}
	if cloud.Cost.SubscriptionPrice != 19.99 {
		t.Errorf("subscription price = %v, want 19.99", cloud.Cost.SubscriptionPrice)
	}
	if cloud.Cost.SubscriptionPeriod != "month" {
		t.Errorf("subscription period = %q, want \"month\"", cloud.Cost.SubscriptionPeriod)
	}
}
