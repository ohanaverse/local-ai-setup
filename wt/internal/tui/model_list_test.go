package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
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
// nothing) and the 1d/7d/30d counts live on the Title() line instead.
func TestModelItemDescriptionEmptyCountsInLine(t *testing.T) {
	store := &mockStore{
		counts: map[string]usage.UsageCounts{
			"ollama/gemma4:9b": {OneDay: 2, SevenDay: 5, ThirtyDay: 10},
		},
	}
	items := buildModelItems([]config.Model{
		{
			ID:         "ollama/gemma4:9b",
			ProviderID: "ollama",
			Family:     "gemma4",
			Location:   config.LocationLocal,
			Tags:       []string{"code"},
		},
	}, map[string]string{"ollama/gemma4:9b": "gemma4"}, store, "", nil)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if desc := it.Description(); desc != "" {
		t.Errorf("Description() = %q, want empty (compact view; counts render on the line)", desc)
	}
	for _, want := range []string{"gemma4", "2/5/10", "ollama/gemma4:9b"} {
		if !strings.Contains(it.Title(), want) {
			t.Errorf("Title() %q missing %q", it.Title(), want)
		}
	}
	// The model has no cost data, so the per-token pricing column renders
	// as a hyphen and still lives on the compact Title() line.
	if !strings.Contains(it.Title(), "  -") {
		t.Errorf("Title() %q missing absent pricing markers", it.Title())
	}
}

// TestModelItemLinePricingAfterUsageCounts verifies that per-token
// pricing is appended to the compact model line right after the 1d/7d/30d
// usage counts, formatted as "$0.1000 0.0500 0.2000".
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
	items := buildModelItems(models, map[string]string{
		"priced":   "test",
		"unpriced": "test",
	}, store, "", nil)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	pricedLine := items[0].Title()
	for _, want := range []string{"0/0/0", "$0.1000 0.0500 0.2000"} {
		if !strings.Contains(pricedLine, want) {
			t.Errorf("priced line %q missing %q", pricedLine, want)
		}
	}

	countsIdx := strings.Index(pricedLine, "0/0/0")
	ptIdx := strings.Index(pricedLine, "$0.1000 0.0500 0.2000")
	if countsIdx == -1 || ptIdx == -1 {
		t.Errorf("expected segments missing from %q", pricedLine)
	}
	if ptIdx < countsIdx {
		t.Errorf("per-token pricing appears before usage counts in %q", pricedLine)
	}

	unpricedLine := items[1].Title()
	unpricedCountsIdx := strings.Index(unpricedLine, "0/0/0")
	unpricedDashIdx := strings.Index(unpricedLine, "-")
	if unpricedCountsIdx == -1 || unpricedDashIdx == -1 || unpricedDashIdx < unpricedCountsIdx {
		t.Errorf("unpriced line %q missing pricing markers after usage counts", unpricedLine)
	}
	if strings.Contains(unpricedLine, "$") {
		t.Errorf("unpriced line %q unexpectedly contains price", unpricedLine)
	}
}

// TestModelItemLinePartialPerTokenPricing verifies that when a model has
// input and output per-token prices but no cache price, the per-token
// segment renders with a single leading "$" and "-0000" for the missing
// cache slot: "$0.5000 -0000 1.0000".
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
	items := buildModelItems(models, map[string]string{"partial": "test"}, store, "", nil)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	line := items[0].Title()
	if !strings.Contains(line, "$0.5000 -0000 1.0000") {
		t.Errorf("partial pricing line %q missing expected $0.5000 -0000 1.0000", line)
	}
}

