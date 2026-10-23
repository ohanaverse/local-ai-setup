package tui

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
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

// sortEntry is the tier entry for the sort tests. A score of -1 is a row with
// no aligned score; any other value is the aligned score the TIER column
// would show.
func sortEntry(score float64) tiers.Entry {
	if score < 0 {
		return tiers.Entry{}
	}
	return tiers.Entry{Tier: "standard", Code: "S", Aligned: score, HasAligned: true}
}

// sortCloud is a cloud row with the given id and score.
func sortCloud(id string, score float64) tableRow {
	return tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: id}}, tier: sortEntry(score)}
}

// sortLocal is a local row, running and/or loading, with the given score.
func sortLocal(id string, running, loading bool, score float64) tableRow {
	return tableRow{Row: catalog.Row{Location: config.LocationLocal, Running: running, Loading: loading, Model: config.Model{ID: id}}, tier: sortEntry(score)}
}

// sortNative is a native (agent-own) cloud row with the given score.
func sortNative(id string, score float64) tableRow {
	return tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: id, Native: true}}, tier: sortEntry(score)}
}

// sortProfile is a slot-profile row with the given index and name.
func sortProfile(index int, name string) tableRow {
	c := slotCheckFor(index, name, "opus", map[string]string{"opus": "openrouter/a"})
	return tableRow{Row: catalog.Row{Model: config.Model{ID: c.Profile.ID()}, Status: catalog.StatusOK,
		Profile: &catalog.ProfileRow{Check: c, Verdict: catalog.ActionLaunch}}}
}

func assertOrder(t *testing.T, rows []tableRow, want string) {
	t.Helper()
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}

// TestSortRowsBands pins the four bands in one table: native, then slot
// profiles, then running local models, then everything else. Every band's
// rows are given a score that would put them in another place if the score
// ordered across bands, so only the band decides who is above whom.
func TestSortRowsBands(t *testing.T) {
	rows := []tableRow{
		sortCloud("cloud-top", 99),
		sortLocal("local-idle", false, false, 98),
		sortLocal("local-run", true, false, 1),
		sortProfile(0, "p"),
		sortNative("native", 0),
	}
	sortRows(rows)
	assertOrder(t, rows, "native,profile/p,local-run,cloud-top,local-idle")
}

// TestSortRowsNativeAlphabetical pins that native rows sort by id, whatever
// their score or order of arrival, and stay ahead of every other row.
func TestSortRowsNativeAlphabetical(t *testing.T) {
	rows := []tableRow{
		sortCloud("a-cloud", 90),
		sortNative("claude/zeta", 80),
		sortNative("claude/alpha", -1),
		sortNative("claude/mid", 10),
	}
	sortRows(rows)
	assertOrder(t, rows, "claude/alpha,claude/mid,claude/zeta,a-cloud")
}

// TestSortRowsProfilesAlphabeticalNotFileOrder pins that profile rows sort by
// their id (profile/<name>) and not by their position in slot-profiles.toml:
// the file order here (zeta, alpha, mid) is the reverse of the alphabet.
func TestSortRowsProfilesAlphabeticalNotFileOrder(t *testing.T) {
	rows := []tableRow{sortProfile(0, "zeta"), sortProfile(1, "alpha"), sortProfile(2, "mid"), sortCloud("m", 50)}
	sortRows(rows)
	assertOrder(t, rows, "profile/alpha,profile/mid,profile/zeta,m")
}

// TestSortRowsRunningLocalFollowsProfiles pins the user's rule: a local model
// that is running sits right after the slot profiles and above the rest, even
// above cloud and idle local models with a higher score. Several running
// local models order among themselves by score.
func TestSortRowsRunningLocalFollowsProfiles(t *testing.T) {
	rows := []tableRow{
		sortCloud("cloud-best", 90),
		sortLocal("local-idle", false, false, 80),
		sortLocal("run-low", true, false, 10),
		sortProfile(0, "p"),
		sortLocal("run-high", true, false, 40),
		sortLocal("run-unscored", true, false, -1),
	}
	sortRows(rows)
	assertOrder(t, rows, "profile/p,run-high,run-low,run-unscored,cloud-best,local-idle")
}

