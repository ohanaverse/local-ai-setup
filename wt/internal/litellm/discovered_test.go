package litellm

import (
	"os"
	"slices"
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

// TestApplyChangeRemovesFamilyMarkedRows pins RemoveFamilies (#179 Phase B):
// a single-model start or stop removes every marked row of the family —
// discovered siblings the registry never named included — plus the family's
// registry ids even when their row predates the marker, but never an
// unmarked row that is not a registry id, another family's row, a cloud row,
// or the id being added. One write, one restart.
func TestApplyChangeRemovesFamilyMarkedRows(t *testing.T) {
	o, restarts, p := opts(t, `model_list:
  - model_name: omlx/disc-a
    litellm_params: {model: openai/disc-a}
    model_info: {wt_managed: true}
  - model_name: omlx/disc-b
    litellm_params: {model: openai/disc-b}
    model_info: {wt_managed: true}
  - model_name: omlx-6bit/Six
    litellm_params: {model: openai/Six}
  - model_name: omlx/hand
    litellm_params: {model: openai/hand}
  - model_name: mtplx/Youssofal--Q
    litellm_params: {model: openai/Youssofal/Q}
    model_info: {wt_managed: true}
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
    model_info: {wt_managed: true}
`)
	ch := Change{Add: []config.Model{DiscoveredModel("omlx", "disc-a")}, RemoveFamilies: []string{"omlx"}}
	res, err := ApplyChange(discoveredCfg(), ch, o)
	if err != nil {
		t.Fatal(err)
	}
	want := []RowInfo{{"omlx/disc-a", true}, {"omlx/hand", false}, {"mtplx/Youssofal--Q", true}, {"openrouter/x/y", true}}
	if got := readRows(t, p); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d, want true/1", res.Changed, *restarts)
	}
}

// handWrittenLlama is the reference host's hand-written ollama row whose
// name is exactly the discovered id of the pulled llama3.2:3b, carrying a
// hand-set timeout, in a file every earlier wt write already normalised
// (litellm_settings and the ollama drop params), so any change is the row.
const handWrittenLlama = `model_list:
  - model_name: ollama/llama3.2:3b
    litellm_params: {model: ollama_chat/llama3.2:3b, timeout: 600, additional_drop_params: [reasoning_effort]}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestApplyChangeNeverReplacesHandWrittenDiscoveredName pins the Phase B
// safety rule on the start path: a discovered model whose id already names a
// hand-written (unmarked) row is not written — no add, no adoption, no
// rewrite, no restart — so the user's row (and its timeout) keeps serving the
// name. A registry id keeps Phase A adoption: its unmarked row is wt's.
func TestApplyChangeNeverReplacesHandWrittenDiscoveredName(t *testing.T) {
	o, restarts, p := opts(t, handWrittenLlama)
	res, err := ApplyChange(discoveredCfg(), Change{Add: []config.Model{DiscoveredModel("ollama", "llama3.2:3b")}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != handWrittenLlama || res.Changed || *restarts != 0 {
		t.Fatalf("hand-written row touched: changed=%v restarts=%d\n%s", res.Changed, *restarts, b)
	}

	o2, _, p2 := opts(t, "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n")
	gemma := discoveredCfg().Models[:1] // ollama/gemma:9b, a registry id
	if _, err := ApplyChange(discoveredCfg(), Change{Add: gemma}, o2); err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p2); !slices.Equal(got, []RowInfo{{"ollama/gemma:9b", true}}) {
		t.Fatalf("registry row = %v, want it adopted (marked)", got)
	}
}

// TestApplyChangeFamilyClearKeepsHandWrittenRow pins the same safety rule on
// the stop path: clearing a family (and naming the id in Remove) while a
// hand-written row carries a discovered id of that family leaves the file
// byte-identical — the row is unmarked and not a registry id, so it is not
// wt's to remove — and when a marked row shares the name, only the marked
// one goes. Deleting it would silently drop a route the user wrote by hand.
func TestApplyChangeFamilyClearKeepsHandWrittenRow(t *testing.T) {
	o, restarts, p := opts(t, handWrittenLlama)
	ch := Change{Remove: []string{"ollama/llama3.2:3b"}, RemoveFamilies: []string{"ollama"}}
	res, err := ApplyChange(discoveredCfg(), ch, o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != handWrittenLlama || res.Changed || *restarts != 0 {
		t.Fatalf("hand-written row touched: changed=%v restarts=%d\n%s", res.Changed, *restarts, b)
	}

	o2, _, p2 := opts(t, `model_list:
  - model_name: ollama/llama3.2:3b
    litellm_params: {model: ollama_chat/llama3.2:3b, timeout: 600}
  - model_name: ollama/llama3.2:3b
    litellm_params: {model: ollama_chat/llama3.2:3b}
    model_info: {wt_managed: true}
