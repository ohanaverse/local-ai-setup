package litellm

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// discoveredCfg is testConfig plus the omlx family (omlx and omlx-6bit, one
// physical server) with one registry 6-bit model, and an ollama cloud model —
// a cloud model on the LOCAL ollama provider.
func discoveredCfg() *config.Config {
	cfg := testConfig()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		config.Provider{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
	)
	cfg.Models = append(cfg.Models,
		config.Model{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
		config.Model{ID: "ollama/glm:cloud", ProviderID: "ollama", ModelName: "glm:cloud", Location: config.LocationCloud},
	)
	return cfg
}

// TestDiscoveredModelAndRowFamily pins the two id rules discovered routing
// rests on (#179 Phase B): a discovered model's id is the catalog's
// config.DiscoveredModelID under its provider FAMILY — two slashes for an
// mtplx repo id — and RowFamily classifies a config.yaml row by family:
// registry local models by provider (omlx-6bit is the omlx family), registry
// cloud models never (even on the local ollama provider — otherwise an
// untrusted ollama probe would freeze its cloud routes), and any other id by
// its local-provider prefix. A wrong family either strands a stale route or
// lets a stop delete a sibling's.
func TestDiscoveredModelAndRowFamily(t *testing.T) {
	m := DiscoveredModel("mtplx", "mlx-community/Qwen3.8-27B-4bit")
	want := config.Model{ID: "mtplx/mlx-community/Qwen3.8-27B-4bit", ProviderID: "mtplx", ModelName: "mlx-community/Qwen3.8-27B-4bit", Location: config.LocationLocal, Source: config.SourceDiscovered}
	if !equalModel(m, want) {
		t.Fatalf("DiscoveredModel = %+v, want %+v", m, want)
	}
	cfg := discoveredCfg()
	cases := map[string]string{
		"omlx-6bit/Six":                   "omlx",
		"mtplx/Youssofal--Q":              "mtplx",
		"ollama/glm:cloud":                "",
		"openrouter/x/y":                  "",
		"omlx/stray-model":                "omlx",
		"omlx-6bit/deleted":               "omlx",
		"mtplx/mlx-community/Qwen3.8-27B": "mtplx",
		"openrouter/qwen/deleted":         "",
		"my-alias":                        "",
	}
	for id, want := range cases {
		if got := RowFamily(cfg, id); got != want {
			t.Errorf("RowFamily(%s) = %q, want %q", id, got, want)
		}
	}
}

// equalModel compares the identity fields DiscoveredModel sets.
func equalModel(a, b config.Model) bool {
	return a.ID == b.ID && a.ProviderID == b.ProviderID && a.ModelName == b.ModelName && a.Location == b.Location && a.Source == b.Source
}

// TestPrepareModelBuildsDiscoveredRow pins the row a discovered model gets:
// the provider policy supplies the prefixed model, the /v1 api_base and the
// literal key; pricing is the explicit $0 every local row carries; and the
// row is marked as wt's, so a later stop or sync can remove it. Without the
// marker a discovered route would read as hand-written and never go away.
func TestPrepareModelBuildsDiscoveredRow(t *testing.T) {
	cfg := discoveredCfg()
	node, err := prepareModel(cfg, DiscoveredModel("omlx", "stray-model"))
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, node)
	if got["model_name"] != "omlx/stray-model" {
		t.Errorf("model_name = %v", got["model_name"])
	}
	params := got["litellm_params"].(map[string]any)
	if params["model"] != "openai/stray-model" || params["api_base"] != "http://localhost:8000/v1" || params["api_key"] != "not-needed" {
		t.Errorf("litellm_params = %v", params)
	}
	info := got["model_info"].(map[string]any)
	if info["input_cost_per_token"] != 0 || info["output_cost_per_token"] != 0 || info[ManagedKey] != true {
		t.Errorf("model_info = %v, want $0 pricing and the marker", info)
	}
	if _, err := prepareModel(cfg, DiscoveredModel("ghost", "x")); err == nil {
		t.Error("a discovered model whose provider is not in the registry must be rejected")
	}
}
