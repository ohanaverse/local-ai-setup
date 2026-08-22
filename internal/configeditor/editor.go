// Package configeditor provides a TUI for viewing and editing config.toml.
package configeditor

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

type phase int

const (
	phaseList phase = iota
	phaseForm
	phaseDelete
	phaseQuit
)

type section int

const (
	sectionAgents section = iota
	sectionProviders
	sectionModels
)

func (s section) String() string {
	switch s {
	case sectionAgents:
		return "agents"
	case sectionProviders:
		return "providers"
	case sectionModels:
		return "models"
	default:
		return "unknown"
	}
}

type formKind int

const (
	formNone formKind = iota
	formProvider
	formModel
	formAgent
)

// loadedMsg carries the config after loading.
type loadedMsg struct {
	cfg *config.Config
	err error
}

type model struct {
	phase  phase
	theme  themes.Theme
	cfg    *config.Config
	dirty  bool
	width  int
	height int
	ready   bool
	status  string // shown above the list
	saving  bool   // prevents duplicate save dispatches
	cfgErr  error  // captured at construction for the initial loadedMsg

	section section
	lists   [3]list.Model

	// delete state
	deleteTarget deleteTarget
	deleteError  string

	// quit state
	quitting bool // true when waiting for save-before-quit

	// Form state
	formKind   formKind
	formIsNew  bool
	formCursor int
	formError  string

	// Provider form fields
	provEdit    config.Provider
	provID      textinput.Model
	provName    textinput.Model
	provAuth    textinput.Model
	provBaseURL textinput.Model
	provLoc     config.Location

	// Model form fields
	modEdit        config.Model
	modID          textinput.Model
	modFamily      textinput.Model
	modProv        textinput.Model
	modName        textinput.Model
	modTags        textinput.Model
	modLoc         config.Location
	modResolvedLoc config.Location // cached to avoid ResolveLocation per frame

	// Agent form fields
	agEdit                 config.Agent
	agName                 textinput.Model
	agProvidersInput       textinput.Model // comma-separated provider IDs
	agDefaultProviderInput textinput.Model
	agInstalled            bool // cached to avoid PATH lookup per frame
}

func newModel(theme themes.Theme, cfg *config.Config, cfgErr error) model {
	return model{theme: theme, cfg: cfg, cfgErr: cfgErr}
}

// Init emits the loaded config immediately. The config is supplied by the
// caller (cmd/wt) rather than reloaded inside the TUI, so a validation error
// can be surfaced without hanging on the "Loading config..." screen.
func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		// cfg and cfgErr are captured when newModel is called.
		return loadedMsg{cfg: m.cfg, err: m.cfgErr}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.ready {
			for i := range m.lists {
				m.lists[i].SetSize(msg.Width-2, msg.Height-4)
			}
		}
		if m.phase == phaseForm {
			m.resizeFormInputs()
		}
	case loadedMsg:
		m.ready = true
		if msg.cfg == nil {
			msg.cfg = &config.Config{DefaultTag: "code"}
		}
		m.cfg = msg.cfg
		if msg.err != nil {
			m.status = "config load/validation error: " + msg.err.Error()
		}
		m.lists[sectionAgents] = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
		m.lists[sectionProviders] = buildProvidersList(m.theme, m.width-2, m.height-4, m.cfg)
		m.lists[sectionModels] = buildModelsList(m.theme, m.width-2, m.height-4, m.cfg)
		return m, nil
	case saveMsg:
		m.saving = false
		if msg.err != nil {
			m.status = "save failed: " + msg.err.Error()
			m.quitting = false
			return m, nil
		}
		m.dirty = false
		m.status = "saved"
		if m.quitting {
			m.quitting = false
			return m, tea.Quit
		}
		return m, nil
	}

	if m.phase == phaseForm {
		return m.handleFormUpdate(msg)
	}
	if m.phase == phaseDelete {
		return m.handleDeleteUpdate(msg)
	}
	if m.phase == phaseQuit {
		return m.handleQuitUpdate(msg)
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		// Delegate to the active list while it is filtering so single-key
		// global shortcuts (n, d, q, 1/2/3) don't intercept filter input.
		if m.ready && m.lists[m.section].FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.lists[m.section], cmd = m.lists[m.section].Update(msg)
			return m, cmd
		}

		// Global shortcuts take precedence over list delegation.
		switch msg.String() {
		case "tab":
			m.section = (m.section + 1) % 3
			return m, nil
		case "shift+tab":
			m.section = (m.section + 2) % 3
			return m, nil
		case "1":
			m.section = sectionAgents
			return m, nil
		case "2":
			m.section = sectionProviders
			return m, nil
		case "3":
			m.section = sectionModels
			return m, nil
		case "ctrl+s":
			return m.handleSave()
		case "q", "ctrl+c":
			if !m.dirty {
				return m, tea.Quit
			}
			m.phase = phaseQuit
			m.quitting = false
			return m, nil
		case "d":
			if m.ready {
				switch m.section {
				case sectionAgents:
					if it, ok := m.lists[sectionAgents].SelectedItem().(agentItem); ok {
						if !it.command && it.configured {
							enterDelete(&m, sectionAgents, it.agent.Name)
						}
					}
				case sectionProviders:
					if it, ok := m.lists[sectionProviders].SelectedItem().(providerItem); ok {
						enterDelete(&m, sectionProviders, it.provider.ID)
					}
				case sectionModels:
					if it, ok := m.lists[sectionModels].SelectedItem().(modelItem); ok {
						enterDelete(&m, sectionModels, it.model.ID)
					}
				}
			}
			return m, nil
		case "n":
			if m.ready {
				switch m.section {
				case sectionAgents:
					enterAgentForm(&m, config.Agent{}, true)
				case sectionProviders:
					enterProviderForm(&m, config.Provider{}, true)
				case sectionModels:
					enterModelForm(&m, config.Model{}, true)
				}
			}
			return m, nil
		case "enter":
			if m.ready {
				switch m.section {
				case sectionAgents:
					if it, ok := m.lists[sectionAgents].SelectedItem().(agentItem); ok {
						if it.command {
							return m, nil
						}
						enterAgentForm(&m, it.agent, !it.configured)
					}
				case sectionProviders:
					if it, ok := m.lists[sectionProviders].SelectedItem().(providerItem); ok {
						enterProviderForm(&m, it.provider, false)
					}
				case sectionModels:
					if it, ok := m.lists[sectionModels].SelectedItem().(modelItem); ok {
						enterModelForm(&m, it.model, false)
					}
				}
			}
			return m, nil
		}

		// Not a global shortcut — delegate to the active list.
		if m.ready {
			var cmd tea.Cmd
			m.lists[m.section], cmd = m.lists[m.section].Update(msg)
			if cmd != nil {
				return m, cmd
			}
		}
	}
	return m, nil
}

