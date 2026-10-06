package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// This file drives the PUBLIC wrappers (Start, Stop, StopModelDeferred +
// SettleRoutes) end to end against the REAL litellm.ApplyChange on a temp
// config.yaml, with fake provider backends and httptest provider servers so the
// real localmodels.Inventory runs. Everything else in the package tests
// routeAfterStart/routeAfterStop directly, which leaves the wiring — "does
// Start actually call the hook?" — unpinned: re-ordering Start to return before
// the hook, or dropping the hook from StopModelDeferred, would keep that suite
// green.

// wrapBackend is a provider backend that records its calls and can be told to
// fail, so a wrapper test can drive start/stop without a real provider.
type wrapBackend struct {
	single   bool
	pool     bool // Pool tenancy, as omlx has; overrides single
	startErr error
	stopErr  error
	calls    *[]string
}

func (b wrapBackend) tenancy() Tenancy {
	switch {
	case b.pool:
		return Pool
	case b.single:
		return Exclusive
	}
	return Shared
}

func (b wrapBackend) stop(context.Context, *env, *config.Config) error {
	*b.calls = append(*b.calls, "stop")
	return b.stopErr
}

func (b wrapBackend) stopModel(_ context.Context, _ *env, _ *config.Config, name string) error {
	*b.calls = append(*b.calls, "stopModel:"+name)
	return b.stopErr
}

func (b wrapBackend) start(_ context.Context, _ *env, _ *config.Config, t Target, _ func(Stage)) error {
	*b.calls = append(*b.calls, "start:"+t.ModelName)
	return b.startErr
}

// swapBackend installs b as family's backend for one test. defaultEnv clones
// backendsByFamily, and TenancyOf reads it directly, so this is the single
// switch that makes the public wrappers run against a fake provider.
func swapBackend(t *testing.T, family string, b backend) {
	t.Helper()
	old, had := backendsByFamily[family]
	backendsByFamily[family] = b
	t.Cleanup(func() {
		if had {
			backendsByFamily[family] = old
		} else {
			delete(backendsByFamily, family)
		}
	})
}

// realRoutes points the route hook at the REAL litellm.ApplyChange over a temp
// config.yaml and a harmless restart command (the package TestMain hard-fails
// applyRoutes by default). It returns the config path, a restart counter and
// the captured warning stream.
//
// The restart runs asynchronously now (see applyAndReport), so every test
// here calls WaitPendingRoutes after the wrapper it drives — standing in for
// the wt process, which does the same before it exits. config.yaml itself is
// still written synchronously, so routedIDs needs no wait.
func realRoutes(t *testing.T, yaml string) (cfgPath string, restarts func() int, warn *strings.Builder) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	marks := filepath.Join(dir, "restarts")
	t.Setenv("WT_LITELLM_CONFIG", cfgPath)
	t.Setenv("WT_LITELLM_RESTART_CMD", "printf x >> "+marks)
	var buf strings.Builder
	oa, ow, op, ww := applyRoutes, waitProxy, probeProxy, routesWarn
	applyRoutes = litellm.ApplyChange
	waitProxy = func(context.Context, string, time.Duration) error { return nil }
	probeProxy = func(context.Context, string, time.Duration) bool { return false }
	routesWarn = &buf
	t.Cleanup(func() { applyRoutes, waitProxy, probeProxy, routesWarn = oa, ow, op, ww })
	// Registered last, so it runs FIRST: settle any in-flight restart before
	// the seams, WT_LITELLM_* env and the temp dir are torn down under it.
	t.Cleanup(WaitPendingRoutes)
	return cfgPath, func() int {
		b, _ := os.ReadFile(marks)
		return len(b)
	}, &buf
}

// routedIDs reads the ids currently routed in the temp config.yaml.
func routedIDs(t *testing.T, path string) []string {
	t.Helper()
	f, err := litellm.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return f.RoutedIDs()
}

