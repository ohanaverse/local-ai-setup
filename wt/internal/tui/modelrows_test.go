package tui

import (
	"strings"
	"testing"
	"time"

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

// TestSortRowsPutsALoadingModelWithTheStartRows pins where a model that omlx
// is still loading sorts (#259). The sort order is also the default selection,
// and group 1 is what a bare Enter launches at once: a loading model left
// there sorts first on its $0 cost and becomes the default pick while it
// cannot answer. It belongs with the other rows Enter starts.
func TestSortRowsPutsALoadingModelWithTheStartRows(t *testing.T) {
	local := func(id string, running, loading bool) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationLocal, Running: running, Loading: loading, Model: config.Model{ID: id}}}
	}
	rows := []tableRow{
		local("m-loading", true, true),
		local("z-off", false, false),
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "cloud", Cost: config.ModelCost{OutputPricePerMillion: f64(1)}}}},
		local("run", true, false),
		local("a-off", false, false),
	}
	sortRows(rows)
	if got, want := strings.Join(rowIDs(rows), ","), "run,cloud,a-off,m-loading,z-off"; got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}

// timedRows are two cloud rows for the price tests: "timed" has a dear flat
// price (3.96 out) and a row that halves it at weekends, and "steady" costs
// 3 out all week. On a weekday steady is the cheaper one; at a weekend timed
// is.
func timedRows(at time.Time) []tableRow {
	weekend := config.TimePrice{Label: "openrouter", Timezone: "UTC",
		InputPricePerMillion: f64(0.66), CachePricePerMillion: f64(0.022), OutputPricePerMillion: f64(1.98),
		Windows: []config.CostWindow{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}}}
	return []tableRow{
		tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "timed", Cost: config.ModelCost{
			InputPricePerMillion: f64(1.32), CachePricePerMillion: f64(0.044), OutputPricePerMillion: f64(3.96), TimePrices: []config.TimePrice{weekend}}}}}.pricedAt(at),
		tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "steady", Cost: config.ModelCost{
			InputPricePerMillion: f64(1), OutputPricePerMillion: f64(3)}}}}.pricedAt(at),
	}
}

var (
	pickerMonday   = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	pickerSaturday = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
)

// TestSortRowsUsesThePriceInForce pins the cost sort on a model priced by
// time of day: it sorts by the price in force when the table was built, not
// by its flat price. The sort order is also the picker's default selection,
// so a model that is the cheapest right now is what a bare Enter launches,
// and one in its dear window does not keep a cheap model's place. A row with
// no clock (one built by hand) sorts by its flat price, and sorting again at
// the same instant changes nothing.
func TestSortRowsUsesThePriceInForce(t *testing.T) {
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"a weekday: the flat price is in force", pickerMonday, "steady,timed"},
		{"a weekend: the cheap row is in force", pickerSaturday, "timed,steady"},
		{"no clock: the flat price", time.Time{}, "steady,timed"},
	} {
		rows := timedRows(c.at)
		sortRows(rows)
		if got := strings.Join(rowIDs(rows), ","); got != c.want {
			t.Errorf("%s: order = %s, want %s", c.name, got, c.want)
		}
		sortRows(rows)
		if got := strings.Join(rowIDs(rows), ","); got != c.want {
			t.Errorf("%s: sorted a second time, order = %s, want it unchanged (%s)", c.name, got, c.want)
		}
	}
	// A model whose flat price is missing and whose row supplies one has cost
	// data while the row is in force, and none outside it.
	rowsOnly := func(at time.Time) []tableRow {
		rows := timedRows(at)
		rows[0].Model.Cost.InputPricePerMillion, rows[0].Model.Cost.CachePricePerMillion, rows[0].Model.Cost.OutputPricePerMillion = nil, nil, nil
		rows[0] = rows[0].pricedAt(at)
		return rows
	}
	if k := rowCostKey(rowsOnly(pickerSaturday)[0]); k.noData || k.out != 1.98 || k.in != 0.66 {
		t.Errorf("rows only, at the weekend: cost key = %+v, want the row's prices", k)
	}
	if k := rowCostKey(rowsOnly(pickerMonday)[0]); !k.noData {
		t.Errorf("rows only, on a weekday: cost key = %+v, want no cost data", k)
	}
}

// TestBuildRowsReadsThePickersClock pins where the instant comes from: the
// table is given one (tableInput.now), and one left out is the picker's own
// clock, read once for the whole table. Every row is priced at that one
// instant, so two rows of a table are never priced on two sides of a window
// boundary.
func TestBuildRowsReadsThePickersClock(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{{ID: "openrouter/a", ProviderID: "openrouter"}, {ID: "openrouter/b", ProviderID: "openrouter"}}
	rows := buildRows(tableInput{cfg: cfg, models: models, now: pickerSaturday})
	if len(rows) != 2 || !rows[0].at.Equal(pickerSaturday) || !rows[1].at.Equal(pickerSaturday) {
		t.Fatalf("rows built with a clock: %d rows, at %v", len(rows), rows)
	}
	calls := 0
	old := pickerNow
	pickerNow = func() time.Time { calls++; return pickerMonday }
	t.Cleanup(func() { pickerNow = old })
	rows = buildRows(tableInput{cfg: cfg, models: models})
	if calls != 1 || !rows[0].at.Equal(pickerMonday) || !rows[1].at.Equal(pickerMonday) {
		t.Errorf("rows built without a clock: the picker's clock was read %d time(s), rows at %v and %v; want one reading for both", calls, rows[0].at, rows[1].at)
	}
}

// TestBuildRowsPricesEachRowOnce pins that a table's rows are priced when
// the table is built and not again: the sort, the COST cell, the mode line's
// price and the "~" mark all read what buildRows resolved. Resolving a price
// checks its rows against the registry's rules and reads their timezones,
// and the sort asks for two prices per comparison, so a picker that resolved
// on every question did that work hundreds of times on the goroutine that
// handles keys, where one that resolves per row does it once per model. The
// test changes a row behind the built table's back, which nothing in wt
// does, only to see whether the row is read a second time.
func TestBuildRowsPricesEachRowOnce(t *testing.T) {
	weekend := config.TimePrice{Label: "openrouter", Timezone: "UTC",
		InputPricePerMillion: f64(0.66), CachePricePerMillion: f64(0.022), OutputPricePerMillion: f64(1.98),
		Windows: []config.CostWindow{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}}}
	models := []config.Model{{ID: "openrouter/a", ProviderID: "openrouter", Cost: config.ModelCost{
		InputPricePerMillion: f64(1.32), CachePricePerMillion: f64(0.044), OutputPricePerMillion: f64(3.96),
		TimePrices: []config.TimePrice{weekend}}}}
	rows := buildRows(tableInput{cfg: rowsTestCfg(), models: models, now: pickerSaturday})
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	check := func(when string) {
		t.Helper()
		r := rows[0]
		if k := rowCostKey(r); k.out != 1.98 || k.in != 0.66 {
			t.Errorf("%s: cost key = %+v, want the weekend row's 0.66 in, 1.98 out", when, k)
		}
		if got, want := costCell(r), " 0.6600  0.0220  1.9800~"; got != want {
			t.Errorf("%s: COST cell = %q, want %q", when, got, want)
		}
		if got, want := costNote(r), "cost~ 0.66/0.022/1.98"; got != want {
			t.Errorf("%s: mode-line price = %q, want %q", when, got, want)
		}
	}
	check("as built")
	// The row now names no zone, so resolving it again would pass it over:
	// the flat price, and no mark.
	rows[0].Model.Cost.TimePrices[0].Timezone = "Nowhere/None"
	check("after the row changed under the table")
}
