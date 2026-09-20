// Tests for the -M pin's verdict inside the model phase. The pin is resolved
// against the same rows the table shows, so what the user sees and what the
// pin does cannot disagree — the property the retired localgate probe could
// not guarantee, because it answered a different question from a different
// probe.
package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func modelTestConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "omlx"}},
		},
	}
	cfg.ExposeAllForTest()
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	return cfg
}

// runningOmlxSnapshot is the live-probe answer the fixture's local model needs
// to read as a launch row.
func runningOmlxSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, Running: true, ArtifactKnown: true},
		},
	}
}

// TestPinOnRunningLocalModelLaunches verifies a -M pin naming a local model
// the live probe reports as running proceeds straight to launch, with no start
// attempt — re-probing or starting an already-serving model would waste a
// warmup and could stop the very model the user pinned.
func TestPinOnRunningLocalModelLaunches(t *testing.T) {
	requireBinary(t, "claude")
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	cfg := modelTestConfig()
	// A pinned running model launches at once, which would leave no table to
	// inspect; strip the fixture's routing so the launch bails back to the
	// picker, the state this test asserts on.
	cfg.SetLitellmForTest(config.LitellmState{})
	cfg.Providers[1].Protocols, cfg.Providers[1].Auth.BaseURL = nil, ""
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	idx := indexOfID(got, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("pinned row missing: %v", itemIDs(got))
	}
	it := got.models.Items()[idx].(*modelItem)
	if it.blocked != "" || it.start {
		t.Errorf("running pinned row: blocked = %q start = %v, want a launch row", it.blocked, it.start)
	}
	// The launch branch ran, not the route-back: proceedToLaunch captured the
	// pinned row in launchModel before the stripped-routing launch attempt
	// failed into status ("launch failed: …"). A route-back lands in
	// phaseAgent with the row's reason in status, clears pinnedModel, and
	// leaves launchModel zero — launchModel is the structural signal that
	// the launch branch ran.
	if got.launchModel.ID != "omlx/qwen3.8" {
		t.Errorf("launchModel = %q, want omlx/qwen3.8 (the launch branch ran, not the route-back)", got.launchModel.ID)
	}
}

// TestPinOnIdleLocalSelectsItsStartRow verifies a -M pin on a non-running
// local model lands on the picker with that row selected and marked as a start
// row, instead of launching something that is not up. The user presses Enter
// to run it through the same start flow every other start row uses.
func TestPinOnIdleLocalSelectsItsStartRow(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel (the pin needs a start, so show it)", got.phase)
	}
	if id := selectedModelID(got); id != "omlx/qwen3.8" {
		t.Errorf("cursor on %q, want the pinned row omlx/qwen3.8", id)
	}
	it := got.models.SelectedItem().(*modelItem)
	if !it.start || it.blocked != "" {
		t.Errorf("pinned idle row: start = %v blocked = %q, want a start row", it.start, it.blocked)
	}
}

// TestPinOnAbsentLocalRoutesBack verifies a -M pin the probe confirmed is
// missing on disk routes back to the agent picker with the pull/download
// reason: the row cannot be started, and the picker is not the place to
// explain a download the user has to perform elsewhere.
func TestPinOnAbsentLocalRoutesBack(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "not on disk") {
		t.Errorf("status = %q, want the not-on-disk reason", got.status)
	}
}

// TestPinNotInEligibleRoutesBack asserts a -M pin missing from the agent's
// eligible list routes back with the generic "not in the eligible list"
// status, not a blocked-row reason. It matters because enterModelPhase's two
// route-backs mean different things: a row reason belongs to a model the agent
// *could* use, so showing one for a pin the agent cannot use at all would send
// users off to start a model that was never the problem.
func TestPinNotInEligibleRoutesBack(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "claude/missing", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "not in the eligible list") {
		t.Errorf("status = %q, want the eligibility wording", got.status)
	}
	if strings.Contains(got.status, "modelman start") || strings.Contains(got.status, "not on disk") {
		t.Errorf("status = %q; a row reason must not be used for a pin the agent cannot use", got.status)
	}
}

// TestPinOnDiscoveredLocalStartsIt verifies a -M pin naming a discovered
// on-disk model (no registry entry) resolves to its own start row: the pin is
// looked up among all rows, so a model the registry does not name is still
// usable, which is what makes the live-row union worth showing.
func TestPinOnDiscoveredLocalStartsIt(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	disc := config.DiscoveredModelID("omlx", "extra")
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "extra", ModelID: disc, ModelName: "extra"},
		},
	})
	cfg := modelTestConfig()
	// Discovered rows are unselectable under LiteLLM routing (they are not in
	// its model_list), so the fixture routes claude straight to omlx: the
	// shared anthropic protocol gives claude a direct overlap with the
	// provider, and the pin resolves to a start row.
	cfg.SetLitellmForTest(config.LitellmState{})
	cfg.Providers[1].Protocols = append(cfg.Providers[1].Protocols, config.ProtocolAnthropic)
	m := model{cfg: cfg, agent: "claude", pinnedModel: disc, selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", got.phase)
	}
	if id := selectedModelID(got); id != disc {
		t.Errorf("cursor on %q, want the discovered pinned row %q", id, disc)
	}
	if it := got.models.SelectedItem().(*modelItem); !it.start {
		t.Error("a pinned non-running discovered model must be a start row")
	}
}