// TestBuildModelItemsMarksLastLaunchedRow verifies that exactly one row —
// the model matching the rotation's last-launched ID — carries the "> "
// marker prefix in Title() and every other row a blank 2-rune prefix, so
// the marker pins the "where I left off" row without shifting any columns
// (all titles stay rune-equal in length). It also pins the inverse
// contract: .line and FilterValue() stay unprefixed, so fuzzy matching and
// any .line consumer never see marker state.
func TestBuildModelItemsMarksLastLaunchedRow(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
		{ID: "ollama/gemma4:14b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	familyOf := map[string]string{
		"ollama/gemma4:9b":  "gemma4",
		"ollama/gemma4:14b": "gemma4",
	}
	items := buildModelItems(models, familyOf, store, "ollama/gemma4:14b", nil)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Equal scores + stable sort = registry order: 9b first, 14b second.
	for i, want := range []bool{false, true} {
		title := items[i].Title()
		if got := strings.HasPrefix(title, markerMarked); got != want {
			t.Errorf("row %d (%q): marker prefix = %v, want %v", i, title, got, want)
		}
		if got := strings.HasPrefix(title, markerBlank); got != !want {
			t.Errorf("row %d (%q): blank prefix = %v, want %v", i, title, got, !want)
		}
		// The marker must not leak into the line the filter scores.
		if got := items[i].line; got != items[i].FilterValue() || strings.HasPrefix(got, markerMarked) || strings.HasPrefix(got, markerBlank) {
			t.Errorf("row %d: line/FilterValue %q must be identical and unprefixed", i, got)
		}
	}
	wantLen := utf8.RuneCountInString(items[0].Title())
	if gotLen := utf8.RuneCountInString(items[1].Title()); gotLen != wantLen {
		t.Errorf("marked row length %d != unmarked row length %d (columns would misalign)", gotLen, wantLen)
	}
}

// TestBuildModelItemsAppendsSurveySegment verifies the survey stats
// segment is appended last on the line — after the usage counts, pricing,
// and [tags] — so it never shifts any existing column.
func TestBuildModelItemsAppendsSurveySegment(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal, Tags: []string{"code"}},
	}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	stats := map[string]survey.Stats{
		"ollama/gemma4:9b": {Answered: 12, Worked: 11, Failed: 1, RatedQuality: 10, QualitySum: 42, RatedSpeed: 10, SpeedSum: 39},
	}
	items := buildModelItems(models, familyOf, store, "", stats)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	line := items[0].line
	wantSeg := "✓92% q4.2 s3.9 n12"
	if !strings.HasSuffix(line, wantSeg) {
		t.Fatalf("line = %q, want it to end with %q", line, wantSeg)
	}
	if idx := strings.Index(line, "[code]"); idx == -1 || idx > strings.Index(line, wantSeg) {
		t.Fatalf("line = %q, want the survey segment after [tags]", line)
	}
}

// TestBuildModelItemsOmitsSurveySegmentWhenNoAnswered verifies a model
// with zero answered surveys renders no segment at all — existing lines
// (models never surveyed) stay byte-identical whether stats is nil or an
// explicit zero-value entry.
func TestBuildModelItemsOmitsSurveySegmentWhenNoAnswered(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	withoutStats := buildModelItems(models, familyOf, store, "", nil)
	withZeroStats := buildModelItems(models, familyOf, store, "", map[string]survey.Stats{"ollama/gemma4:9b": {}})
	if withoutStats[0].line != withZeroStats[0].line {
		t.Fatalf("nil stats map produced %q, zero-value stats entry produced %q, want identical", withoutStats[0].line, withZeroStats[0].line)
	}
	if strings.Contains(withoutStats[0].line, "✓") || strings.Contains(withoutStats[0].line, "⚠") {
		t.Errorf("line = %q, want no survey segment when Answered == 0", withoutStats[0].line)
	}
}

// TestBuildModelItemsNoMarkerWithoutLastLaunched verifies that an empty
// last-launched ID (no rotation.state) or an ID outside the eligible slice
// (different agent, -T/-F filter, deleted model) leaves every row unmarked —
// the picker must not fabricate a "last used" signal.
func TestBuildModelItemsNoMarkerWithoutLastLaunched(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	models := []config.Model{
		{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Location: config.LocationLocal},
	}
	familyOf := map[string]string{"ollama/gemma4:9b": "gemma4"}
	for _, lastID := range []string{"", "ollama/gone"} {
		items := buildModelItems(models, familyOf, store, lastID, nil)
		for i, it := range items {
			if strings.HasPrefix(it.Title(), markerMarked) {
				t.Errorf("lastID %q: row %d unexpectedly marked: %q", lastID, i, it.Title())
			}
		}
	}
}
