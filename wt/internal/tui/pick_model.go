package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// pickModel is a minimal standalone Bubble Tea program that shows the same
// decorated, sorted model list buildModelItems produces for the main TUI's
// agent flow, for callers (e.g. wt smoke) that need a one-off model pick
// without the full worktree->agent->model->resume->launch state machine.
// Unlike that flow, the list here is never scoped to a single agent, so its
// survey segment is always empty (agent-specific) — see PickModel. A
// per-row exception marker can still appear, though: passing a non-nil cfg
// still runs buildModelItems' route-resolution branch, so "(litellm
// required)"/"(unavailable)" can show; only "(via proxy)" is suppressed,
// since ProtocolsFor("") returns nil and ResolveRoute's forced-through-proxy
// path never triggers for an empty protocol requirement.
type pickModel struct {
	list     list.Model
	selected config.Model
	canceled bool
	quitting bool
}

// newPickModel builds the picker's list from models via the same
// buildModelItems the agent flow uses. cfg may be nil (buildModelItems skips
// per-row route resolution in that case). The agent argument to
// buildModelItems is "" and stats is nil since this list isn't scoped to one
// agent. familyOf must map the FULL catalog, not just models (an eligible
// subset — e.g. wt smoke's cross-agent union), so a family's 30-day total
// reflects every model in it, matching buildModelItems' contract
// (model_list.go's doc comment) and the agent flow's own picker. cfg.Models
// is that full catalog; a nil cfg (picker-only tests) falls back to models
// itself since there's nothing wider to draw from.
func newPickModel(cfg *config.Config, models []config.Model, theme themes.Theme) pickModel {
	catalog := models
	if cfg != nil {
		catalog = cfg.Models
	}
	familyOf := make(map[string]string, len(catalog))
	for _, m := range catalog {
		familyOf[m.ID] = m.Family
	}
	items := buildModelItems(cfg, "", models, familyOf, newUsageStore(), newRefcountStore(), "", nil)
	delegate := ThemedListDelegate(theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	listItems := make([]list.Item, len(items))
	for i, it := range items {
		listItems[i] = it
	}
	// 80x24 matches the common default terminal size other picker fixtures
	// in this package use; the real program resizes via the WindowSizeMsg
	// bubbletea sends immediately on start.
	l := list.New(listItems, delegate, 80, 24)
	l.Title = "Pick a model"
	l.SetShowStatusBar(false)
	return pickModel{list: l}
}

func (m pickModel) Init() tea.Cmd { return nil }

// Update mirrors app.go's phaseModel key handling: 'q' cancels only when the
// list isn't in filter mode (letting it type into the query otherwise, per
// TestQDoesNotQuitWhileFilteringModelList's fix for the main TUI), Esc and
// Ctrl+C always cancel, and Enter selects the highlighted row unless it's
// committing a filter query instead.
func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width-2, msg.Height-2)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			m.quitting = true
			return m, tea.Quit
		case "q":
			if m.list.FilterState() != list.Filtering {
				m.canceled = true
				m.quitting = true
				return m, tea.Quit
			}
		case "enter":
			if m.list.FilterState() != list.Filtering {
				if it, ok := m.list.SelectedItem().(*modelItem); ok {
					m.selected = it.model
					m.quitting = true
					return m, tea.Quit
				}
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m pickModel) View() string {
	if m.quitting {
		return ""
	}
	return m.list.View()
}

// PickModel runs a standalone Bubble Tea program showing a decorated, sorted
// list of models — the same row format the wt agent flow's model picker
// uses — and returns the user's selection. ok is false when the user
// canceled (Esc/q/Ctrl+C) rather than selecting a model. Unlike the agent
// flow's picker, models is never filtered down to one agent's eligible set;
// callers scoped to "any agent that can run this model" (e.g. wt smoke)
// pass their own unfiltered union list.
func PickModel(cfg *config.Config, models []config.Model, theme themes.Theme) (config.Model, bool, error) {
	m := newPickModel(cfg, models, theme)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return config.Model{}, false, err
	}
	fm := final.(pickModel)
	return fm.selected, !fm.canceled, nil
}
