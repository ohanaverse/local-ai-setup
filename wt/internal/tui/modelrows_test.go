package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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

// TestBuildRowsIncludesConfiguredAndDiscoveredLocal verifies the three row
// sources: a configured cloud model, a configured local model (running or
// not), and a discovered on-disk model with no registry entry — the union the
// selector is meant to show.
func TestBuildRowsIncludesConfiguredAndDiscoveredLocal(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{
		{ID: "openrouter/cheap", ProviderID: "openrouter", ModelName: "cheap"},
		{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"},
	}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc"},
	}}
	rows := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, inventory: inv, usage: usage.NewStoreAt(t.TempDir())})

	if got := strings.Join(rowIDs(rows), ","); got != "openrouter/cheap,omlx/a,omlx/disc" {
		t.Fatalf("rows = %s", got)
	}
	if !rows[0].exposed || rows[0].location != config.LocationCloud || rows[0].status != statusOK {
		t.Errorf("cloud row = %+v", rows[0])
	}
	if !rows[1].running || rows[1].status != statusOK || rows[1].discovered {
		t.Errorf("configured local row = %+v", rows[1])
	}
	d := rows[2]
	if !d.discovered || d.status != statusNew || d.exposed || d.running || d.model.ModelName != "disc" || d.location != config.LocationLocal {
		t.Errorf("discovered row = %+v", d)
	}
}

// TestBuildRowsMarksAbsentAndRespectsAgentAndFilters verifies: a configured
// local model with no artifact reads "absent"; discovered models from a
// provider the agent does not support are skipped (agent supported_providers
// stays a hard constraint); hideDiscovered (-T/-F) drops discovered rows; a
// discovered id equal to an existing row id is dropped (registry row wins).
func TestBuildRowsMarksAbsentAndRespectsAgentAndFilters(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"}}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true},
		{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x"},      // claude does not support mtplx
		{ProviderID: "ollama", ModelID: "omlx/gone", Artifact: "dup"}, // id collision with a row
		{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok"},
	}}
	in := tableInput{cfg: cfg, agent: "claude", models: models, inventory: inv, usage: usage.NewStoreAt(t.TempDir())}
	rows := buildRows(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/gone,ollama/ok" {
		t.Fatalf("rows = %s", got)
	}
	if rows[0].status != statusAbsent {
		t.Errorf("status = %q, want absent", rows[0].status)
	}
	in.hideDiscovered = true
	if got := strings.Join(rowIDs(buildRows(in)), ","); got != "omlx/gone" {
		t.Errorf("hideDiscovered rows = %s", got)
	}
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

// TestLaunchableRules verifies which rows Enter may launch: cloud always; a
// running local row; a pulled ollama model even when not loaded (ollama loads
// on demand) — but not an absent ollama model, and not a non-running omlx row.
func TestLaunchableRules(t *testing.T) {
	cases := []struct {
		name string
		row  tableRow
		want bool
	}{
		{"cloud", tableRow{location: config.LocationCloud}, true},
		{"running local", tableRow{location: config.LocationLocal, running: true, model: config.Model{ProviderID: "omlx"}}, true},
		{"idle ollama pulled", tableRow{location: config.LocationLocal, status: statusOK, model: config.Model{ProviderID: "ollama"}}, true},
		{"idle ollama discovered", tableRow{location: config.LocationLocal, status: statusNew, model: config.Model{ProviderID: "ollama"}}, true},
		{"absent ollama", tableRow{location: config.LocationLocal, status: statusAbsent, model: config.Model{ProviderID: "ollama"}}, false},
		{"idle omlx", tableRow{location: config.LocationLocal, status: statusOK, model: config.Model{ProviderID: "omlx"}}, false},
	}
	for _, tc := range cases {
		if got := tc.row.launchable(); got != tc.want {
			t.Errorf("%s: launchable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestNotLaunchableHint verifies the status text names the fix: `modelman
// start <id>` for a configured model, the provider CLI for a discovered one.
func TestNotLaunchableHint(t *testing.T) {
	reg := tableRow{model: config.Model{ID: "omlx/a", ProviderID: "omlx"}}
	if h := reg.notLaunchableHint(); !strings.Contains(h, "modelman start omlx/a") {
		t.Errorf("registered hint = %q", h)
	}
	disc := tableRow{discovered: true, model: config.Model{ID: "omlx/d", ProviderID: "omlx"}}
	if h := disc.notLaunchableHint(); !strings.Contains(h, "omlx/d") || strings.Contains(h, "modelman start") {
		t.Errorf("discovered hint = %q", h)
	}
}

// TestBuildRowsNativeModelIsExposedWithoutFlag pins that a native model
// (Anthropic-direct, provider auth.type "native") shows EXPOSED without any
// modelman.toml flag, matching modelman's own EXPOSED column, which reports
// native rows as exposed unconditionally. A regression here would make wt's
// picker disagree with modelman for native rows.
func TestBuildRowsNativeModelIsExposedWithoutFlag(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{{ID: "openrouter/native", ProviderID: "openrouter", ModelName: "native", Native: true}}
	rows := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, usage: usage.NewStoreAt(t.TempDir())})
	if len(rows) != 1 || !rows[0].exposed {
		t.Fatalf("native row = %+v, want exposed=true with no flag set", rows)
	}
	if cfg.ExposedFlag("openrouter/native") {
		t.Fatal("test premise broken: flag must be unset")
	}
}
