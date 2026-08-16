// Package tui implements the Bubble Tea terminal UI for wt.
//
// Lesson 12 establishes the app shell: a single screen that shows a status,
// responds to q/esc/ctrl+c, and demonstrates the Model/Update/View cycle.
// Lesson 13 layers on the worktree/branch picker using bubbles/list.
// Lesson 14 adds the agent+model screen reached after picking a worktree.
package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// phase identifies which screen the TUI is currently showing.
type phase int

const (
	phaseList  phase = iota // worktree list (lesson 13)
	phaseModel              // agent+model screen (this lesson)
)

// model holds the entire UI state.
type model struct {
	status string
	width  int
	height int

	entries []worktree.Entry
	list    list.Model
	loading bool
	ready   bool

	phase    phase
	agent    string         // current agent name
	tag      string         // active rotation tag group
	otherTag string         // tag group to cross-skip against during rotation
	current  config.Model   // currently shown model
	cfg      *config.Config // loaded config for the model catalog
}

// Init returns the initial command: load worktrees/branches.
func (m model) Init() tea.Cmd {
	return loadEntriesCmd()
}

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-2)
		}
	case entriesLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.entries = msg.entries
		m.list = buildList(msg.entries, m.width-2, m.height-2)
		m.ready = true
	case selectedEntryMsg:
		// Selection is stored for lesson 16's launch. Move to the model
		// phase and pick an initial agent + model (first agent default,
		// first model in the default tag group).
		m.phase = phaseModel
		m.agent = firstAgent(m.cfg)
		m.tag = m.cfg.DefaultTag
		m.current = firstModel(m.cfg, m.tag)
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.ready && m.phase == phaseList {
				item, ok := m.list.SelectedItem().(entryItem)
				if ok {
					return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
				}
			}
		case "r":
			if m.phase == phaseModel {
				// Rotate to the next model in the active tag group, skipping
				// whatever the other tag group last used (cross-tag skip).
				rot := rotation.ForTag(m.cfg, m.tag)
				next, ok := rot.Next(m.otherTag)
				if ok {
					m.current = next
				}
			}
		case "m":
			if m.phase == phaseModel {
				m.status = "model browser coming in lesson 15"
			}
		case "d":
			if m.phase == phaseModel {
				// Toggle the active tag group between code and design.
				if m.tag == "code" {
					m.tag, m.otherTag = "design", "code"
				} else {
					m.tag, m.otherTag = "code", "design"
				}
				// Re-resolve the shown model to the new group's first entry.
				m.current = firstModel(m.cfg, m.tag)
			}
		}
	}

	if m.ready && m.phase == phaseList {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
}

// View renders the screen as a string.
func (m model) View() string {
	if m.phase == phaseModel {
		style := lipgloss.NewStyle().Padding(2, 2)
		return style.Render(
			fmt.Sprintf("agent : %s\nmodel : %s\n\ntag : %s\n\n"+
				"[r] rotate   [m] browse models   [enter] launch   [q] quit",
				m.agent, m.current.ID, m.tag))
	}
	switch {
	case m.loading:
		return "loading worktrees..."
	case !m.ready:
		return m.status
	default:
		return m.list.View()
	}
}

// firstAgent returns the first configured agent, defaulting to "claude".
func firstAgent(cfg *config.Config) string {
	if cfg != nil && len(cfg.Agents) > 0 {
		return cfg.Agents[0].Name
	}
	return "claude"
}

// firstModel returns the first model in a tag group, or a "(none)" placeholder.
func firstModel(cfg *config.Config, tag string) config.Model {
	none := config.Model{ID: "(none)", ProviderID: "", Location: config.LocationCloud}
	if cfg == nil {
		return none
	}
	ms := cfg.ModelsWithTag(tag)
	if len(ms) == 0 {
		return none
	}
	return ms[0]
}

// loadEntriesCmd returns a command that enumerates worktrees/branches.
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		entries, err := worktree.Enumerate(root, root)
		return entriesLoadedMsg{entries: entries, err: err}
	}
}

// entriesLoadedMsg carries the enumeration result to Update.
type entriesLoadedMsg struct {
	entries []worktree.Entry
	err     error
}

// Run starts the TUI in alternate-screen mode and returns when it quits.
func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := tea.NewProgram(model{loading: true, status: "loading worktrees...", cfg: cfg}, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
