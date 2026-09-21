package litellm

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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

// TestSyncDecidesUnderTheLock pins that Sync reads the current routes inside
// the lock: a route added by another process while Sync waits for the lock
// (a lifecycle hook exposing a just-started model) is seen and handled, not
// judged against a stale pre-lock read. The sleep only gives Sync time to
// reach the lock; a slow scheduler can make the test pass vacuously against
// the old code, never fail against the fixed code.
func TestSyncDecidesUnderTheLock(t *testing.T) {
	o, _, p := opts(t, "model_list: []\n")
	done := make(chan error, 1)
	err := WithLock(p, func() error {
		go func() {
			_, err := Sync(testConfig(), nil, o)
			done <- err
		}()
		time.Sleep(100 * time.Millisecond)
		// Another process exposes a local model while Sync waits.
		return os.WriteFile(p, []byte("model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 0 {
		t.Fatalf("routed = %v, want the not-running model's route removed", ids)
	}
}

// TestModelForToleratesSpellingDifferences pins that ModelFor resolves a
// provider-side name the way the live inventory does — ollama's implicit
// ":latest" tag and an org-prefixed omlx/mtplx spelling — so a started model
// whose name differs from the registry's still gets its LiteLLM route instead
// of a "not in the registry" warning. An exact match must win, and a name that
// fits more than one registry model resolves to none rather than the wrong one.
func TestModelForToleratesSpellingDifferences(t *testing.T) {
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/gemma", ProviderID: "ollama", ModelName: "gemma"},
		{ID: "ollama/qwen:latest", ProviderID: "ollama", ModelName: "qwen:latest"},
		{ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Org/Q35"},
		{ID: "mtplx/A--Q35", ProviderID: "mtplx", ModelName: "Other/Q35"},
		{ID: "mtplx/Y--Q27", ProviderID: "mtplx", ModelName: "Org/Q27"},
	}}
	cases := []struct{ provider, name, want string }{
		{"ollama", "gemma", "ollama/gemma"},          // exact
		{"ollama", "gemma:latest", "ollama/gemma"},   // target has the implicit tag
		{"ollama", "qwen", "ollama/qwen:latest"},     // registry has the tag
		{"mtplx", "Org/Q27", "mtplx/Y--Q27"},         // exact
		{"mtplx", "/models/Org/Q27", "mtplx/Y--Q27"}, // path-prefixed spelling
		{"mtplx", "Q35", ""},                         // fits two registry models: ambiguous
		{"ollama", "nope:1", ""},                     // unregistered
		{"omlx", "gemma", ""},                        // wrong provider
	}
	for _, c := range cases {
		m, ok := ModelFor(cfg, c.provider, c.name)
		if got := m.ID; (c.want == "") == ok || got != c.want {
			t.Errorf("ModelFor(%s, %q) = %q ok=%v, want %q", c.provider, c.name, got, ok, c.want)
		}
	}
}

// TestSyncRecheckKeepsModelsThatStartedMeanwhile pins the under-lock re-probe:
// a model that Sync's earlier probe saw stopped but that Recheck finds running
// (a start finished between the probe and the lock) keeps its route, while a
// model still stopped is removed. Without it, sync could delete the fresh
// route of a model that is running and leave it unreachable through LiteLLM.
func TestSyncRecheckKeepsModelsThatStartedMeanwhile(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
  - model_name: mtplx/Youssofal--Q
    litellm_params: {model: openai/Youssofal/Q}
`)
	o.Recheck = func() []string { return []string{"ollama/gemma:9b"} }
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "ollama/gemma:9b" {
		t.Fatalf("routed = %s, want only the model Recheck found running", got)
	}
}

// TestApplyRejectsEmptyModelName pins that a model with an empty model_name is
// rejected per id and no route is written: BuildEntry would otherwise emit a
// bogus "ollama/" row now that wt commands no longer refuse on registry
// validation errors. A FixedModel provider (llamacpp) ignores model_name, so
// an empty one there must keep working.
func TestApplyRejectsEmptyModelName(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n")
	o.SkipReadyGate = true
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "llamacpp", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8080"}},
		},
		Models: []config.Model{
			{ID: "ollama/blank", ProviderID: "ollama", Location: config.LocationLocal},
			// Whitespace-only names (spaces, tab, newline) are just as empty as ""
			// and would otherwise write a bogus 'ollama_chat/   ' route.
			{ID: "ollama/spaces", ProviderID: "ollama", ModelName: "   ", Location: config.LocationLocal},
			{ID: "ollama/tab", ProviderID: "ollama", ModelName: "\t", Location: config.LocationLocal},
			{ID: "ollama/nl", ProviderID: "ollama", ModelName: " \n ", Location: config.LocationLocal},
			{ID: "llamacpp/fixed", ProviderID: "llamacpp", Location: config.LocationLocal},
		},
	}
	res, err := Apply(cfg, []string{"ollama/blank", "ollama/spaces", "ollama/tab", "ollama/nl", "llamacpp/fixed"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 2, 3} {
		if res.Outcomes[i].Err == nil || !strings.Contains(res.Outcomes[i].Err.Error(), "empty model_name") {
			t.Fatalf("outcome %d = %+v, want empty model_name error", i, res.Outcomes[i])
		}
	}
	if res.Outcomes[4].Err != nil {
		t.Fatalf("fixed-model provider rejected: %v", res.Outcomes[4].Err)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 1 || ids[0] != "llamacpp/fixed" || *restarts != 1 {
		t.Fatalf("routed = %v restarts=%d", ids, *restarts)
	}
}
