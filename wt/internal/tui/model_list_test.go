package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// TestPhaseModelViewRendersStatus asserts that an error set while in the
// model phase is rendered by the View. Previously phaseModelView dropped
// m.status, so a launch failure (e.g. the chosen agent's binary is not on
// PATH) made pressing Enter look like "nothing happened": the screen
// silently stayed on the unchanged model list.
func TestPhaseModelViewRendersStatus(t *testing.T) {
	m := model{
		phase:  phaseModel,
		agent:  "copilot",
		tag:    "code",
		status: "launch failed: exec: \"copilot\": executable file not found",
		models: list.New(
			[]list.Item{&modelItem{model: config.Model{ID: "copilot/native"}}},
			list.NewDefaultDelegate(), 78, 22),
		width: 80, height: 24, theme: themes.Default,
	}
	view := m.View()
	if !strings.Contains(view, m.status) {
		t.Errorf("model View missing status %q in:\n%s", m.status, view)
	}
}

// TestModelItemDescriptionEmptyCountsInLine verifies the compact one-line
// view: Description() is empty (the delegate's description row renders
// nothing) and the 1D/7D/30D counts live on the Title() line as separate cells.
func TestModelItemDescriptionEmptyCountsInLine(t *testing.T) {
	store := &mockStore{
		counts: map[string]usage.UsageCounts{
			"ollama/gemma4:9b": {OneDay: 2, SevenDay: 5, ThirtyDay: 10},
		},
	}
	items := buildTable(tableInput{models: []config.Model{
		{
			ID:         "ollama/gemma4:9b",
			ProviderID: "ollama",
			Family:     "gemma4",
			Location:   config.LocationLocal,
			Tags:       []string{"code"},
		},
	}, usage: store}, "").items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if desc := it.Description(); desc != "" {
		t.Errorf("Description() = %q, want empty (compact view; counts render on the line)", desc)
	}
	for _, want := range []string{"gemma4", "ollama/gemma4:9b"} {
		if !strings.Contains(it.Title(), want) {
			t.Errorf("Title() %q missing %q", it.Title(), want)
		}
	}
	// The model has no cost data, so the COST cell renders as a hyphen and the
	// usage cells (1D 7D 30D) follow it on the compact Title() line.
	fields := strings.Fields(it.Title())
	if tail := strings.Join(fields[len(fields)-4:], " "); tail != "- 2 5 10" {
		t.Errorf("Title() %q tail = %q, want absent-cost marker then 1D/7D/30D cells %q", it.Title(), tail, "- 2 5 10")
	}
}

// TestModelItemLinePricingAfterUsageCounts verifies the COST cell renders
// per-token pricing as " 0.1000  0.0500  0.2000" (input, cache, output, each
// seven wide with four decimals), that an unpriced row gets the "-"
// placeholder instead, and that the COST column sits before the 1D/7D/30D
// usage cells in both header and row (the table's column order; the old line
// put pricing after the counts).
func TestModelItemLinePricingAfterUsageCounts(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	in := 0.10
	cache := 0.05
	out := 0.20
	models := []config.Model{
		{
			ID:         "priced",
			ProviderID: "openrouter",
			Family:     "test",
			Location:   config.LocationCloud,
			Tags:       []string{"code"},
			Cost: config.ModelCost{
				InputPricePerMillion:  &in,
				CachePricePerMillion:  &cache,
				OutputPricePerMillion: &out,
			},
		},
		{
			ID:         "unpriced",
			ProviderID: "openrouter",
			Family:     "test",
			Location:   config.LocationCloud,
			Tags:       []string{"code"},
		},
	}
	tbl := buildTable(tableInput{models: models, usage: store}, "")
	items := tbl.items
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	byID := map[string]*modelItem{}
	for _, it := range items {
		byID[it.model.ID] = it
	}

	const price = " 0.1000  0.0500  0.2000"
	pricedLine := byID["priced"].Title()
	if !strings.Contains(pricedLine, price) {
		t.Errorf("priced line %q missing %q", pricedLine, price)
	}
	// Usage cells follow the cost cell: 0, 0, 0 after the price.
	ptIdx := strings.Index(pricedLine, price)
	if rest := strings.Fields(pricedLine[ptIdx+len(price):]); strings.Join(rest, " ") != "0 0 0" {
		t.Errorf("priced line %q: cells after the price = %q, want the 1D/7D/30D counts %q", pricedLine, rest, "0 0 0")
	}
	if strings.Index(tbl.header, "COST") > strings.Index(tbl.header, "1D") {
		t.Errorf("header %q: COST should come before the 1D usage column", tbl.header)
	}

	unpricedLine := byID["unpriced"].Title()
	if fields := strings.Fields(unpricedLine); strings.Join(fields[len(fields)-4:], " ") != "- 0 0 0" {
		t.Errorf("unpriced line %q missing the absent-cost marker before the usage counts", unpricedLine)
	}
	if strings.Contains(unpricedLine, "0.1000") {
		t.Errorf("unpriced line %q unexpectedly contains a price", unpricedLine)
	}
}

