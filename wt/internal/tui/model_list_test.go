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
	}, store)
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
}