// ollamaSrv is a fake ollama daemon: pulled models on /api/tags, loaded ones
// on /api/ps, which is what localmodels.Inventory reads to decide Running.
func ollamaSrv(t *testing.T, pulled, loaded []string) string {
	t.Helper()
	body := func(names []string) map[string]any {
		ms := []map[string]any{}
		for _, n := range names {
			ms = append(ms, map[string]any{"name": n, "model": n})
		}
		return map[string]any{"models": ms}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body(pulled))
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body(loaded))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s.URL
}

// openaiSrv is a fake single-model provider answering /v1/models with ids.
func openaiSrv(t *testing.T, ids []string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		data := []map[string]any{}
		for _, id := range ids {
			data = append(data, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// wrapCfg is the registry the wrapper tests run against: one multi-tenant
// family (ollama) and one single-model family (mtplx) with two sibling models.
func wrapCfg(t *testing.T, ollamaURL, mtplxURL string) *config.Config {
	t.Helper()
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: ollamaURL}},
			{ID: "mtplx", Location: config.LocationLocal, ModelDir: t.TempDir(), Auth: config.AuthConfig{Type: "none", BaseURL: mtplxURL + "/v1"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Y/Q35", Location: config.LocationLocal},
			{ID: "mtplx/Y--Q27", ProviderID: "mtplx", ModelName: "Y/Q27", Location: config.LocationLocal},
		},
	}
}

// staleSiblingYAML routes only mtplx/Y--Q27 — the stale row the reported bug
// leaves behind after the provider was handed a different model.
const staleSiblingYAML = `model_list:
  - model_name: mtplx/Y--Q27
    litellm_params:
      model: openai/Y/Q27
      api_base: http://localhost:8003/v1
      api_key: not-needed
litellm_settings:
  drop_params: true
  use_chat_completions_url_for_anthropic_messages: true
`

// TestStartWrapperAddsRouteOnSuccess drives the real public Start with a fake
// backend and the real litellm.ApplyChange: a successful start must leave the
// model's row in config.yaml and bounce the proxy once. Without this, Start
// could return before its route hook and every routeAfterStart test would
// still pass while started models had no route — the bug this phase fixes.
func TestStartWrapperAddsRouteOnSuccess(t *testing.T) {
	path, restarts, warn := realRoutes(t, "model_list: []\n")
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))

	if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, Options{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	if !slices.Equal(calls, []string{"start:a:1"}) {
		t.Fatalf("backend calls = %v, want one start", calls)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"ollama/a:1"}) {
		t.Fatalf("routed = %v, want [ollama/a:1] (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d, want 1", restarts())
	}
}

// TestStartWrapperFailedStartLeavesConfigUntouched verifies a start that fails
// neither writes config.yaml nor restarts the proxy: routing a model that
// never loaded would send every agent request to a dead server.
func TestStartWrapperFailedStartLeavesConfigUntouched(t *testing.T) {
	path, restarts, _ := realRoutes(t, staleSiblingYAML)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls, startErr: errors.New("out of memory")})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))

	if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, Options{}); err == nil {
		t.Fatal("Start = nil, want the backend's failure")
	}
	WaitPendingRoutes()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("a failed start rewrote config.yaml:\n%s", after)
	}
	if restarts() != 0 {
		t.Errorf("restarts = %d, want 0 after a failed start", restarts())
	}
}

// TestStartWrapperAlreadyRunningStillFixesStaleRoutes pins the reported bug:
// mtplx is already serving Y/Q35 (so Start is a no-op on the provider) while
// config.yaml still carries only the stale sibling Y--Q27. Start must still
// route the model that is actually serving and drop the stale sibling, in one
// proxy restart — otherwise a `wt start` on an already-running model leaves
// clients with "Invalid model name".
func TestStartWrapperAlreadyRunningStillFixesStaleRoutes(t *testing.T) {
	path, restarts, warn := realRoutes(t, staleSiblingYAML)
	var calls []string
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, []string{"Y/Q35"}))

	if err := Start(context.Background(), cfg, Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, Options{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	if len(calls) != 0 {
		t.Fatalf("backend calls = %v, want none (the model is already running)", calls)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"mtplx/Y--Q35"}) {
		t.Fatalf("routed = %v, want only [mtplx/Y--Q35] (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d, want exactly 1", restarts())
	}
}