// TestModelItemLinePartialPerTokenPricing verifies that when a model has
// input and output per-token prices but no cache price, the COST cell
// renders a 7-dash placeholder for the missing cache slot:
// " 0.5000 -------  1.0000" (the three price slots keep their width, so the
// output price stays in its column; the extra space before "1.0000" is the
// leading-space padding of its single-digit integer part).
func TestModelItemLinePartialPerTokenPricing(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	in := 0.50
	out := 1.00
	models := []config.Model{
		{
			ID:         "partial",
			ProviderID: "openrouter",
			Family:     "test",
			Location:   config.LocationCloud,
			Tags:       []string{"code"},
			Cost: config.ModelCost{
				InputPricePerMillion:  &in,
				OutputPricePerMillion: &out,
			},
		},
	}
	items := buildTable(tableInput{models: models, usage: store}, "").items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	line := items[0].Title()
	if !strings.Contains(line, " 0.5000 -------  1.0000") {
		t.Errorf("partial pricing line %q missing expected  0.5000 -------  1.0000", line)
	}
}

// TestBuildTableMarksLastLaunchedRow verifies that exactly one row —
// the model matching the rotation's last-launched ID — carries the "> "
// marker prefix in Title() and every other row a blank 2-rune prefix, so
// the marker pins the "where I left off" row without shifting any columns
// (all titles stay rune-equal in length). It also pins the inverse
// contract: .line and FilterValue() stay unprefixed, so fuzzy matching and
// any .line consumer never see marker state.
func TestBuildTableMarksLastLaunchedRow(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
		{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	items := buildTable(tableInput{models: models, usage: store}, "ollama/gemma4:14b").items
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Row order is the table's sort (non-running local rows alphabetical), so
	// look rows up by id rather than position. With the leading ref column,
	// the marker sits at columns 3–4; strip the 2-rune ref prefix before
	// asserting the marker shape.
	for _, it := range items {
		want := it.model.ID == "ollama/gemma4:14b"
		title := it.Title()
		if len(title) < 4 {
			t.Fatalf("row %s title %q too short to hold ref column + marker", it.model.ID, title)
		}
		afterRef := title[2:]
		if got := strings.HasPrefix(afterRef, markerMarked); got != want {
			t.Errorf("row %s (%q): marker prefix = %v, want %v", it.model.ID, title, got, want)
		}
		if got := strings.HasPrefix(afterRef, markerBlank); got != !want {
			t.Errorf("row %s (%q): blank prefix = %v, want %v", it.model.ID, title, got, !want)
		}
		// The marker must not leak into the line the filter scores.
		if got := it.line; got != it.FilterValue() || strings.HasPrefix(got, markerMarked) || strings.HasPrefix(got, markerBlank) {
			t.Errorf("row %s: line/FilterValue %q must be identical and unprefixed", it.model.ID, got)
		}
	}
	wantLen := utf8.RuneCountInString(items[0].Title())
	if gotLen := utf8.RuneCountInString(items[1].Title()); gotLen != wantLen {
		t.Errorf("marked row length %d != unmarked row length %d (columns would misalign)", gotLen, wantLen)
	}
}

// TestBuildTableHasNoSurveyColumn verifies the picker's table no longer draws
// the survey stats: the header has no SURVEY heading and the usage counts are
// the last cells on a row, so no trailing segment follows them.
func TestBuildTableHasNoSurveyColumn(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal, Tags: []string{"code"}},
	}
	tbl := buildTable(tableInput{models: models, usage: store}, "")
	if strings.Contains(tbl.header, "SURVEY") {
		t.Errorf("header = %q, want no SURVEY heading", tbl.header)
	}
	if len(tbl.items) != 1 {
		t.Fatalf("got %d items, want 1", len(tbl.items))
	}
	fields := strings.Fields(tbl.items[0].line)
	if got := strings.Join(fields[len(fields)-3:], " "); got != "0 0 0" {
		t.Fatalf("line = %q, want it to end with the 1D/7D/30D cells %q, got %q", tbl.items[0].line, "0 0 0", got)
	}
}

