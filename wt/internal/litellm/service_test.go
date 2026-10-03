package litellm

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
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

// TestApplyRoutesRowsAndRestartsOnce covers the happy path: routing two
// models writes both rows in one save, reports each as "routed", and
// restarts the proxy exactly once. The single restart matters — each one
// kills in-flight agent requests.
func TestApplyRoutesRowsAndRestartsOnce(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n")
	o.SkipReadyGate = true // gemma is not ready in testConfig; this test is about the write, not the gate
	res, err := Apply(testConfig(), []string{"ollama/gemma:9b", "openrouter/x/y"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d, want true/1", res.Changed, *restarts)
	}
	for _, oc := range res.Outcomes {
		if oc.Err != nil || oc.Action != "routed" {
			t.Errorf("outcome %+v, want action routed", oc)
		}
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 2 {
		t.Fatalf("routed = %v", ids)
	}
}

// TestApplyIsIdempotent pins that re-routing an identical row writes nothing
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
		t.Fatalf("idempotent re-route: changed=%v restarts=%d", res.Changed, *restarts)
	}
}

// TestApplyGateRejections pins the route gate: unknown model, native
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
	if !strings.Contains(errs["nope/x"], "not found") || !strings.Contains(errs["claude/sonnet"], "native and is not routed") || !strings.Contains(errs["ollama/gemma:9b"], "not ready") {
		t.Fatalf("errs = %v", errs)
	}
	if _, bad := errs["openrouter/x/y"]; bad {
		t.Fatal("cloud model must be exempt from the ready gate")
	}

	cfg.SetReadyForTest("ollama/gemma:9b", true)
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

