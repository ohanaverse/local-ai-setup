package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// modelCmdConfig is a minimal local-only registry shared by the start/stop
// command tests: two ollama models and one omlx model, no agents needed.
func modelCmdConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "omlx/c", ProviderID: "omlx", ModelName: "c", Location: config.LocationLocal},
		},
	}
	return cfg
}

func cand(provider, id, name string, sessions int) survey.Candidate {
	return survey.Candidate{
		Entry:    localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true},
		Sessions: sessions,
	}
}

// stubStop swaps the stop seams for one test: candidates is what is "running"
// under trusted probes, and the returned slice records every entry passed to
// the stop loop. The family-wide session counts are the candidates' own, as
// the real read gives for a pool whose sessions are all on running models.
func stubStop(t *testing.T, cands []survey.Candidate) *[]localmodels.Entry {
	t.Helper()
	fams := map[string]survey.FamilyState{}
	for _, c := range cands {
		if c.Sessions == 0 || lifecycle.TenancyOf(c.Entry.ProviderID) != lifecycle.Pool {
			continue
		}
		fam := localmodels.Family(c.Entry.ProviderID)
		fs := fams[fam]
		fs.Sessions += c.Sessions
		fs.Users = append(fs.Users, c.Entry.ModelID)
		fams[fam] = fs
	}
	return stubStopState(t, survey.StopState{Candidates: cands, Families: fams})
}

// stubStopState is stubStop for a test that sets the provider families' own
// state: an untrusted or refused probe, or sessions no candidate carries.
func stubStopState(t *testing.T, st survey.StopState) *[]localmodels.Entry {
	t.Helper()
	var stopped []localmodels.Entry
	oldState, oldEntries := stopState, stopEntries
	stopState = func(*config.Config) survey.StopState { return st }
	stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
		stopped = append(stopped, es...)
		return nil
	}
	t.Cleanup(func() { stopState, stopEntries = oldState, oldEntries })
	return &stopped
}

// TestStopModelIDStopsThatModel verifies `wt stop <provider>/<name>` stops
// exactly that running model and nothing else — the core contract of the command.
func TestStopModelIDStopsThatModel(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" {
		t.Fatalf("stopped = %v, want only ollama/a:1", *stopped)
	}
}

// TestStopProviderStopsAllItsModels verifies a bare provider id stops every
// running model of that provider and leaves other providers alone; this is the
// "stop everything on ollama" shortcut.
func TestStopProviderStopsAllItsModels(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0), cand("omlx", "omlx/c", "c", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 2 {
		t.Fatalf("stopped = %v, want the two ollama models", *stopped)
	}
}

// TestStopInvalidArgErrors verifies an unknown provider, an unknown model, and
// a registered-but-not-running model each exit with an error and stop nothing.
// A typo must never silently succeed.
func TestStopInvalidArgErrors(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	cases := map[string]string{
		"bogus":         "unknown provider",
		"ollama/nope:9": "unknown model",
		"ollama/b:1":    "not running",
		"mlx_lm_server": "unknown provider", // real provider id, but wt has no stop backend
	}
	for arg, want := range cases {
		err := runStop(io.Discard, modelCmdConfig(), arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runStop(%q) err = %v, want it to contain %q", arg, err, want)
		}
	}
	if len(*stopped) != 0 {
		t.Errorf("stopped = %v, want nothing", *stopped)
	}
}

// TestStopProviderNothingRunningIsNotAnError verifies `wt stop ollama` with no
// running ollama model prints a note and exits 0, so the command is idempotent.
func TestStopProviderNothingRunningIsNotAnError(t *testing.T) {
	stubStop(t, nil)
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing running") {
		t.Errorf("out = %q, want a nothing-running note", out.String())
	}
}

// TestStopInUseConfirms verifies stopping a model a live wt session uses asks
// first, declining aborts without stopping, accepting (or --yes) stops it.
// This is the safety net for stopping another session's model out from under it.
func TestStopInUseConfirms(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)})
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })

	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err == nil {
		t.Fatal("declined confirm must return an error (non-zero exit)")
	}
	if !strings.Contains(asked, "2") || len(*stopped) != 0 {
		t.Fatalf("asked = %q stopped = %v, want a session-count prompt and no stop", asked, *stopped)
	}

	confirmStop = func(string) (bool, error) { return true, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err != nil || len(*stopped) != 1 {
		t.Fatalf("accepted: err = %v stopped = %v, want one stop", err, *stopped)
	}

	*stopped = nil
	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", true); err != nil || len(*stopped) != 1 {
		t.Fatalf("--yes: err = %v stopped = %v", err, *stopped)
	}
}

// TestStopFailurePropagates verifies a failed stop makes the command fail, so
// scripts can rely on the exit code.
func TestStopFailurePropagates(t *testing.T) {
	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err == nil {
		t.Fatal("want the stop failure returned")
	}
}