// TestStopRouteRemovalSymmetry pins stop's route effect through the real
// deferred stop and its settle: a single-model provider goes down as a whole,
// so every one of its models loses its route, while an ollama stop only
// unloads the model and leaves every route in place, owing no restart (a
// pulled ollama model is still served on request — #179) — and an unrelated
// cloud row is never touched. Removing too much would break models the user
// never stopped; too little leaves dead routes.
func TestStopRouteRemovalSymmetry(t *testing.T) {
	const full = `model_list:
  - model_name: ollama/a:1
    litellm_params: {model: ollama_chat/a:1, api_base: http://localhost:11434}
  - model_name: ollama/b:1
    litellm_params: {model: ollama_chat/b:1, api_base: http://localhost:11434}
  - model_name: mtplx/Y--Q35
    litellm_params: {model: openai/Y/Q35, api_base: http://localhost:8003/v1, api_key: not-needed}
  - model_name: mtplx/Y--Q27
    litellm_params: {model: openai/Y/Q27, api_base: http://localhost:8003/v1, api_key: not-needed}
  - model_name: openrouter/z
    litellm_params: {model: openrouter/z}
`
	path, restarts, warn := realRoutes(t, full)
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, nil))

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if owed, err := StopModelDeferred(context.Background(), cfg, localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/a:1", ModelName: "a:1"}); err != nil || owed {
		t.Fatalf("StopModelDeferred(ollama) = (%v, %v), want (false, nil): stop leaves an ollama route in place", owed, err)
	}
	WaitPendingRoutes()
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("ollama stop rewrote config.yaml:\n%s", after)
	}

	if owed, err := StopModelDeferred(context.Background(), cfg, localmodels.Entry{ProviderID: "mtplx", ModelID: "mtplx/Y--Q35", ModelName: "Y/Q35"}); err != nil || !owed {
		t.Fatalf("StopModelDeferred(mtplx) = (%v, %v), want (true, nil)", owed, err)
	}
	SettleRoutes(context.Background(), cfg)
	WaitPendingRoutes()
	want := []string{"ollama/a:1", "ollama/b:1", "openrouter/z"}
	if got := routedIDs(t, path); !slices.Equal(got, want) {
		t.Fatalf("after mtplx stop routed = %v, want %v (warn %q)", got, want, warn.String())
	}
	if !slices.Equal(calls, []string{"stopModel:a:1", "stopModel:Y/Q35"}) {
		t.Fatalf("backend calls = %v", calls)
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d, want 1 (only the mtplx stop owed one)", restarts())
	}
}

// TestStartWrapperIsIdempotent verifies repeating a start of an
// already-routed, already-running model rewrites nothing and bounces the proxy
// only once: every agent launch on a warm model would otherwise drop in-flight
// requests through a needless restart.
func TestStartWrapperIsIdempotent(t *testing.T) {
	path, restarts, warn := realRoutes(t, "model_list: []\n")
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, []string{"a:1"}), openaiSrv(t, nil))

	for i := range 3 {
		if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, Options{}); err != nil {
			t.Fatalf("Start #%d: %v", i+1, err)
		}
		WaitPendingRoutes() // settle each start's restart before the next one
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"ollama/a:1"}) {
		t.Fatalf("routed = %v (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d after 3 starts, want 1", restarts())
	}
}

// TestStartWrapperReplaceThenFailDropsOccupantRoute pins the replace-then-fail
// hole: Start stops the running occupant, the new model then fails to load,
// and the wrapper returns the error with no route hook. The occupant is dead,
// so its row must be gone — otherwise agents keep routing to a stopped model
// and `wt litellm sync` cannot repair it (a stopped provider probes partial
// and is deliberately left untouched).
func TestStartWrapperReplaceThenFailDropsOccupantRoute(t *testing.T) {
	path, _, warn := realRoutes(t, staleSiblingYAML) // routes mtplx/Y--Q27, the occupant
	var calls []string
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls, startErr: errors.New("out of memory")})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, []string{"Y/Q27"}))

	err := Start(context.Background(), cfg, Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, Options{AllowReplace: true})
	if err == nil {
		t.Fatal("Start = nil, want the backend's failure")
	}
	WaitPendingRoutes()
	if !slices.Equal(calls, []string{"stop", "start:Y/Q35"}) {
		t.Fatalf("backend calls = %v, want the occupant stopped then a failed start", calls)
	}
	if got := routedIDs(t, path); len(got) != 0 {
		t.Fatalf("routed = %v, want none: the occupant was stopped and the new model never started (warn %q)", got, warn.String())
	}
}

