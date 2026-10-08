package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

func tableTestRows() []tableRow {
	return []tableRow{
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/cheap", Family: "kimi", Cost: config.ModelCost{InputPricePerMillion: f64(0.5), OutputPricePerMillion: f64(2)}},
			Location: config.LocationCloud, Status: catalog.StatusOK}, counts: usage.UsageCounts{OneDay: 1, SevenDay: 12, ThirtyDay: 340}},
		{Row: catalog.Row{Model: config.Model{ID: "omlx/Qwen3.8-27B-4bit", Family: "qwen"}, Location: config.LocationLocal, Status: catalog.StatusOK, Running: true}},
		{Row: catalog.Row{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx"}, Location: config.LocationLocal, Status: catalog.StatusNew, Discovered: true}},
		{Row: catalog.Row{Model: config.Model{ID: "mlx_lm_server/pair", ProviderID: "mlx_lm_server", Family: "qwen"}, Location: config.LocationLocal, Status: catalog.StatusUnknown}},
	}
}

// TestRenderTableHeaderAndCells verifies the header carries every column
// name in order and each row renders the specified ASCII cells: LOC
// cloud/local, STATUS ok/new/unknown, RUNNING run/-, discovered
// rows with family "-" and cost "-". The header must not carry the EXPOSED
// column removed by #179 (configured is exposed — there is no flag to show).
func TestRenderTableHeaderAndCells(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", nil, "")
	last := -1
	for _, h := range []string{"FAMILY", "MODEL", "LOC", "STATUS", "RUNNING", "COST", "1D", "7D", "30D", "SURVEY"} {
		i := strings.Index(tbl.header, h)
		if i <= last {
			t.Fatalf("header %q: column %q out of order or missing", tbl.header, h)
		}
		last = i
	}
	// #179: no exposed flag is left to show, so the EXPOSED column is gone.
	if strings.Contains(tbl.header, "EXPOSED") {
		t.Errorf("header %q still carries the removed EXPOSED column", tbl.header)
	}
	cloud, run, disc, unknown := tbl.items[0].line, tbl.items[1].line, tbl.items[2].line, tbl.items[3].line
	for _, want := range []string{"kimi", "openrouter/cheap", "cloud", "ok", "0.5000", "12", "340"} {
		if !strings.Contains(cloud, want) {
			t.Errorf("cloud line %q missing %q", cloud, want)
		}
	}
	if !strings.Contains(run, "local") || !strings.Contains(run, "run") {
		t.Errorf("running line = %q", run)
	}
	if !strings.HasPrefix(disc, "-") || !strings.Contains(disc, "new") {
		t.Errorf("discovered line = %q (want family '-' and status new)", disc)
	}
	if !strings.Contains(unknown, "unknown") {
		t.Errorf("unknown line = %q", unknown)
	}
}

// TestRenderTableColumnsAlign verifies the header and every row share column
// offsets: each heading starts at the same rune offset as the value beneath
// it. This is what makes the header trustworthy — a drift would mislabel
// every column.
func TestRenderTableColumnsAlign(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", nil, "")
	runes := func(s string) []rune { return []rune(s) }
	// header includes the 4-rune prefix (ref column + marker) that Title() adds to rows
	col := func(name string) int { return len(runes(tbl.header[:strings.Index(tbl.header, name)])) }
	line := func(i int) []rune { return runes(strings.Repeat(" ", 4) + tbl.items[i].line) }
	if got := string(line(0)[col("LOC") : col("LOC")+5]); got != "cloud" {
		t.Errorf("LOC cell = %q", got)
	}
	if got := string(line(1)[col("RUNNING") : col("RUNNING")+3]); got != "run" {
		t.Errorf("RUNNING cell = %q", got)
	}
	if got := string(line(3)[col("STATUS") : col("STATUS")+7]); got != "unknown" {
		t.Errorf("STATUS cell = %q", got)
	}
}

// TestRenderTableSurveySegmentTrailing verifies the survey segment is appended
// last and omitted when nothing is answered, so it never shifts a column.
func TestRenderTableSurveySegmentTrailing(t *testing.T) {
	rows := tableTestRows()
	rows[0].stats.Answered, rows[0].stats.Worked = 5, 4
	tbl := renderTable(rows, nil, "", nil, "")
	if !strings.Contains(tbl.items[0].line, "n5") {
		t.Errorf("survey segment missing: %q", tbl.items[0].line)
	}
	if strings.Contains(tbl.items[1].line, "n0") || strings.HasSuffix(tbl.items[1].line, " ") {
		t.Errorf("no-answer row has stray segment/trailing space: %q", tbl.items[1].line)
	}
}