// TestStopNoArgNeedsTTYAndOpensPicker verifies `wt stop` with no argument
// errors without a TTY, opens the in-use-inclusive picker (screen 2) when a TTY
// is present, and says so when the picker reports nothing to offer. It also
// pins that this path never probes the inventory itself (stopState) — the
// picker owns the single snapshot, so nothing is probed twice.
func TestStopNoArgNeedsTTYAndOpensPicker(t *testing.T) {
	oldTTY, oldPick := stdinTTY, stopPickerAll
	t.Cleanup(func() { stdinTTY, stopPickerAll = oldTTY, oldPick })
	opened, offer := false, true
	stopPickerAll = func(*config.Config) (bool, []string, map[string]lifecycle.Loading) {
		opened = true
		return offer, nil, nil
	}

	oldState := stopState
	t.Cleanup(func() { stopState = oldState })
	stopState = func(*config.Config) survey.StopState {
		t.Fatal("no-arg wt stop probed the inventory outside the picker")
		return survey.StopState{}
	}

	stdinTTY = func() bool { return false }
	if err := runStop(io.Discard, modelCmdConfig(), "", false); err == nil {
		t.Fatal("no TTY and no arg must error")
	}
	stdinTTY = func() bool { return true }
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "", false); err != nil || !opened || out.Len() != 0 {
		t.Fatalf("err = %v opened = %v out = %q, want the picker opened and no note", err, opened, out.String())
	}

	offer, out = false, bytes.Buffer{}
	if err := runStop(&out, modelCmdConfig(), "", false); err != nil || !strings.Contains(out.String(), "no running local models") {
		t.Fatalf("err = %v out = %q, want the empty note when the picker had nothing", err, out.String())
	}
}

// TestStopUnknownProviderListsConfiguredStoppable verifies the error for a bad
// bare provider names the providers the check actually accepts, derived from
// the same list — so the message cannot drift from the check when a stop
// backend is added.
func TestStopUnknownProviderListsConfiguredStoppable(t *testing.T) {
	err := runStop(io.Discard, modelCmdConfig(), "bogus", false)
	if err == nil || !strings.Contains(err.Error(), "valid: ollama, omlx") {
		t.Fatalf("err = %v, want it to list the configured stoppable providers", err)
	}
}

// startFixture stubs the inventory: ollama/a:1 running, ollama/b:1 pulled but
// idle (start row), omlx/c missing from disk (blocked). Returns the start recorder.
func startFixture(t *testing.T) (*config.Config, *startRequest) {
	t.Helper()
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "a:1", ModelID: "ollama/a:1", ModelName: "a:1", Registered: true, Running: true, ArtifactKnown: true},
			{ProviderID: "ollama", Artifact: "b:1", ModelID: "ollama/b:1", ModelName: "b:1", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", Artifact: "", ModelID: "omlx/c", ModelName: "c", Registered: true, ArtifactKnown: true},
			// A stopped mlx_lm_server pairing (its model is added to cfg only
			// by TestStartInvalidArgErrors): no row, wt cannot start it.
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", ModelName: "p", Registered: true},
		},
	})
	return modelCmdConfig(), stubStartDriver(t, nil)
}

// TestStartRunningModelDoesNotStartIt verifies `wt start` on an already-running
// model says so and never calls the start driver — starting twice must be
// harmless. It is not a no-op overall: the model's missing LiteLLM route is
// still written (#192, TestStartRunningModelRepairsRoute).
func TestStartRunningModelDoesNotStartIt(t *testing.T) {
	cfg, req := startFixture(t)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if req.called || !strings.Contains(out.String(), "already running") {
		t.Fatalf("called = %v out = %q, want the already-running note and no start", req.called, out.String())
	}
}

// TestStartIdleModelStartsIt verifies an idle local model goes through the
// start driver with the caller's --replace permission.
func TestStartIdleModelStartsIt(t *testing.T) {
	cfg, req := startFixture(t)
	if err := runStart(io.Discard, cfg, themes.Theme{}, "ollama/b:1", true); err != nil {
		t.Fatal(err)
	}
	if !req.called || req.row.Model.ID != "ollama/b:1" || !req.replace {
		t.Fatalf("req = %+v, want ollama/b:1 started with replace", req)
	}
}

// TestStartInvalidArgErrors verifies unknown ids, cloud ids, and registry
// local models with no row exit with an error naming the problem and never
// start anything. The two hidden models (omlx/c not on disk, a stopped
// mlx_lm_server pairing) get their reason only through catalog.MissingReason
// — the llmbench command for the pairing, since wt cannot start it.
func TestStartInvalidArgErrors(t *testing.T) {
	cfg, req := startFixture(t)
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud},
		config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Location: config.LocationLocal},
	)
	cases := map[string]string{
		"ollama/nope:9": "unknown model", "openrouter/x": "not a local model", "omlx/c": "not on disk",
		"mlx_lm_server/p": "llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>",
	}
	for arg, want := range cases {
		err := runStart(io.Discard, cfg, themes.Theme{}, arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runStart(%q) err = %v, want %q", arg, err, want)
		}
	}
	if req.called {
		t.Error("nothing may be started for an invalid argument")
	}
}

