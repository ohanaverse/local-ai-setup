package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// FindAfter must return the model at target+1, wrapping to models[0]
// when target is the last item. The picker uses this to position the
// cursor at the next rotation entry.
func TestFindAfterMiddle(t *testing.T) {
	models := []config.Model{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got, ok := FindAfter(models, config.Model{ID: "b"})
	if !ok || got.ID != "c" {
		t.Errorf("FindAfter = (%q, %v), want (c, true)", got.ID, ok)
	}
}

// FindAfter must return models[0] when target is not in the list.
// Mirrors the rotation.FirstAfter contract.
func TestFindAfterMissing(t *testing.T) {
	models := []config.Model{{ID: "a"}}
	got, ok := FindAfter(models, config.Model{ID: "ghost"})
	if !ok || got.ID != "a" {
		t.Errorf("FindAfter = (%q, %v), want (a, true)", got.ID, ok)
	}
}

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
