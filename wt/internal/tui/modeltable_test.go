package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

func tableTestRows() []tableRow {
	return []tableRow{
		{model: config.Model{ID: "openrouter/cheap", Family: "kimi", Cost: config.ModelCost{InputPricePerMillion: f64(0.5), OutputPricePerMillion: f64(2)}},
			location: config.LocationCloud, status: statusOK, exposed: true, counts: usage.UsageCounts{OneDay: 1, SevenDay: 12, ThirtyDay: 340}},
		{model: config.Model{ID: "omlx/Qwen3.8-27B-4bit", Family: "qwen"}, location: config.LocationLocal, status: statusOK, running: true},
		{model: config.Model{ID: "omlx/disc"}, location: config.LocationLocal, status: statusNew, discovered: true},
		{model: config.Model{ID: "omlx/gone", Family: "qwen"}, location: config.LocationLocal, status: statusAbsent},
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
	hdr := runes(tbl.header)
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
	_ = hdr
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

// TestRenderTableBlockedAndMarkers verifies items carry the not-launchable
// hint for a non-running omlx row, mark the last-launched row, and pick up
// the in-use ref count.
func TestRenderTableBlockedAndMarkers(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", map[string]int{"openrouter/cheap": 2}, "omlx/Qwen3.8-27B-4bit")
	if tbl.items[0].blocked != "" || tbl.items[1].blocked != "" {
		t.Error("cloud and running rows must not be blocked")
	}
	if tbl.items[2].blocked == "" || tbl.items[3].blocked == "" {
		t.Error("non-running omlx rows must carry a hint")
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
	row := tableRow{model: config.Model{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal}, location: config.LocationLocal, status: statusNew, running: true, discovered: true}

	direct := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if direct.items[0].blocked != "" || direct.items[0].exception != "" {
		t.Errorf("direct mode: blocked=%q exception=%q, want none", direct.items[0].blocked, direct.items[0].exception)
	}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	lite := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if lite.items[0].exception != "(not in LiteLLM)" || lite.items[0].blocked == "" {
		t.Errorf("litellm mode: exception=%q blocked=%q", lite.items[0].exception, lite.items[0].blocked)
	}
}