// TestStopUnknownProviderErrorsBeforeProbe verifies a typo'd provider is
// rejected before the live inventory probe (the stopState seam), so a bad
// argument costs nothing.
func TestStopUnknownProviderErrorsBeforeProbe(t *testing.T) {
	old := stopState
	t.Cleanup(func() { stopState = old })
	called := false
	stopState = func(*config.Config) survey.StopState { called = true; return survey.StopState{} }
	if err := runStop(io.Discard, &config.Config{}, "bogus", false); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("err = %v, want unknown provider", err)
	}
	if called {
		t.Fatal("stopState was probed before provider validation")
	}
}

// TestStartNoArgUsesPickerAndRunningPickDoesNotStartIt verifies the no-arg flow
// needs a TTY, lists every local model via the shared picker, starts an idle
// pick, and never calls the start driver for a running pick (which gets only
// the launch-time route check, #192).
func TestStartNoArgUsesPickerAndRunningPickDoesNotStartIt(t *testing.T) {
	cfg, req := startFixture(t)
	oldTTY, oldPick := stdinTTY, pickStartModelTUI
	t.Cleanup(func() { stdinTTY, pickStartModelTUI = oldTTY, oldPick })

	stdinTTY = func() bool { return false }
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err == nil {
		t.Fatal("no TTY and no arg must error")
	}

	stdinTTY = func() bool { return true }
	var offered []string
	pick := "ollama/b:1"
	pickStartModelTUI = func(_ *config.Config, models []config.Model, _ themes.Theme) (config.Model, bool, error) {
		for _, m := range models {
			offered = append(offered, m.ID)
		}
		return config.Model{ID: pick}, true, nil
	}
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err != nil || !req.called {
		t.Fatalf("idle pick: err = %v called = %v, want a start", err, req.called)
	}
	if !slices.Equal(offered, []string{"ollama/a:1", "ollama/b:1"}) {
		t.Errorf("picker offered %v, want the running and idle models only (omlx/c is not on disk, so it has no row)", offered)
	}

	*req = startRequest{}
	pick = "ollama/a:1"
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err != nil || req.called {
		t.Fatalf("running pick: err = %v called = %v, want no start", err, req.called)
	}
}

// TestStartCancelledPicker verifies canceling the picker (ok=false) is an
// error mentioning cancellation and starts nothing.
func TestStartCancelledPicker(t *testing.T) {
	cfg, req := startFixture(t)
	oldTTY, oldPick := stdinTTY, pickStartModelTUI
	t.Cleanup(func() { stdinTTY, pickStartModelTUI = oldTTY, oldPick })
	stdinTTY = func() bool { return true }
	pickStartModelTUI = func(*config.Config, []config.Model, themes.Theme) (config.Model, bool, error) {
		return config.Model{}, false, nil
	}
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err == nil || !strings.Contains(err.Error(), "canceled") || req.called {
		t.Fatalf("err = %v called = %v, want canceled error and no start", err, req.called)
	}
}

// TestStartDiscoveredRunningModel verifies a detected (unregistered) model that
// is running is offered to the picker and `wt start` on it is a no-op, so the
// screen-1 row set reflects live state for models missing from the registry.
func TestStartDiscoveredRunningModel(t *testing.T) {
	cfg, req := startFixture(t)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "d:1", ModelID: "ollama/d:1", ModelName: "d:1", Running: true, ArtifactKnown: true},
		},
	})
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "ollama/d:1", false); err != nil {
		t.Fatal(err)
	}
	if req.called || !strings.Contains(out.String(), "already running") {
		t.Fatalf("called = %v out = %q, want already-running no-op", req.called, out.String())
	}
	found := false
	for _, r := range localRows(cfg) {
		if r.Model.ID == "ollama/d:1" && r.Discovered && r.Running {
			found = true
		}
	}
	if !found {
		t.Error("localRows must include the discovered running model")
	}
}

// TestStopSingleModelProviderStopsAndNamesWholeFamily verifies that stopping
// one model of a single-model provider (mtplx) — which takes the whole provider
// down — puts every co-running model in the stop list and names the collateral
// in the output. Otherwise a sibling variant dies with no warning.
func TestStopSingleModelProviderStopsAndNamesWholeFamily(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("mtplx", "mtplx/c", "c", 0), cand("mtplx", "mtplx/d", "d", 0), cand("ollama", "ollama/a:1", "a:1", 0)})
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "mtplx/c", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 2 {
		t.Fatalf("stopped = %v, want both mtplx models (the provider stops as a whole) and not ollama", *stopped)
	}
	if !strings.Contains(out.String(), "mtplx/d") {
		t.Errorf("out = %q, want the collateral model mtplx/d named", out.String())
	}
}

