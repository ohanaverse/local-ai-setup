package litellm

import (
	"context"
	"errors"
	"os"
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
// (a lifecycle hook exposing a just-started model) is seen and handled, not
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
// the under-lock Recheck filter drops an id from Add. Both lists are part of the
// exported plan, and Sync relabels an added id "adopted" by looking it up in
// Adopt, so an id left in Adopt after being pruned from Add describes a plan wt
// does not carry out: a caller reports an adoption of a row that is never
// written. Unreachable from the CLI today (the dry run nulls Recheck, and Sync
// consults Adopt only for ids that produced an outcome), which is exactly why it
// is worth pinning: the trap is for the next caller of PlanSync.
func TestPlanSyncAdoptStaysSubsetOfAddWhenRecheckPrunes(t *testing.T) {
	o, _, _ := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	const id = "ollama/gemma:9b"
	// The outer probe saw the local model running, so its unmarked row is an
	// adopt; Recheck, under the lock, finds nothing running, so the add side
	// drops it.
	o.Recheck = func() []string { return nil }
	plan, err := PlanSync(testConfig(), []string{id}, o)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Add, id) || slices.Contains(plan.Adopt, id) {
		t.Fatalf("plan = %+v, want the pruned id gone from both Add and Adopt", plan)
	}
	if !isAdoptSubsetOfAdd(plan) {
		t.Fatalf("plan = %+v, want every Adopt id also in Add", plan)
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
// expose step, and skips native models.
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
// wt-owned keys are not — a custom api_base on a marked row loses to the
// registry on the next sync, because a provider base_url change must reach
// config.yaml (a marked row is wt's to re-derive).
func TestSyncCarriesUserButNotWTParams(t *testing.T) {
	const id = "ollama/gemma:9b"
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b, api_base: "http://localhost:9999", timeout: 120}
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
