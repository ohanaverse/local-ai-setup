package litellm

import (
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

func f64(v float64) *float64 { return &v }

// testConfig is the shared registry fixture: two local providers, one cloud,
// one native, with one model each.
func testConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}},
			{ID: "claude", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma:9b", ProviderID: "ollama", ModelName: "gemma:9b", Location: config.LocationLocal},
			{ID: "mtplx/Youssofal--Q", ProviderID: "mtplx", ModelName: "Youssofal/Q", Location: config.LocationLocal},
			{
				ID: "openrouter/x/y", ProviderID: "openrouter", ModelName: "x/y", Location: config.LocationCloud,
				Cost:      config.ModelCost{InputPricePerMillion: f64(1), OutputPricePerMillion: f64(2), CachePricePerMillion: f64(0.5)},
				ModelInfo: map[string]any{"supports_vision": true, "input_cost_per_token": 0.25},
			},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Native: true},
		},
	}
}

func decode(t *testing.T, n *yaml.Node) map[string]any {
	t.Helper()
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBuildEntryCloudRow pins the exact row shape for a priced cloud model:
// prefixed model, provider api_base, the secret_ref as api_key, per-token
// pricing converted from per-million, cache price on both cache keys, and
// the model's own model_info overriding derived pricing. LiteLLM cost
// tracking and budget checks depend on these keys being right.
func TestBuildEntryCloudRow(t *testing.T) {
	cfg := testConfig()
	node, err := BuildEntry(cfg.Models[2], cfg.Providers[2])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model_name": "openrouter/x/y",
		"litellm_params": map[string]any{
			"model":    "openrouter/x/y",
			"api_base": "https://openrouter.ai/api/v1",
			"api_key":  "sk-test",
		},
		"model_info": map[string]any{
			"input_cost_per_token":            0.25, // model_info overrides derived 1e-06
			"output_cost_per_token":           2.0 / 1_000_000,
			"cache_creation_input_token_cost": 0.5 / 1_000_000,
			"cache_read_input_token_cost":     0.5 / 1_000_000,
			"supports_vision":                 true,
		},
	}
	if got := decode(t, node); !reflect.DeepEqual(got, want) {
		t.Fatalf("row =\n%v\nwant\n%v", got, want)
	}
}

// TestBuildEntryLocalRows pins the local mappings: ollama uses the
// ollama_chat/ prefix and no api_key; mtplx uses openai/ with the
// "not-needed" key; unpriced models get explicit zero costs so LiteLLM
// bypasses budget checks for them.
func TestBuildEntryLocalRows(t *testing.T) {
	cfg := testConfig()
	oll, err := BuildEntry(cfg.Models[0], cfg.Providers[0])
	if err != nil {
		t.Fatal(err)
	}
	params := decode(t, oll)["litellm_params"].(map[string]any)
	if params["model"] != "ollama_chat/gemma:9b" || params["api_base"] != "http://localhost:11434" {
		t.Fatalf("ollama params = %v", params)
	}
	if _, has := params["api_key"]; has {
		t.Fatal("ollama row must not carry an api_key")
	}
	info := decode(t, oll)["model_info"].(map[string]any)
	if info["input_cost_per_token"] != 0 || info["output_cost_per_token"] != 0 {
		t.Fatalf("unpriced model_info = %v, want explicit zeros", info)
	}

	mt, err := BuildEntry(cfg.Models[1], cfg.Providers[1])
	if err != nil {
		t.Fatal(err)
	}
	mp := decode(t, mt)["litellm_params"].(map[string]any)
	if mp["model"] != "openai/Youssofal/Q" || mp["api_key"] != "not-needed" {
		t.Fatalf("mtplx params = %v", mp)
	}
}

// TestBuildEntryRejectsUnmappedProvider guards the "no LiteLLM mapping"
// invariant: a provider missing from the policy table must error, never
// produce a half-built row.
func TestBuildEntryRejectsUnmappedProvider(t *testing.T) {
	cfg := testConfig()
	if _, err := BuildEntry(cfg.Models[3], cfg.Providers[3]); err == nil {
		t.Fatal("BuildEntry(native claude) = nil error, want no-mapping error")
	}
}

// TestOmlx6bitHasAPolicy pins that the 6-bit oMLX provider is routable like
// its 4-bit sibling: one oMLX server serves both quantizations, so a missing
// policy meant a started omlx-6bit model never got a route (a stderr warning
// on every start), was invisible to LocalModels/sync, and its stale rows
// survived the family sweep on stop.
func TestOmlx6bitHasAPolicy(t *testing.T) {
	pol, ok := PolicyFor("omlx-6bit")
	if !ok {
		t.Fatal("PolicyFor(omlx-6bit) not found")
	}
	if four, _ := PolicyFor("omlx"); pol != four {
		t.Errorf("omlx-6bit policy = %+v, want the same mapping as omlx (%+v)", pol, four)
	}
	p := config.Provider{ID: "omlx-6bit", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000/v1"}}
	m := config.Model{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal}
	node, err := BuildEntry(m, p)
	if err != nil {
		t.Fatalf("BuildEntry: %v", err)
	}
	got := decode(t, node)
	want := map[string]any{"model": "openai/Six", "api_base": "http://localhost:8000/v1", "api_key": "not-needed"}
	if !reflect.DeepEqual(got["litellm_params"], want) {
		t.Errorf("litellm_params = %#v, want %#v", got["litellm_params"], want)
	}

	cfg := &config.Config{Providers: []config.Provider{p}, Models: []config.Model{m}}
	if locals := LocalModels(cfg); len(locals) != 1 || locals[0].ID != "omlx-6bit/Six" {
		t.Errorf("LocalModels = %v, want the omlx-6bit model (sync must manage it)", locals)
	}
}