// TestStopPoolModelLeavesSiblingsRunning pins #213 for `wt stop <omlx model>`:
// omlx holds a pool, so stopping one model stops that model alone and names no
// collateral. Widening it to the family, as for mtplx, unloaded models other
// sessions were using.
func TestStopPoolModelLeavesSiblingsRunning(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("omlx", "omlx/c", "c", 0), cand("omlx", "omlx/d", "d", 0), cand("omlx-6bit", "omlx-6bit/e", "e", 0)})
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "omlx/c", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "omlx/c" {
		t.Fatalf("stopped = %v, want only omlx/c", *stopped)
	}
	if strings.Contains(out.String(), "also stops") {
		t.Errorf("out = %q, want no collateral named on a pool", out.String())
	}
}

// TestStopInUseCountIsTotalAcrossModels verifies the in-use prompt reports the
// real number of affected sessions: summed across models on a multi-model
// provider (1 + 3 = 4, not the max 3; an omlx pool counts per model the same
// way) and counted once for a single-model provider (mtplx), whose candidates
// each carry the family total. The prompt also names the in-use models so the
// user sees what dies.
func TestStopInUseCountIsTotalAcrossModels(t *testing.T) {
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })
	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }

	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 1), cand("ollama", "ollama/b:1", "b:1", 3)})
	_ = runStop(io.Discard, modelCmdConfig(), "ollama", false)
	if !strings.Contains(asked, "4 live") || !strings.Contains(asked, "ollama/a:1") || !strings.Contains(asked, "ollama/b:1") {
		t.Errorf("ollama prompt = %q, want 4 sessions and both models named", asked)
	}

	cfg := modelCmdConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
	stubStop(t, []survey.Candidate{cand("mtplx", "mtplx/c", "c", 2), cand("mtplx", "mtplx/d", "d", 2)})
	_ = runStop(io.Discard, cfg, "mtplx", false)
	if !strings.Contains(asked, "2 live") {
		t.Errorf("mtplx prompt = %q, want the family total (2) counted once, not 4", asked)
	}

	stubStop(t, []survey.Candidate{cand("omlx", "omlx/c", "c", 1), cand("omlx", "omlx/d", "d", 2)})
	_ = runStop(io.Discard, modelCmdConfig(), "omlx", false)
	if !strings.Contains(asked, "3 live") || !strings.Contains(asked, "omlx/c") || !strings.Contains(asked, "omlx/d") {
		t.Errorf("omlx prompt = %q, want 3 sessions (each pool model carries its own count) and both models named", asked)
	}
}

// TestStartRunningModelRepairsRoute pins #192: `wt start` on a model that is
// already running — possibly started outside wt — checks its LiteLLM route
// and waits for the proxy, instead of only reporting "already running". It
// still never calls the start driver.
func TestStartRunningModelRepairsRoute(t *testing.T) {
	cfg, req := startFixture(t)
	events := stubEnsureRoute(t)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if req.called {
		t.Fatal("the start driver ran for a model that is already running")
	}
	if got := strings.Join(*events, ","); got != "ensure:ollama/a:1,wait" {
		t.Fatalf("events = %q, want ensure:ollama/a:1,wait", got)
	}
	if !strings.Contains(out.String(), "already running") {
		t.Fatalf("out = %q, want the already-running line", out.String())
	}
}

