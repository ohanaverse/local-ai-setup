package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// pickModel is a minimal standalone Bubble Tea program that shows the same
// selector table buildTable produces for the main TUI's agent flow, for
// callers (e.g. wt smoke) that need a one-off model pick without the full
// worktree->agent->model->launch state machine. Unlike that flow, the
// list here is never scoped to a single agent, so its survey segment is
// always empty (agent-specific) and undiscovered models are hidden (wt smoke
// only shows the models it passes in). A per-row exception marker can still
// appear: passing a non-nil cfg still runs buildTable's route-resolution
// branch, so "(litellm required)"/"(unavailable)" can show; only
// "(via proxy)" is suppressed, since ProtocolsFor("") returns nil and
// ResolveRoute's forced-through-proxy path never triggers for an empty
// protocol requirement.
type pickModel struct {
	list     list.Model
	selected config.Model
	canceled bool
	quitting bool
	// notice is a one-line message under the list, set when Enter hits a
	// blocked row and cleared on the next key.
	notice string
	// width and height are the terminal's: the list is sized to what the
	// picker's frame leaves of them after every message (fit), and the frame
	// clips the notice to the width.
	width, height int
}

// frame is the picker's layout (layout.go): the list, and the notice under it
// when there is one. View renders through it and fit measures it, so a line
// added here is counted in the list's height; one added to View instead would
// push the table's header off the top of the screen.
func (m pickModel) frame() listFrame {
	return func(listView string) string {
		if m.notice != "" {
			return listView + "\n" + clip(m.notice, m.width)
		}
		return listView
	}
}

// fit sizes the list to the room the frame leaves, as the agent flow's fitLists
// does for its screens. Before the terminal has reported a usable size the list
// keeps the size it was built with: bubbles is never given a zero width.
func (m *pickModel) fit() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	fitTo(&m.list, m.width, m.height, m.frame())
}

// newPickModel builds the picker's list from models via the same buildTable
// the agent flow uses. cfg may be nil (no inventory probe and no per-row route
// resolution). The agent argument is "" and stats is nil since this list isn't
// scoped to one agent; discovered (unregistered) local models are hidden.
// The caller's models may include start-able and blocked rows; blocked rows
// are shown (with their reason) but Enter on them does not select.
func newPickModel(cfg *config.Config, models []config.Model, theme themes.Theme, skipRoute bool) pickModel {
	var snap *localmodels.Snapshot
	if cfg != nil {
		s := runInventory(cfg)
		snap = &s
	}
	tbl := buildTable(tableInput{
		cfg: cfg, agent: "", models: models, inventory: snap, hideDiscovered: true, skipRoute: skipRoute,
		usage: newUsageStore(),
	}, newRefcountStore(), "")
	items := tbl.items
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
	l.Title = tbl.header
	styleTableTitle(&l, theme)
	l.SetShowStatusBar(false)
	return pickModel{list: l}
}

func (m pickModel) Init() tea.Cmd { return nil }

// Update mirrors app.go's phaseModel key handling: 'q' cancels only when the
// list isn't in filter mode (letting it type into the query otherwise, per
// TestQDoesNotQuitWhileFilteringModelList's fix for the main TUI), Esc and
// Ctrl+C always cancel, and Enter selects the highlighted row unless it's
// committing a filter query instead.
//
// The list is fitted after every message, not only a resize: the notice
// appearing or being cleared and `?` expanding the help each change the room
// the list has, and a list sized one message late is a header pushed off the
// screen (see fitLists).
func (m pickModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next.fit()
	return next, cmd
}

func (m pickModel) update(msg tea.Msg) (pickModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		m.notice = ""
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
					if it.blocked != "" {
						m.notice = it.blocked
						return m, nil
					}
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
	return m.frame()(m.list.View())
}

// PickModel runs a standalone Bubble Tea program showing the selector
// table — the same columns the wt agent flow's model picker uses — and returns the user's selection. ok is false when the user
// canceled (Esc/q/Ctrl+C) rather than selecting a model. Unlike the agent
// flow's picker, models is never filtered down to one agent's eligible set;
// callers scoped to "any agent that can run this model" (e.g. wt smoke)
// pass their own unfiltered union list. models may include start and blocked
// rows (wt start); blocked rows are shown but not selectable.
func PickModel(cfg *config.Config, models []config.Model, theme themes.Theme) (config.Model, bool, error) {
	return runPick(newPickModel(cfg, models, theme, false))
}

// PickStartModel is PickModel for callers that do not launch an agent from the
// pick: `wt start` (starting is not launching) and `wt smoke` (its candidates
// were already route-checked per agent), so the launch-route rules (a
// cloud model not in LiteLLM, a route that fails to resolve) do not block
// or decorate rows.
// Rows that cannot start at all (no start backend) stay blocked; a model that
// is not on disk has no row to block.
func PickStartModel(cfg *config.Config, models []config.Model, theme themes.Theme) (config.Model, bool, error) {
	return runPick(newPickModel(cfg, models, theme, true))
}

func runPick(m pickModel) (config.Model, bool, error) {
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return config.Model{}, false, err
	}
	fm := final.(pickModel)
	return fm.selected, !fm.canceled, nil
}
