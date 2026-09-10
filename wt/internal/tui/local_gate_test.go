package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
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

// TestEnterModelPhaseNoMarkerHidesLocalModels asserts the picker-filter
// half of issue #65's gate: with no [local].running_model marker (the
// gate active but empty), a local model is not offered — only cloud
// models appear, matching "no marker → cloud only, by construction" in
// the design doc.
func TestEnterModelPhaseNoMarkerHidesLocalModels(t *testing.T) {
	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("") // gate active, nothing running
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, _ := m.enterModelPhase("claude", models, fullCatalog, "code")

	items := got.models.Items()
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (cloud model only)", len(items))
	}
	if items[0].(*modelItem).model.ID != "claude/opus" {
		t.Errorf("got model %q, want claude/opus", items[0].(*modelItem).model.ID)
	}
}

// TestEnterModelPhaseVerifiedMarkerShowsLocalModel asserts that once the
// marker names a model that verifies as actually running (an
// httptest.Server standing in for omlx's /v1/models probe), it appears in
// the picker alongside cloud models.
func TestEnterModelPhaseVerifiedMarkerShowsLocalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer localgate.SetOmlxProbeURLForTest(srv.URL)()

	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, _ := m.enterModelPhase("claude", models, fullCatalog, "code")

	if len(got.models.Items()) != 2 {
		t.Fatalf("got %d items, want 2 (cloud + verified running local)", len(got.models.Items()))
	}
}

// TestEnterModelPhaseStaleMarkerQuits asserts the "exit with a message"
// half of the gate: a marker that fails its availability probe quits the
// whole TUI program (via tea.Quit + fatalErr, which Run() surfaces as its
// returned error) rather than silently falling back to cloud-only or
// showing the stale model.
func TestEnterModelPhaseStaleMarkerQuits(t *testing.T) {
	defer localgate.SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here

	cfg := gateTestConfig()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	m := model{cfg: cfg, width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, cmd := m.enterModelPhase("claude", models, fullCatalog, "code")

	if got.fatalErr == nil {
		t.Fatal("expected fatalErr to be set")
	}
	var notRunning *localgate.NotRunningError
	if !errorsAsNotRunning(got.fatalErr, &notRunning) {
		t.Fatalf("fatalErr = %v, want *localgate.NotRunningError", got.fatalErr)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd (tea.Quit)")
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
	fullCatalog, _ := cfg.ModelsForAgent("claude")

	got, _ := m.enterModelPhase("claude", models, fullCatalog, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "modelman start") {
		t.Errorf("status = %q, want it to mention `modelman start`", got.status)
	}
}

// errorsAsNotRunning is a tiny local errors.As wrapper so the test above
// reads plainly; avoids importing "errors" just for one call site.
func errorsAsNotRunning(err error, target **localgate.NotRunningError) bool {
	nre, ok := err.(*localgate.NotRunningError)
	if ok {
		*target = nre
	}
	return ok
}
