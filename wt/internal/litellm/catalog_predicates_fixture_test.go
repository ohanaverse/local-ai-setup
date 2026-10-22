package litellm

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestCloudModelsMatchesSharedFixture pins sync's desired cloud set to the
// shared contract (#179): openrouter and ollama cloud models are routed; a
// native model, a cloud provider with no LiteLLM mapping, a dangling
// provider_id and a local model are not.
func TestCloudModelsMatchesSharedFixture(t *testing.T) {
	const dir = "../../../docs/contracts/"
	var reg struct {
		Providers []config.Provider `toml:"providers"`
		Models    []config.Model    `toml:"models"`
	}
	if _, err := toml.DecodeFile(dir+"catalog-predicates.sample.toml", &reg); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Providers: reg.Providers, Models: reg.Models}
	for i := range cfg.Models {
		if p := cfg.ProviderByID(cfg.Models[i].ProviderID); p != nil && p.Auth.Type == "native" {
			cfg.Models[i].Native = true
		}
	}
	raw, err := os.ReadFile(dir + "catalog-predicates.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		RoutableCloud []string `json:"routable_cloud"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range CloudModels(cfg) {
		got = append(got, m.ID)
	}
	if !slices.Equal(got, want.RoutableCloud) {
		t.Fatalf("CloudModels = %v, want %v", got, want.RoutableCloud)
	}
}