// TestRenderTableBlockedAndMarkers verifies a non-running omlx row is a
// start row, a provider wt cannot start is blocked with the command that
// starts it, only the last-launched row is marked, and the in-use ref count is
// picked up.
func TestRenderTableBlockedAndMarkers(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", map[string]int{"openrouter/cheap": 2}, "omlx/Qwen3.8-27B-4bit")
	if tbl.items[0].blocked != "" || tbl.items[1].blocked != "" {
		t.Error("cloud and running rows must not be blocked")
	}
	if tbl.items[2].blocked != "" || !tbl.items[2].start {
		t.Errorf("discovered non-running omlx row: start = %v blocked = %q, want a start row with no hint", tbl.items[2].start, tbl.items[2].blocked)
	}
	if tbl.items[3].blocked == "" || tbl.items[3].start {
		t.Errorf("mlx_lm_server row: start = %v blocked = %q, want blocked and not startable", tbl.items[3].start, tbl.items[3].blocked)
	}
	if !strings.Contains(tbl.items[3].blocked, "llmbench provider isolate --solo mlx_lm_server") {
		t.Errorf("mlx_lm_server row blocked = %q, want the llmbench command", tbl.items[3].blocked)
	}
	if !tbl.items[1].marked || tbl.items[0].marked {
		t.Error("only the last-launched row is marked")
	}
	if tbl.items[0].ref != 2 {
		t.Errorf("ref = %d, want 2", tbl.items[0].ref)
	}
}

// TestRenderTableDiscoveredSelectableUnderLitellm verifies a discovered model
// is selectable with LiteLLM routing on, exactly as in direct mode (#179
// Phase B): wt routes it under its discovered id, so the old "(not in
// LiteLLM)" block would refuse a model the proxy serves. Under routing it is
// labelled "(via proxy)" only when the route is forced, like any other row.
func TestRenderTableDiscoveredSelectableUnderLitellm(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "opencode", SupportedProviders: []string{"omlx"}}},
	}
	row := tableRow{Row: catalog.Row{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal}, Location: config.LocationLocal, Status: catalog.StatusNew, Running: true, Discovered: true}}

	for _, enabled := range []bool{false, true} {
		cfg.SetLitellmForTest(config.LitellmState{Enabled: enabled, URL: "http://localhost:4000", APIKey: "sk-test"})
		tbl := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
		if it := tbl.items[0]; it.blocked != "" || it.exception != "" {
			t.Errorf("litellm enabled=%v: blocked=%q exception=%q, want a selectable row", enabled, it.blocked, it.exception)
		}
	}
}

// TestRenderTableDiscoveredLitellmUnconfigured verifies that a discovered row
// under enabled-but-unconfigured LiteLLM (no URL/key) is labelled
// "(litellm required)" and stays launchable: a route error is reported on
// Enter, like any launch row's.
func TestRenderTableDiscoveredLitellmUnconfigured(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "opencode", SupportedProviders: []string{"omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true})
	row := tableRow{Row: catalog.Row{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal}, Location: config.LocationLocal, Status: catalog.StatusNew, Running: true, Discovered: true}}
	tbl := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if tbl.items[0].exception != "(litellm required)" || tbl.items[0].blocked != "" {
		t.Errorf("exception=%q blocked=%q, want (litellm required) and no block", tbl.items[0].exception, tbl.items[0].blocked)
	}
}

// TestRenderTableUnknownStatusAligns verifies a 7-rune "unknown" cell widens
// the STATUS column for the whole table rather than pushing every later column
// out of line — the header and the rows must agree, or RUNNING and COST start
// rendering under the wrong headings.
//
// The asserted row is deliberately BOTH the widest-status row and one whose
// later cells are non-empty. padRunes pads but never truncates, so a hardcoded
// narrow STATUS width leaves the row's cell at 7 runes while the header's stays
// at 6: the header is the narrow side and every row drifts one rune right. Only
// a row carrying the widest status AND something in the following column can
// expose that drift. An earlier version of this test asserted on the running
// row while marking a *different* row unknown — it passed with the width
// computation fully reverted, so it did not catch the regression it was named
// for. Keep the two concerns on one row.
func TestRenderTableUnknownStatusAligns(t *testing.T) {
	rows := tableTestRows()
	rows[1].Status = catalog.StatusUnknown // running omlx row: widest status, non-empty RUNNING
	tbl := renderTable(rows, nil, "", nil, "")
	col := func(name string) int { return len([]rune(tbl.header[:strings.Index(tbl.header, name)])) }
	line := func(i int) []rune { return []rune(strings.Repeat(" ", 4) + tbl.items[i].line) }
	if got := string(line(1)[col("STATUS") : col("STATUS")+7]); got != "unknown" {
		t.Errorf("STATUS cell = %q, want unknown", got)
	}
	if got := string(line(1)[col("RUNNING") : col("RUNNING")+3]); got != "run" {
		t.Errorf("RUNNING cell = %q (column drift after a widened STATUS)", got)
	}
}

