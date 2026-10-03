package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

func f64(v float64) *float64 { return &v }

func rowsTestCfg() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud},
			{ID: "omlx", Location: config.LocationLocal},
			{ID: "ollama", Location: config.LocationLocal},
			{ID: "mtplx", Location: config.LocationLocal},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx", "ollama"}}},
	}
	return cfg
}

func rowIDs(rows []tableRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.Model.ID
	}
	return ids
}

// TestBuildRowsCountsAreAgentScoped verifies the 1d/7d/30d counts come from
// CountsForAgent when an agent is chosen (only that pair's launches) and from
// the model-level Counts when there is no agent (wt smoke).
func TestBuildRowsCountsAreAgentScoped(t *testing.T) {
	cfg := rowsTestCfg()
	store := usage.NewStoreAt(t.TempDir())
	_ = store.RecordFor("claude", "openrouter/cheap")
	_ = store.RecordFor("codex", "openrouter/cheap")
	models := []config.Model{{ID: "openrouter/cheap", ProviderID: "openrouter"}}

	byAgent := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, usage: store})
	if byAgent[0].counts.ThirtyDay != 1 {
		t.Errorf("claude pair 30d = %d, want 1", byAgent[0].counts.ThirtyDay)
	}
	noAgent := buildRows(tableInput{cfg: cfg, models: models, usage: store})
	if noAgent[0].counts.ThirtyDay != 2 {
		t.Errorf("model-level 30d = %d, want 2", noAgent[0].counts.ThirtyDay)
	}
}

// TestSortRowsTwoGroups verifies the spec's sort: group 1 (cloud + running
// local) by cost ascending — output price, then input price; local and
// subscription-only count as $0; no cost data last — then 7-day usage
// ascending; group 2 (non-running local) alphabetical. Cheap and already-
// running models must come first.
func TestSortRowsTwoGroups(t *testing.T) {
	cloud := func(id string, in, out *float64, sub *float64) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: id, Cost: config.ModelCost{InputPricePerMillion: in, OutputPricePerMillion: out, SubscriptionPrice: sub}}}}
	}
	local := func(id string, running bool, sevenDay int) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationLocal, Running: running, Model: config.Model{ID: id}}, counts: usage.UsageCounts{SevenDay: sevenDay}}
	}
	rows := []tableRow{
		local("z-off", false, 0),
		cloud("pricey", f64(3), f64(15), nil),
		cloud("nodata", nil, nil, nil),
		local("run-busy", true, 5),
		cloud("sub", nil, nil, f64(100)),
		cloud("cheap-in-hi", f64(2), f64(1), nil),
		cloud("cheap-in-lo", f64(1), f64(1), nil),
		local("a-off", false, 0),
		local("run-idle", true, 1),
	}
	sortRows(rows)
	// $0 rows (sub, run-idle, run-busy) tie on cost and order by 7d usage
	// ascending: sub(0), run-idle(1), run-busy(5). Then priced cloud rows by
	// output then input price, then the no-data row; group 2 alphabetical.
	want := "sub,run-idle,run-busy,cheap-in-lo,cheap-in-hi,pricey,nodata,a-off,z-off"
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}

// TestSortRowsNativeFirst verifies issue #172: a native model sorts ahead of
// every other row, even when it would otherwise lose the $0 tie on 7-day
// usage to a subscription-only cloud model or an idle running local model.
func TestSortRowsNativeFirst(t *testing.T) {
	rows := []tableRow{
		{Row: catalog.Row{Location: config.LocationLocal, Running: true, Model: config.Model{ID: "run-idle"}}},
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "sub", Cost: config.ModelCost{SubscriptionPrice: f64(100)}}}},
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "native", Native: true}}, counts: usage.UsageCounts{SevenDay: 50}},
		{Row: catalog.Row{Location: config.LocationLocal, Model: config.Model{ID: "a-off"}}},
	}
	sortRows(rows)
	want := "native,run-idle,sub,a-off"
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}

// TestSortRowsNativeTieBreak verifies native-first is a partition, not an
// escape from the ordinary ordering: among the native rows the cost → 7-day
// usage → id rules still decide. Each native row here has a different cost
// shape — no cost data, subscription-only $0, and priced — so a regression
// that dropped the cost comparison among natives, or ordered natives by id,
// reorders them and fails. (An earlier version gave every native row identical
// no-data cost, so the cost tier was never exercised and the test passed
// either way.)
func TestSortRowsNativeTieBreak(t *testing.T) {
	native := func(id string, cost config.ModelCost, sevenDay int) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: id, Native: true, Cost: cost}}, counts: usage.UsageCounts{SevenDay: sevenDay}}
	}
	rows := []tableRow{
		// Priced below every native row, so it doubles as the native-first
		// control: cost alone must not lift it above a native.
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "cheap", Cost: config.ModelCost{InputPricePerMillion: f64(0.01), OutputPricePerMillion: f64(0.01)}}}},
		native("n-nodata", config.ModelCost{}, 0),
		native("n-cheap", config.ModelCost{InputPricePerMillion: f64(0.1), OutputPricePerMillion: f64(0.1)}, 9),
		native("n-free-a", config.ModelCost{SubscriptionPrice: f64(50)}, 3),
		native("n-free-b", config.ModelCost{SubscriptionPrice: f64(50)}, 1),
	}
	sortRows(rows)
	// Natives first. Among them the $0 subscription pair ties on cost and
	// orders by 7-day usage ascending (b:1, a:3), then the priced native, then
	// the no-cost-data one; the cheaper non-native row still trails them all.
	want := "n-free-b,n-free-a,n-cheap,n-nodata,cheap"
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}
