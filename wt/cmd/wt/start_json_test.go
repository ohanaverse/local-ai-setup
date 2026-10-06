package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
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
