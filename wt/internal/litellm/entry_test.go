package litellm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
			"wt_managed":                      true,
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

// TestBuildEntryOpenAICompatAPIBaseEndsInV1 pins issue #168: LiteLLM's
// openai/ provider appends only /chat/completions to api_base, so an
// OpenAI-compatible local server's row must dial <origin>/v1 whichever form
// the registry stores (the seeded omlx base_url is the bare origin). Ollama's
// ollama_chat/ rows keep the bare origin.
func TestBuildEntryOpenAICompatAPIBaseEndsInV1(t *testing.T) {
	cases := []struct {
		provider, baseURL, want string
	}{
		{"omlx", "http://localhost:8000", "http://localhost:8000/v1"},
		{"omlx", "http://localhost:8000/", "http://localhost:8000/v1"},
		{"omlx", "http://localhost:8000/v1", "http://localhost:8000/v1"},
		{"omlx", "http://localhost:8000/v1/", "http://localhost:8000/v1"},
		{"omlx-6bit", "http://localhost:8000", "http://localhost:8000/v1"},
		{"mlx_lm_server", "http://localhost:8001", "http://localhost:8001/v1"},
		{"mtplx", "http://127.0.0.1:8003", "http://127.0.0.1:8003/v1"},
		{"ollama", "http://localhost:11434/", "http://localhost:11434/"},
	}
	for _, c := range cases {
		p := config.Provider{ID: c.provider, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: c.baseURL}}
		m := config.Model{ID: c.provider + "/m", ProviderID: c.provider, ModelName: "org/m", Location: config.LocationLocal}
		node, err := BuildEntry(m, p)
		if err != nil {
			t.Fatalf("%s %q: BuildEntry: %v", c.provider, c.baseURL, err)
		}
		params := decode(t, node)["litellm_params"].(map[string]any)
		if params["api_base"] != c.want {
			t.Errorf("%s %q: api_base = %v, want %q", c.provider, c.baseURL, params["api_base"], c.want)
		}
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

// TestBuildEntryUnencodableModelInfoReturnsError pins that a model_info value
// yaml.Node.Encode cannot marshal (a hand-edited registry.toml decoded into a
// weird shape) surfaces as a per-model error from BuildEntry instead of
// panicking and crashing the whole wt process on expose/unexpose/sync/start.
func TestBuildEntryUnencodableModelInfoReturnsError(t *testing.T) {
	cfg := testConfig()
	m := cfg.Models[2] // openrouter/x/y
	m.ModelInfo = map[string]any{"bad": make(chan int)}
	if _, err := BuildEntry(m, cfg.Providers[2]); err == nil {
		t.Fatal("want an error, got nil (and no panic)")
	}
}

// TestBuildEntrySecretRefResolvesExecForm pins that a SecretRef:true policy
// (currently only openrouter) resolves auth.secret_ref through
// config.ResolveSecret before writing config.yaml's api_key — including the
// exec: form. Writing the ref string verbatim (the pre-fix behavior) sends
// LiteLLM the literal string "exec:..." as a bearer token: a silent,
// confusing 401 with no error surfaced anywhere.
func TestBuildEntrySecretRefResolvesExecForm(t *testing.T) {
	script := filepath.Join(t.TempDir(), "key.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho sk-from-exec\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	p := cfg.Providers[2] // openrouter
	p.Auth.SecretRef = "exec:" + script

	node, err := BuildEntry(cfg.Models[2], p)
	if err != nil {
		t.Fatalf("BuildEntry: %v", err)
	}
	params := decode(t, node)["litellm_params"].(map[string]any)
	if params["api_key"] != "sk-from-exec" {
		t.Errorf("api_key = %v, want the exec: form resolved to %q", params["api_key"], "sk-from-exec")
	}
}

// TestBuildEntrySecretRefPropagatesResolveError pins that a failing
// secret_ref (e.g. a broken exec: helper) surfaces as a BuildEntry error
// instead of silently writing a bad api_key.
func TestBuildEntrySecretRefPropagatesResolveError(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fail.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'no vault token' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	p := cfg.Providers[2] // openrouter
	p.Auth.SecretRef = "exec:" + script

	if _, err := BuildEntry(cfg.Models[2], p); err == nil || !strings.Contains(err.Error(), "no vault token") {
		t.Fatalf("BuildEntry error = %v, want it to surface the helper's stderr", err)
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

// TestBuildEntryStampsMarker pins #179's ownership marker: every row wt
// builds carries model_info.wt_managed: true, which is what lets sync delete
// its own stale rows while never touching hand-written ones.
func TestBuildEntryStampsMarker(t *testing.T) {
	cfg := testConfig()
	for _, id := range []string{"ollama/gemma:9b", "openrouter/x/y"} {
		m := cfg.Models[config.IndexModelByID(cfg.Models, id)]
		n, err := BuildEntry(m, *cfg.ProviderByID(m.ProviderID))
		if err != nil {
			t.Fatal(err)
		}
		if !IsManaged(n) {
			t.Errorf("%s: row not marked wt_managed: %v", id, decode(t, n))
		}
	}
}

// TestBuildEntryMarkerWins pins that a registry model_info cannot disown a
// wt row: wt_managed: false in model_info is overridden, or sync would treat
// its own route as hand-written and strand it forever.
func TestBuildEntryMarkerWins(t *testing.T) {
	cfg := testConfig()
	m := cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/x/y")]
	m.ModelInfo = map[string]any{ManagedKey: false}
	n, err := BuildEntry(m, *cfg.ProviderByID(m.ProviderID))
	if err != nil {
		t.Fatal(err)
	}
	if !IsManaged(n) {
		t.Fatalf("model_info override disowned the row: %v", decode(t, n))
	}
}

// TestBuildEntrySecretRefEmptyIsAnError pins that a non-empty secret_ref
// resolving to "" (an env-name or os.environ/ ref whose variable is unset in
// this shell) is a BuildEntry error, not an `api_key: ""` row. Sync rebuilds
// every cloud row, so writing the empty key would replace every working key
// and fail every cloud route with an auth error.
func TestBuildEntrySecretRefEmptyIsAnError(t *testing.T) {
	t.Setenv("WT_TEST_UNSET_KEY", "")
	cfg := testConfig()
	for _, ref := range []string{"WT_TEST_UNSET_KEY", "os.environ/WT_TEST_UNSET_KEY"} {
		p := cfg.Providers[2] // openrouter
		p.Auth.SecretRef = ref
		if _, err := BuildEntry(cfg.Models[2], p); err == nil || !strings.Contains(err.Error(), "WT_TEST_UNSET_KEY") {
			t.Errorf("ref %q: BuildEntry error = %v, want one naming the empty ref", ref, err)
		}
	}
}