// omlxServer is a keyed omlx with two models in its pool and B loaded: /health
// gives the counts to anyone, /v1/models/status says which only to the key.
func omlxServer(t *testing.T, key string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"engine_pool":{"model_count":2,"loaded_count":1}}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"A"},{"id":"B"}]}`))
		case "/v1/models/status":
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, `{"detail":"Invalid API key"}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"id":"A","loaded":false},{"id":"B","loaded":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func omlxServedCfg(origin, secretRef string) *config.Config {
	return &config.Config{Providers: []config.Provider{{
		ID: "omlx", Location: config.LocationLocal,
		Auth: config.AuthConfig{Type: "api_key", SecretRef: secretRef, BaseURL: origin + "/v1"},
	}}}
}

// TestServedReportsWhatAKeyedOmlxHasLoaded pins the reason `wt served`
// exists: a partly loaded omlx pool names the loaded model only to a caller
// holding the server's key, and wt is the one that resolves the registry's
// secret_ref. A script whose own keyless probe is refused asks here.
func TestServedReportsWhatAKeyedOmlxHasLoaded(t *testing.T) {
	srv := omlxServer(t, "sk-omlx")
	cfg := omlxServedCfg(srv.URL, "sk-omlx")

	var out bytes.Buffer
	if err := runServed(&out, cfg, srv.Client(), "omlx", true); err != nil {
		t.Fatalf("runServed --json: %v", err)
	}
	if got, want := out.String(), `{"provider":"omlx","served":["B"]}`+"\n"; got != want {
		t.Errorf("json = %q, want %q", got, want)
	}

	out.Reset()
	if err := runServed(&out, cfg, srv.Client(), "omlx", false); err != nil {
		t.Fatalf("runServed: %v", err)
	}
	if got := out.String(); got != "B\n" {
		t.Errorf("plain = %q, want %q", got, "B\n")
	}
}

// TestServedFailsRatherThanAnswerNothing: without the key the server will not
// say which model is loaded, and that must be an error — an empty list with
// exit 0 would read as "nothing is serving" and clear the flag of a model the
// server still has loaded.
func TestServedFailsRatherThanAnswerNothing(t *testing.T) {
	srv := omlxServer(t, "sk-omlx")

	var out bytes.Buffer
	err := runServed(&out, omlxServedCfg(srv.URL, ""), srv.Client(), "omlx", true)
	if err == nil || !strings.Contains(err.Error(), "omlx gave no usable answer") {
		t.Fatalf("err = %v, want a no-usable-answer error", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a failed probe, want nothing", out.String())
	}
}

// TestServedRefusesOllama: ollama has no "serving now" — a pulled model loads
// on request — so it is refused by name rather than answered with its pulls.
func TestServedRefusesOllama(t *testing.T) {
	var out bytes.Buffer
	err := runServed(&out, &config.Config{}, http.DefaultClient, "ollama", false)
	if err == nil || !strings.Contains(err.Error(), `unknown provider "ollama"`) {
		t.Fatalf("err = %v, want an unknown-provider error", err)
	}
}

// TestWarmSendsTheRegistryKeyToAKeyedOmlx pins `wt warm`, the step llmbench's
// omlx backend asks for when omlx refuses its keyless warmup (#256): the chat
// request carries the key the registry's omlx provider names, and names the
// model by its directory basename.
func TestWarmSendsTheRegistryKeyToAKeyedOmlx(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-omlx" {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
		}
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	if err := runWarm(context.Background(), &out, omlxServedCfg(srv.URL, "sk-omlx"), "omlx", "org/Qwen-4bit"); err != nil {
		t.Fatalf("runWarm: %v", err)
	}
	if gotAuth != "Bearer sk-omlx" || !strings.Contains(gotBody, `"model":"Qwen-4bit"`) {
		t.Errorf("auth = %q body = %s", gotAuth, gotBody)
	}
	if got := out.String(); got != "org/Qwen-4bit is loaded on omlx\n" {
		t.Errorf("out = %q", got)
	}

	// Without the key the refusal is the error, with the setting to change.
	out.Reset()
	err := runWarm(context.Background(), &out, omlxServedCfg(srv.URL, ""), "omlx", "Qwen-4bit")
	if err == nil || !strings.Contains(err.Error(), "auth.secret_ref") {
		t.Fatalf("err = %v, want one naming auth.secret_ref", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a refused warm, want nothing", out.String())
	}
}

// TestWarmRefusesOtherProviders: the other local servers take no key, so
// there is nothing wt can add to their caller's own warmup.
func TestWarmRefusesOtherProviders(t *testing.T) {
	var out bytes.Buffer
	err := runWarm(context.Background(), &out, &config.Config{}, "mtplx", "m")
	if err == nil || !strings.Contains(err.Error(), `unknown provider "mtplx"`) {
		t.Fatalf("err = %v, want an unknown-provider error", err)
	}
}

// TestStopBareOmlxHaltsTheService verifies `wt stop omlx` stops the omlx
// service as a whole, even with nothing loaded. A per-model stop only unloads,
// so this is the one command that frees the server's memory.
func TestStopBareOmlxHaltsTheService(t *testing.T) {
	stopped := stubStop(t, nil)
	var halted []string
	old := stopProvider
	stopProvider = func(_ context.Context, _ *config.Config, id string) error { halted = append(halted, id); return nil }
	t.Cleanup(func() { stopProvider = old })
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := runStop(&out, cfg, "omlx", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(halted, []string{"omlx"}) || len(*stopped) != 0 {
		t.Errorf("halted = %v, per-model stops = %v; want [omlx] and none", halted, *stopped)
	}
}

// stubHalt records every provider `wt stop` halts as a whole, and fails the
// ones named in fail.
func stubHalt(t *testing.T, fail ...string) *[]string {
	t.Helper()
	var halted []string
	old := stopProvider
	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		halted = append(halted, id)
		if slices.Contains(fail, id) {
			return errors.New("still listening")
		}
		return nil
	}
	t.Cleanup(func() { stopProvider = old })
	return &halted
}

// TestStopAllStopsEveryModelThenHaltsThePool verifies `wt stop --all` stops
// each running model of a non-pool provider through the stop loop and halts
// the omlx service once, without first unloading its models one by one. A
// user frees the machine with one command; unloading omlx's models would
// leave the server and its memory in place.
func TestStopAllStopsEveryModelThenHaltsThePool(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("omlx", "omlx/c", "c", 0)})
	halted := stubHalt(t)
	var out bytes.Buffer
	if err := runStopAll(&out, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" {
		t.Errorf("per-model stops = %v, want only ollama/a:1 (the omlx model goes down with its service)", *stopped)
	}
	if !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("halted = %v, want [omlx]", *halted)
	}
	if !strings.Contains(out.String(), "Stopping omlx... done") {
		t.Errorf("out = %q, want the halt reported", out.String())
	}
}