// TestBuildTableNoMarkerWithoutLastLaunched verifies that an empty
// last-launched ID (no rotation.state) or an ID outside the eligible slice
// (different agent, -T/-F filter, deleted model) leaves every row unmarked —
// the picker must not fabricate a "last used" signal.
func TestBuildTableNoMarkerWithoutLastLaunched(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	for _, lastID := range []string{"", "ollama/gone"} {
		items := buildTable(tableInput{models: models, usage: store}, lastID).items
		for i, it := range items {
			if it.marked || strings.HasPrefix(it.Title(), markerMarked) {
				t.Errorf("lastID %q: row %d unexpectedly marked: %q", lastID, i, it.Title())
			}
		}
	}
}

// TestBuildTableRefColumnBlankWhenUnused verifies a model with zero
// client sessions renders no ref digit — Title()'s 4-rune prefix stays two
// blank ref-column spaces followed by the (also blank) marker.
func TestBuildTableRefColumnBlankWhenUnused(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	items := refTable(tableInput{models: models, usage: store}, nil, "").items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "    ") {
		t.Errorf("Title() = %q, want a 4-space blank prefix (no ref digit, no marker)", got)
	}
}

// TestBuildTableRefColumnRendersDigit verifies a model with N live
// sessions (1 <= N <= 9) renders that exact digit as the first rune of
// Title(), ahead of the marker prefix.
func TestBuildTableRefColumnRendersDigit(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	items := refTable(tableInput{models: models, usage: store}, map[string]int{"ollama/gemma4:9b": 3}, "").items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "3   ") {
		t.Errorf("Title() = %q, want it to start with \"3   \" (ref digit, then blank marker)", got)
	}
}

// TestBuildTableRefColumnClampsAtNine verifies a model with more than
// 9 client sessions still renders a single "9" — the design's fixed-width
// column would misalign if a two-digit count were ever rendered.
func TestBuildTableRefColumnClampsAtNine(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	items := refTable(tableInput{models: models, usage: store}, map[string]int{"ollama/gemma4:9b": 12}, "").items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "9   ") {
		t.Errorf("Title() = %q, want it clamped to \"9   \" for 12 client sessions", got)
	}
}

// TestBuildTableRefColumnBeforeMarker verifies the ref digit and the
// last-launched marker compose correctly when both apply to the same row —
// "1 > " — matching the design's table (ref column, then the rotation
// marker, then the line).
func TestBuildTableRefColumnBeforeMarker(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal}}
	items := refTable(tableInput{models: models, usage: store}, map[string]int{"ollama/gemma4:9b": 1}, "ollama/gemma4:9b").items
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if got := items[0].Title(); !strings.HasPrefix(got, "1 > ") {
		t.Errorf("Title() = %q, want it to start with \"1 > \" (ref digit before the last-launched marker)", got)
	}
}

var (
	ollamaTestModel     = config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama", Location: config.LocationLocal}
	openrouterTestModel = config.Model{ID: "openrouter/qwen/qwen3.8-27b", ModelName: "qwen/qwen3.8-27b", ProviderID: "openrouter", Location: config.LocationCloud}
)

// refTable is buildTable with in-use counts: what the agent flow's catalog
// load renders, each row's count of the model server's client sessions in
// refs (the ref column).
func refTable(in tableInput, refs map[string]int, lastID string) modelTable {
	rows := buildRows(in)
	sortRows(rows)
	return renderTable(rows, in.agent, refs, lastID)
}
