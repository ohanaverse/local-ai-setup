// Package tui implements the Bubble Tea terminal UI for wt.
//
// Lesson 12 establishes the app shell: a single screen that shows a status,
// responds to q/esc/ctrl+c, and demonstrates the Model/Update/View cycle.
// Subsequent lessons (13+) layer on the worktree list, model browser, and
// the actual launch.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// model holds the entire UI state. For now, a single screen with a status.
type model struct {
	status string
	width  int
	height int
}

// Init returns any initial commands (none yet).
func (m model) Init() tea.Cmd { return nil }

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders the screen as a string.
func (m model) View() string {
	style := lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center)
	return style.Render(fmt.Sprintf("wt\n%s\n\nPress q to quit", m.status))
}

// Run starts the TUI in alternate-screen mode and returns when it quits.
func Run() error {
	p := tea.NewProgram(model{status: "ready"}, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
