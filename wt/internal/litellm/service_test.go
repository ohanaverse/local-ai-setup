package litellm

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// opts returns Options with a counting fake restart so no test can bounce a
// real proxy, and the config path pointed at a temp file.
func opts(t *testing.T, body string) (Options, *int, string) {
	t.Helper()
	p := writeConfig(t, body)
	n := 0
	return Options{Path: p, Restart: func() []string { n++; return nil }}, &n, p
}

// TestApplyExposeWritesRowAndRestartsOnce covers the happy path: exposing two
// models writes both rows in one save and restarts the proxy exactly once.
// The single restart matters — each one kills in-flight agent requests.
func TestApplyExposeWritesRowAndRestartsOnce(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n")
	o.SkipReadyGate = true // gemma is not ready in testConfig; this test is about the write, not the gate
	res, err := Apply(testConfig(), []string{"ollama/gemma:9b", "openrouter/x/y"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d, want true/1", res.Changed, *restarts)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 2 {
		t.Fatalf("routed = %v", ids)
	}
}

// TestApplyIsIdempotent pins that re-exposing an identical row writes nothing
// and does not restart the proxy.
func TestApplyIsIdempotent(t *testing.T) {
	o, restarts, _ := opts(t, "model_list: []\n")
	o.SkipReadyGate = true // otherwise the row is rejected and the test passes vacuously
	if _, err := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, o); err != nil {
		t.Fatal(err)
	}
	*restarts = 0
	res, err := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || *restarts != 0 {
		t.Fatalf("idempotent re-expose: changed=%v restarts=%d", res.Changed, *restarts)
	}
}

// TestApplyGateRejections pins the expose gate: unknown model, native
// provider and a not-ready local model are each reported per id without
// blocking the valid ids in the same batch, and SkipReadyGate lifts only the
// ready check.
func TestApplyGateRejections(t *testing.T) {
	cfg := testConfig()
	o, _, _ := opts(t, "model_list: []\n")
	res, err := Apply(cfg, []string{"nope/x", "claude/sonnet", "ollama/gemma:9b", "openrouter/x/y"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	errs := map[string]string{}
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			errs[oc.ID] = oc.Err.Error()
		}
	}
	if !strings.Contains(errs["nope/x"], "not found") || !strings.Contains(errs["claude/sonnet"], "native") || !strings.Contains(errs["ollama/gemma:9b"], "not ready") {
		t.Fatalf("errs = %v", errs)
	}
	if _, bad := errs["openrouter/x/y"]; bad {
		t.Fatal("cloud model must be exempt from the ready gate")
	}

	cfg.SetExposureForTest("ollama/gemma:9b", config.ExposureEntry{Ready: true})
	o2, _, _ := opts(t, "model_list: []\n")
	if r, _ := Apply(cfg, []string{"ollama/gemma:9b"}, nil, o2); r.Outcomes[0].Err != nil {
		t.Fatalf("ready model rejected: %v", r.Outcomes[0].Err)
	}
	o3, _, _ := opts(t, "model_list: []\n")
	o3.SkipReadyGate = true
	if r, _ := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, o3); r.Outcomes[0].Err != nil {
		t.Fatalf("SkipReadyGate still rejected: %v", r.Outcomes[0].Err)
	}
}

// TestApplyUnexposeRemovesRow covers removal and that removing a stale row
// (present in the file, unknown to the registry) still works — the exact
// shape of the stale Qwen3.8-27B route that motivated this work.
func TestApplyUnexposeRemovesRow(t *testing.T) {
	o, restarts, p := opts(t, baseConfig)
	res, err := Apply(testConfig(), nil, []string{"keep/me"}, o)
	if err != nil || !res.Changed || *restarts != 1 {
		t.Fatalf("err=%v changed=%v restarts=%d", err, res.Changed, *restarts)
	}
	f, _ := Open(p)
	for _, id := range f.RoutedIDs() {
		if id == "keep/me" {
			t.Fatal("row not removed")
		}
	}
}