`)
	if _, err := ApplyChange(discoveredCfg(), Change{RemoveFamilies: []string{"ollama"}}, o2); err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p2); !slices.Equal(got, []RowInfo{{"ollama/llama3.2:3b", false}}) {
		t.Fatalf("rows = %v, want only the hand-written row left", got)
	}
}

// TestPrepareModelRejectsFamilylessDiscoveredID pins that a discovered model
// for a provider with a LiteLLM policy but no probe family (retired llamacpp)
// — whose id comes out as "/<artifact>" — is rejected instead of built: such
// a row has no family, so no stop or family clear could ever remove it.
// ApplyChange reports it per model and writes nothing.
func TestPrepareModelRejectsFamilylessDiscoveredID(t *testing.T) {
	cfg := discoveredCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "llamacpp", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8080"}})
	m := DiscoveredModel("llamacpp", "local-model")
	if m.ID != "/local-model" {
		t.Fatalf("id = %q, want the family-less /local-model this test guards", m.ID)
	}
	for _, bad := range []config.Model{m, {ProviderID: "omlx", ModelName: "x"}} {
		if node, err := prepareModel(cfg, bad); err == nil || node != nil {
			t.Errorf("prepareModel(%q) = %v, %v; want a rejection", bad.ID, node, err)
		}
	}
	const body = "model_list: []\nlitellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	o, restarts, p := opts(t, body)
	res, err := ApplyChange(cfg, Change{Add: []config.Model{m}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Err == nil || res.Changed || *restarts != 0 {
		t.Fatalf("res = %+v restarts=%d, want one error outcome and no write", res, *restarts)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatalf("config.yaml changed:\n%s", b)
	}
}

// TestApplyPlannedNeverWritesNilRow pins the write path's guard: a planned
// add with neither a row nor an error is reported as that id's error and
// skipped, never handed to SetRow — a nil node in model_list would corrupt
// config.yaml (or panic on save) for every route, not just this one. The
// other add in the same plan is still written.
func TestApplyPlannedNeverWritesNilRow(t *testing.T) {
	cfg := testConfig()
	o, restarts, p := opts(t, "model_list: []\n")
	good, err := prepareModel(cfg, cfg.Models[0])
	if err != nil {
		t.Fatal(err)
	}
	res, err := applyPlanned(cfg, func(*File) ([]plannedAdd, []plannedRemove) {
		return []plannedAdd{{id: "omlx/no-row"}, {id: cfg.Models[0].ID, row: good}}, nil
	}, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 2 || res.Outcomes[0].ID != "omlx/no-row" || res.Outcomes[0].Err == nil || res.Outcomes[0].Action != "" {
		t.Fatalf("outcomes = %+v, want the row-less add reported as an error", res.Outcomes)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"ollama/gemma:9b", true}}) || *restarts != 1 {
		t.Fatalf("rows = %v restarts=%d, want only the built row written", got, *restarts)
	}
}