// TestStartWrapperReportsRoutingStage pins the progress hand-off: the engine's
// last stage fires before the route hook, so without a stage of its own every
// caller keeps rendering "warming the model" while wt rewrites config.yaml,
// bounces the proxy and waits for it — seconds of visibly stale progress. The
// stage must arrive BEFORE the route is written, and only on success.
func TestStartWrapperReportsRoutingStage(t *testing.T) {
	path, _, _ := realRoutes(t, "model_list: []\n")
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1", "b:1"}, nil), openaiSrv(t, nil))

	var stages []Stage
	routedAtStage := []string{"unset"}
	report := func(s Stage) {
		stages = append(stages, s)
		if s == StageRouting {
			routedAtStage = routedIDs(t, path)
		}
	}
	if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, Options{Progress: report}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	if len(stages) == 0 || stages[len(stages)-1] != StageRouting {
		t.Fatalf("stages = %v, want StageRouting last", stages)
	}
	if len(routedAtStage) != 0 {
		t.Errorf("routed = %v when StageRouting fired, want the stage reported before the write", routedAtStage)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"ollama/a:1"}) {
		t.Fatalf("routed = %v after Start", got)
	}

	stages = nil
	swapBackend(t, "ollama", wrapBackend{calls: &calls, startErr: errors.New("boom")})
	if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "b:1"}, Options{Progress: report}); err == nil {
		t.Fatal("Start = nil, want the backend's failure")
	}
	WaitPendingRoutes()
	if slices.Contains(stages, StageRouting) {
		t.Errorf("stages = %v, want no routing stage after a failed start", stages)
	}
}

