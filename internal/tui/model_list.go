package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
)

// modelItem adapts a config.Model to a list.Item for the model picker.
type modelItem struct {
	model config.Model
}

// FilterValue returns the model ID so the list's built-in fuzzy filter
// (currently unused) would narrow by ID.
func (m modelItem) FilterValue() string { return m.model.ID }

// Title renders the model ID — the primary identifier users scan for.
func (m modelItem) Title() string { return m.model.ID }

// Description renders the metadata columns: provider, location, tags.
// Source is omitted because the picker is sourced from config.toml only
// (every model is curated). The column is reserved for future use if
// the picker grows a source filter.
func (m modelItem) Description() string {
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %s",
		m.model.ProviderID,
		string(m.model.Location),
		tags,
	)
}

// buildModelList builds a bubble/list from the given models. The caller
// passes the desired width/height. The list is created with the default
// delegate and a fixed title.
func buildModelList(models []config.Model, width, height int) list.Model {
	items := make([]list.Item, 0, len(models))
	for _, m := range models {
		items = append(items, modelItem{model: m})
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = "Models"
	l.SetShowStatusBar(false)
	return l
}

// indexOfModel returns the index of target in models, or -1 if not found.
// Used to position the list cursor on the rotation's next-to-use model
// (in selectedEntryMsg and the 'd' tag toggle).
func indexOfModel(models []config.Model, target config.Model) int {
	for i, m := range models {
		if m.ID == target.ID {
			return i
		}
	}
	return -1
}

// FindAfter returns the model that comes after target in models,
// wrapping to models[0] when target is the last or missing. It is a
// thin, testable wrapper around rotation.FirstAfter shared by the
// picker's cursor positioning (positionAfterLastLaunched) and the
// rotation tests; model_list.go imports the rotation package for
// exactly this call.
func FindAfter(models []config.Model, target config.Model) (config.Model, bool) {
	return rotation.FirstAfter(models, target)
}

// positionAfterLastLaunched rebuilds the rotation snapshot for tag over
// models and positions the picker cursor on the model after the
// last-launched one, falling back to index 0 when there is no
// last-launched model or its ID is no longer in the snapshot. Shared by
// the picker-entry (selectedEntryMsg) and 'd' tag-toggle paths so the
// cursor-positioning logic lives in one place.
func (m *model) positionAfterLastLaunched(tag string, models []config.Model) {
	m.rotation = rotation.New(tag, models, "")
	if last, ok := m.rotation.LastLaunched(); ok {
		if next, ok := FindAfter(models, last); ok {
			if idx := indexOfModel(models, next); idx >= 0 {
				m.models.Select(idx)
			}
		}
	}
}

// phaseModelView renders the model picker screen: the list of
// agent+tag-compatible models, an agent/tag header, and a footer
// describing the keybinds. The picker IS the agent+model screen —
// there is no separate browser.
func (m *model) phaseModelView() string {
	style := lipgloss.NewStyle().Padding(1, 2)
	header := fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag)
	footer := "\n[↑/↓] navigate   [d] switch tag   [enter] launch   [q] quit"
	return style.Render(header + m.models.View() + footer)
}
