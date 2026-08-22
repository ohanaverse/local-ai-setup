package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/usage"
)

// modelItem adapts a config.Model to a list.Item for the model picker.
type modelItem struct {
	model  config.Model
	counts usage.UsageCounts
}

// FilterValue returns the model ID so the list's built-in fuzzy filter
// (currently unused) would narrow by ID.
func (m modelItem) FilterValue() string { return m.model.ID }

// Title renders the model ID — the primary identifier users scan for.
func (m modelItem) Title() string { return m.model.ID }

// Description renders the metadata columns: provider, location, tags, and
// 1d/7d/30d usage counts. Counts are display-only and help the user spot
// model-selection bias.
func (m modelItem) Description() string {
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %-20s  1d:%-3d 7d:%-3d 30d:%-3d",
		m.model.ProviderID,
		string(m.model.Location),
		tags,
		m.counts.OneDay,
		m.counts.SevenDay,
		m.counts.ThirtyDay,
	)
}

// buildModelList builds a bubble/list from the given models. The caller
// passes the desired width/height. The list is created with a themed
// delegate (ThemedListDelegate) so the picker honors the active color
// theme, and a fixed title. counts is attached to each item for display.
func buildModelList(models []config.Model, counts map[string]usage.UsageCounts, theme themes.Theme, width, height int) list.Model {
	items := make([]list.Item, 0, len(models))
	for _, m := range models {
		items = append(items, modelItem{model: m, counts: counts[m.ID]})
	}
	l := list.New(items, ThemedListDelegate(theme), width, height)
	l.Title = "Models"
	l.SetShowStatusBar(false)
	return l
}

// indexOfModel returns the index of target in models, or -1 if not found.
// Used to position the list cursor on the rotation's next-to-use model
// in selectedEntryMsg.
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
// picker's cursor positioning (the picker reads rotation.Last to
// locate the prior launch and calls FindAfter to advance past it)
// and the rotation tests; model_list.go imports the rotation
// package for exactly this call.
func FindAfter(models []config.Model, target config.Model) (config.Model, bool) {
	return rotation.FirstAfter(models, target)
}

// phaseModelView renders the model picker screen: the list of
// agent+tag-compatible models, an agent/tag header, and a footer
// describing the keybinds. The picker IS the agent+model screen —
// there is no separate browser.
func (m *model) phaseModelView() string {
	pad := lipgloss.NewStyle().Padding(1, 2)
	headerStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader))
	dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	header := headerStyle.Render(fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag))
	footer := dimStyle.Render("\n[↑/↓] navigate   [enter] launch   [q] quit")
	body := header + m.models.View() + footer
	// A launch/config/session/ollama error set on the model phase must be
	// visible; phaseModelView previously dropped m.status, making a failed
	// launch look like "nothing happens" when Enter was pressed.
	if m.status != "" {
		body = ErrorStyle(m.theme).Render(m.status) + "\n\n" + body
	}
	return pad.Render(body)
}
