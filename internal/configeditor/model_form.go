package configeditor

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// enterModelForm transitions the model into the model edit/add form.
func enterModelForm(m *model, mod config.Model, isNew bool) {
	m.phase = phaseForm
	m.formKind = formModel
	m.formIsNew = isNew
	m.formError = ""
	m.modEdit = mod

	m.modID = newTextInput(mod.ID, "ID")
	if isNew {
		m.formCursor = 0
		m.modID.Focus()
	} else {
		m.formCursor = 1
	}
	m.modFamily = newTextInput(mod.Family, "Family")
	if !isNew {
		m.modFamily.Focus()
	}
	m.modProv = newTextInput(mod.ProviderID, "Provider ID")
	m.modName = newTextInput(mod.ModelName, "Model Name")
	m.modTags = newTextInput(config.TagsToString(mod.Tags), "Tags")
	m.modLoc = mod.Location
	m.refreshModelResolvedLocation()
}

// refreshModelResolvedLocation updates the cached resolved location from the
// current provider ID and explicit location.
func (m *model) refreshModelResolvedLocation() {
	loc, _ := m.cfg.ResolveLocation(config.Model{ProviderID: strings.TrimSpace(m.modProv.Value()), Location: m.modLoc})
	m.modResolvedLoc = loc
}

// handleModelFormUpdate processes keys in the model form.
func (m model) handleModelFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			m.phase = phaseList
			m.formKind = formNone
			return m, nil
		case tea.KeyCtrlS:
			return m.saveModelForm()
		case tea.KeyTab, tea.KeyDown:
			if m.formIsNew {
				m.formCursor = (m.formCursor + 1) % 6
			} else {
				m.formCursor = 1 + (m.formCursor)%5
			}
			m.focusModelField()
			return m, nil
		case tea.KeyUp:
			if m.formIsNew {
				m.formCursor = (m.formCursor + 5) % 6
			} else {
				m.formCursor = 1 + (m.formCursor+3)%5
			}
			m.focusModelField()
			return m, nil
		case tea.KeySpace:
			if m.formCursor == 5 {
				switch m.modLoc {
				case "":
					m.modLoc = config.LocationLocal
				case config.LocationLocal:
					m.modLoc = config.LocationCloud
				case config.LocationCloud:
					m.modLoc = ""
				default:
					m.modLoc = config.LocationLocal
				}
				m.refreshModelResolvedLocation()
				return m, nil
			}
		}

		switch m.formCursor {
		case 0:
			if m.formIsNew {
				var cmd tea.Cmd
				m.modID, cmd = m.modID.Update(msg)
				return m, cmd
			}
		case 1:
			var cmd tea.Cmd
			m.modFamily, cmd = m.modFamily.Update(msg)
			return m, cmd
		case 2:
			var cmd tea.Cmd
			m.modProv, cmd = m.modProv.Update(msg)
			m.refreshModelResolvedLocation()
			return m, cmd
		case 3:
			var cmd tea.Cmd
			m.modName, cmd = m.modName.Update(msg)
			return m, cmd
		case 4:
			var cmd tea.Cmd
			m.modTags, cmd = m.modTags.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// focusModelField focuses/blurs inputs based on formCursor.
func (m *model) focusModelField() {
	m.modID.Blur()
	m.modFamily.Blur()
	m.modProv.Blur()
	m.modName.Blur()
	m.modTags.Blur()
	switch m.formCursor {
	case 0:
		if m.formIsNew {
			m.modID.Focus()
		}
	case 1:
		m.modFamily.Focus()
	case 2:
		m.modProv.Focus()
	case 3:
		m.modName.Focus()
	case 4:
		m.modTags.Focus()
	}
}

// saveModelForm validates and applies the model form to cfg.
func (m model) saveModelForm() (tea.Model, tea.Cmd) {
	updated := m.modEdit
	if m.formIsNew {
		updated.ID = strings.TrimSpace(m.modID.Value())
	}
	updated.Family = strings.TrimSpace(m.modFamily.Value())
	updated.ProviderID = strings.TrimSpace(m.modProv.Value())
	updated.ModelName = strings.TrimSpace(m.modName.Value())
	updated.Tags = config.ParseFilterList(m.modTags.Value())
	updated.Location = m.modLoc

	if err := m.cfg.UpsertModel(updated, m.formIsNew); err != nil {
		m.formError = err.Error()
		// Put the cursor on the offending field when we can tell which one.
		switch {
		case updated.ID == "":
			m.formCursor = 0
		case updated.ProviderID == "" || m.cfg.ProviderByID(updated.ProviderID) == nil:
			m.formCursor = 2
		default:
			m.formCursor = 0
		}
		m.focusModelField()
		return m, nil
	}

	m.dirty = true
	m.phase = phaseList
	m.formKind = formNone
	m.lists[sectionModels] = buildModelsList(m.theme, m.width-2, m.height-4, m.cfg)
	return m, nil
}

// modelFormView renders the model add/edit form.
func (m model) modelFormView() string {
	locStr := string(m.modResolvedLoc)
	if locStr == "" {
		locStr = "-"
	}
	if m.modLoc == "" && locStr != "-" {
		locStr += " (derived)"
	}

	idVal := m.modID.View()
	if !m.formIsNew {
		idVal = m.modEdit.ID
	}

	fields := []formField{
		{"ID", idVal, m.formCursor == 0},
		{"Family", m.modFamily.View(), m.formCursor == 1},
		{"Provider ID", m.modProv.View(), m.formCursor == 2},
		{"Model Name", m.modName.View(), m.formCursor == 3},
		{"Tags", m.modTags.View(), m.formCursor == 4},
		{"Location", locStr, m.formCursor == 5},
	}

	var b strings.Builder
	b.WriteString("Model\n\n")
	b.WriteString(renderFormFields(m.theme, fields))
	if m.formError != "" {
		b.WriteString("\n" + m.formError + "\n")
	}
	b.WriteString("\nCtrl-S save  Esc cancel\n")
	return b.String()
}

