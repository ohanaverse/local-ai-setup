package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestResolveModel covers the non-TUI model resolution path used after
// -W/--cwd has resolved the worktree and -A has resolved the agent.
// This is the gate that catches config errors and -M mismatches before
// launching an agent.
func TestResolveModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "ollama", ModelName: "sonnet", Family: "sonnet", Tags: []string{"design"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"claude", "ollama"}},
		},
	}
	cfg.ExposeAllForTest()

	// resolveModel errors on an ambiguous LAUNCHABLE list rather than
	// falling back to any single model. launch.go calls resolveModel and
	// handles the error via rotation.

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		pinned  string
		wantID  string
		wantErr bool
	}{
		{"single match", "claude", "", "", "", "claude/opus", false},
		{"pinned cloud in eligible", "pi", "", "", "claude/opus", "claude/opus", false},
		{"pinned not in eligible", "pi", "", "", "ollama/missing", "", true},
		{"pinned wrong provider for agent", "claude", "", "", "ollama/gemma4:9b", "", true},
		// Two local rows are startable, not launchable: they do not make the
		// choice ambiguous, and rotation must never pick one.
		{"only cloud rows are launchable", "pi", "", "", "", "claude/opus", false},
		{"all-local list needs a pin", "pi", "code", "gemma4", "", "", true},
		{"empty eligible errors", "claude", "design", "", "", "", true},
		{"unknown agent", "nope", "", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, err := resolveModel(tc.agent, cfg, tc.tags, tc.family, tc.pinned)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if m.ID != tc.wantID {
				t.Errorf("got ID %q, want %q", m.ID, tc.wantID)
			}
		})
	}

	// A command agent returns the errCommandAgent sentinel specifically, so
	// launchFiltered's errors.Is(err, errCommandAgent) dispatch matches. This
	// locks the sentinel identity the dispatch depends on.
	if _, _, err := resolveModel("shell", cfg, "", "", ""); !errors.Is(err, errCommandAgent) {
		t.Errorf("resolveModel(shell) err = %v, want errCommandAgent", err)
	}
}

// TestResolveModelReturnsLaunchable verifies resolveModel returns the
// launchable list even when it errors, so callers can reuse it instead of
// calling cfg.EligibleModels a second time. Returning the raw eligible list
// would let rotation pick a non-running local model.
func TestResolveModelReturnsLaunchable(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", Tags: []string{"code"}},
			{ID: "openrouter/b", ProviderID: "openrouter", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter"}}},
	}
	cfg.ExposeAllForTest()

	m, launchable, err := resolveModel("claude", cfg, "", "", "")
	if err == nil {
		t.Fatal("expected multiple-models error")
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero", m.ID)
	}
	if len(launchable) != 2 {
		t.Fatalf("launchable = %d, want 2", len(launchable))
	}
}

// TestResolveModelRunningLocalModelIsLaunchable verifies a local model the
// live probe reports as running resolves without any start: the row is a
// launch row, so the launch proceeds directly. This is the replacement for
// the retired localgate probe test — the verdict now comes from the same
// inventory the picker shows.
func TestResolveModelRunningLocalModelIsLaunchable(t *testing.T) {
	calls := stubStartDriver(t, nil)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Artifact: "qwen3.8", ModelName: "qwen3.8", Registered: true, Running: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", "")
	if err != nil || m.ID != "omlx/qwen3.8" {
		t.Fatalf("resolveModel() = (%v, %v), want (omlx/qwen3.8, nil)", m, err)
	}
	if calls.called {
		t.Error("a running model must not be started")
	}
}

// TestResolveModelPinOnIdleLocalStartsIt verifies -M pinning a configured
// local model that is not running starts it through the driver and returns
// the model, so `wt -A pi -M omlx/qwen3.8` works without a separate
// `modelman start`. It must NOT pass replace: a plain pin never opts into
// stopping a running model.
func TestResolveModelPinOnIdleLocalStartsIt(t *testing.T) {
	allowReplace = false
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Artifact: "qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		// BaseURL so the pin's route guard (picker parity) resolves a launch
		// route; without it the row is a blocked row the picker refuses.
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", "omlx/qwen3.8")
	if err != nil || m.ID != "omlx/qwen3.8" {
		t.Fatalf("resolveModel() = (%v, %v), want (omlx/qwen3.8, nil)", m, err)
	}
	if !calls.called {
		t.Fatal("the start driver was not called for a non-running pinned local model")
	}
	if calls.row.Model.ID != "omlx/qwen3.8" || calls.row.Model.ModelName != "qwen3.8" {
		t.Errorf("start request row = %+v, want omlx/qwen3.8", calls.row.Model)
	}
	if calls.replace {
		t.Error("a plain pin must not pass replace, or it could stop a running model unasked")
	}
}

