package configeditor

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// deleteTarget identifies the agent pending deletion.
type deleteTarget struct {
	id string
}

// enterDelete transitions to the delete confirmation phase.
func enterDelete(m *model, id string) {
	m.phase = phaseDelete
	m.deleteTarget = deleteTarget{id: id}
	m.deleteError = ""
}

// handleDeleteUpdate processes keys in the delete confirmation prompt.
func (m model) handleDeleteUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			return m.confirmDelete()
		case "n", "N", "esc":
			m.phase = phaseList
			m.deleteTarget = deleteTarget{id: ""}
			return m, nil
		}
	}
	return m, nil
}

// confirmDelete removes the agent and rebuilds the list.
func (m model) confirmDelete() (tea.Model, tea.Cmd) {
	if m.deleteTarget.id == "" {
		m.phase = phaseList
		return m, nil
	}

	m.cfg.DeleteAgent(m.deleteTarget.id)

	m.dirty = true
	m.phase = phaseList
	m.deleteTarget = deleteTarget{id: ""}
	m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
	return m, nil
}

// deleteView renders the delete confirmation prompt.
func (m model) deleteView() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Delete agent %q? [y/N]\n", m.deleteTarget.id))
	if m.deleteError != "" {
		b.WriteString("\n" + m.deleteError + "\n")
	}
	return b.String()
}