// handleQuitUpdate processes keys in the unsaved-changes quit prompt.
func (m model) handleQuitUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			m.quitting = true
			return m.handleSave()
		case "n", "N":
			return m, tea.Quit
		case "c", "C", "esc":
			m.phase = phaseList
			m.quitting = false
			return m, nil
		}
	}
	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Loading config..."
	}
	switch m.phase {
	case phaseForm:
		return m.formView()
	case phaseDelete:
		return m.deleteView()
	case phaseQuit:
		return "You have unsaved changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n"
	default:
		header := fmt.Sprintf("  [1] Agents    [2] Providers    [3] Models\n\n")
		activeList := m.lists[m.section].View()
		var status string
		if m.status != "" {
			status = m.status + "\n\n"
		}
		return header + status + activeList
	}
}

// resizeFormInputs updates input widths after a resize.
func (m *model) resizeFormInputs() {
	w := m.width - 20
	if w < 10 {
		w = 10
	}
	switch m.formKind {
	case formProvider:
		m.provID.Width = w
		m.provName.Width = w
		m.provAuth.Width = w
		m.provBaseURL.Width = w
	case formModel:
		m.modID.Width = w
		m.modFamily.Width = w
		m.modProv.Width = w
		m.modName.Width = w
		m.modTags.Width = w
	case formAgent:
		m.agName.Width = w
		m.agProvidersInput.Width = w
		m.agDefaultProviderInput.Width = w
	}
}

// formView dispatches to the appropriate form renderer.
func (m model) formView() string {
	switch m.formKind {
	case formProvider:
		return m.providerFormView()
	case formModel:
		return m.modelFormView()
	case formAgent:
		return m.agentFormView()
	default:
		return ""
	}
}

// handleFormUpdate dispatches to the appropriate form update handler.
func (m model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.formKind {
	case formProvider:
		return m.handleProviderFormUpdate(msg)
	case formModel:
		return m.handleModelFormUpdate(msg)
	case formAgent:
		return m.handleAgentFormUpdate(msg)
	default:
		return m, nil
	}
}

// Run starts the config editor TUI with the config already loaded by the
// caller. A non-nil cfgErr is surfaced as a status message so the user can
// repair the config without the CLI exiting early.
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, opts ...tea.ProgramOption) error {
	m := newModel(theme, cfg, cfgErr)
	allOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)
	p := tea.NewProgram(m, allOpts...)
	_, err := p.Run()
	return err
}
