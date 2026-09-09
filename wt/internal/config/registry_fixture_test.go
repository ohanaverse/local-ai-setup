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

	if len(providers) != 5 {
		t.Fatalf("got %d providers, want 5", len(providers))
	}
	ollama, openrouter, agy := providers[0], providers[1], providers[2]
	if ollama.ID != "ollama" || ollama.Auth.Type != "none" || ollama.Auth.BaseURL != "http://localhost:11434" {
		t.Errorf("ollama provider decoded wrong: %+v", ollama)
	}
	if openrouter.ID != "openrouter" || openrouter.Auth.Type != "api_key" || openrouter.Auth.SecretRef != "OPENROUTER_API_KEY" {
		t.Errorf("openrouter provider decoded wrong: %+v", openrouter)
	}
	// The native provider's location must match what production writers
	// emit (modelman's sync_agent_providers, wt's migrate.go) — a fixture
	// pinned to a shape modelman never writes lets location-keyed logic
	// pass CI while breaking on real registries.
	if agy.ID != "agy" || agy.Auth.Type != "native" || agy.Location != LocationCloud {
		t.Errorf("agy provider decoded wrong: %+v", agy)
	}

	pinned := providers[3]
	if pinned.ID != "pinned-cloud" || pinned.Auth.Type != "api_key" || pinned.Location != LocationCloud {
		t.Errorf("pinned-cloud provider decoded wrong: %+v", pinned)
	}

	// The mlx_lm_server provider must decode with the shape modelman's
	// default template writes (auth.type "none", OpenAI-compatible
	// base_url, location "local") — a fixture pinned to a shape modelman
	// never writes lets location-keyed logic pass CI while breaking on
	// real registries.
	mlx := providers[4]
	if mlx.ID != "mlx_lm_server" || mlx.Auth.Type != "none" || mlx.Auth.BaseURL != "http://localhost:8001/v1" || mlx.Location != LocationLocal {
		t.Errorf("mlx_lm_server provider decoded wrong: %+v", mlx)
	}

	if len(models) != 5 {
		t.Fatalf("got %d models, want 5", len(models))
	}
	cloud := models[1]
	if cloud.ID != "openrouter/contract-fixture:cloud" || cloud.Location != "cloud" || cloud.ProviderID != "openrouter" {
		t.Errorf("cloud model decoded wrong: %+v", cloud)
	}

	inherit := models[3]
	if inherit.ID != "pinned-cloud/contract-fixture:inherit" || inherit.Location != "" || inherit.ProviderID != "pinned-cloud" {
		t.Errorf("inherit model decoded wrong: %+v", inherit)
	}

	// The mlx_lm_server pairing model must decode its provider linkage
	// (fetch/draft are modelman-only and ignored by wt's parser, but the
	// provider_id must resolve so the model is offered/eligible).
	pair := models[4]
	if pair.ID != "mlx_lm_server/contract-fixture:pair" || pair.ProviderID != "mlx_lm_server" {
		t.Errorf("mlx_lm_server pairing model decoded wrong: %+v", pair)
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

// TestRegistryFixtureNativeExposure pins the cross-language rule that a
// native model is always exposed even without a model_state row. The same
// fixture file is read by modelman's contract test.
func TestRegistryFixtureNativeExposure(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}

	cfg := &Config{Providers: providers, Models: models}
	deriveNative(cfg)
	cfg.SetExposedForTest(map[string]struct {
		LitellmExposed bool
		Ready          bool
	}{})

	native := cfg.Models[2]
	if native.ID != "agy/contract-fixture:native" {
		t.Fatalf("expected third model to be the native fixture, got %q", native.ID)
	}
	if !native.Native {
		t.Errorf("native model %q has Native=%v, want true", native.ID, native.Native)
	}
	if !cfg.IsExposed(native) {
		t.Errorf("IsExposed(native model %q) = false, want true", native.ID)
	}
}

// TestRegistryFixtureProviderLocationInheritance pins issue #46 from the
// Go side: IsExposed resolves the model's location through the provider,
// so a flag-on, not-ready model on a location=cloud provider is exposed.
func TestRegistryFixtureProviderLocationInheritance(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	c := &Config{Providers: providers, Models: models}
	deriveNative(c)
	c.SetExposedForTest(map[string]struct {
		LitellmExposed bool
		Ready          bool
	}{
		"pinned-cloud/contract-fixture:inherit": {LitellmExposed: true, Ready: false},
	})

	var inherit *Model
	for i := range models {
		if models[i].ID == "pinned-cloud/contract-fixture:inherit" {
			inherit = &models[i]
		}
	}
	if inherit == nil {
		t.Fatal("fixture missing pinned-cloud/contract-fixture:inherit")
	}
	if inherit.Native {
		t.Errorf("inherit model %q is native, want non-native", inherit.ID)
	}
	if !c.IsExposed(*inherit) {
		t.Error("IsExposed(inherit model) = false, want true (provider location=cloud must inherit)")
	}
}
