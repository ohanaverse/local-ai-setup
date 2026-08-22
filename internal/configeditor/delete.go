package configeditor

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// deleteTarget identifies which item is pending deletion.
type deleteTarget struct {
	section section
	id      string
}

// enterDelete transitions to the delete confirmation phase.
func enterDelete(m *model, sec section, id string) {
	m.phase = phaseDelete
	m.deleteTarget = deleteTarget{section: sec, id: id}
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

// deleteTargetInfo returns the human-readable kind and id for the current
// delete target, or ("", "") if no target is set.
func deleteTargetInfo(m model) (kind, id string) {
	if m.deleteTarget.id == "" {
		return "", ""
	}
	switch m.deleteTarget.section {
	case sectionAgents:
		return "agent", m.deleteTarget.id
	case sectionProviders:
		return "provider", m.deleteTarget.id
	case sectionModels:
		return "model", m.deleteTarget.id
	}
	return "", ""
}

// confirmDelete runs the FK check and either removes the row or sets an error.
func (m model) confirmDelete() (tea.Model, tea.Cmd) {
	target := m.deleteTarget
	kind, id := deleteTargetInfo(m)
	if kind == "" || id == "" {
		m.phase = phaseList
		return m, nil
	}

	switch target.section {
	case sectionAgents:
		m.cfg.DeleteAgent(id)
	case sectionProviders:
		if refs := m.cfg.ProviderInUse(id); len(refs) > 0 {
			m.deleteError = fmt.Sprintf("cannot delete provider %q: referenced by %s", id, strings.Join(refs, ", "))
			return m, nil
		}
		m.cfg.DeleteProvider(id)
	case sectionModels:
		m.cfg.DeleteModel(id)
	}

	m.dirty = true
	m.phase = phaseList
	m.deleteTarget = deleteTarget{id: ""}
	m.lists[target.section] = buildListForSection(m, target.section)
	return m, nil
}

// buildListForSection rebuilds the list for the given section.
func buildListForSection(m model, sec section) list.Model {
	switch sec {
	case sectionAgents:
		return buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
	case sectionProviders:
		return buildProvidersList(m.theme, m.width-2, m.height-4, m.cfg)
	case sectionModels:
		return buildModelsList(m.theme, m.width-2, m.height-4, m.cfg)
	}
	return list.Model{}
}

// deleteView renders the delete confirmation prompt.
func (m model) deleteView() string {
	kind, id := deleteTargetInfo(m)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Delete %s %q? [y/N]\n", kind, id))
	if m.deleteError != "" {
		b.WriteString("\n" + m.deleteError + "\n")
	}
	return b.String()
}
