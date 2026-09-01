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
			[]list.Item{modelItem{model: config.Model{ID: "copilot/native"}}},
			list.NewDefaultDelegate(), 78, 22),
		width: 80, height: 24, theme: themes.Default,
	}
	view := m.View()
	if !strings.Contains(view, m.status) {
		t.Errorf("model View missing status %q in:\n%s", m.status, view)
	}
}

// TestModelItemDescriptionShowsCounts verifies the picker description line
// exposes the 1d/7d/30d counts so users can see their model bias.
func TestModelItemDescriptionShowsCounts(t *testing.T) {
	it := modelItem{
		model: config.Model{
			ID:         "ollama/gemma4:9b",
			ProviderID: "ollama",
			Location:   config.LocationLocal,
			Tags:       []string{"code"},
		},
		counts: usage.UsageCounts{OneDay: 2, SevenDay: 5, ThirtyDay: 10},
	}
	desc := it.Description()
	if !strings.Contains(desc, "1d:2") || !strings.Contains(desc, "7d:5") || !strings.Contains(desc, "30d:10") {
		t.Fatalf("Description %q missing expected counts", desc)
	}
}
