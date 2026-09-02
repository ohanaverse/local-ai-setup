package tui

import (
	"strings"
	"testing"

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
	}, map[string]string{"ollama/gemma4:9b": "gemma4"}, store)
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
	// The model has no cost data, so both pricing columns render as
	// hyphens and still live on the compact Title() line.
	if !strings.Contains(it.Title(), "  -  -") {
		t.Errorf("Title() %q missing absent pricing markers", it.Title())
	}
}

// TestModelItemLinePricingAfterUsageCounts verifies that per-token and
// subscription pricing strings are appended to the compact model line right
// after the 1d/7d/30d usage counts, and that absent pricing renders as an em
// dash while preserving dynamic column padding.
func TestModelItemLinePricingAfterUsageCounts(t *testing.T) {
	store := &mockStore{counts: map[string]usage.UsageCounts{}}
	in := 0.10
	cache := 0.05
	out := 0.20
	sub := 19.99
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
				SubscriptionPrice:     &sub,
				SubscriptionPeriod:    "month",
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
	}, store)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	pricedLine := items[0].Title()
	for _, want := range []string{"0/0/0", "$0.10/0.05/0.20", "$19.99/mo"} {
		if !strings.Contains(pricedLine, want) {
			t.Errorf("priced line %q missing %q", pricedLine, want)
		}
	}

	countsIdx := strings.Index(pricedLine, "0/0/0")
	ptIdx := strings.Index(pricedLine, "$0.10/0.05/0.20")
	subIdx := strings.Index(pricedLine, "$19.99/mo")
	if countsIdx == -1 || ptIdx == -1 || subIdx == -1 {
		t.Fatalf("expected segments missing from %q", pricedLine)
	}
	if ptIdx < countsIdx {
		t.Errorf("per-token pricing appears before usage counts in %q", pricedLine)
	}
	if subIdx < ptIdx {
		t.Errorf("subscription pricing appears before per-token pricing in %q", pricedLine)
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
// segment renders with a single leading "$" and a hyphen for the missing
// cache slot: "$0.50/-/1.00".
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
	items := buildModelItems(models, map[string]string{"partial": "test"}, store)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	line := items[0].Title()
	if !strings.Contains(line, "$0.50/-/1.00") {
		t.Errorf("partial pricing line %q missing expected $0.50/-/1.00", line)
	}
}