// TestStopAllHaltsOneServicePerPool verifies a registry with both an omlx
// and an omlx-6bit row halts the one omlx service once. Two halts would run
// `omlx stop` twice and print a second "Stopping omlx... done" for one
// service.
func TestStopAllHaltsOneServicePerPool(t *testing.T) {
	stubStop(t, nil)
	halted := stubHalt(t)
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "omlx", Location: config.LocationLocal}, {ID: "omlx-6bit", Location: config.LocationLocal},
	}}
	if err := runStopAll(io.Discard, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("halted = %v, want [omlx] once", *halted)
	}
}

// TestStopAllWithNothingToStop verifies `wt stop --all` on a machine with no
// running model and no pool provider says so and exits 0, so a cleanup script
// can call it unconditionally.
func TestStopAllWithNothingToStop(t *testing.T) {
	stopped := stubStop(t, nil)
	halted := stubHalt(t)
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := runStopAll(&out, cfg, false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 0 || len(*halted) != 0 || !strings.Contains(out.String(), "no running local models") {
		t.Errorf("stopped = %v halted = %v out = %q; want nothing stopped and a note", *stopped, *halted, out.String())
	}
}

// TestStopAllInUseConfirms verifies `wt stop --all` asks once, with the total
// session count, before stopping models other wt sessions are using; declining
// stops nothing at all, and --yes skips the question. Without the question a
// cleanup in one terminal kills the agent running in another.
func TestStopAllInUseConfirms(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
	halted := stubHalt(t)
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })

	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	err := runStopAll(io.Discard, modelCmdConfig(), false)
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") {
		t.Fatalf("declined: err = %v, want a cancellation", err)
	}
	if !strings.Contains(asked, "3 live wt session(s)") || !strings.Contains(asked, "ollama/a:1, omlx/c") {
		t.Errorf("asked = %q, want the total and both models", asked)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Fatalf("declined: stopped = %v halted = %v, want nothing", *stopped, *halted)
	}

	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := runStopAll(io.Discard, modelCmdConfig(), true); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("--yes: stopped = %v halted = %v, want the ollama model and the omlx halt", *stopped, *halted)
	}
}

// TestStopAllKeepsGoingAfterAFailure verifies a failed model stop does not
// stop `wt stop --all` from halting the pool service, and that the command
// still exits non-zero naming what failed. "Stop everything" that gives up at
// the first failure leaves the largest server running.
func TestStopAllKeepsGoingAfterAFailure(t *testing.T) {
	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	halted := stubHalt(t, "omlx")
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if err == nil || !strings.Contains(err.Error(), "1 of 1 stops failed") || !strings.Contains(err.Error(), "omlx: still listening") {
		t.Fatalf("err = %v, want both failures", err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) || !strings.Contains(out.String(), "Stopping omlx... failed") {
		t.Errorf("halted = %v out = %q, want the halt attempted and reported", *halted, out.String())
	}
}

// TestStopAllCancelledModelLoopLeavesThePoolUp verifies Ctrl+C during the
// per-model stops ends `wt stop --all` there: the omlx service is not halted,
// and the error says so. Filed as one more failure, the cancellation was
// stepped over and the command went on to halt the largest server the user
// had just asked it to leave alone.
func TestStopAllCancelledModelLoopLeavesThePoolUp(t *testing.T) {
	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return survey.ErrStopCancelled }
	halted := stubHalt(t)
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, survey.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if len(*halted) != 0 || strings.Contains(out.String(), "Stopping omlx") {
		t.Errorf("halted = %v out = %q, want no halt after a cancelled model loop", *halted, out.String())
	}
}

// TestStopAllCancelledHaltReportsCancelled verifies Ctrl+C while the omlx
// service is being halted prints "cancelled", not "failed", and the error
// names the service as not stopped. A killed `omlx stop` reported as a failure
// tells the user their provider is broken when they interrupted it.
func TestStopAllCancelledHaltReportsCancelled(t *testing.T) {
	stubStop(t, nil)
	oldCtx, oldStop := startSignalCtx, stopProvider
	t.Cleanup(func() { startSignalCtx, stopProvider = oldCtx, oldStop })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	stopProvider = func(c context.Context, _ *config.Config, _ string) error {
		cancel()
		return c.Err()
	}
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, survey.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopping omlx... cancelled") || strings.Contains(got, "failed") {
		t.Errorf("out = %q, want the halt reported as cancelled, not failed", got)
	}
}