// TestApplyRemovesRow covers removal and that removing a stale row
// (present in the file, unknown to the registry) still works — the exact
// shape of the stale Qwen3.8-27B route that motivated this work. The
// removal is reported as "unrouted".
func TestApplyRemovesRow(t *testing.T) {
	o, restarts, p := opts(t, baseConfig)
	res, err := Apply(testConfig(), nil, []string{"keep/me"}, o)
	if err != nil || !res.Changed || *restarts != 1 {
		t.Fatalf("err=%v changed=%v restarts=%d", err, res.Changed, *restarts)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Action != "unrouted" {
		t.Fatalf("outcomes = %+v, want one unrouted", res.Outcomes)
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
// a route and stopped local models lose theirs, while a row wt does not own
// (keep/me here) is left alone. That is what repairs the stale-route drift.
// The fixture's unmarked cloud row (openrouter/x/y) is not left untouched: the
// reconciling sync adopts and rewrites it, which is why it stays in place.
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
// Since #179 the configured cloud model is also routed, in the same pass.
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
	if got := strings.Join(f.RoutedIDs(), ","); got != "openrouter/x/y,mtplx/Youssofal--Q" {
		t.Fatalf("routed = %s", got)
	}
}

// TestSyncLeavesUntouchedModelsAlone pins that ids in Options.Untouched are
// neither removed (existing route survives while not "running") nor added
// (running but untouched gets no new route). The caller uses this for
// families whose provider probe failed, where Running cannot be trusted.
// The configured cloud model, not being local, is still routed — Untouched
// covers only the local half.
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
	if err != nil || !res.Changed {
		t.Fatalf("err=%v changed=%v, want the cloud model routed", err, res.Changed)
	}
	f, _ := Open(p)
	got := f.RoutedIDs()
	if !slices.Contains(got, "ollama/gemma:9b") {
		t.Fatalf("routed = %v, untouched gemma:9b was removed", got)
	}
	if slices.Contains(got, "mtplx/Youssofal--Q") {
		t.Fatalf("routed = %v, untouched mtplx got a new route", got)
	}
}

// TestSyncDecidesUnderTheLock pins that Sync reads the current routes inside
// the lock: a route added by another process while Sync waits for the lock
// (a lifecycle hook routing a just-started model) is seen and handled, not
// judged against a stale pre-lock read. The sleep only gives Sync time to
// reach the lock; a slow scheduler can make the test pass vacuously against
// the old code, never fail against the fixed code.
func TestSyncDecidesUnderTheLock(t *testing.T) {
	o, _, p := opts(t, "model_list: []\n")
	done := make(chan error, 1)
	err := WithLock(context.Background(), p, func() error {
		go func() {
			_, err := Sync(testConfig(), nil, o)
			done <- err
		}()
		time.Sleep(100 * time.Millisecond)
		// Another process routes a local model while Sync waits.
		return os.WriteFile(p, []byte("model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b}\n"), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "openrouter/x/y" {
		t.Fatalf("routed = %s, want the not-running model's route removed and the cloud route added", got)
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
	if got := strings.Join(f.RoutedIDs(), ","); got != "ollama/gemma:9b,openrouter/x/y" {
		t.Fatalf("routed = %s, want the model Recheck found running (plus the cloud route)", got)
	}
}

// TestSyncRecheckAlsoAppliesToAddSet pins the other half of the under-lock
// re-probe: a model the outer probe saw running but that stopped before the
// lock was acquired must not get a fresh route added for a backend that is
// no longer there. Only Recheck's remove-side filtering was covered before;
// this pins that add is filtered too.
func TestSyncRecheckAlsoAppliesToAddSet(t *testing.T) {
	o, _, p := opts(t, "model_list: []\n")
	// Outer probe says both are running; Recheck (under the lock) finds only
	// one still running.
	o.Recheck = func() []string { return []string{"ollama/gemma:9b"} }
	if _, err := Sync(testConfig(), []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"}, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "openrouter/x/y,ollama/gemma:9b" {
		t.Fatalf("routed = %s, want the model Recheck still found running (plus the cloud route)", got)
	}
}

// isAdoptSubsetOfAdd reports whether every Adopt id is also in Add — the
// invariant SyncPlan's doc comment states.
func isAdoptSubsetOfAdd(p SyncPlan) bool {
	return !slices.ContainsFunc(p.Adopt, func(id string) bool { return !slices.Contains(p.Add, id) })
}

// TestPlanSyncAdoptStaysSubsetOfAddWhenRecheckPrunes pins SyncPlan's documented
// invariant — Adopt is the subset of Add that replaces an unmarked row — when
// Recheck prunes an id from Add. The under-lock Recheck is a Sync concept
// (PlanSync forces it off, see TestPlanSyncIgnoresRecheck), so the invariant is
// exercised against planSync directly, the function both callers share. Both
// lists are part of the exported plan, and Sync relabels an added id "adopted"
// by looking it up in Adopt, so an id left in Adopt after being pruned from Add
// describes a plan wt does not carry out.
func TestPlanSyncAdoptStaysSubsetOfAddWhenRecheckPrunes(t *testing.T) {
	f, err := Open(writeConfig(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`))
	if err != nil {
		t.Fatal(err)
	}
	const id = "ollama/gemma:9b"
	// The outer probe saw the local model running, so its unmarked row is an
	// adopt; Recheck, under the lock, finds nothing running, so the add side
	// drops it.
	o := Options{Recheck: func() []string { return nil }}
	plan, _ := planSync(testConfig(), f, []string{id}, o)
	if slices.Contains(plan.Add, id) || slices.Contains(plan.Adopt, id) {
		t.Fatalf("plan = %+v, want the pruned id gone from both Add and Adopt", plan)
	}
	if !isAdoptSubsetOfAdd(plan) {
		t.Fatalf("plan = %+v, want every Adopt id also in Add", plan)
	}
}

// TestPlanSyncIgnoresRecheck pins that the exported dry run never runs
// Recheck: PlanSync holds no config.yaml lock (Recheck is Sync's under-the-lock
// re-verification) and must report exactly the plan built from the caller's
// original probe. The probe here reports the model running but Recheck would
// find nothing running — a PlanSync that honored Recheck would both call it
// (counted) and wrongly prune the add.
func TestPlanSyncIgnoresRecheck(t *testing.T) {
	o, _, _ := opts(t, "model_list: []\n")
	called := 0
	o.Recheck = func() []string { called++; return nil }
	const id = "ollama/gemma:9b"
	plan, err := PlanSync(testConfig(), []string{id}, o)
	if err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("PlanSync ran Recheck %d time(s)", called)
	}
	if !slices.Contains(plan.Add, id) {
		t.Fatalf("plan = %+v, want the running model's add kept (Recheck is not PlanSync's to run)", plan)
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

// TestSyncRecheckTimesOutInsteadOfHoldingTheLock pins that a slow/hung
// Recheck probe cannot hold the config.yaml lock indefinitely: past
// recheckTimeout, Sync gives up on the recheck and applies the pre-recheck
// plan, the same as if Recheck were unset. Without this a hung provider
// probe could starve a concurrent bounded-context caller (the lifecycle
// route hook's settling bounce) of the lock.
func TestSyncRecheckTimesOutInsteadOfHoldingTheLock(t *testing.T) {
	old := recheckTimeout
	recheckTimeout = 30 * time.Millisecond
	t.Cleanup(func() { recheckTimeout = old })

	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	block := make(chan []string) // never sent to: simulates a hung probe
	o.Recheck = func() []string { return <-block }

	start := time.Now()
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sync took %s, want it to give up around recheckTimeout (%s)", elapsed, recheckTimeout)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "openrouter/x/y" {
		t.Fatalf("routed = %s, want the local route removed (recheck timed out, pre-recheck plan applied)", got)
	}
}

// readRows opens path and returns its rows (id + managed).
func readRows(t *testing.T, p string) []RowInfo {
	t.Helper()
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return f.Rows()
}

// TestSyncRoutesEveryCloudModel pins #179's core rule: configured means
// exposed, so sync adds a marked route for every registry cloud model with no
// opt-in step, and skips native models.
func TestSyncRoutesEveryCloudModel(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n")
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v", got)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d", res.Changed, *restarts)
	}
}

// TestSyncSkipsNativeModelOnMappedProvider pins routeableModels' native-model
// skip on its own. testConfig's native model sits on a native provider that
// PolicyFor also leaves unmapped, so that gate alone would hide it; here the
// native models sit on providers that DO have a LiteLLM mapping (openrouter
// cloud, ollama local, the latter even reported running), so only the
// m.Native check keeps them out of CloudModels and out of config.yaml. A
// native model talks to its agent directly; a LiteLLM route for it would
// advertise a model the proxy cannot serve.
func TestSyncSkipsNativeModelOnMappedProvider(t *testing.T) {
	cfg := testConfig()
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/native", ProviderID: "openrouter", ModelName: "native", Location: config.LocationCloud, Native: true},
		config.Model{ID: "ollama/native", ProviderID: "ollama", ModelName: "native", Location: config.LocationLocal, Native: true},
	)
	for _, m := range CloudModels(cfg) {
		if m.Native {
			t.Errorf("CloudModels includes native model %q", m.ID)
		}
	}
	for _, m := range LocalModels(cfg) {
		if m.Native {
			t.Errorf("LocalModels includes native model %q", m.ID)
		}
	}
	o, _, p := opts(t, "model_list: []\n")
	if _, err := Sync(cfg, []string{"ollama/native"}, o); err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v, want only the non-native cloud model", got)
	}
}

// TestSyncRemovesMarkedRowOfDeletedModel pins that deleting a cloud model
// from the registry removes its route: the marked row is wt's and is no
// longer desired.
func TestSyncRemovesMarkedRowOfDeletedModel(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: openrouter/gone
    litellm_params: {model: openrouter/gone}
    model_info: {wt_managed: true}
`)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	for _, r := range readRows(t, p) {
		if r.ID == "openrouter/gone" {
			t.Fatalf("stale marked row survived: %v", readRows(t, p))
		}
	}
}

// TestSyncNeverTouchesHandWrittenRows pins the safety rule: an unmarked row
// whose name is not a registry id wt manages — even one shaped like a
// registry id — is never removed or rewritten.
func TestSyncNeverTouchesHandWrittenRows(t *testing.T) {
	const hand = `  - model_name: openrouter/deleted-before-upgrade
    litellm_params: {model: openrouter/deleted-before-upgrade}
  - model_name: my-alias
    litellm_params: {model: openrouter/x/y}
`
	o, _, p := opts(t, "model_list:\n"+hand)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	got := readRows(t, p)
	for _, id := range []string{"openrouter/deleted-before-upgrade", "my-alias"} {
		if !slices.Contains(got, RowInfo{id, false}) {
			t.Fatalf("hand-written %s changed or removed: %v", id, got)
		}
	}
}

// TestSyncAdoptsUnmarkedRowOfManagedModel pins the one-time migration: a
// pre-upgrade wt row (unmarked, named like a registry cloud id) is rewritten
// with the marker and reported as adopted.
func TestSyncAdoptsUnmarkedRowOfManagedModel(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
`)
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v", got)
	}
	if len(res.Outcomes) != 1 || res.Outcomes[0].Action != "adopted" {
		t.Fatalf("outcomes = %+v, want one adopted", res.Outcomes)
	}
}

// TestSyncAdoptionCarriesUserParams pins the carry rule on adoption: an
// unmarked row named like a managed id is rewritten with the marker, and the
// rewrite keeps the old row's user-authored litellm_params keys the built row
// lacks (a hand-written timeout), while the keys wt owns (api_base) are
// re-derived from the registry. Before the carry rule, adoption silently
// dropped every param beyond the two presence-based ones.
func TestSyncAdoptionCarriesUserParams(t *testing.T) {
	o, restarts, p := opts(t, `model_list:
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y, api_base: "https://custom.example/v1", timeout: 120}
`)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v, want the adopted row marked", got)
	}
	var params map[string]any
	if err := mapGet(f.row("openrouter/x/y"), "litellm_params").Decode(&params); err != nil {
		t.Fatal(err)
	}
	if params["timeout"] != 120 {
		t.Errorf("timeout = %v, want the user's 120 carried over", params["timeout"])
	}
	// The registry owns endpoints: a hand-written api_base is not carried, or
	// a provider base_url change would never reach config.yaml.
	if params["api_base"] != "https://openrouter.ai/api/v1" {
		t.Errorf("api_base = %v, want the registry's", params["api_base"])
	}
	// Carried params are compared after the carry, so the next sync is a
	// no-op instead of rewriting (and restarting) forever.
	if *restarts != 1 {
		t.Fatalf("restarts after adoption = %d, want 1", *restarts)
	}
	if res, err := Sync(testConfig(), nil, o); err != nil || res.Changed || *restarts != 1 {
		t.Fatalf("second sync: err=%v changed=%v restarts=%d, want a no-op", err, res.Changed, *restarts)
	}
}

// TestSyncCarriesUserButNotWTParams pins the carry rule on every rewrite, not
// just adoption: a marked row's user-authored key (timeout) is carried, while
// all three wt-owned keys are not — a hand-written model, api_base or api_key
// on a marked row loses to the registry on the next sync, because a changed
// provider endpoint or mapping (and, for a stale model, the deployment LiteLLM
// actually dials) must reach config.yaml: a marked row is wt's to re-derive.
func TestSyncCarriesUserButNotWTParams(t *testing.T) {
	const id = "ollama/gemma:9b"
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/EVIL:1b, api_base: "http://localhost:9999", api_key: sk-hand-written, timeout: 120}
    model_info: {wt_managed: true}
`)
	if _, err := Sync(testConfig(), []string{id}, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	var params map[string]any
	if err := mapGet(f.row(id), "litellm_params").Decode(&params); err != nil {
		t.Fatal(err)
	}
	if params["timeout"] != 120 {
		t.Errorf("timeout = %v, want the user's 120 carried over", params["timeout"])
	}
	if params["api_base"] != "http://localhost:11434" {
		t.Errorf("api_base = %v, want the registry's http://localhost:11434", params["api_base"])
	}
	// The other two wt-authored keys are pinned the same way, but they do
	// different work. api_key is the half the rule's mutation actually
	// exercises: dropping it from wtParamKeys let a hand-written key reach the
	// row, and that survived the whole suite before this assertion existed. The
	// model pin does not catch that mutation — BuildEntry emits model
	// unconditionally, so carryUserParams can never find it missing — it is the
	// backstop for the day BuildEntry stops emitting model unconditionally:
	// model is the string LiteLLM dials.
	if params["model"] != "ollama_chat/gemma:9b" {
		t.Errorf("model = %v, want the registry's ollama_chat/gemma:9b", params["model"])
	}
	if v, ok := params["api_key"]; ok {
		t.Errorf("api_key = %v, want none: ollama's policy authors no api_key, so a hand-written one must not be carried", v)
	}
}

// TestSyncIgnoresMalformedUserParams pins that a rewrite never derives a
// garbled row from a litellm_params node it cannot interpret. carryUserParams
// copies the old row's user-authored params by walking that node's Content as
// key/value pairs, which is only valid while the node is a mapping whose keys
// are scalars; a hand-edited sequence ([foo, bar] — every element paired off)
// or a mapping with a complex key (? {weird: key}: 1 — a key node whose Value
// is "") is read as params instead. Both inputs are ones LiteLLM itself would
// reject on load, so this is a data-fidelity guard rather than a
// reachable-in-practice crash: it keeps wt's rule that a file it cannot
// interpret is left alone (checkModelList refuses a non-list model_list the
// same way) instead of silently writing derived garbage.
func TestSyncIgnoresMalformedUserParams(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"sequence", "model_list:\n  - model_name: openrouter/x/y\n    litellm_params: [foo, bar]\n"},
		{"complex key", "model_list:\n  - model_name: openrouter/x/y\n    litellm_params:\n      ? {weird: key}\n      : 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, _, p := opts(t, tc.body)
			if _, err := Sync(testConfig(), nil, o); err != nil {
				t.Fatal(err)
			}
			f, _ := Open(p)
			params := decode(t, mapGet(f.row("openrouter/x/y"), "litellm_params"))
			// The rebuilt row is exactly what the registry authors — before the
			// guard the sequence injected foo:bar and the complex key injected a
			// "" key, so either leaks a param LiteLLM would choke on.
			want := map[string]any{
				"model":    "openrouter/x/y",
				"api_base": "https://openrouter.ai/api/v1",
				"api_key":  "sk-test",
			}
			if !reflect.DeepEqual(params, want) {
				t.Fatalf("litellm_params = %v, want the registry's %v with no injected key", params, want)
			}
		})
	}
}

// TestSyncRemovesLegacyUnmarkedLocalRoute pins that a pre-upgrade route of a
// stopped REGISTRY local model is still removed, as sync did before #179 —
// ownership covers unmarked rows named like a managed registry id.
func TestSyncRemovesLegacyUnmarkedLocalRoute(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	for _, r := range readRows(t, p) {
		if r.ID == "ollama/gemma:9b" {
			t.Fatalf("stopped local model kept its route: %v", readRows(t, p))
		}
	}
}

// TestSyncDeduplicatesRepeatedStaleRow pins that two rows sharing one managed id
// produce one removal, not two. RemoveRow drops every duplicate, so the FILE is
// right either way, but the plan and the outcomes are built from the raw rows:
// the same removal was announced twice, in both the text and the JSON output of
// `wt litellm sync`, and plan.Remove handed a duplicate id to any caller of the
// exported PlanSync. Duplicate rows are exactly what a hand-edited config.yaml
// grows, and a report that names the same route twice reads as two stale routes.
func TestSyncDeduplicatesRepeatedStaleRow(t *testing.T) {
	const id = "ollama/gemma:9b"
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	plan, err := PlanSync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Remove, ","); got != id {
		t.Fatalf("plan.Remove = %s, want the stale id exactly once", got)
	}
	// The real sync reports the removal it performs, so the outcomes must be
	// deduplicated from the same source: both renderers walk res.Outcomes.
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, oc := range res.Outcomes {
		got = append(got, oc.ID+":"+oc.Action)
	}
	if s := strings.Join(got, ","); s != id+":unrouted,openrouter/x/y:routed" {
		t.Fatalf("outcomes = %s, want one unrouted line for the stale id", s)
	}
	for _, r := range readRows(t, p) {
		if r.ID == id {
			t.Fatalf("stale row survived: %v", readRows(t, p))
		}
	}
}

// TestSyncPrunesStaleDuplicateOfDesiredModel pins that a duplicate of a
// DESIRED id (a running local or cloud model) is reconciled. planSync compared
// only the first row per id and skipped the whole id when that row matched the
// rebuilt one, and the removal loop skips wanted ids — so a stale duplicate
// sharing a wanted id survived every sync and LiteLLM load-balanced between
// the fresh and stale backends. The canonical row comes from a real first
// sync, so the duplicate copies exactly what wt would save (drop params and
// pricing included): only its backend model string is stale.
func TestSyncPrunesStaleDuplicateOfDesiredModel(t *testing.T) {
	const id = "ollama/gemma:9b"
	o, restarts, p := opts(t, "model_list: []\n")
	if _, err := Sync(testConfig(), []string{id}, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	var dupe map[string]any
	if err := f.row(id).Decode(&dupe); err != nil {
		t.Fatal(err)
	}
	dupe["litellm_params"].(map[string]any)["model"] = "ollama_chat/STALE-GONE"
	node, err := toNode(dupe)
	if err != nil {
		t.Fatal(err)
	}
	ml, err := f.modelList()
	if err != nil {
		t.Fatal(err)
	}
	ml.Content = append(ml.Content, node)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanSync(testConfig(), []string{id}, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Add, []string{id}) || len(plan.Adopt) != 0 || len(plan.Remove) != 0 {
		t.Fatalf("plan = %+v, want the first row (managed, value-equal) re-planned so the duplicate is pruned", plan)
	}
	res, err := Sync(testConfig(), []string{id}, o)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, oc := range res.Outcomes {
		got = append(got, oc.ID+":"+oc.Action)
	}
	if s := strings.Join(got, ","); s != id+":rewritten" {
		t.Fatalf("outcomes = %s, want one rewritten line for the duplicated id", s)
	}
	f2, _ := Open(p)
	rows := readRows(t, p)
	n := 0
	for _, r := range rows {
		if r.ID == id {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rows = %v, want exactly one %s row (the stale duplicate pruned)", rows, id)
	}
	var params map[string]any
	if err := mapGet(f2.row(id), "litellm_params").Decode(&params); err != nil {
		t.Fatal(err)
	}
	if params["model"] != "ollama_chat/gemma:9b" {
		t.Fatalf("surviving row model = %v, want the fresh backend, not STALE-GONE", params["model"])
	}
	// The pruned sync is finished; the next one must be a clean no-op.
	before := *restarts
	if res, err := Sync(testConfig(), []string{id}, o); err != nil || res.Changed || *restarts != before {
		t.Fatalf("third sync: err=%v changed=%v restarts=%d→%d, want a no-op", err, res.Changed, before, *restarts)
	}
}

// TestSyncUnchangedDoesNotRestart pins that a second sync with nothing to do
// writes nothing and does not bounce the proxy (each bounce drops requests).
func TestSyncUnchangedDoesNotRestart(t *testing.T) {
	o, restarts, _ := opts(t, "model_list: []\n")
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	*restarts = 0
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || *restarts != 0 || len(res.Outcomes) != 0 {
		t.Fatalf("second sync: changed=%v restarts=%d outcomes=%+v", res.Changed, *restarts, res.Outcomes)
	}
}

// TestPlanSyncWritesNothing pins `sync --dry-run`: the plan names the add
// and the removal, and the file is byte-identical afterwards.
func TestPlanSyncWritesNothing(t *testing.T) {
	const body = `model_list:
  - model_name: openrouter/gone
    litellm_params: {model: openrouter/gone}
    model_info: {wt_managed: true}
`
	o, restarts, p := opts(t, body)
	plan, err := PlanSync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Add, []string{"openrouter/x/y"}) || !slices.Equal(plan.Remove, []string{"openrouter/gone"}) {
		t.Fatalf("plan = %+v", plan)
	}
	after, _ := os.ReadFile(p)
	if string(after) != body || *restarts != 0 {
		t.Fatalf("dry run changed the file or restarted: %q restarts=%d", after, *restarts)
	}
}

// TestPlanSyncRejectsNonSequenceModelList pins that a dry run refuses the same
// config.yaml the real sync refuses: PlanSync returns (and errors.Is sees)
// ErrInvalid, so `wt litellm sync --dry-run` cannot report a plan for a file
// the real sync would not touch. Without it a user previews work that then
// fails — or worse, trusts a preview that never happens.
func TestPlanSyncRejectsNonSequenceModelList(t *testing.T) {
	o, restarts, p := opts(t, "model_list: nope\n")
	if _, err := PlanSync(testConfig(), nil, o); !errors.Is(err, ErrInvalid) {
		t.Fatalf("PlanSync err = %v, want ErrInvalid", err)
	}
	if after, _ := os.ReadFile(p); string(after) != "model_list: nope\n" {
		t.Fatalf("dry run modified the file:\n%s", after)
	}
	if *restarts != 0 {
		t.Fatalf("dry run restarted the proxy %d times, want 0", *restarts)
	}
}

// TestPlanSyncAcceptsAbsentModelListWithoutTouchingIt pins the trap in the fix
// above: a config.yaml with no model_list at all (or an explicit null) must
// stay a clean no-op. The plan is empty and neither the dry run nor the real
// sync creates the key or writes the file — a fix that called modelList() from
// the plan path would create it, flip File.Changed, and make a no-op sync write
// config.yaml and restart the proxy, dropping in-flight requests.
func TestPlanSyncAcceptsAbsentModelListWithoutTouchingIt(t *testing.T) {
	// litellm_settings is seeded so EnsureSettings cannot add it and be the
	// thing that changes the file.
	const settings = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	empty := &config.Config{}
	for _, body := range []string{settings, "model_list:\n" + settings} {
		o, restarts, p := opts(t, body)
		plan, err := PlanSync(empty, nil, o)
		if err != nil {
			t.Fatalf("PlanSync(%q) err = %v, want nil", body, err)
		}
		if len(plan.Add)+len(plan.Adopt)+len(plan.Remove)+len(plan.Errors) != 0 {
			t.Fatalf("PlanSync(%q) = %+v, want an empty plan", body, plan)
		}
		if after, _ := os.ReadFile(p); string(after) != body {
			t.Fatalf("dry run over %q changed the file:\n%s", body, after)
		}
		res, err := Sync(empty, nil, o)
		if err != nil || res.Changed || *restarts != 0 {
			t.Fatalf("Sync(%q): err=%v changed=%v restarts=%d, want nil/false/0", body, err, res.Changed, *restarts)
		}
		if after, _ := os.ReadFile(p); string(after) != body {
			t.Fatalf("no-op sync over %q wrote the file:\n%s", body, after)
		}
	}

	// A null model_list with work to do is planned, not refused: the real sync
	// creates the key and routes the cloud model, so the dry run must agree.
	o, _, _ := opts(t, "model_list:\n")
	plan, err := PlanSync(testConfig(), nil, o)
	if err != nil || !slices.Equal(plan.Add, []string{"openrouter/x/y"}) {
		t.Fatalf("PlanSync(null model_list) = %+v err=%v, want the cloud add", plan, err)
	}
}

// TestPlanSyncAndSyncAgreeOnInvalidModelList pins that the dry run and the real
// sync give the same answer for a config.yaml whose model_list is not a list:
// both fail with ErrInvalid and neither writes the file or restarts the proxy,
// so a preview can never promise a plan the real run refuses — nor refuse a
// file the real run would happily rewrite.
//
// Both halves must be covered. With a non-empty plan the write path used to
// reach SetRow -> modelList, which refused; with an empty plan (an empty or
// all-native registry) nothing was written but EnsureSettings still appended
// litellm_settings and saved, so a shape check only on the dry-run side left
// the two disagreeing in the other direction. The empty half is what keeps that
// gap closed.
func TestPlanSyncAndSyncAgreeOnInvalidModelList(t *testing.T) {
	const body = "model_list: nope\n"
	for _, c := range []struct {
		name string
		cfg  *config.Config
	}{
		{"empty plan", &config.Config{}},
		{"non-empty plan", testConfig()},
	} {
		t.Run(c.name, func(t *testing.T) {
			o, restarts, p := opts(t, body)
			if _, err := PlanSync(c.cfg, nil, o); !errors.Is(err, ErrInvalid) {
				t.Fatalf("PlanSync err = %v, want ErrInvalid", err)
			}
			res, err := Sync(c.cfg, nil, o)
			if !errors.Is(err, ErrInvalid) || res.Changed || *restarts != 0 {
				t.Fatalf("Sync err=%v changed=%v restarts=%d, want ErrInvalid/false/0", err, res.Changed, *restarts)
			}
			if after, _ := os.ReadFile(p); string(after) != body {
				t.Fatalf("invalid config was written:\n%s", after)
			}
		})
	}
}

// TestSyncKeepsRouteWhenSecretUnresolved pins that a cloud row whose
// credentials do not resolve in this shell keeps its working api_key: the
// build failure is reported, and the row is neither rewritten nor removed.
func TestSyncKeepsRouteWhenSecretUnresolved(t *testing.T) {
	t.Setenv("WT_TEST_UNSET_KEY", "")
	cfg := testConfig()
	cfg.Providers[2].Auth.SecretRef = "WT_TEST_UNSET_KEY"
	o, _, p := opts(t, `model_list:
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y, api_key: sk-old}
    model_info: {wt_managed: true}
`)
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Outcomes, func(oc Outcome) bool { return oc.ID == "openrouter/x/y" && oc.Err != nil }) {
		t.Fatalf("want a build error for openrouter/x/y, got %+v", res.Outcomes)
	}
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	row := f.row("openrouter/x/y")
	if row == nil {
		t.Fatal("route removed")
	}
	if k := mapGet(mapGet(row, "litellm_params"), "api_key"); k == nil || k.Value != "sk-old" {
		t.Fatalf("api_key = %v, want sk-old kept", k)
	}
}

// TestSyncKeepsRowsOfModelsWithDanglingProvider pins the registry-data-gap
// guard: a registry model whose provider_id names no provider (e.g.
// `providers = []`) drops out of CloudModels, but its marked row — with any
// hand-added params — must survive until the registry is repaired.
func TestSyncKeepsRowsOfModelsWithDanglingProvider(t *testing.T) {
	cfg := testConfig()
	cfg.Providers = slices.DeleteFunc(cfg.Providers, func(p config.Provider) bool { return p.ID == "openrouter" })
	cfg.Models[2].Location = "" // location comes from the (missing) provider
	o, _, p := opts(t, `model_list:
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y, timeout: 600}
    model_info: {wt_managed: true}
`)
	if _, err := Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := readRows(t, p); !slices.Equal(got, []RowInfo{{"openrouter/x/y", true}}) {
		t.Fatalf("rows = %v, want the gap model's row kept", got)
	}
}

// TestSyncStaleRemoveSparesHandWrittenDuplicate pins that removing a stale
// marked row drops only marked rows of that name: an unmarked hand-written
// row sharing the name is never wt's to delete.
func TestSyncStaleRemoveSparesHandWrittenDuplicate(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: foo/x
    litellm_params: {model: openrouter/foo/x}
    model_info: {wt_managed: true}
  - model_name: foo/x
    litellm_params: {model: openrouter/foo/x-mine}
`)
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	got := readRows(t, p)
	if !slices.Contains(got, RowInfo{"foo/x", false}) || slices.Contains(got, RowInfo{"foo/x", true}) {
		t.Fatalf("rows = %v, want only the hand-written foo/x", got)
	}
}

// TestSyncReportsRewriteSeparately pins that a changed wt row is a rewrite,
// not a new route: the dry-run plan lists it under Rewrite (a subset of Add,
// like Adopt) and the real sync reports it as "rewritten".
func TestSyncReportsRewriteSeparately(t *testing.T) {
	o, _, _ := opts(t, `model_list:
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y, api_key: sk-test}
    model_info: {input_cost_per_token: 9, wt_managed: true}
`)
	plan, err := PlanSync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Rewrite, []string{"openrouter/x/y"}) || !slices.Contains(plan.Add, "openrouter/x/y") || len(plan.Adopt) != 0 {
		t.Fatalf("plan = %+v, want openrouter/x/y as a rewrite only", plan)
	}
	res, err := Sync(testConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(res.Outcomes, func(oc Outcome) bool { return oc.ID == "openrouter/x/y" && oc.Action == "rewritten" }) {
		t.Fatalf("outcomes = %+v, want openrouter/x/y rewritten", res.Outcomes)
	}
}

// TestSyncResolvesFailingSecretOncePerProvider pins that a failing exec:
// secret_ref runs once per sync, not once per cloud model: failures are never
// cached by config.ResolveSecret, and each run may take the full exec timeout,
// so per-model resolution turned one broken helper into minutes of stall.
func TestSyncResolvesFailingSecretOncePerProvider(t *testing.T) {
	dir := t.TempDir()
	count := dir + "/count"
	script := dir + "/fail.sh"
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho x >> "+count+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Providers[2].Auth.SecretRef = "exec:" + script
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/a/b", ProviderID: "openrouter", ModelName: "a/b", Location: config.LocationCloud})
	o, _, _ := opts(t, "model_list: []\n")
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			failed = append(failed, oc.ID)
		}
	}
	if !slices.Equal(failed, []string{"openrouter/x/y", "openrouter/a/b"}) {
		t.Fatalf("failed ids = %v, want both openrouter models", failed)
	}
	b, _ := os.ReadFile(count)
	if n := strings.Count(string(b), "x"); n != 1 {
		t.Fatalf("helper ran %d times, want 1", n)
	}
}
