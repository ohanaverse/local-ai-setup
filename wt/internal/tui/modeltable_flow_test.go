package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// flowEnter builds the eligible list the way the caller does and enters the
// model phase, isolating usage/rotation state from the host.
func flowEnter(t *testing.T, m model, agent string) model {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	models, _ := m.cfg.EligibleModels(agent, m.activeTags, m.activeFamily)
	fullCatalog, _ := m.cfg.ModelsForAgent(agent)
	got, _ := m.enterModelPhase(agent, models, fullCatalog, "code")
	return got
}

func itemIDs(got model) []string {
	var ids []string
	for _, it := range got.models.Items() {
		ids = append(ids, it.(*modelItem).model.ID)
	}
	return ids
}

func indexOfID(got model, id string) int {
	for i, x := range itemIDs(got) {
		if x == id {
			return i
		}
	}
	return -1
}

// TestEnterModelPhaseShowsNonRunningAndDiscoveredRows verifies the picker now
// lists a configured local model that is not running and a discovered model
// alongside cloud models, instead of hiding local models the way the old gate
// did — the point of the new table.
func TestEnterModelPhaseShowsNonRunningAndDiscoveredRows(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true},
		{ProviderID: "omlx", Artifact: "extra", ModelID: config.DiscoveredModelID("omlx", "extra")},
	}})
	got := flowEnter(t, model{cfg: gateTestConfig(), width: 80, height: 24}, "claude")

	ids := itemIDs(got)
	for _, want := range []string{"claude/opus", "omlx/qwen3.8", config.DiscoveredModelID("omlx", "extra")} {
		if indexOfID(got, want) < 0 {
			t.Errorf("ids = %v, missing %q", ids, want)
		}
	}
}

// TestEnterModelPhaseHeaderIsListTitle verifies the column header is the
// model list's title (so it renders above the rows) and starts with the
// FAMILY heading after the 4-rune row prefix.
func TestEnterModelPhaseHeaderIsListTitle(t *testing.T) {
	got := flowEnter(t, model{cfg: gateTestConfig(), width: 80, height: 24}, "claude")
	if !strings.HasPrefix(got.models.Title, "    FAMILY") {
		t.Errorf("Title = %q, want 4 spaces then FAMILY", got.models.Title)
	}
	// Alignment: the FAMILY column must start at the same offset in the header
	// as the family text does in the first row's line (after the prefix).
	first := got.models.Items()[0].(*modelItem)
	if !strings.Contains(first.line, first.model.Family) {
		t.Fatalf("line %q lacks family %q", first.line, first.model.Family)
	}
	if strings.Index(got.models.Title, "MODEL") != rowPrefixWidth+strings.Index(first.line, first.model.ID) {
		t.Errorf("MODEL column misaligned: title %q vs line %q", got.models.Title, first.line)
	}
}

// TestEnterOnNonRunningRowShowsHintAndDoesNotLaunch verifies Enter on a
// non-running omlx row sets the start hint in m.status and leaves the phase at
// phaseModel (no launch, no rotation/usage write).
func TestEnterOnNonRunningRowShowsHintAndDoesNotLaunch(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true},
	}})
	got := flowEnter(t, model{cfg: gateTestConfig(), width: 80, height: 24}, "claude")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", got.phase)
	}
	idx := indexOfID(got, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("no omlx row in %v", itemIDs(got))
	}
	got.models.Select(idx)

	next, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := next.(model)
	if nm.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel", nm.phase)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil (no launch)", cmd)
	}
	if !strings.Contains(nm.status, "modelman start") {
		t.Errorf("status = %q, want the modelman start hint", nm.status)
	}
}

// TestEnterModelPhaseHidesDiscoveredWhenFiltered verifies -T/-F (activeTags /
// activeFamily) drop discovered rows, which have no tags or family.
func TestEnterModelPhaseHidesDiscoveredWhenFiltered(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "extra", ModelID: config.DiscoveredModelID("omlx", "extra")},
	}})
	disc := config.DiscoveredModelID("omlx", "extra")

	unfiltered := flowEnter(t, model{cfg: gateTestConfig(), width: 80, height: 24}, "claude")
	if indexOfID(unfiltered, disc) < 0 {
		t.Fatalf("control: discovered row absent without filter: %v", itemIDs(unfiltered))
	}
	filtered := flowEnter(t, model{cfg: gateTestConfig(), width: 80, height: 24, activeTags: "code"}, "claude")
	if indexOfID(filtered, disc) >= 0 {
		t.Errorf("discovered row present with -T filter: %v", itemIDs(filtered))
	}
}

// TestEnterModelPhaseSingleRowShortcut verifies the one-row shortcut still
// applies (exactly one row that is launchable), and does NOT apply when the
// only row is non-launchable (the table shows so the hint is visible).
func TestEnterModelPhaseSingleRowShortcut(t *testing.T) {
	requireBinary(t, "claude")
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}}},
		Models:     []config.Model{{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}}},
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	cfg.ExposeAllForTest()
	m := model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}
	_, cmd := func() (model, tea.Cmd) {
		tempStateDir(t)
		stubUsageStore(t)
		stubRefcountStore(t)
		models, _ := cfg.EligibleModels("claude", "", "")
		full, _ := cfg.ModelsForAgent("claude")
		return m.enterModelPhase("claude", models, full, "code")
	}()
	if cmd == nil {
		t.Error("single launchable row: want launch cmd, got nil")
	}

	// A single non-launchable row must show the table instead.
	local := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:     []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:     []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	local.ExposeAllForTest()
	got := flowEnter(t, model{cfg: local, width: 80, height: 24}, "pi")
	if got.phase != phaseModel || len(got.models.Items()) != 1 {
		t.Errorf("phase = %v items = %d, want phaseModel with 1 blocked row", got.phase, len(got.models.Items()))
	}
}

// TestEnterModelPhaseAllLocalNoneRunningShowsBlockedRows replaces the old
// gate-emptied route-back test: when every eligible model is local and none is
// running, the picker now shows the table with every row blocked instead of
// routing back to the agent picker.
func TestEnterModelPhaseAllLocalNoneRunningShowsBlockedRows(t *testing.T) {
	local := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "a", Tags: []string{"code"}},
			{ID: "omlx/b", ProviderID: "omlx", ModelName: "b", Family: "b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	local.ExposeAllForTest()
	got := flowEnter(t, model{cfg: local, width: 80, height: 24}, "pi")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel (no route back)", got.phase)
	}
	items := got.models.Items()
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for _, it := range items {
		if it.(*modelItem).blocked == "" {
			t.Errorf("%s launchable, want blocked", it.(*modelItem).model.ID)
		}
	}
}