// TestSortRowsByScoreThenID pins the order inside a band: score descending;
// scores that round to the same whole number tie and fall to id; a row with
// no score sorts after every scored row, a score of 0 included, and
// alphabetically among the unscored.
func TestSortRowsByScoreThenID(t *testing.T) {
	rows := []tableRow{
		sortCloud("unscored-b", -1),
		sortCloud("zero", 0),
		sortCloud("tie-z", 44.6), // rounds to 45
		sortCloud("low", 3),
		sortCloud("tie-a", 45.4), // rounds to 45
		sortCloud("unscored-a", -1),
		sortCloud("top", 70),
		sortCloud("near-zero", 0.4), // rounds to 0, ties with zero
	}
	sortRows(rows)
	assertOrder(t, rows, "top,tie-a,tie-z,low,near-zero,zero,unscored-a,unscored-b")
}

// TestSortRowsTiedScoresGroupByFamily pins that rows tied on score sit with
// their family, not split by the provider prefix of their id (the S34 qwen3.8
// rows were interleaved with a mistral row), that a row with no family follows
// the named ones, and that a higher score still outranks a family's grouping.
func TestSortRowsTiedScoresGroupByFamily(t *testing.T) {
	fam := func(r tableRow, f string) tableRow { r.Model.Family = f; return r }
	rows := []tableRow{
		fam(sortCloud("omlx/qwen", 34), "qwen3.8"),
		fam(sortCloud("ollama/mistral", 34), "mistral-large-4"),
		fam(sortCloud("mtplx/qwen", 34), "qwen3.8"),
		sortCloud("a-nofamily", 34),
		fam(sortCloud("ollama/qwen", 34), "qwen3.8"),
		fam(sortCloud("zz/qwen-top", 50), "qwen3.8"),
	}
	sortRows(rows)
	assertOrder(t, rows, "zz/qwen-top,ollama/mistral,mtplx/qwen,ollama/qwen,omlx/qwen,a-nofamily")
}

// TestSortRowsIgnoresCostAndUsage pins that price and 7-day usage no longer
// order anything: a dear heavily used model with the higher score is above a
// free unused one.
func TestSortRowsIgnoresCostAndUsage(t *testing.T) {
	dear := sortCloud("dear", 60)
	dear.Model.Cost = config.ModelCost{InputPricePerMillion: f64(30), OutputPricePerMillion: f64(150)}
	dear.counts = usage.UsageCounts{SevenDay: 500}
	free := sortCloud("free", 20)
	free.Model.Cost = config.ModelCost{SubscriptionPrice: f64(0)}
	rows := []tableRow{free, dear}
	sortRows(rows)
	assertOrder(t, rows, "dear,free")
}

// TestSortRowsLoadingLocalIsNotInTheRunningBand pins #259 under the new
// bands: a local model omlx is still loading (Running and Loading) cannot
// answer, so it sorts with the rest by score and not after the profiles. It
// must never be a default pick ahead of a model that can answer.
func TestSortRowsLoadingLocalIsNotInTheRunningBand(t *testing.T) {
	rows := []tableRow{
		sortCloud("cloud", 30),
		sortLocal("m-loading", true, true, 95),
		sortLocal("run", true, false, 5),
	}
	sortRows(rows)
	assertOrder(t, rows, "run,m-loading,cloud")
}

