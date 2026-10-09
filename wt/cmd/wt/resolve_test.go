package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
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
// `wt start`. It must NOT pass replace: a plain pin never opts into
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
// model whose provider wt cannot start (mlx_lm_server) reports the llmbench
// command that starts the pairing, with the target and the draft the registry
// row records, instead of silently doing nothing — the user needs to know
// which tool owns that provider's lifecycle.
func TestResolveModelPinOnNoEngineProviderReportsReason(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Tags: []string{"code"},
			Fetch: config.ModelArtifact{Repo: "org/target"}, Draft: config.ModelArtifact{Repo: "org/draft"}}},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"mlx_lm_server"}}},
	}

	_, _, err := resolveModel("pi", cfg, "", "", "mlx_lm_server/p")
	if err == nil || !strings.Contains(err.Error(), "`llmbench provider isolate --solo mlx_lm_server org/target --draft org/draft`") || strings.Contains(err.Error(), "modelman") {
		t.Errorf("err = %v, want the llmbench command for this pairing", err)
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
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Artifact: "qwen3.8", Registered: true, ArtifactKnown: true}},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}

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
			{ProviderID: "mtplx", ModelID: "mtplx/registered", Artifact: "registered", ModelName: "registered", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "mtplx/registered", ProviderID: "mtplx", ModelName: "registered", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mtplx"}}},
	}

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

// TestResolveModelPinOnDiscoveredForcedThroughLitellm pins #179 Phase B's
// end of the "not in LiteLLM" refusal: codex speaks only openai-responses, so
// its route to omlx is forced through the proxy, and a -M pin on a discovered
// model now starts it (idle) or launches it (running) instead of being
// refused — wt routes a discovered model under its discovered id as soon as
// it runs. Before, the only way to use one from codex was to register it.
func TestResolveModelPinOnDiscoveredForcedThroughLitellm(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "codex", SupportedProviders: []string{"omlx"}}},
	}
	// Routing off but configured: codex's protocol forces LiteLLM anyway.
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test"})
	for _, running := range []bool{false, true} {
		stubProbeInventory(t, localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
			Entries: []localmodels.Entry{
				{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", ArtifactKnown: true, Running: running},
			},
		})
		calls := stubStartDriver(t, nil)
		m, _, err := resolveModel("codex", cfg, "", "", disc)
		if err != nil || m.ID != disc {
			t.Fatalf("running=%v: resolveModel = (%q, %v), want the discovered model", running, m.ID, err)
		}
		if calls.called == running {
			t.Errorf("running=%v: start driver called = %v, want a start only for the idle model", running, calls.called)
		}
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

// TestResolveModelDiscoveredUnderLitellmNeverRotatedInto verifies the no-pin
// path under LiteLLM routing (#179 Phase B): a running discovered model is
// now launchable — it is routed, so the picker no longer refuses it — but
// rotation still never lands on it: with a running registry model beside it,
// resolveModel reports the ambiguity and the rotation fallback picks the
// registry model, because rotation walks the registry's order.
func TestResolveModelDiscoveredUnderLitellmNeverRotatedInto(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/reg", Artifact: "reg", ModelName: "reg", Registered: true, ArtifactKnown: true, Running: true},
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", ArtifactKnown: true, Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models:    []config.Model{{ID: "omlx/reg", ProviderID: "omlx", ModelName: "reg", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})

	_, launchable, err := resolveModel("pi", cfg, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "multiple models match") {
		t.Fatalf("err = %v, want the multiple-models ambiguity", err)
	}
	if len(launchable) != 2 {
		t.Fatalf("launchable = %v, want the registry and the discovered model", launchable)
	}
	if next, ok := rotation.NewAt(t.TempDir()).NextFromEligible(launchable, cfg); !ok || next.ID != "omlx/reg" {
		t.Errorf("rotation picked (%q, %v), want the registry model", next.ID, ok)
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

	_, _, err := resolveModel("pi", cfg, "design", "", "") // -T filter matches nothing
	if err == nil || !strings.Contains(err.Error(), "no models match") {
		t.Errorf("err = %v, want the generic no-models-match error", err)
	}
}

// offDiskResolveFixture is a pi agent over two omlx models in different
// families, with the probe confirming omlx/gone is missing from disk and
// omlx/here on disk — the setup for the zero-rows and filtered-pin cases.
func offDiskResolveFixture(t *testing.T) *config.Config {
	t.Helper()
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/gone", ModelName: "gone", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/here", ModelName: "here", Artifact: "here", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone", Family: "a", Tags: []string{"code"}},
			{ID: "omlx/here", ProviderID: "omlx", ModelName: "here", Family: "b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	return cfg
}

// TestResolveModelAllOffDiskNamesTheCause verifies an unpinned launch whose
// eligible list is non-empty but builds zero rows (every eligible model is a
// local one confirmed not on disk) returns catalog.NoRowsReason — not the
// generic "no models match ... tags/family" wording, which would send the
// operator hunting for a -T/-F problem that is not there.
func TestResolveModelAllOffDiskNamesTheCause(t *testing.T) {
	cfg := offDiskResolveFixture(t)

	_, _, err := resolveModel("pi", cfg, "", "a", "")
	if err == nil || err.Error() != catalog.NoRowsReason("pi") {
		t.Errorf("err = %v, want %q", err, catalog.NoRowsReason("pi"))
	}
}

// TestResolveModelPinFilteredOutIsNotToldToPull verifies a -M pin that -F
// removed from the eligible list is refused as "not in the eligible list"
// even though it is also not on disk: the pull hint would be a lie, because
// after pulling the same pin would still be ineligible.
func TestResolveModelPinFilteredOutIsNotToldToPull(t *testing.T) {
	cfg := offDiskResolveFixture(t)

	_, _, err := resolveModel("pi", cfg, "", "b", "omlx/gone")
	if err == nil || !strings.Contains(err.Error(), "not in the eligible list") || strings.Contains(err.Error(), "not on disk") {
		t.Errorf("err = %v, want the eligibility wording, not the pull hint", err)
	}
}

// TestResolveModelSoleRunningDiscoveredAutoResolves pins a Phase B ruling
// (#179): when the only launchable row is a running discovered model, a
// launch with no -M resolves to it — under LiteLLM routing as in direct mode,
// now that it is routed. The spec restricts rotation (which never lands on a
// discovered row), not this single-launchable-row shortcut; this test makes
// that a decision rather than an accident.
func TestResolveModelSoleRunningDiscoveredAutoResolves(t *testing.T) {
	disc := config.DiscoveredModelID("omlx", "extra")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: disc, Artifact: "extra", ModelName: "extra", ArtifactKnown: true, Running: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	for _, enabled := range []bool{false, true} {
		cfg.SetLitellmForTest(config.LitellmState{Enabled: enabled, URL: "http://localhost:4000", APIKey: "sk-test"})
		m, launchable, err := resolveModel("pi", cfg, "", "", "")
		if err != nil || m.ID != disc || len(launchable) != 1 {
			t.Errorf("litellm enabled=%v: resolveModel = (%q, %d launchable, %v), want the discovered model", enabled, m.ID, len(launchable), err)
		}
	}
	if calls.called {
		t.Error("a launch row must not reach the start driver")
	}
}
