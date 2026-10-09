package config

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

const predicatesDir = "../../../docs/contracts/"

type predicateExpectations struct {
	InCatalog        []string `json:"in_catalog"`
	OpenRouterPriced []string `json:"openrouter_priced"`
	RoutableCloud    []string `json:"routable_cloud"`
}

func loadPredicateFixture(t *testing.T) (*Config, predicateExpectations) {
	t.Helper()
	t.Setenv("WT_REGISTRY", predicatesDir+"catalog-predicates.sample.toml")
	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry: %v", err)
	}
	cfg := &Config{Providers: providers, Models: models}
	deriveNative(cfg)
	raw, err := os.ReadFile(predicatesDir + "catalog-predicates.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var want predicateExpectations
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	return cfg, want
}

// TestCatalogPredicatesFixture pins InCatalog and OpenRouterPriced to the
// contract fixture (#179, #180), which this test reads. internal/cloudsync
// asserts the same openrouter_priced list over registry rows, so a rule
// change in one reader only — e.g. a new excluded provider kind — fails
// instead of silently making wt nag about refreshes with nothing to refresh.
func TestCatalogPredicatesFixture(t *testing.T) {
	cfg, want := loadPredicateFixture(t)
	var inCatalog, priced []string
	for _, m := range cfg.Models {
		if cfg.InCatalog(m) {
			inCatalog = append(inCatalog, m.ID)
		}
		if cfg.OpenRouterPriced(m) {
			priced = append(priced, m.ID)
		}
	}
	if !slices.Equal(inCatalog, want.InCatalog) {
		t.Errorf("in_catalog = %v, want %v", inCatalog, want.InCatalog)
	}
	if !slices.Equal(priced, want.OpenRouterPriced) {
		t.Errorf("openrouter_priced = %v, want %v", priced, want.OpenRouterPriced)
	}
}