// TestStartWrapperRestartSurvivesCancelledCaller is the end-to-end half of
// TestRouteRestartSurvivesCallerCancelAfterReturn: it drives the real public
// Start with the real litellm.ApplyChange AND the real RestartContext (counting
// actual restart-command executions, not stubbed ones) against a caller whose
// context is already cancelled.
//
// Since the restart became asynchronous, every caller's own scoped
// `defer cancel()` fires before it runs, so inheriting the caller's
// cancellation means the restart command never executes: config.yaml gets the
// new route and the running proxy never learns about it. This test fails with
// 0 restarts if the async phase stops detaching from the caller's ctx.
func TestStartWrapperRestartSurvivesCancelledCaller(t *testing.T) {
	path, restarts, warn := realRoutes(t, "model_list: []\n")
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"a:1"}, nil), openaiSrv(t, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller's own scoped defer cancel() has already fired
	if err := Start(ctx, cfg, Target{ProviderID: "ollama", ModelName: "a:1"}, Options{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	if got := routedIDs(t, path); !slices.Equal(got, []string{"ollama/a:1"}) {
		t.Fatalf("routed = %v, want [ollama/a:1] (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d, want 1: a cancelled caller must not cancel the async restart", restarts())
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning: %q", warn.String())
	}
}

// TestStopRemovesOmlx6bitSibling verifies `wt stop omlx` — the public Stop of
// the provider, through the real litellm.ApplyChange — drops the omlx-6bit
// variant's route too: one physical oMLX server serves both quantizations, so
// halting the service takes both down. Before omlx-6bit had a LiteLLM policy
// its rows were invisible to the family sweep and survived as dead routes.
//
// The fake backend has omlx's own tenancy (Pool), so the rule under test is
// the real one, routeRemoveFamily. A stop of one omlx MODEL is a different
// rule and removes that model's ids only
// (TestStopModelDeferredOnPoolKeepsSiblingRoutes).
func TestStopRemovesOmlx6bitSibling(t *testing.T) {
	const both = `model_list:
  - model_name: omlx/Four
    litellm_params: {model: openai/Four, api_base: http://localhost:8000/v1, api_key: not-needed}
  - model_name: omlx-6bit/Six
    litellm_params: {model: openai/Six, api_base: http://localhost:8000/v1, api_key: not-needed}
`
	path, restarts, warn := realRoutes(t, both)
	var calls []string
	swapBackend(t, "omlx", wrapBackend{pool: true, calls: &calls})
	srv := openaiSrv(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx", Location: config.LocationLocal, ModelDir: t.TempDir(), Auth: config.AuthConfig{Type: "none", BaseURL: srv + "/v1"}},
			{ID: "omlx-6bit", Location: config.LocationLocal, ModelDir: t.TempDir(), Auth: config.AuthConfig{Type: "none", BaseURL: srv + "/v1"}},
		},
		Models: []config.Model{
			{ID: "omlx/Four", ProviderID: "omlx", ModelName: "Four", Location: config.LocationLocal},
			{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
		},
	}

	if err := Stop(context.Background(), cfg, "omlx"); err != nil {
		t.Fatalf("Stop(omlx): %v", err)
	}
	WaitPendingRoutes()
	if !slices.Equal(calls, []string{"stop"}) {
		t.Fatalf("backend calls = %v, want one provider stop", calls)
	}
	if got := routedIDs(t, path); len(got) != 0 {
		t.Fatalf("routed = %v, want none: one oMLX server serves both variants (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d, want 1", restarts())
	}
}

// TestStartWrapperReplaceBouncesProxyOnce pins that a replace start (stop the
// occupant, start the new model) restarts the LiteLLM proxy exactly once: the
// occupant's route removal is written without a restart and the post-start
// route write settles it. Two bounces per replace doubled the proxy downtime
// and readiness wait, dropping in-flight requests twice.
func TestStartWrapperReplaceBouncesProxyOnce(t *testing.T) {
	path, restarts, warn := realRoutes(t, staleSiblingYAML) // routes mtplx/Y--Q27, the occupant
	var calls []string
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, []string{"Y/Q27"}))

	if err := Start(context.Background(), cfg, Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, Options{AllowReplace: true}); err != nil {
		t.Fatal(err)
	}
	WaitPendingRoutes()
	if got := routedIDs(t, path); !slices.Equal(got, []string{"mtplx/Y--Q35"}) {
		t.Fatalf("routed = %v, want only the new model (warn %q)", got, warn.String())
	}
	if restarts() != 1 {
		t.Fatalf("restarts = %d for one replace, want 1", restarts())
	}
}

// TestStartWrapperReplaceThenFailStillBouncesOnce pins the failure half: the
// occupant's route is dropped from config.yaml at stop time without a restart,
// so when the new model then fails to load Start must still bounce the proxy
// (once) — otherwise the proxy keeps serving the dead occupant's route until
// something else restarts it.
func TestStartWrapperReplaceThenFailStillBouncesOnce(t *testing.T) {
	_, restarts, _ := realRoutes(t, staleSiblingYAML)
	var calls []string
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls, startErr: errors.New("out of memory")})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, []string{"Y/Q27"}))

	if err := Start(context.Background(), cfg, Target{ProviderID: "mtplx", ModelName: "Y/Q35"}, Options{AllowReplace: true}); err == nil {
		t.Fatal("Start = nil, want the backend's failure")
	}
	WaitPendingRoutes()
	if restarts() != 1 {
		t.Fatalf("restarts = %d after replace-then-fail, want 1", restarts())
	}
}

