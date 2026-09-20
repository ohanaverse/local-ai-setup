package tui

import (
	"strings"
	"testing"

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
	cfg.SetExposedForTest(map[string]config.ExposureEntry{"openrouter/cheap": {Exposed: true, Ready: true}})
	return cfg
}

func rowIDs(rows []tableRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.model.ID
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
		return tableRow{location: config.LocationCloud, model: config.Model{ID: id, Cost: config.ModelCost{InputPricePerMillion: in, OutputPricePerMillion: out, SubscriptionPrice: sub}}}
	}
	local := func(id string, running bool, sevenDay int) tableRow {
		return tableRow{location: config.LocationLocal, running: running, model: config.Model{ID: id}, counts: usage.UsageCounts{SevenDay: sevenDay}}
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
