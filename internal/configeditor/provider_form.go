package configeditor

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// enterProviderForm transitions the model into the provider edit/add form.
func enterProviderForm(m *model, prov config.Provider, isNew bool) {
	m.phase = phaseForm
	m.formKind = formProvider
	m.formIsNew = isNew
	m.formError = ""
	m.provEdit = prov

	m.provID = newTextInput(prov.ID, "ID")
	if isNew {
		m.formCursor = 0
		m.provID.Focus()
	} else {
		m.formCursor = 1
	}
	m.provName = newTextInput(prov.Name, "Name")
	if !isNew {
		m.provName.Focus()
	}
	m.provAuth = newTextInput(prov.Auth.Type, "Auth Type")
	m.provBaseURL = newTextInput(prov.Auth.BaseURL, "Base URL")
	m.provLoc = prov.Location
	if m.provLoc == "" {
		m.provLoc = config.LocationLocal
	}
}

func newTextInput(value, placeholder string) textinput.Model {
	inp := textinput.New()
	inp.SetValue(value)
	inp.Placeholder = placeholder
	return inp
}

// handleProviderFormUpdate processes keys in the provider form.
func (m model) handleProviderFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			m.phase = phaseList
			m.formKind = formNone
			return m, nil
		case tea.KeyCtrlS:
			return m.saveProviderForm()
		case tea.KeyTab, tea.KeyDown:
			if m.formIsNew {
				m.formCursor = (m.formCursor + 1) % 5
			} else {
				m.formCursor = 1 + (m.formCursor)%4
			}
			m.focusProviderField()
			return m, nil
		case tea.KeyUp:
			if m.formIsNew {
				m.formCursor = (m.formCursor + 4) % 5
			} else {
				m.formCursor = 1 + (m.formCursor+2)%4
			}
			m.focusProviderField()
			return m, nil
		case tea.KeySpace:
			if m.formCursor == 4 {
				m.provLoc = config.ToggleLocation(m.provLoc)
				return m, nil
			}
		}

		// Delegate to focused textinput.
		switch m.formCursor {
		case 0:
			if m.formIsNew {
				var cmd tea.Cmd
				m.provID, cmd = m.provID.Update(msg)
				return m, cmd
			}
		case 1:
			var cmd tea.Cmd
			m.provName, cmd = m.provName.Update(msg)
			return m, cmd
		case 2:
			var cmd tea.Cmd
			m.provAuth, cmd = m.provAuth.Update(msg)
			return m, cmd
		case 3:
			var cmd tea.Cmd
			m.provBaseURL, cmd = m.provBaseURL.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// focusProviderField focuses/blurs inputs based on formCursor.
func (m *model) focusProviderField() {
	m.provID.Blur()
	m.provName.Blur()
	m.provAuth.Blur()
	m.provBaseURL.Blur()
	switch m.formCursor {
	case 0:
		if m.formIsNew {
			m.provID.Focus()
		}
	case 1:
		m.provName.Focus()
	case 2:
		m.provAuth.Focus()
	case 3:
		m.provBaseURL.Focus()
	}
}

// saveProviderForm validates and applies the provider form to cfg.
func (m model) saveProviderForm() (tea.Model, tea.Cmd) {
	updated := m.provEdit
	if m.formIsNew {
		updated.ID = strings.TrimSpace(m.provID.Value())
	}
	updated.Name = strings.TrimSpace(m.provName.Value())
	updated.Auth.Type = strings.TrimSpace(m.provAuth.Value())
	updated.Auth.BaseURL = strings.TrimSpace(m.provBaseURL.Value())
	updated.Location = m.provLoc

	if err := m.cfg.UpsertProvider(updated, m.formIsNew); err != nil {
		m.formError = err.Error()
		if updated.ID == "" {
			m.formCursor = 0
		}
		m.focusProviderField()
		return m, nil
	}

	m.dirty = true
	m.phase = phaseList
	m.formKind = formNone
	m.lists[sectionProviders] = buildProvidersList(m.theme, m.width-2, m.height-4, m.cfg)
	return m, nil
}

// providerFormView renders the provider add/edit form.
func (m model) providerFormView() string {
	idVal := m.provID.View()
	if !m.formIsNew {
		idVal = m.provEdit.ID
	}

	fields := []formField{
		{"ID", idVal, m.formCursor == 0},
		{"Name", m.provName.View(), m.formCursor == 1},
		{"Auth Type", m.provAuth.View(), m.formCursor == 2},
		{"Base URL", m.provBaseURL.View(), m.formCursor == 3},
		{"Location", string(m.provLoc), m.formCursor == 4},
	}

	var b strings.Builder
	b.WriteString("Provider\n\n")
	b.WriteString(renderFormFields(m.theme, fields))
	if m.formError != "" {
		b.WriteString("\n" + m.formError + "\n")
	}
	b.WriteString("\nCtrl-S save  Esc cancel\n")
	return b.String()
}