// TestStopModelDeferredBatchRestartsOnce pins issue #142: a multi-select stop
// writes every route removal with StopModelDeferred (no restart each), then
// SettleRoutes bounces the proxy exactly once. One restart per stop meant N
// overlapping `launchctl kickstart -k`s, each killing the proxy the previous
// one had just brought up and dropping in-flight requests every time.
func TestStopModelDeferredBatchRestartsOnce(t *testing.T) {
	const full = `model_list:
  - model_name: ollama/a:1
    litellm_params: {model: ollama_chat/a:1, api_base: http://localhost:11434}
  - model_name: ollama/b:1
    litellm_params: {model: ollama_chat/b:1, api_base: http://localhost:11434}
  - model_name: mtplx/Y--Q35
    litellm_params: {model: openai/Y/Q35, api_base: http://localhost:8003/v1, api_key: not-needed}
  - model_name: openrouter/z
    litellm_params: {model: openrouter/z}
`
	path, restarts, warn := realRoutes(t, full)
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, nil))

	owed := false
	for _, s := range []struct{ provider, id, name string }{{"ollama", "ollama/a:1", "a:1"}, {"ollama", "ollama/b:1", "b:1"}, {"mtplx", "mtplx/Y--Q35", "Y/Q35"}} {
		o, err := StopModelDeferred(context.Background(), cfg, localmodels.Entry{ProviderID: s.provider, ModelID: s.id, ModelName: s.name})
		if err != nil {
			t.Fatalf("StopModelDeferred(%s %s): %v", s.provider, s.name, err)
		}
		owed = owed || o
	}
	WaitPendingRoutes()
	if !owed {
		t.Fatal("removals changed config.yaml, so a restart must be owed")
	}
	if restarts() != 0 {
		t.Fatalf("restarts before settle = %d, want 0", restarts())
	}
	// The ollama stops only unloaded their models; their routes stay (#179).
	if got, want := routedIDs(t, path), []string{"ollama/a:1", "ollama/b:1", "openrouter/z"}; !slices.Equal(got, want) {
		t.Fatalf("routed = %v, want %v (warn %q)", got, want, warn.String())
	}
	SettleRoutes(context.Background(), cfg)
	WaitPendingRoutes()
	if restarts() != 1 {
		t.Fatalf("restarts after settle = %d, want 1", restarts())
	}
}

// TestStopModelDeferredNothingRoutedOwesNothing pins that a stop whose model
// had no route leaves no restart owed, so the batch skips the bounce entirely
// — a stop of an unrouted model must not cost a proxy restart.
func TestStopModelDeferredNothingRoutedOwesNothing(t *testing.T) {
	_, restarts, _ := realRoutes(t, "model_list:\n  - model_name: openrouter/z\n    litellm_params: {model: openrouter/z}\n")
	var calls []string
	// mtplx, not ollama: an ollama stop never writes routes at all (#179), so
	// it could not exercise the "write changed nothing" path this pins.
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, nil))
	// The first write may still change the file: every write ensures
	// LiteLLM's launcher-required litellm_settings keys, which this minimal
	// fixture lacks. Only the second write sees a file in normal form.
	mtplxY := localmodels.Entry{ProviderID: "mtplx", ModelID: "mtplx/Y--Q35", ModelName: "Y/Q35"}
	if _, err := StopModelDeferred(context.Background(), cfg, mtplxY); err != nil {
		t.Fatalf("first StopModelDeferred: %v", err)
	}
	owed, err := StopModelDeferred(context.Background(), cfg, mtplxY)
	if err != nil || owed {
		t.Fatalf("StopModelDeferred = (%v, %v), want (false, nil)", owed, err)
	}
	WaitPendingRoutes()
	if restarts() != 0 {
		t.Fatalf("restarts = %d, want 0", restarts())
	}
}

// TestStopModelDeferredFailedStopWritesNothing pins that a failed provider
// stop leaves the route alone and owes no restart: the model is still
// serving, so removing its route would strand it.
func TestStopModelDeferredFailedStopWritesNothing(t *testing.T) {
	// mtplx, not ollama: an ollama stop keeps its route even on success (#179),
	// so only a single-model stop shows the failure path holding back a removal.
	const full = "model_list:\n  - model_name: mtplx/Y--Q35\n    litellm_params: {model: openai/Y/Q35, api_base: http://localhost:8003/v1, api_key: not-needed}\n"
	path, _, _ := realRoutes(t, full)
	var calls []string
	swapBackend(t, "mtplx", wrapBackend{single: true, calls: &calls, stopErr: errors.New("boom")})
	cfg := wrapCfg(t, ollamaSrv(t, nil, nil), openaiSrv(t, nil))
	owed, err := StopModelDeferred(context.Background(), cfg, localmodels.Entry{ProviderID: "mtplx", ModelID: "mtplx/Y--Q35", ModelName: "Y/Q35"})
	if err == nil || owed {
		t.Fatalf("StopModelDeferred = (%v, %v), want (false, error)", owed, err)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"mtplx/Y--Q35"}) {
		t.Fatalf("routed = %v, want the route kept", got)
	}
}