// TestResolveModelPinOnDiscoveredLocalStartsIt verifies the pin is looked up
// among ALL rows, discovered ones included: a running-state model on disk
// with no registry entry can be started by pinning its discovered id, which
// is the whole point of the live-row lookup.
func TestResolveModelPinOnDiscoveredLocalStartsIt(t *testing.T) {
	disc := config.DiscoveredModelID("mtplx", "on-disk")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "mtplx", ModelID: disc, Artifact: "on-disk", ModelName: "on-disk"},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		// BaseURL so the pin's route guard (picker parity) resolves a launch
		// route; without it the row is a blocked row the picker refuses.
		Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mtplx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", disc)
	if err != nil || m.ID != disc {
		t.Fatalf("resolveModel() = (%v, %v), want (%s, nil)", m, err, disc)
	}
	if !calls.called || calls.row.Model.ModelName != "on-disk" {
		t.Errorf("start request = %+v, want the discovered artifact on-disk", calls.row.Model)
	}
}

// TestResolveModelPinOnAbsentLocalReportsReason verifies pinning a local
// model the probe confirmed is missing on disk fails with the pull/download
// message and starts nothing: the engine would otherwise wait out its warmup
// on a model that is not there.
func TestResolveModelPinOnAbsentLocalReportsReason(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	_, launchable, err := resolveModel("pi", cfg, "", "", "omlx/gone")
	if err == nil || !strings.Contains(err.Error(), "not on disk") {
		t.Errorf("err = %v, want the not-on-disk message", err)
	}
	// The contract: callers reuse this list instead of recomputing it, and
	// rotation must never see a start row, so an error path still returns
	// it empty (nothing is running in this fixture).
	if len(launchable) != 0 {
		t.Errorf("launchable = %d models, want 0", len(launchable))
	}
	if calls.called {
		t.Error("an absent model must not reach the start driver")
	}
}

// TestResolveModelPinOnNoEngineProviderReportsReason verifies pinning a local
// model whose provider wt cannot start (mlx_lm_server) reports the
// `modelman start` hint instead of silently doing nothing — the user needs to
// know which tool owns that provider's lifecycle.
func TestResolveModelPinOnNoEngineProviderReportsReason(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mlx_lm_server"}}},
	}
	cfg.ExposeAllForTest()

	_, _, err := resolveModel("pi", cfg, "", "", "mlx_lm_server/p")
	if err == nil || !strings.Contains(err.Error(), "modelman start mlx_lm_server/p") {
		t.Errorf("err = %v, want the modelman start hint", err)
	}
}

// TestResolveModelAllLocalGivesPinMessage asserts the empty-launchable error:
// when every eligible model is local and none is running, resolveModel must
// say a pin would start one — NOT the generic "no models match" wording,
// which would send the operator hunting for a -T/-F/config problem that is
// not there. It also asserts the launchable list is still returned.
func TestResolveModelAllLocalGivesPinMessage(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true}},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	_, launchable, err := resolveModel("pi", cfg, "", "", "")
	if err == nil {
		t.Fatal("expected an error when nothing is launchable")
	}
	if !strings.Contains(err.Error(), "no cloud or running local model") || !strings.Contains(err.Error(), "wt -M <id>") {
		t.Errorf("err = %q, want the pin-the-model message", err)
	}
	if strings.Contains(err.Error(), "no models match") {
		t.Errorf("err = %q; generic no-match wording must not be used here", err)
	}
	if len(launchable) != 0 {
		t.Errorf("launchable = %d models, want 0", len(launchable))
	}
}