// TestApplyFileLevelFailures pins that a missing or invalid config.yaml
// fails the whole call with a typed error and changes nothing.
func TestApplyFileLevelFailures(t *testing.T) {
	_, err := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, Options{Path: t.TempDir() + "/none.yaml", SkipReadyGate: true})
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v, want ErrMissing", err)
	}
	p := writeConfig(t, "model_list: nope\n")
	_, err = Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, Options{Path: p, SkipReadyGate: true})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "model_list: nope\n" {
		t.Fatal("invalid config was overwritten")
	}
}

// TestSyncReconcilesLocalRoutes pins reconciliation: running local models get
// a route, stopped local models lose theirs, cloud rows and unrelated rows are
// untouched. This is what repairs the stale-route drift.
func TestSyncReconcilesLocalRoutes(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
  - model_name: keep/me
    litellm_params: {model: openai/gpt-4o}
`)
	res, err := Sync(testConfig(), []string{"mtplx/Youssofal--Q"}, o)
	if err != nil || !res.Changed {
		t.Fatalf("err=%v changed=%v", err, res.Changed)
	}
	f, _ := Open(p)
	got := strings.Join(f.RoutedIDs(), ",")
	if got != "openrouter/x/y,keep/me,mtplx/Youssofal--Q" {
		t.Fatalf("routed = %s", got)
	}
}

// TestCheckIsSideEffectFree pins --dry-run: Check reports per-id validity and
// never touches the file.
func TestCheckIsSideEffectFree(t *testing.T) {
	out := Check(testConfig(), []string{"claude/sonnet", "openrouter/x/y"}, false)
	if out[0].Err == nil || out[1].Err != nil {
		t.Fatalf("outcomes = %+v", out)
	}
}

// TestModelForAndLocalModels pins the lookup helpers the lifecycle hook uses:
// resolve a registry model by (provider, provider-side name), and list only
// non-native local models with a LiteLLM mapping.
func TestModelForAndLocalModels(t *testing.T) {
	cfg := testConfig()
	if m, ok := ModelFor(cfg, "mtplx", "Youssofal/Q"); !ok || m.ID != "mtplx/Youssofal--Q" {
		t.Fatalf("ModelFor = %v %v", m, ok)
	}
	if _, ok := ModelFor(cfg, "mtplx", "other"); ok {
		t.Fatal("ModelFor matched an unregistered name")
	}
	var ids []string
	for _, m := range LocalModels(cfg) {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "ollama/gemma:9b,mtplx/Youssofal--Q" {
		t.Fatalf("LocalModels = %v", ids)
	}
	if p := Providers(); !p["openrouter"] || p["ollama"] {
		t.Fatalf("Providers = %v (openrouter must be cloud, ollama local)", p)
	}
}

// TestSyncRestartsOnceAndSkipsReadyGate pins that a Sync that changes routes
// restarts the proxy exactly once, and that a running local model that is not
// ready in the registry still gets its route (Sync passes SkipReadyGate: the
// model is verifiably running). Each restart kills in-flight agent requests.
func TestSyncRestartsOnceAndSkipsReadyGate(t *testing.T) {
	o, restarts, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	if testConfig().ReadyFlag("mtplx/Youssofal--Q") {
		t.Fatal("precondition: mtplx model must be not-ready")
	}
	res, err := Sync(testConfig(), []string{"mtplx/Youssofal--Q"}, o)
	if err != nil || !res.Changed || *restarts != 1 {
		t.Fatalf("err=%v changed=%v restarts=%d, want nil/true/1", err, res.Changed, *restarts)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "mtplx/Youssofal--Q" {
		t.Fatalf("routed = %s", got)
	}
}

// TestSyncLeavesUntouchedModelsAlone pins that ids in Options.Untouched are
// neither removed (existing route survives while not "running") nor added
// (running but untouched gets no new route). The caller uses this for
// families whose provider probe failed, where Running cannot be trusted.
func TestSyncLeavesUntouchedModelsAlone(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b, additional_drop_params: [reasoning_effort]}
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`)
	o.Untouched = []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"}
	res, err := Sync(testConfig(), []string{"mtplx/Youssofal--Q"}, o)
	if err != nil || res.Changed {
		t.Fatalf("err=%v changed=%v, want nothing changed", err, res.Changed)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "ollama/gemma:9b" {
		t.Fatalf("routed = %s", got)
	}
}