// TestStopAllSettlesTheModelLoopsRestartBeforeAHalt verifies `wt stop --all`
// waits for the LiteLLM proxy restart the model loop may have started before
// it halts the omlx service, and does not wait when there was no model loop.
// Halting omlx starts a restart of its own; nothing else orders the two, and
// two at once bounce the proxy under each other.
func TestStopAllSettlesTheModelLoopsRestartBeforeAHalt(t *testing.T) {
	var events []string
	oldState, oldEntries, oldHalt, oldWait := stopState, stopEntries, stopProvider, waitPendingRoutes
	t.Cleanup(func() {
		stopState, stopEntries, stopProvider, waitPendingRoutes = oldState, oldEntries, oldHalt, oldWait
	})
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		events = append(events, "models")
		return nil
	}
	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		events = append(events, "halt "+id)
		return nil
	}
	waitPendingRoutes = func() { events = append(events, "wait") }

	stopState = func(*config.Config) survey.StopState {
		return survey.StopState{Candidates: []survey.Candidate{cand("mtplx", "mtplx/c", "c", 0)}}
	}
	if err := runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"models", "wait", "halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}

	events = nil
	stopState = func(*config.Config) survey.StopState { return survey.StopState{} }
	if err := runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("no model loop: events = %v, want %v (nothing to wait for)", events, want)
	}
}

// TestStopAllYesFlagSkipsTheQuestion verifies `wt stop --all --yes`, run as
// the command, stops in-use models without asking, and `wt stop --all` alone
// asks. The other --all tests call runStopAll with the flag's value already in
// hand; if the command stopped passing --yes, a cleanup script would block on
// a prompt (or fail with no terminal) and no test would say so.
func TestStopAllYesFlagSkipsTheQuestion(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
	halted := stubHalt(t)
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })
	asked := 0
	confirmStop = func(string) (bool, error) { asked++; return false, nil }
	run := func(args ...string) error {
		cmd := stopCmd(&app{cfg: modelCmdConfig()})
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}

	if err := run("--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if asked != 0 {
		t.Errorf("--all --yes asked %d time(s), want none", asked)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("--all --yes: stopped = %v halted = %v, want ollama/a:1 and the omlx halt", *stopped, *halted)
	}

	*stopped, *halted = nil, nil
	err := run("--all")
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") {
		t.Fatalf("--all, declined: err = %v, want a cancellation", err)
	}
	if asked != 1 || len(*stopped) != 0 || len(*halted) != 0 {
		t.Errorf("--all: asked = %d stopped = %v halted = %v, want one question and nothing stopped", asked, *stopped, *halted)
	}
}

// TestStopAllTakesNoArgument verifies `wt stop --all <something>` is refused
// before anything is probed or stopped: a user who meant one provider must
// not have every model stopped instead.
func TestStopAllTakesNoArgument(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	halted := stubHalt(t)
	cmd := stopCmd(&app{cfg: modelCmdConfig()})
	cmd.SetArgs([]string{"--all", "ollama"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "takes no model or provider") {
		t.Fatalf("err = %v, want the --all usage error", err)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Errorf("stopped = %v halted = %v, want nothing", *stopped, *halted)
	}
}

// TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp verifies a
// Ctrl+C the model loop did not report — it landed as the last model's stop
// completed, so the loop returned nil — still ends `wt stop --all` before the
// omlx halt. The loop's own signal context is gone once it returns; without
// one spanning the whole command that Ctrl+C is lost and the command goes on
// to halt the service the user had just asked it to leave alone.
func TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp(t *testing.T) {
	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	oldCtx, oldEntries := startSignalCtx, stopEntries
	t.Cleanup(func() { startSignalCtx, stopEntries = oldCtx, oldEntries })
	// A signal reaches the contexts installed when it arrives and no later
	// one, as with signal.NotifyContext: each call gets a context of its own,
	// and the "Ctrl+C" below cancels only those that exist by then.
	var installed []context.CancelFunc
	startSignalCtx = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		installed = append(installed, cancel)
		return ctx, cancel
	}
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		for _, cancel := range installed {
			cancel()
		}
		return nil
	}
	halted := stubHalt(t)
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, survey.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if len(*halted) != 0 || strings.Contains(out.String(), "Stopping omlx") {
		t.Errorf("halted = %v out = %q, want no halt after the Ctrl+C", *halted, out.String())
	}
}

// TestStopBareOmlxCancelledReportsCancelled verifies Ctrl+C during `wt stop
// omlx` prints "cancelled", not "failed", and the error says the service was
// not stopped — the same report `wt stop --all` gives for the same halt. A
// killed `omlx stop` reported as a failure tells the user their provider is
// broken when they interrupted it.
func TestStopBareOmlxCancelledReportsCancelled(t *testing.T) {
	stubStop(t, nil)
	oldCtx, oldStop := startSignalCtx, stopProvider
	t.Cleanup(func() { startSignalCtx, stopProvider = oldCtx, oldStop })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	stopProvider = func(c context.Context, _ *config.Config, _ string) error {
		cancel()
		return c.Err()
	}
	var out bytes.Buffer
	err := runStop(&out, modelCmdConfig(), "omlx", true)
	if !errors.Is(err, survey.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopping omlx... cancelled") || strings.Contains(got, "failed") {
		t.Errorf("out = %q, want the halt reported as cancelled, not failed", got)
	}
}

