package configeditor

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// enterAgentForm transitions the model into the agent edit/add form.
func enterAgentForm(m *model, ag config.Agent, isNew bool) {
	m.phase = phaseForm
	m.formKind = formAgent
	m.formIsNew = isNew
	m.formError = ""
	m.formCursor = 0
	m.agEdit = ag

	m.agName = newTextInput(ag.Name, "Name")
	m.agName.Focus()
	m.agProvidersInput = newTextInput(config.TagsToString(ag.SupportedProviders), "Supported Providers")
	m.agDefaultProviderInput = newTextInput(ag.DefaultProvider, "Default Provider")
	m.refreshAgentInstalled()
}

// refreshAgentInstalled updates the cached installation status from the
// current agent name.
func (m *model) refreshAgentInstalled() {
	m.agInstalled = agents.Installed(strings.TrimSpace(m.agName.Value()))
}

// handleAgentFormUpdate processes keys in the agent form.
func (m model) handleAgentFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			m.phase = phaseList
			m.formKind = formNone
			return m, nil
		case tea.KeyCtrlS:
			return m.saveAgentForm()
		case tea.KeyTab, tea.KeyDown:
			m.formCursor = (m.formCursor + 1) % 4
			m.focusAgentField()
			return m, nil
		case tea.KeyUp:
			m.formCursor = (m.formCursor + 3) % 4
			m.focusAgentField()
			return m, nil
		}

		switch m.formCursor {
		case 0:
			var cmd tea.Cmd
			m.agName, cmd = m.agName.Update(msg)
			m.refreshAgentInstalled()
			return m, cmd
		case 1:
			var cmd tea.Cmd
			m.agProvidersInput, cmd = m.agProvidersInput.Update(msg)
			return m, cmd
		case 2:
			var cmd tea.Cmd
			m.agDefaultProviderInput, cmd = m.agDefaultProviderInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// focusAgentField focuses/blurs inputs based on formCursor.
func (m *model) focusAgentField() {
	m.agName.Blur()
	m.agProvidersInput.Blur()
	m.agDefaultProviderInput.Blur()
	switch m.formCursor {
	case 0:
		m.agName.Focus()
	case 1:
		m.agProvidersInput.Focus()
	case 2:
		m.agDefaultProviderInput.Focus()
	}
}

// saveAgentForm validates and applies the agent form to cfg.
func (m model) saveAgentForm() (tea.Model, tea.Cmd) {
	updated := m.agEdit
	updated.Name = strings.TrimSpace(m.agName.Value())
	updated.SupportedProviders = config.ParseFilterList(m.agProvidersInput.Value())
	updated.DefaultProvider = strings.TrimSpace(m.agDefaultProviderInput.Value())

	oldName := ""
	if !m.formIsNew {
		oldName = m.agEdit.Name
	}
	if err := m.cfg.UpsertAgent(updated, oldName); err != nil {
		m.formError = err.Error()
		switch {
		case updated.Name == "":
			m.formCursor = 0
		case len(updated.SupportedProviders) == 0:
			m.formCursor = 1
		default:
			m.formCursor = 2
		}
		m.focusAgentField()
		return m, nil
	}

	m.dirty = true
	m.phase = phaseList
	m.formKind = formNone
	m.lists[sectionAgents] = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
	return m, nil
}

// agentFormView renders the agent add/edit form.
func (m model) agentFormView() string {
	installedStr := "✗ not installed"
	if m.agInstalled {
		installedStr = "✓ installed"
	}

	title := "Agent"
	if d := agents.ByName(m.agName.Value()); d != nil {
		if yolo := d.YoloFlag(); yolo != "" {
			title = fmt.Sprintf("Agent  (yolo flag: %s)", yolo)
		}
	}

	fields := []formField{
		{"Name", m.agName.View(), m.formCursor == 0},
		{"Supported Providers", m.agProvidersInput.View(), m.formCursor == 1},
		{"Default Provider", m.agDefaultProviderInput.View(), m.formCursor == 2},
		{"Installed", installedStr, m.formCursor == 3},
	}

	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(renderFormFields(m.theme, fields))
	if m.formError != "" {
		b.WriteString("\n" + m.formError + "\n")
	}
	b.WriteString("\nCtrl-S save  Esc cancel\n")
	return b.String()
}
