package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// jsonStartFixture is an omlx with A loaded and B on disk, in a pool whose
// ceiling decides whether B fits beside A.
func jsonStartFixture(t *testing.T, ceiling int64) *config.Config {
	t.Helper()
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://127.0.0.1:1"}}}}
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/A", ModelName: "A", Artifact: "A", Running: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", ArtifactKnown: true},
		},
		OmlxPool: &localmodels.Pool{Ceiling: ceiling, InUse: 60, SizesKnown: true, Models: []localmodels.PoolModel{
			{ID: "A", Loaded: true, Size: 60}, {ID: "B", Size: 60},
		}},
	}
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
	oldCounts := sessionCounts
	sessionCounts = func(ids []string) map[string]int { return map[string]int{"omlx/A": 1} }
	t.Cleanup(func() { probeInventory, sessionCounts = old, oldCounts })
	return cfg
}

func fixtureShape(t *testing.T, key string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/contracts/wt-start-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's "_comment" is a string, so decode one key at a time.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(doc[key], &shape); err != nil {
		t.Fatalf("fixture key %q: %v", key, err)
	}
	return shape
}

// TestStartPlanJSONMatchesTheContract verifies the dry run names what a start
// would unload, with session counts, in exactly the shape the shared fixture
// pins — modelman's confirm dialog is built from it, and it must change
// nothing.
func TestStartPlanJSONMatchesTheContract(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	for _, tc := range []struct {
		ceiling int64
		key     string
	}{{100, "plan_would_unload"}, {1000, "plan_fits"}} {
		var out bytes.Buffer
		if err := runStartJSON(&out, jsonStartFixture(t, tc.ceiling), "omlx/B", true, false); err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%s: output %q is not JSON: %v", tc.key, out.String(), err)
		}
		if want := fixtureShape(t, tc.key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", tc.key, got, want)
		}
	}
	if len(*started) != 0 {
		t.Errorf("a plan started a model (%d starts)", len(*started))
	}
}

// runPlanJSON runs `wt start omlx/B --plan --json` against snap and returns the
// decoded plan.
func runPlanJSON(t *testing.T, snap localmodels.Snapshot) map[string]any {
	t.Helper()
	cfg := jsonStartFixture(t, 1000)
	stubProbeInventory(t, snap)
	var out bytes.Buffer
	if err := runStartJSON(&out, cfg, "omlx/B", true, false); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q is not JSON: %v", out.String(), err)
	}
	return got
}

// TestStartPlanJSONColdServerFits verifies a plan for an omlx that is not
// running — the probe's connection was refused — says the model fits. Nothing
// is serving, so nothing can be unloaded. It answered "unknown", which made
// modelman's dialog say wt could not tell and made `wt start <id> --json`
// without --replace exit 1 on every cold start.
func TestStartPlanJSONColdServerFits(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	got := runPlanJSON(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial},
		Down:      map[string]bool{"omlx": true},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", ArtifactKnown: true}},
	})
	if want := fixtureShape(t, "plan_fits"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(*started) != 0 {
		t.Errorf("a plan started a model (%d starts)", len(*started))
	}
}

// TestStartPlanJSONRunningAndUnknown pins the two plan answers that involve no
// eviction arithmetic, against the shared fixture: `running` for a target that
// is already loaded, and `unknown` when the server answered but would not say
// what it has loaded. modelman words its confirm dialog from these; a renamed
// status or a missing would_unload key would break it silently.
func TestStartPlanJSONRunningAndUnknown(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	events := stubEnsureRoute(t)
	b := localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", ArtifactKnown: true}
	running := b
	running.Running = true
	for _, tc := range []struct {
		key  string
		snap localmodels.Snapshot
	}{
		{"plan_running", localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
			Entries:   []localmodels.Entry{running},
		}},
		// Partial without Down: something answered at the port, so "nothing
		// is serving" cannot be assumed.
		{"plan_unknown", localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial},
			Entries:   []localmodels.Entry{b},
		}},
	} {
		if got, want := runPlanJSON(t, tc.snap), fixtureShape(t, tc.key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", tc.key, got, want)
		}
	}
	if len(*started) != 0 || len(*events) != 0 {
		t.Errorf("a plan changed something: %d starts, route events %v", len(*started), *events)
	}
}