// TestRenderTableResolvesRoutesConcurrently pins that renderTable resolves
// different rows' routes in parallel rather than one at a time: a slow
// provider (e.g. a hung exec: secret_ref helper) must not add its own
// latency on top of every other row's otherwise-fast resolution. Before this
// fix, a sequential per-row loop meant N distinct slow providers summed
// their costs into renderTable's total wall-clock time.
func TestRenderTableResolvesRoutesConcurrently(t *testing.T) {
	scriptDir := t.TempDir()
	// Two DISTINCT scripts (not the same ref twice): config.ResolveSecret
	// memoizes per exact ref string, so reusing one script for both
	// providers would let the second resolution ride the first's cached
	// result and pass even without this fix.
	slowScript1 := filepath.Join(scriptDir, "slow1.sh")
	slowScript2 := filepath.Join(scriptDir, "slow2.sh")
	if err := os.WriteFile(slowScript1, []byte("#!/bin/sh\nsleep 1\necho sk-slow-1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slowScript2, []byte("#!/bin/sh\nsleep 1\necho sk-slow-2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "slow-provider", Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "api_key", BaseURL: "http://localhost:9001", SecretRef: "exec:" + slowScript1}},
			{ID: "slow-provider-2", Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "api_key", BaseURL: "http://localhost:9002", SecretRef: "exec:" + slowScript2}},
		},
		Agents: []config.Agent{{Name: "opencode", SupportedProviders: []string{"slow-provider", "slow-provider-2"}}},
	}
	rows := []tableRow{
		{Row: catalog.Row{Model: config.Model{ID: "slow-provider/m1", ProviderID: "slow-provider", ModelName: "m1"}, Status: catalog.StatusOK}},
		{Row: catalog.Row{Model: config.Model{ID: "slow-provider-2/m2", ProviderID: "slow-provider-2", ModelName: "m2"}, Status: catalog.StatusOK}},
	}

	start := time.Now()
	renderTable(rows, cfg, "opencode", nil, "")
	elapsed := time.Since(start)

	// Two distinct providers each with a ~1s exec: helper: sequential
	// resolution would take >=2s, parallel resolution ~1s.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("renderTable took %v resolving 2 distinct 1s-slow providers, want ~1s (parallel), not ~2s (sequential)", elapsed)
	}
}

// TestRenderTableStartRowNotStartableWhenRouteUnresolvable verifies a
// non-running local row whose route cannot resolve (LiteLLM on but
// unconfigured) is blocked instead of startable. Starting it would spawn the
// server — and possibly stop another model via replace — only for the launch to
// fail at ResolveRoute afterwards.
func TestRenderTableStartRowNotStartableWhenRouteUnresolvable(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "opencode", SupportedProviders: []string{"omlx"}}},
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true})
	row := tableRow{Row: catalog.Row{Model: config.Model{ID: "omlx/x", ProviderID: "omlx", ModelName: "x", Location: config.LocationLocal}, Location: config.LocationLocal, Status: catalog.StatusOK}}
	tbl := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	it := tbl.items[0]
	if it.start || it.blocked == "" || it.exception != "(litellm required)" {
		t.Errorf("start=%v blocked=%q exception=%q, want a blocked, non-startable row", it.start, it.blocked, it.exception)
	}
}

// TestRenderTableShowsALoadingModelAsLoad pins #259 in the picker. A model
// omlx is still loading used to render as "run" and launch on Enter, handing
// the agent a model that could not answer. Its RUNNING cell now reads "load"
// and Enter starts it, which joins the load and waits. "load" fits the
// column's fixed width, so the header and the other rows do not move, and
// the loading row's own later columns stay under their headers: its line is
// the running row's with the one cell changed.
func TestRenderTableShowsALoadingModelAsLoad(t *testing.T) {
	rows := tableTestRows()
	// omlx/Qwen3.8-27B-4bit: Running and Loading. The shared rows leave its
	// provider empty, which a launch row never needs and a start row does.
	rows[1].Loading, rows[1].Model.ProviderID = true, "omlx"
	tbl := renderTable(rows, nil, "", nil, "")
	it := tbl.items[1]
	fields := strings.Fields(it.line)
	if !slices.Contains(fields, "load") || slices.Contains(fields, "run") {
		t.Errorf("loading line = %q, want a RUNNING cell of load", it.line)
	}
	if !it.start || it.blocked != "" {
		t.Errorf("loading row: start = %v blocked = %q, want a start row", it.start, it.blocked)
	}
	plain := renderTable(tableTestRows(), nil, "", nil, "")
	if plain.header != tbl.header {
		t.Errorf("a loading row changed the header:\n%q\n%q", plain.header, tbl.header)
	}
	for i := range plain.items {
		want := plain.items[i].line
		if i == 1 {
			// Same width, so every column after RUNNING keeps its place.
			want = strings.Replace(want, " run ", " load", 1)
		}
		if tbl.items[i].line != want {
			t.Errorf("row %d moved or changed beside a loading row:\n got %q\nwant %q", i, tbl.items[i].line, want)
		}
	}
}