// TestStopAllFailurePrintsNoUsage verifies a `wt stop --all` that fails while
// stopping does not print the command's usage text, and one given an argument
// still does. A failed halt is not a usage mistake: the usage block would
// push the progress lines that say what was and was not stopped off screen.
func TestStopAllFailurePrintsNoUsage(t *testing.T) {
	stubStop(t, nil)
	stubHalt(t, "omlx")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := stopCmd(&app{cfg: modelCmdConfig()})
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	got, err := run("--all")
	if err == nil || !strings.Contains(err.Error(), "omlx: still listening") {
		t.Fatalf("err = %v, want the failed halt", err)
	}
	if strings.Contains(got, "Usage:") || !strings.Contains(got, "Stopping omlx... failed") {
		t.Errorf("out = %q, want the progress line and no usage text", got)
	}

	got, err = run("--all", "ollama")
	if err == nil || !strings.Contains(got, "Usage:") {
		t.Errorf("err = %v out = %q, want the usage text for an argument mistake", err, got)
	}
}

// TestNoHelpTextNamesModelman pins success criterion 1 of the retirement for
// wt's own help: no command's help may send a reader to modelman, which no
// longer exists. It walks the whole command tree, so a command added later is
// covered too. For each command it reads the short line, the long text, the
// examples, the deprecation notice and the usage text, and then every flag's
// description and deprecation notice one by one: the usage text alone leaves
// out the command's own short line and every hidden flag. The flags of a
// parent that a subcommand inherits (root's persistent ones) are read at the
// parent, where they are declared. The check is for
// the lowercase tool name: the env alias names (MODELMAN_REGISTRY and the
// three MODELMAN_LITELLM_ ones) are uppercase, are read forever, and may be
// named.
func TestNoHelpTextNamesModelman(t *testing.T) {
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		seen++
		for _, text := range []string{c.Short, c.Long, c.Example, c.Deprecated, c.UsageString()} {
			if strings.Contains(text, "modelman") {
				t.Errorf("%s: help names modelman:\n%s", c.CommandPath(), text)
			}
		}
		checkFlags := func(f *pflag.Flag) {
			if strings.Contains(f.Usage+f.Deprecated, "modelman") {
				t.Errorf("%s --%s: flag help names modelman: %s", c.CommandPath(), f.Name, f.Usage)
			}
		}
		// c.Flags() takes in the parent's persistent flags only when they are
		// merged for it — a side effect of resolving the usage template the
		// text loop above happened to trigger, not anything it promised.
		// Reading the command's own flags, local and persistent, directly
		// keeps the coverage from depending on that rendering; a flag both
		// sets hold after a merge may then be named twice, as two failures.
		c.Flags().VisitAll(checkFlags)
		c.PersistentFlags().VisitAll(checkFlags)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd())
	if seen < 20 {
		t.Fatalf("walked %d commands, want the whole tree (wt has more than 20)", seen)
	}
}

// TestStartDoesNotSayRunningForAServerThatIsGone runs `wt start` through the
// real start driver for #343's sequence: the engine starts the model, and by
// the end of the proxy wait its server is gone. The command must fail with the
// one line that says so and must not print "is running" — which it did, with
// exit 0, over a port nothing was listening on.
func TestStartDoesNotSayRunningForAServerThatIsGone(t *testing.T) {
	cfg, _ := startFixture(t)
	stubSignals(t)
	stubLifecycleStart(t, []error{nil})
	oldDriver, oldWait, oldConfirm, oldErr := startModel, waitPendingRoutes, confirmStarted, osStderr
	startModel, waitPendingRoutes, osStderr = startForLaunch, func() {}, io.Discard
	confirmStarted = func(context.Context, *config.Config, lifecycle.Target) error {
		return &lifecycle.StoppedError{Why: "ollama no longer answers at http://127.0.0.1:11434"}
	}
	t.Cleanup(func() {
		startModel, waitPendingRoutes, confirmStarted, osStderr = oldDriver, oldWait, oldConfirm, oldErr
	})

	var out bytes.Buffer
	err := runStart(&out, cfg, themes.Theme{}, "ollama/b:1", false)
	if err == nil || !strings.HasPrefix(err.Error(), "ollama/b:1 is not running: ") || strings.Contains(err.Error(), "\n") {
		t.Errorf("runStart() error = %v, want one line saying ollama/b:1 is not running", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing: the model is not running", out.String())
	}
}
