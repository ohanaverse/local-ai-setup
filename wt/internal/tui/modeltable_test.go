package tui

import (
	"os"
	"path/filepath"
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
			Location: config.LocationCloud, Status: catalog.StatusOK, Exposed: true}, counts: usage.UsageCounts{OneDay: 1, SevenDay: 12, ThirtyDay: 340}},
		{Row: catalog.Row{Model: config.Model{ID: "omlx/Qwen3.8-27B-4bit", Family: "qwen"}, Location: config.LocationLocal, Status: catalog.StatusOK, Running: true}},
		{Row: catalog.Row{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx"}, Location: config.LocationLocal, Status: catalog.StatusNew, Discovered: true}},
		{Row: catalog.Row{Model: config.Model{ID: "omlx/gone", ProviderID: "omlx", Family: "qwen"}, Location: config.LocationLocal, Status: catalog.StatusAbsent}},
	}
}

// TestRenderTableHeaderAndCells verifies the header carries every column
// name in order and each row renders the specified ASCII cells: LOC
// cloud/local, STATUS ok/new/absent, EXPOSED Y/-, RUNNING run/-, discovered
// rows with family "-" and cost "-".
func TestRenderTableHeaderAndCells(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", nil, "")
	last := -1
	for _, h := range []string{"FAMILY", "MODEL", "LOC", "STATUS", "EXPOSED", "RUNNING", "COST", "1D", "7D", "30D", "SURVEY"} {
		i := strings.Index(tbl.header, h)
		if i <= last {
			t.Fatalf("header %q: column %q out of order or missing", tbl.header, h)
		}
		last = i
	}
	cloud, run, disc, absent := tbl.items[0].line, tbl.items[1].line, tbl.items[2].line, tbl.items[3].line
	for _, want := range []string{"kimi", "openrouter/cheap", "cloud", "ok", "Y", "0.5000", "12", "340"} {
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
	if !strings.Contains(absent, "absent") {
		t.Errorf("absent line = %q", absent)
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
	if got := string(line(0)[col("EXPOSED")]); got != "Y" {
		t.Errorf("EXPOSED cell = %q", got)
	}
	if got := string(line(3)[col("STATUS") : col("STATUS")+6]); got != "absent" {
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
// start row, an absent row is blocked with the not-on-disk reason, only the
// last-launched row is marked, and the in-use ref count is picked up.
func TestRenderTableBlockedAndMarkers(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", map[string]int{"openrouter/cheap": 2}, "omlx/Qwen3.8-27B-4bit")
	if tbl.items[0].blocked != "" || tbl.items[1].blocked != "" {
		t.Error("cloud and running rows must not be blocked")
	}
	if tbl.items[2].blocked != "" || !tbl.items[2].start {
		t.Errorf("discovered non-running omlx row: start = %v blocked = %q, want a start row with no hint", tbl.items[2].start, tbl.items[2].blocked)
	}
	if tbl.items[3].blocked == "" || tbl.items[3].start {
		t.Errorf("absent omlx row: start = %v blocked = %q, want blocked and not startable", tbl.items[3].start, tbl.items[3].blocked)
	}
	if !strings.Contains(tbl.items[3].blocked, "not on disk") {
		t.Errorf("absent row blocked = %q, want the not-on-disk reason", tbl.items[3].blocked)
	}
	if !tbl.items[1].marked || tbl.items[0].marked {
		t.Error("only the last-launched row is marked")
	}
	if tbl.items[0].ref != 2 {
		t.Errorf("ref = %d, want 2", tbl.items[0].ref)
	}
}

// TestRenderTableDiscoveredBlockedUnderLitellm verifies a discovered model —
// which is not in LiteLLM's model_list — is unselectable and labelled when
// LiteLLM routing is on, but fine in direct mode.
func TestRenderTableDiscoveredBlockedUnderLitellm(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Agents:    []config.Agent{{Name: "opencode", SupportedProviders: []string{"omlx"}}},
	}
	row := tableRow{Row: catalog.Row{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal}, Location: config.LocationLocal, Status: catalog.StatusNew, Running: true, Discovered: true}}

	direct := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if direct.items[0].blocked != "" || direct.items[0].exception != "" {
		t.Errorf("direct mode: blocked=%q exception=%q, want none", direct.items[0].blocked, direct.items[0].exception)
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	lite := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if lite.items[0].exception != "(not in LiteLLM)" || lite.items[0].blocked == "" || lite.items[0].start {
		t.Errorf("litellm mode: exception=%q blocked=%q start=%v", lite.items[0].exception, lite.items[0].blocked, lite.items[0].start)
	}
	// Advice must name a command that changes the state wt reads (wt owns
	// routing now; `modelman litellm off` would not).
	if !strings.Contains(lite.items[0].blocked, "wt litellm off") || strings.Contains(lite.items[0].blocked, "modelman litellm") {
		t.Errorf("blocked hint = %q, want `wt litellm off`", lite.items[0].blocked)
	}
}

// TestRenderTableDiscoveredLitellmUnconfigured verifies that a discovered row
// under enabled-but-unconfigured LiteLLM (no URL/key) is labelled
// "(litellm required)" and stays launchable: the "(not in LiteLLM)" block only
// applies when the route resolves without error, so a config error must not be
// masked by it.
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
