package config

import (
	"os"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestLoadRegistryMatchesSharedFixture guards wt's registry.toml decoding
// against every schema variant the registry holds. The fixture at
// docs/contracts/registry.sample.toml is read by this test and by
// llmbench/tests/test_registry.py — a schema change that one reader misses
// fails here instead of wt silently dropping a provider or a price.
func TestLoadRegistryMatchesSharedFixture(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}

	if len(providers) != 6 {
		t.Fatalf("got %d providers, want 6", len(providers))
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

	if len(models) != 7 {
		t.Fatalf("got %d models, want 7", len(models))
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
	// (its fetch and draft are read by TestModelDecodesFetchAndDraft; the
	// provider_id must resolve so the model is offered/eligible).
	pair := models[4]
	if pair.ID != "mlx_lm_server/contract-fixture:pair" || pair.ProviderID != "mlx_lm_server" {
		t.Errorf("mlx_lm_server pairing model decoded wrong: %+v", pair)
	}

	// #179 Phase B overlay rows: a "--"-style local id whose model_name is
	// the repo form, and an overlay that is not on disk. Both must decode
	// like any local model; the catalog test proves which one gets a row.
	mtplx := providers[5]
	if mtplx.ID != "mtplx" || mtplx.Location != LocationLocal || mtplx.Auth.BaseURL != "http://localhost:8003/v1" {
		t.Errorf("mtplx provider decoded wrong: %+v", mtplx)
	}
	dashed, absent := models[5], models[6]
	if dashed.ID != "mtplx/org--contract-fixture-dashed" || dashed.ModelName != "org/contract-fixture-dashed" || dashed.ProviderID != "mtplx" {
		t.Errorf("dashed overlay decoded wrong: %+v", dashed)
	}
	if absent.ID != "ollama/contract-fixture:absent" || absent.ModelName != "contract-fixture:absent" || absent.ProviderID != "ollama" {
		t.Errorf("absent overlay decoded wrong: %+v", absent)
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
		ID   string      `toml:"id"`
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

// TestRegistryFixtureNativeExposure pins the rule that a native model is
// always in the catalog, with no stored flag to say so. If it were not, a
// native agent's own model would drop out of its picker and a launch would
// fail on model resolution. The fixture (docs/contracts/registry.sample.toml)
// is read by this test and by llmbench/tests/test_registry.py.
func TestRegistryFixtureNativeExposure(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}

	cfg := &Config{Providers: providers, Models: models}
	deriveNative(cfg)

	native := cfg.Models[2]
	if native.ID != "agy/contract-fixture:native" {
		t.Fatalf("expected third model to be the native fixture, got %q", native.ID)
	}
	if !native.Native {
		t.Errorf("native model %q has Native=%v, want true", native.ID, native.Native)
	}
	if !cfg.InCatalog(native) {
		t.Errorf("InCatalog(native model %q) = false, want true", native.ID)
	}
}

// TestRegistryFixtureProviderProtocols pins that wt decodes the shared
// `protocols` array the same way modelman does; ResolveRoute's direct-vs-
// litellm decision (Task 5) depends on both languages agreeing on this.
func TestRegistryFixtureProviderProtocols(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, _, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	ollama := providers[0]
	if got := ollama.EffectiveProtocols(); !equalProtocols(got, []Protocol{ProtocolAnthropic, ProtocolOpenAIChat}) {
		t.Errorf("ollama protocols = %v", got)
	}
	openrouter := providers[1]
	if got := openrouter.EffectiveProtocols(); !equalProtocols(got, []Protocol{ProtocolOpenAIChat}) {
		t.Errorf("openrouter protocols = %v", got)
	}
}

// equalProtocols reports whether two protocol slices contain the same
// elements in the same order.
func equalProtocols(a, b []Protocol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRegistryFixtureProviderLocationInheritance pins issue #46 from the
// Go side: InCatalog resolves the model's location through the provider,
// so a model on a location=cloud provider is in the catalog even when its
// own row omits `location`.
func TestRegistryFixtureProviderLocationInheritance(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	c := &Config{Providers: providers, Models: models}
	deriveNative(c)

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
	if !c.InCatalog(*inherit) {
		t.Error("InCatalog(inherit model) = false, want true (provider location=cloud must inherit)")
	}
}

// TestRegistryFixtureTimePrices pins the time-windowed pricing rows
// (docs/superpowers/specs/2026-09-28-ollama-catalog-sync-design.md) that
// modelman writes under [models.cost]. wt only decodes them today.
func TestRegistryFixtureTimePrices(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	_, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	var cloud *Model
	for i := range models {
		if models[i].ID == "openrouter/contract-fixture:cloud" {
			cloud = &models[i]
		}
	}
	if cloud == nil {
		t.Fatal("missing priced cloud model in fixture")
	}
	if len(cloud.Cost.TimePrices) != 1 {
		t.Fatalf("got %d time prices, want 1", len(cloud.Cost.TimePrices))
	}
	tp := cloud.Cost.TimePrices[0]
	if tp.Label != "off-peak" || tp.Timezone != "UTC" {
		t.Errorf("time price decoded wrong: %+v", tp)
	}
	if tp.InputPricePerMillion == nil || *tp.InputPricePerMillion != 0.25 {
		t.Errorf("input price = %v, want 0.25", tp.InputPricePerMillion)
	}
	if tp.OutputPricePerMillion == nil || *tp.OutputPricePerMillion != 0.50 {
		t.Errorf("output price = %v, want 0.50", tp.OutputPricePerMillion)
	}
	if len(tp.Windows) != 3 || tp.Windows[1].Start != "18:00" || tp.Windows[1].End != "24:00" {
		t.Errorf("windows decoded wrong: %+v", tp.Windows)
	}
	if got := tp.Windows[2].Days; len(got) != 2 || got[0] != "sat" || got[1] != "sun" {
		t.Errorf("weekend window days = %v", got)
	}
}

// TestTypedReaderLoadsTheWrittenFixture pins that wt's own reader accepts a
// registry in the form wt's writer produces: docs/contracts/
// registry.written.sample.toml, which wt/internal/tomlw re-emits byte for
// byte. It holds what the hand-written sample cannot — integer prices, an
// empty tags array, keys wt does not model at every level below the top,
// [[header]] windows — and a reader that choked on any of them would fail on
// the user's real registry after the first wt write.
func TestTypedReaderLoadsTheWrittenFixture(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.written.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}
	if len(providers) != 4 || len(models) != 4 {
		t.Fatalf("got %d providers and %d models, want 4 and 4", len(providers), len(models))
	}
	ints := models[0]
	if ints.ID != "ollama/written-fixture:int" || ints.Tags == nil || len(ints.Tags) != 0 {
		t.Errorf("first model decoded wrong: %+v", ints)
	}
	if ints.Cost.InputPricePerMillion == nil || *ints.Cost.InputPricePerMillion != 3 ||
		ints.Cost.OutputPricePerMillion == nil || *ints.Cost.OutputPricePerMillion != 15 {
		t.Errorf("integer prices should decode as 3 and 15, got %+v", ints.Cost)
	}
	if got := ints.ModelInfo["max_input_tokens"]; got != int64(131072) {
		t.Errorf("model_info.max_input_tokens = %v (%T), want 131072", got, got)
	}
	cloud := models[1]
	if len(cloud.Cost.TimePrices) != 1 || len(cloud.Cost.TimePrices[0].Windows) != 2 {
		t.Fatalf("time_prices decoded wrong: %+v", cloud.Cost.TimePrices)
	}
	if w := cloud.Cost.TimePrices[0].Windows[1]; len(w.Days) != 2 || w.Days[0] != "sat" || w.End != "24:00" {
		t.Errorf("second window decoded wrong: %+v", w)
	}
	if cloud.Cost.SubscriptionPrice == nil || *cloud.Cost.SubscriptionPrice != 20 || cloud.Cost.SubscriptionPeriod != "month" {
		t.Errorf("subscription decoded wrong: %+v", cloud.Cost)
	}
	cfg := &Config{DefaultTag: "code", Providers: providers, Models: models}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the written fixture should validate: %v", err)
	}
}