// TestSortRowsStable pins that the order depends only on the rows, not on the
// order they arrive in: two differently shuffled copies sort identically.
func TestSortRowsStable(t *testing.T) {
	mk := func() []tableRow {
		return []tableRow{sortCloud("b", 5), sortCloud("a", 5), sortLocal("r", true, false, 1), sortProfile(0, "p"), sortNative("n", 1)}
	}
	x, y := mk(), mk()
	rand.New(rand.NewSource(1)).Shuffle(len(x), func(i, j int) { x[i], x[j] = x[j], x[i] })
	rand.New(rand.NewSource(2)).Shuffle(len(y), func(i, j int) { y[i], y[j] = y[j], y[i] })
	sortRows(x)
	sortRows(y)
	assertOrder(t, x, strings.Join(rowIDs(y), ","))
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

// TestPriceInForceForTimedModels pins that a model priced by time of day
// resolves to the price in force when the table was built (tableRow.price),
// not its flat price: the COST column and the mode line read it. A row with
// no clock (one built by hand) is priced flat. Sorting no longer reads the
// price, and so must not reorder these rows whatever the clock.
func TestPriceInForceForTimedModels(t *testing.T) {
	for _, c := range []struct {
		name      string
		at        time.Time
		timedOut  float64
		timedLate bool
	}{
		{"a weekday: the flat price is in force", pickerMonday, 3.96, false},
		{"a weekend: the cheap row is in force", pickerSaturday, 1.98, true},
		{"no clock: the flat price", time.Time{}, 3.96, false},
	} {
		rows := timedRows(c.at)
		p := rows[0].price()
		if p.Output == nil || *p.Output != c.timedOut {
			t.Errorf("%s: timed output price = %v, want %v", c.name, p.Output, c.timedOut)
		}
		if got := rows[1].price(); got.Output == nil || *got.Output != 3 {
			t.Errorf("%s: steady output price = %v, want 3", c.name, got.Output)
		}
		sortRows(rows)
		assertOrder(t, rows, "steady,timed") // unscored, so by id
	}
	// A model whose flat price is missing and whose row supplies one has cost
	// data while the row is in force, and none outside it.
	rowsOnly := func(at time.Time) []tableRow {
		rows := timedRows(at)
		rows[0].Model.Cost.InputPricePerMillion, rows[0].Model.Cost.CachePricePerMillion, rows[0].Model.Cost.OutputPricePerMillion = nil, nil, nil
		rows[0] = rows[0].pricedAt(at)
		return rows
	}
	if p := rowsOnly(pickerSaturday)[0].price(); p.Output == nil || *p.Output != 1.98 || p.Input == nil || *p.Input != 0.66 {
		t.Errorf("rows only, at the weekend: price = %+v, want the row's prices", p)
	}
	if p := rowsOnly(pickerMonday)[0].price(); p.Input != nil || p.Cache != nil || p.Output != nil {
		t.Errorf("rows only, on a weekday: price = %+v, want no price", p)
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
		if p := r.price(); p.Output == nil || *p.Output != 1.98 || p.Input == nil || *p.Input != 0.66 {
			t.Errorf("%s: price = %+v, want the weekend row's 0.66 in, 1.98 out", when, p)
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

// TestBuildRowsSortByTheTiersFileScore pins the path from a written tiers
// file to the order: buildRows rates each row from the file and sortRows
// orders by the rounded aligned score, highest first, with a model the file
// does not list after the scored ones. The file is the one writeTiers puts
// under a temporary MODELTIERS_OUT.
func TestBuildRowsSortByTheTiersFileScore(t *testing.T) {
	writeTiers(t) // openrouter/a 44, omlx/qwen3.8 31, openrouter/b 9 (unrated)
	models := []config.Model{
		{ID: "openrouter/zzz", ProviderID: "openrouter", ModelName: "zzz"},
		{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b"},
		{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a"},
		{ID: "ollama/q", ProviderID: "ollama", ModelName: "q"},
	}
	rows := buildRows(tableInput{cfg: rowsTestCfg(), agent: "claude", models: models, now: pickerMonday})
	sortRows(rows)
	assertOrder(t, rows, "openrouter/a,openrouter/b,ollama/q,openrouter/zzz")
}