// TestResolveModelFiltersHideDiscoveredRows verifies -T/-F drop discovered
// rows on the non-TUI path, exactly as the picker does: a running discovered
// model must not be auto-launched (or be launchable) when the operator asked
// for tags/family, because a discovered row carries no tags the filter could
// have matched.
func TestResolveModelFiltersHideDiscoveredRows(t *testing.T) {
	disc := config.DiscoveredModelID("mtplx", "stray")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "mtplx", ModelID: disc, Artifact: "stray", ModelName: "stray", Running: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "mtplx/registered", ProviderID: "mtplx", ModelName: "registered", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mtplx"}}},
	}
	cfg.ExposeAllForTest()

	m, launchable, err := resolveModel("pi", cfg, "code", "", "")
	if err == nil {
		t.Fatalf("resolveModel() = (%v, %v), want an error: the running discovered row must be hidden by -T", m.ID, launchable)
	}
	if !strings.Contains(err.Error(), "no cloud or running local model") {
		t.Errorf("err = %q, want the pin-the-model wording", err)
	}
	if len(launchable) != 0 {
		t.Errorf("launchable = %d models, want 0 (the discovered row is filtered out)", len(launchable))
	}
}

// TestResolveModelPinOnDiscoveredUnderLitellmRefuses verifies the non-TUI pin
// path applies the same route guard the picker does: a discovered start row
// under LiteLLM routing is refused with the picker's wording and starts
// nothing, so the two paths cannot disagree about the same pin.
func TestResolveModelPinOnDiscoveredUnderLitellmRefuses(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra"},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})

	_, _, err := resolveModel("pi", cfg, "", "", disc)
	if err == nil || !strings.Contains(err.Error(), "not in LiteLLM") {
		t.Errorf("err = %v, want the picker's not-in-LiteLLM wording", err)
	}
	// Advice must name a command that changes the state wt reads (wt owns
	// routing now; `modelman litellm off` would not).
	if err != nil && (!strings.Contains(err.Error(), "wt litellm off") || strings.Contains(err.Error(), "modelman litellm")) {
		t.Errorf("hint = %v, want `wt litellm off`", err)
	}
	if calls.called {
		t.Error("a route-blocked pin must not reach the start driver")
	}
}

// TestResolveModelPinOnUnresolvableRouteRefuses is the guard's second
// condition, mirroring renderTable's err != nil branch: a start row whose
// ResolveRoute fails (here the agent's protocol forces LiteLLM and it is
// unconfigured) must be refused with the picker's "cannot be launched"
// wording BEFORE the start engine runs, which under --replace would stop a
// running occupant for a launch that cannot resolve anyway.
func TestResolveModelPinOnUnresolvableRouteRefuses(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true}},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	// No SetLitellmForTest: the claude driver speaks only Anthropic, omlx
	// only OpenAI chat, so ResolveRoute forces LiteLLM and finds it
	// unconfigured — the same error renderTable blocks the row on.

	_, _, err := resolveModel("claude", cfg, "", "", "omlx/q")
	if err == nil || !strings.Contains(err.Error(), "cannot be launched") {
		t.Errorf("err = %v, want the picker's cannot-be-launched wording", err)
	}
	if !strings.Contains(err.Error(), "litellm routing is required") {
		t.Errorf("err = %v, want the underlying ResolveRoute reason", err)
	}
	if calls.called {
		t.Error("a route-blocked pin must not reach the start driver")
	}
}

// TestResolveModelPinOnRunningDiscoveredUnderLitellmRefuses extends the pin
// parity guard to the row the start-row-only guard missed: a RUNNING
// discovered row is a launch row, and modeltable.go's route switch blocks it
// before the action matters — so a -M pin on it must refuse here too instead
// of launching straight into a route the proxy cannot serve.
func TestResolveModelPinOnRunningDiscoveredUnderLitellmRefuses(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})

	_, _, err := resolveModel("pi", cfg, "", "", disc)
	if err == nil || !strings.Contains(err.Error(), "not in LiteLLM") {
		t.Errorf("err = %v, want the picker's not-in-LiteLLM wording", err)
	}
	if calls.called {
		t.Error("a route-blocked pin must not reach the launch path or the start driver")
	}
}