// TestStopRemovesDiscoveredFamilyRoutesKeepsHandWritten drives a provider stop
// through the real litellm.ApplyChange (#179 Phase B): `wt stop omlx-6bit`
// halts the one oMLX server, so every marked route of the omlx family goes —
// the 6-bit model's own row and a discovered sibling's "omlx/<artifact>" row
// the registry never named — while an unmarked hand-written row that merely
// shares the family prefix survives, as do other families' rows.
//
// It is a provider stop, with a fake backend of omlx's own tenancy (Pool): the
// family sweep is what halting the service does. Stopping one omlx model
// unloads that model and leaves its siblings' routes alone.
func TestStopRemovesDiscoveredFamilyRoutesKeepsHandWritten(t *testing.T) {
	const rows = `model_list:
  - model_name: omlx-6bit/Six
    litellm_params: {model: openai/Six, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: omlx/stray-model
    litellm_params: {model: openai/stray-model, api_base: http://localhost:8000/v1, api_key: not-needed}
    model_info: {wt_managed: true}
  - model_name: omlx/my-hand-row
    litellm_params: {model: openai/my-hand-row, api_base: http://localhost:8000/v1, api_key: not-needed}
  - model_name: mtplx/org/other
    litellm_params: {model: openai/org/other, api_base: http://localhost:8003/v1, api_key: not-needed}
    model_info: {wt_managed: true}
`
	path, _, warn := realRoutes(t, rows)
	var calls []string
	swapBackend(t, "omlx", wrapBackend{pool: true, calls: &calls})
	srv := openaiSrv(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "omlx-6bit", Location: config.LocationLocal, ModelDir: t.TempDir(), Auth: config.AuthConfig{Type: "none", BaseURL: srv + "/v1"}},
		},
		Models: []config.Model{
			{ID: "omlx-6bit/Six", ProviderID: "omlx-6bit", ModelName: "Six", Location: config.LocationLocal},
		},
	}

	if err := Stop(context.Background(), cfg, "omlx-6bit"); err != nil {
		t.Fatalf("Stop(omlx-6bit): %v", err)
	}
	WaitPendingRoutes()
	if !slices.Equal(calls, []string{"stop"}) {
		t.Fatalf("backend calls = %v, want one provider stop", calls)
	}
	if got := routedIDs(t, path); !slices.Equal(got, []string{"omlx/my-hand-row", "mtplx/org/other"}) {
		t.Fatalf("routed = %v, want only the hand-written omlx row and the mtplx row (warn %q)", got, warn.String())
	}
}

// TestStartWrapperRoutesDiscoveredModel drives the real public Start for a
// pulled ollama model with no registry overlay through the real
// litellm.ApplyChange: the route lands under its discovered id, marked as
// wt's, so a LiteLLM-forced agent can reach it (#179 Phase B). Before, the
// hook warned "not in the registry" and wrote nothing.
func TestStartWrapperRoutesDiscoveredModel(t *testing.T) {
	path, _, warn := realRoutes(t, "model_list: []\n")
	var calls []string
	swapBackend(t, "ollama", wrapBackend{calls: &calls})
	cfg := wrapCfg(t, ollamaSrv(t, []string{"llama3.2:3b"}, nil), openaiSrv(t, nil))

	if err := Start(context.Background(), cfg, Target{ProviderID: "ollama", ModelName: "llama3.2:3b"}, Options{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	WaitPendingRoutes()
	f, err := litellm.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Rows(); !slices.Equal(got, []litellm.RowInfo{{ID: "ollama/llama3.2:3b", Managed: true}}) {
		t.Fatalf("rows = %v, want the discovered id, marked (warn %q)", got, warn.String())
	}
}
