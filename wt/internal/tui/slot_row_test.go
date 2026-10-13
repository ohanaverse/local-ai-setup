// Tests for a slot profile's row in the model picker: its cells, where it
// sorts, the slot line under the table, and the fit of the screen around it.
// The fixtures here are shared with the cursor and launch-flow tests.
package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// slotTestConfig is modelTestConfig (pin_model_test.go) plus an "openrouter"
// cloud provider with two models, both tagged "code" and supported by the
// "claude" agent: "openrouter/a" (family "a", provider-side name "a", priced
// 0.40 / 0.04 / 1.60 per million) and "openrouter/b" (name "b", no price).
// modelTestConfig already sets the [litellm] url and key.
func slotTestConfig() *config.Config {
	cfg := modelTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID: "openrouter", Location: config.LocationCloud,
		Protocols: []config.Protocol{config.ProtocolOpenAIChat},
		Auth:      config.AuthConfig{Type: "api_key", BaseURL: "https://openrouter.ai/api"},
	})
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Tags: []string{"code"}, Cost: config.ModelCost{
			InputPricePerMillion: f64(0.40), CachePricePerMillion: f64(0.04), OutputPricePerMillion: f64(1.60)}},
		config.Model{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Tags: []string{"code"}},
	)
	cfg.Agents[0].SupportedProviders = append(cfg.Agents[0].SupportedProviders, "openrouter")
	return cfg
}

// slotCheckFor is a profile that passed static validation: usable, for agent
// "claude", with Claude's four slots declared. index is its position in the
// file, which is the order profile rows sort in.
func slotCheckFor(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: []string{"fable", "opus", "sonnet", "haiku"},
	}
}

// slotPicker enters the model phase for "claude" on cfg with checks as the
// picker's slot profiles, in a terminal of width x height. It isolates the
// state directory and the usage and refcount stores (flowEnter). The
// inventory is whatever the test stubbed before the call; with no stub it is
// TestMain's: every local model on disk and none running.
func slotPicker(t *testing.T, cfg *config.Config, checks []profiles.SlotCheck, width, height int) model {
	t.Helper()
	m := flowEnter(t, model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), slotChecks: checks, width: width, height: height}, "claude")
	return resized(t, m, width, height)
}

// slotRowChecks are the three profiles the row tests draw: "cloud" (every
// slot a cloud model, a launch row), "mixed" (a cloud default and the
// fixture's local model) and "broken" (one slot names a model the registry
// does not have, a blocked row).
func slotRowChecks() []profiles.SlotCheck {
	return []profiles.SlotCheck{
		slotCheckFor(0, "cloud", "opus", map[string]string{"opus": "openrouter/a", "haiku": "openrouter/b"}),
		slotCheckFor(1, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/qwen3.8"}),
		slotCheckFor(2, "broken", "opus", map[string]string{"opus": "openrouter/a", "haiku": "openrouter/gone"}),
	}
}

// idleOmlxSnapshot is the fixture's local model on disk and not running,
// which makes its own row, and a profile that names it, a start row.
func idleOmlxSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	}
}

// slotTable builds the picker's table for "claude" over cfg and snap with
// checks opted in, the way tableFor does, with a usage store of its own.
func slotTable(t *testing.T, cfg *config.Config, snap localmodels.Snapshot, checks []profiles.SlotCheck) modelTable {
	t.Helper()
	models, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	store := stubUsageStore(t)
	for i := 0; i < 2; i++ {
		if err := store.RecordFor("claude", "profile/cloud"); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
	}
	return buildTable(tableInput{cfg: cfg, agent: "claude", models: models, inventory: &snap, usage: store, slots: checks}, stubRefcountStore(t), "")
}

// tableItem is the table's item for id.
func tableItem(t *testing.T, tbl modelTable, id string) *modelItem {
	t.Helper()
	for _, it := range tbl.items {
		if it.model.ID == id {
			return it
		}
	}
	t.Fatalf("the table has no row %q", id)
	return nil
}

