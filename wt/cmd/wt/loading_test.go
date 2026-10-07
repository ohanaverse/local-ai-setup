package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// loadingFixture stubs the inventory with one omlx model, omlx/c, that omlx is
// still loading: Running (it occupies the pool) and Loading. The pool reading
// is one in which a fresh start of it would need room (ceiling 100, 60 in use,
// 60 more wanted), so a test can tell "planned as a new start" from "joins the
// load".
func loadingFixture(t *testing.T) *config.Config {
	t.Helper()
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/c", ModelName: "c", Artifact: "c", Registered: true, Running: true, Loading: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/other", ModelName: "other", Artifact: "other", Running: true, ArtifactKnown: true},
		},
		OmlxPool: &localmodels.Pool{Ceiling: 100, InUse: 60, SizesKnown: true, Models: []localmodels.PoolModel{
			{ID: "c", Loading: true, Size: 60}, {ID: "other", Loaded: true, Size: 60},
		}},
	})
	return modelCmdConfig()
}

// TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning pins #259 for
// `wt start <id>`: on a model omlx is still loading it printed "already
// running" and returned at once, so a script that started a model and then
// used it raced the load. It now goes through the start driver, which returns
// when the model is loaded, and reports it as running.
func TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning(t *testing.T) {
	cfg := loadingFixture(t)
	req := stubStartDriver(t, nil)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "omlx/c", false); err != nil {
		t.Fatal(err)
	}
	if !req.called || req.row.Model.ID != "omlx/c" {
		t.Fatalf("start driver called = %v for %q, want it called for omlx/c", req.called, req.row.Model.ID)
	}
	if got := out.String(); strings.Contains(got, "already running") || !strings.Contains(got, "wt: omlx/c is running") {
		t.Errorf("out = %q, want the is-running line once the load is done, not already-running", got)
	}
}

// TestStartJSONOnALoadingModelPlansNothingAndWaits pins #259 for the form
// modelman drives. The dry run used to answer "running", so `modelman start`
// reported a model as started while it was still loading. It now answers
// "fits" with nothing to unload — the load is already admitted, so the pool's
// other model is not named — and the real start runs the engine, which waits,
// and answers "started".
func TestStartJSONOnALoadingModelPlansNothingAndWaits(t *testing.T) {
	cfg := loadingFixture(t)
	started := stubLifecycleStart(t, nil)
	stubEnsureRoute(t)

	var plan bytes.Buffer
	if err := runStartJSON(&plan, cfg, "omlx/c", true, false); err != nil {
		t.Fatal(err)
	}
	var p startPlanJSON
	if err := json.Unmarshal(plan.Bytes(), &p); err != nil {
		t.Fatalf("plan %q is not JSON: %v", plan.String(), err)
	}
	if p.Status != "fits" || len(p.WouldUnload) != 0 || len(*started) != 0 {
		t.Errorf("plan = %+v, engine calls = %d; want fits, nothing to unload, nothing started", p, len(*started))
	}

	var out bytes.Buffer
	if err := runStartJSON(&out, cfg, "omlx/c", false, false); err != nil {
		t.Fatalf("start without --replace = %v, want it to join the load unasked", err)
	}
	var r startResultJSON
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("result %q is not JSON: %v", out.String(), err)
	}
	if r.Status != "started" || len(*started) != 1 {
		t.Errorf("result = %+v, engine calls = %d; want started after one engine call", r, len(*started))
	}
}

// TestALoadingModelIsNotLaunchableButAPinStartsIt pins #259 for the non-TUI
// launch. Without -M, wt picks only among models that can answer now, so a
// model mid-load must not be chosen (the agent's first request would hang on
// the load). With -M the pin starts it, which waits for the load, exactly as
// a pin on an idle model does.
func TestALoadingModelIsNotLaunchableButAPinStartsIt(t *testing.T) {
	cfg := loadingFixture(t)
	cfg.Agents = []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}}
	// A base url, so the pin's route guard resolves a launch route; without
	// one the row is refused before the start (pickerBlockedReason).
	cfg.ProviderByID("omlx").Auth.BaseURL = "http://localhost:8000"
	req := stubStartDriver(t, nil)

	_, launchable, _ := resolveModel("pi", cfg, "", "", "")
	ids := make([]string, len(launchable))
	for i, m := range launchable {
		ids[i] = m.ID
	}
	if !slices.Equal(ids, []string{"omlx/other"}) {
		t.Errorf("launchable = %v, want only the loaded omlx/other", ids)
	}
	if req.called {
		t.Fatal("resolving without a pin must never start a model")
	}

	m, _, err := resolveModel("pi", cfg, "", "", "omlx/c")
	if err != nil || m.ID != "omlx/c" || !req.called {
		t.Errorf("pin: model = %q err = %v started = %v, want omlx/c started and returned", m.ID, err, req.called)
	}
}

// TestALoadingModelKeepsItsRoute verifies the half of #259 that must not
// change: a model mid-load is still in the set `wt litellm sync` routes.
// Dropping it would remove the route a start wrote moments ago and restart the
// proxy under every other session, only to add the route back when the load
// ends.
func TestALoadingModelKeepsItsRoute(t *testing.T) {
	cfg := loadingFixture(t)
	if got := desiredLocalIDs(cfg, probeInventory(cfg)); !slices.Equal(got, []string{"omlx/c", "omlx/other"}) {
		t.Errorf("desiredLocalIDs = %v, want the loading omlx/c beside omlx/other", got)
	}
}
