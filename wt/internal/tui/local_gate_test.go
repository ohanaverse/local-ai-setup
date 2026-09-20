package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func gateTestConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
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
	return cfg
}

// TestEnterModelPhaseNoMarkerShowsLocalModelsAsNonRunning asserts the
// selector-table replacement of the old gate's hiding: with an empty
// inventory (nothing running) the configured local model still appears, as a
// non-launchable row whose Enter hint says how to start it, next to the cloud
// model. Hiding it (the old behavior) is what the table exists to fix.
func TestEnterModelPhaseNoMarkerShowsLocalModelsAsNonRunning(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	cfg := gateTestConfig()
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	items := got.models.Items()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (cloud + non-running local)", len(items))
	}
	byID := map[string]*modelItem{}
	for _, it := range items {
		byID[it.(*modelItem).model.ID] = it.(*modelItem)
	}
	if byID["claude/opus"] == nil || byID["claude/opus"].blocked != "" {
		t.Errorf("claude/opus must be a launchable row, got %+v", byID["claude/opus"])
	}
	if l := byID["omlx/qwen3.8"]; l == nil || l.blocked == "" {
		t.Errorf("omlx/qwen3.8 must be a blocked (non-running) row, got %+v", l)
	}
}

// TestEnterModelPhaseRunningLocalModelIsLaunchable asserts that an inventory
// reporting a configured local model as running makes its row launchable
// (blocked == ""), alongside the cloud row. The inventory replaces the old
// httptest probe stand-in: running-state now comes from localmodels.
func TestEnterModelPhaseRunningLocalModelIsLaunchable(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true, Running: true},
	}})
	cfg := gateTestConfig()
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if len(got.models.Items()) != 2 {
		t.Fatalf("got %d items, want 2 (cloud + running local)", len(got.models.Items()))
	}
	for _, it := range got.models.Items() {
		if mi := it.(*modelItem); mi.blocked != "" {
			t.Errorf("%s blocked = %q, want launchable", mi.model.ID, mi.blocked)
		}
	}
}

// TestEnterModelPhaseMixedRunningState asserts that with one local model
// running and another not, the running one is launchable and the other is
// blocked — a non-running model neither hides the running one nor quits the
// program (the old "drifted marker" concern, now expressed as row state).
func TestEnterModelPhaseMixedRunningState(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true, Running: true},
		{ProviderID: "omlx", Artifact: "other", ModelID: "omlx/other", Registered: true},
	}})
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}},
			{ID: "omlx/other", ProviderID: "omlx", ModelName: "other", Family: "other", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "omlx"}},
		},
	}
	cfg.ExposeAllForTest()
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, cmd := m.enterModelPhase("claude", models, "code")

	if got.fatalErr != nil || cmd != nil {
		t.Fatalf("fatalErr = %v, cmd = %v; want neither (no quit)", got.fatalErr, cmd)
	}
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", got.phase)
	}
	blocked := map[string]string{}
	for _, it := range got.models.Items() {
		mi := it.(*modelItem)
		blocked[mi.model.ID] = mi.blocked
	}
	if len(blocked) != 3 {
		t.Fatalf("got %d items, want 3", len(blocked))
	}
	if blocked["omlx/qwen3.8"] != "" {
		t.Errorf("running omlx/qwen3.8 blocked = %q, want launchable", blocked["omlx/qwen3.8"])
	}
	if blocked["omlx/other"] == "" {
		t.Errorf("non-running omlx/other must be blocked")
	}
}

// TestEnterModelPhasePinnedStaleLocalModelRejected asserts the pinned-
// model guard: -M pinning a local model that is not the currently-running
// one is rejected with the same "start it in modelman" message the CLI
// path uses, not the generic "not in the eligible list" message.
func TestEnterModelPhasePinnedStaleLocalModelRejected(t *testing.T) {
	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("") // nothing running
	m := model{cfg: cfg, width: 80, height: 24, pinnedModel: "omlx/qwen3.8"}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "modelman start") {
		t.Errorf("status = %q, want it to mention `modelman start`", got.status)
	}
}

// TestEnterModelPhasePinnedNotInEligibleRoutesBack asserts a -M pin that is
// missing from the agent's eligible list routes back to the agent picker with
// the generic "not in the eligible list" status, not the `modelman start`
// hint. It matters because enterModelPhase's two route-backs mean different
// things: the hint belongs to the local-pin rejection (a flagged local model
// the gate could not verify as running), so showing it for a pin the agent
// cannot use at all would send users off to start a model that was never the
// problem. The test above covers the hint's own path.
func TestEnterModelPhasePinnedNotInEligibleRoutesBack(t *testing.T) {
	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("") // nothing running (irrelevant to this pin's failure)
	m := model{cfg: cfg, width: 80, height: 24, pinnedModel: "claude/missing"}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if strings.Contains(got.status, "modelman start") {
		t.Errorf("status = %q; the gate message must not be used for a generic pin miss", got.status)
	}
}