// TestResolveModelRotationSkipsDiscoveredUnderLitellm verifies the same rule
// on the no-pin path: a discovered running model under LiteLLM routing is
// unselectable in the picker, so rotation must never land on it either — the
// launchable list comes back empty and resolveModel reports the
// pin-the-model wording instead of silently launching through a dead route.
func TestResolveModelRotationSkipsDiscoveredUnderLitellm(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})

	m, launchable, err := resolveModel("pi", cfg, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "no cloud or running local model") {
		t.Errorf("err = %v, want the no-launchable-model wording", err)
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero", m.ID)
	}
	if len(launchable) != 0 {
		t.Errorf("launchable = %d models, want 0 (rotation must never pick a discovered model the picker refuses)", len(launchable))
	}
	if calls.called {
		t.Error("rotation must not start anything")
	}
}

// TestResolveModelPinOnRunningDiscoveredLaunches pins the non-blocked side of
// the guard: with LiteLLM routing OFF, the same discovered running row
// resolves a direct route and the launch proceeds — the guard must refuse
// exactly what the picker refuses, not every discovered row.
func TestResolveModelPinOnRunningDiscoveredLaunches(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	// No SetLitellmForTest: LiteLLM routing is off, so the route dials omlx
	// directly through BaseURL and the picker leaves the row selectable.

	m, launchable, err := resolveModel("pi", cfg, "", "", disc)
	if err != nil {
		t.Fatalf("resolveModel() error = %v", err)
	}
	if m.ID != disc {
		t.Errorf("model = %q, want the pinned discovered id %q", m.ID, disc)
	}
	if calls.called {
		t.Error("a launch row must not reach the start driver")
	}
	if len(launchable) != 1 {
		t.Errorf("launchable = %d models, want 1 (the row is rotation-eligible too)", len(launchable))
	}
}

// TestResolveModelStartFailurePropagates verifies a start-driver failure is
// returned as resolveModel's error rather than swallowed: the caller must
// abort the launch with the engine's own message (daemon down, port busy,
// occupancy unknown, …) instead of running the agent against nothing.
func TestResolveModelStartFailurePropagates(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true}},
	})
	boom := errors.New("omlx is not answering at http://localhost:8000")
	stubStartDriver(t, boom)
	cfg := &config.Config{
		// BaseURL so the pin's route guard (picker parity) resolves a launch
		// route; without it the row is a blocked row the picker refuses.
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	m, launchable, err := resolveModel("pi", cfg, "", "", "omlx/q")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the driver's error", err)
	}
	// Same contract as the other error paths: callers reuse this list
	// instead of recomputing it, and rotation must never see a start row —
	// the pinned model failed to start and nothing else is running, so it
	// comes back empty.
	if len(launchable) != 0 {
		t.Errorf("launchable = %d models, want 0", len(launchable))
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero on a failed start", m.ID)
	}
}

// TestResolveModelReplaceFlagReachesDriver verifies --replace is passed
// through to the driver, which is what lets a non-interactive caller opt into
// stopping a running occupant. Without it every occupied provider would need
// a TTY prompt, breaking scripted launches.
func TestResolveModelReplaceFlagReachesDriver(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true}},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		// BaseURL so the pin's route guard (picker parity) resolves a launch
		// route; without it the row is a blocked row the picker refuses.
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	allowReplace = true
	t.Cleanup(func() { allowReplace = false })

	if _, _, err := resolveModel("pi", cfg, "", "", "omlx/q"); err != nil {
		t.Fatalf("resolveModel() error = %v", err)
	}
	if !calls.replace {
		t.Error("--replace must reach the driver's allowReplace argument")
	}
}

// TestResolveModelNoMatchKeepsGenericMessage is the control for the
// all-local case: with no models at all matching the filters, an empty
// eligible list still gets the pre-existing generic "no models match" error.
func TestResolveModelNoMatchKeepsGenericMessage(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "ollama/code", ProviderID: "ollama", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"ollama"}}},
	}
	cfg.ExposeAllForTest()

	_, _, err := resolveModel("pi", cfg, "design", "", "") // -T filter matches nothing
	if err == nil || !strings.Contains(err.Error(), "no models match") {
		t.Errorf("err = %v, want the generic no-models-match error", err)
	}
}
