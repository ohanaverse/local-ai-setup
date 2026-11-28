package sessioneditor

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
)

// enterAgentForm transitions the model into the agent edit/add form.
func enterAgentForm(m *model, ag config.Agent, isNew bool) {
	m.phase = phaseForm
	m.formIsNew = isNew
	m.formError = ""
	m.formCursor = 0
	m.agEdit = ag

	m.agName = editorkit.NewTextInput(ag.Name, "Name")
	m.agName.Focus()
	m.refreshAgentInstalled()
}

// refreshAgentInstalled updates the cached installation status from the
// current agent name. The lookup is skipped when the name is unchanged since
// the last refresh, so typing in the name field does not trigger a PATH
// lookup per keystroke.
func (m *model) refreshAgentInstalled() {
	name := strings.TrimSpace(m.agName.Value())
	if name == m.agInstalledName {
		return
	}
	m.agInstalledName = name
	m.agInstalled = agents.Installed(name)
}

// handleAgentFormUpdate processes keys in the agent form.
func (m *model) handleAgentFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			m.phase = phaseList
			return m, nil
		case tea.KeyCtrlS:
			return m.saveAgentForm()
		case tea.KeyTab, tea.KeyDown:
			m.formCursor = (m.formCursor + 1) % agentFormFields
			m.focusAgentField()
			return m, nil
		case tea.KeyUp:
			m.formCursor = (m.formCursor + agentFormFields - 1) % agentFormFields
			m.focusAgentField()
			return m, nil
		}

		// Only the name takes typing; the Installed line is display-only.
		if m.formCursor == 0 {
			var cmd tea.Cmd
			m.agName, cmd = m.agName.Update(msg)
			m.refreshAgentInstalled()
			return m, cmd
		}
	}
	return m, nil
}

// agentFormFields is the number of cursor positions in the agent form: the
// Name input and the read-only Installed line.
const agentFormFields = 2

// focusAgentField focuses/blurs the name input based on formCursor.
func (m *model) focusAgentField() {
	m.agName.Blur()
	if m.formCursor == 0 {
		m.agName.Focus()
	}
}

// saveAgentForm validates and applies the agent form to cfg. It starts from
// the stored entry (m.agEdit) and changes only the name, so the Synthesized
// flag stays as found: the form has no other input.
func (m *model) saveAgentForm() (tea.Model, tea.Cmd) {
	updated := m.agEdit
	updated.Name = strings.TrimSpace(m.agName.Value())

	oldName := ""
	if !m.formIsNew {
		oldName = m.agEdit.Name
	}
	if err := m.cfg.UpsertAgent(updated, oldName); err != nil {
		// An error is always about the name.
		m.formError = err.Error()
		m.formCursor = 0
		m.focusAgentField()
		return m, nil
	}

	m.dirty = true
	m.phase = phaseList
	m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
	return m, nil
}

// agentFormView renders the agent add/edit form.
func (m *model) agentFormView() string {
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

	fields := []editorkit.FormField{
		{Label: "Name", Value: m.agName.View(), Focus: m.formCursor == 0},
		{Label: "Installed", Value: installedStr, Focus: m.formCursor == 1},
	}

	var b strings.Builder
	b.WriteString(title + "\n\n")
	b.WriteString(editorkit.RenderFormFields(m.theme, fields))
	if m.formError != "" {
		b.WriteString("\n" + m.formError + "\n")
	}
	b.WriteString("\nCtrl-S save  Esc cancel\n")
	return b.String()
}
