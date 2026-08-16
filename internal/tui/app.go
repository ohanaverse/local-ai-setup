// Package tui implements the Bubble Tea terminal UI for wt.
//
// Lesson 12 establishes the app shell: a single screen that shows a status,
// responds to q/esc/ctrl+c, and demonstrates the Model/Update/View cycle.
// Lesson 13 layers on the worktree/branch picker using bubbles/list.
package tui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// model holds the entire UI state.
type model struct {
	status  string
	width   int
	height  int

	entries []worktree.Entry
	list    list.Model
	loading bool
	ready   bool
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
		m.status = "selected: " + msg.entry.Branch
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.ready {
				item, ok := m.list.SelectedItem().(entryItem)
				if ok {
					return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
				}
			}
		}
	}

	if m.ready {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
}

// View renders the screen as a string.
func (m model) View() string {
	switch {
	case m.loading:
		return "loading worktrees..."
	case !m.ready:
		return m.status
	default:
		return m.list.View()
	}
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
	p := tea.NewProgram(model{loading: true, status: "loading worktrees..."}, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