// TestStartJSONAlreadyRunningMatchesTheContract verifies a JSON start of a
// model that is already loaded reports `already_running` in the fixture's
// shape, starts nothing, and still runs the launch-time route check: a model
// loaded outside wt has no route until something writes one.
func TestStartJSONAlreadyRunningMatchesTheContract(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	events := stubEnsureRoute(t)
	cfg := jsonStartFixture(t, 1000)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", ArtifactKnown: true, Running: true}},
	})
	var out bytes.Buffer
	if err := runStartJSON(&out, cfg, "omlx/B", false, false); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output %q is not JSON: %v", out.String(), err)
	}
	if want := fixtureShape(t, "already_running"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(*started) != 0 || len(*events) == 0 || (*events)[0] != "ensure:omlx/B" {
		t.Errorf("starts = %d, route events = %v; want no start and the route check for omlx/B", len(*started), *events)
	}
}

// TestStartJSONFailedStartPrintsNothing verifies a start that fails in the
// engine leaves stdout empty and returns the error. A caller parses stdout as
// the result; anything printed there on a failure would read as a start that
// happened.
func TestStartJSONFailedStartPrintsNothing(t *testing.T) {
	boom := errors.New("omlx did not come up")
	started := stubLifecycleStart(t, []error{boom})
	var out bytes.Buffer
	err := runStartJSON(&out, jsonStartFixture(t, 1000), "omlx/B", false, false)
	if !errors.Is(err, boom) || len(*started) != 1 {
		t.Fatalf("err = %v, starts = %d; want the engine's error from one start", err, len(*started))
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing on a failed start", out.String())
	}
}

// TestStartPlanNeedsJSON verifies `wt start <id> --plan` without --json is
// refused before anything runs. --plan promises to change nothing; falling
// through to the interactive start would load the model the user only asked
// about.
func TestStartPlanNeedsJSON(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	driver := stubStartDriver(t, nil)
	c := startCmd(&app{cfg: jsonStartFixture(t, 1000)})
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs([]string{"omlx/B", "--plan"})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "--plan needs --json") {
		t.Fatalf("err = %v, want the --plan needs --json refusal", err)
	}
	if len(*started) != 0 || driver.called || out.Len() != 0 {
		t.Errorf("starts = %d, driver called = %v, stdout = %q; want nothing done", len(*started), driver.called, out.String())
	}
}

// TestStartJSONReportsWhatWasUnloaded verifies a JSON start with --replace
// reports the models the start unloaded. modelman clears their running flags
// from this; without it they would read as running forever.
func TestStartJSONReportsWhatWasUnloaded(t *testing.T) {
	cfg := jsonStartFixture(t, 100)
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		if !opts.AllowReplace {
			t.Error("--replace did not reach the engine")
		}
		opts.OnUnloaded(localmodels.Entry{ModelID: "omlx/A"})
		return nil
	}
	t.Cleanup(func() { lifecycleStart = old })
	var out bytes.Buffer
	if err := runStartJSON(&out, cfg, "omlx/B", false, true); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if want := fixtureShape(t, "started"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestStartJSONRefusesToEvictWithoutReplace verifies a scripted start never
// evicts on its own: without --replace it prints the plan, exits non-zero and
// starts nothing, so a caller can ask its user first.
func TestStartJSONRefusesToEvictWithoutReplace(t *testing.T) {
	started := stubLifecycleStart(t, nil)
	var out bytes.Buffer
	err := runStartJSON(&out, jsonStartFixture(t, 100), "omlx/B", false, false)
	if err == nil || len(*started) != 0 {
		t.Fatalf("err = %v, starts = %d; want a refusal and no start", err, len(*started))
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if got["status"] != "would_unload" {
		t.Errorf("stdout = %q, want the plan", out.String())
	}
}