// TestProfileRowsNeedTheOptIn pins that the table has profile rows only when
// its input carries the slot profiles, and none under -T or -F. `wt start`
// and `wt smoke` build their picker from the same function without them; a
// profile row there would be offered as a model to start.
func TestProfileRowsNeedTheOptIn(t *testing.T) {
	cfg := slotTestConfig()
	models, _ := cfg.EligibleModels("claude", "", "")
	snap := runningOmlxSnapshot()
	count := func(in tableInput) int {
		in.cfg, in.agent, in.models, in.inventory = cfg, "claude", models, &snap
		n := 0
		for _, it := range buildTable(in, nil, "").items {
			if strings.HasPrefix(it.model.ID, profiles.SlotIDPrefix) {
				n++
			}
		}
		return n
	}
	if got := count(tableInput{slots: slotRowChecks()}); got != 3 {
		t.Errorf("with the slot profiles: %d profile rows, want 3", got)
	}
	if got := count(tableInput{}); got != 0 {
		t.Errorf("without the slot profiles: %d profile rows, want none", got)
	}
	if got := count(tableInput{slots: slotRowChecks(), hideDiscovered: true}); got != 0 {
		t.Errorf("under -T or -F: %d profile rows, want none", got)
	}
}

// TestSortRowsPutsProfilesBetweenNativeAndTheRest pins the picker's three
// bands: native rows, then profile rows in file order, then every other row
// in the order it had before. The sort order is also the default selection,
// so a profile sorted by cost or id would move whenever a price changed, and
// one sorted above a native row would become what a bare Enter picks.
func TestSortRowsPutsProfilesBetweenNativeAndTheRest(t *testing.T) {
	profile := func(index int, name string, verdict catalog.Action) tableRow {
		c := slotCheckFor(index, name, "opus", map[string]string{"opus": "openrouter/a"})
		return tableRow{Row: catalog.Row{Model: config.Model{ID: c.Profile.ID()}, Status: catalog.StatusOK,
			Profile: &catalog.ProfileRow{Check: c, Verdict: verdict}}}
	}
	rows := []tableRow{
		{Row: catalog.Row{Model: config.Model{ID: "omlx/idle"}, Location: config.LocationLocal}},
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/dear", Cost: config.ModelCost{OutputPricePerMillion: f64(9)}}, Location: config.LocationCloud}},
		profile(2, "zeta", catalog.ActionLaunch),
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/cheap", Cost: config.ModelCost{OutputPricePerMillion: f64(1)}}, Location: config.LocationCloud}},
		profile(0, "start", catalog.ActionStart),
		{Row: catalog.Row{Model: config.Model{ID: "claude/native", Native: true}, Location: config.LocationCloud}},
		profile(1, "alpha", catalog.ActionBlock),
	}

	sortRows(rows)

	var got []string
	for _, r := range rows {
		got = append(got, r.Model.ID)
	}
	want := "claude/native,profile/start,profile/alpha,profile/zeta,openrouter/cheap,openrouter/dear,omlx/idle"
	if strings.Join(got, ",") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, ","), want)
	}
}

// TestRefreshedTableKeepsProfileRows pins that a table rebuilt after a start
// attempt still has its profile rows. The rebuild runs from a reduced copy of
// the model, and a field left out of that copy is lost on every refresh: the
// profiles would vanish from the picker the first time a start failed.
func TestRefreshedTableKeepsProfileRows(t *testing.T) {
	stubInventory(t, runningOmlxSnapshot())
	m := slotPicker(t, slotTestConfig(), slotRowChecks(), 80, 24)
	before := strings.Join(itemIDs(m), ",")

	m, cmd := m.refreshTable()
	m = drainCmds(t, m, cmd)

	if got := strings.Join(itemIDs(m), ","); got != before || !strings.Contains(got, "profile/mixed") {
		t.Errorf("rows after a refresh = %s, want %s", got, before)
	}
}
