// Package configeditor provides a TUI for viewing and editing the agent
// section of config.toml. Providers and models live in modelman-owned
// registry.toml and are read-only for wt, so the editor only manages agents.
package configeditor

import (
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

type formKind int

const (
	formNone formKind = iota
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
	ready  bool
	status string // shown above the list
	saving bool   // prevents duplicate save dispatches
	cfgErr error  // captured at construction for the initial loadedMsg

	list list.Model

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
			m.list.SetSize(msg.Width-2, msg.Height-4)
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
		m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
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
		// Delegate to the list while it is filtering so single-key global
		// shortcuts (n, d, q) don't intercept filter input.
		if m.ready && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}

		// Global shortcuts take precedence over list delegation.
		switch msg.String() {
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
				if it, ok := m.list.SelectedItem().(agentItem); ok {
					if !it.command && it.configured {
						enterDelete(&m, it.agent.Name)
					}
				}
			}
			return m, nil
		case "n":
			if m.ready {
				enterAgentForm(&m, config.Agent{}, true)
			}
			return m, nil
		case "enter":
			if m.ready {
				if it, ok := m.list.SelectedItem().(agentItem); ok {
					if it.command {
						return m, nil
					}
					enterAgentForm(&m, it.agent, !it.configured)
				}
			}
			return m, nil
		}

		// Not a global shortcut — delegate to the list.
		if m.ready {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
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
		var status string
		if m.status != "" {
			status = m.status + "\n\n"
		}
		return "Agents (providers/models are managed by modelman)\n\n" + status + m.list.View()
	}
}

// resizeFormInputs updates input widths after a resize.
func (m *model) resizeFormInputs() {
	w := m.width - 20
	if w < 10 {
		w = 10
	}
	m.agName.Width = w
	m.agProvidersInput.Width = w
	m.agDefaultProviderInput.Width = w
}

// formView dispatches to the appropriate form renderer.
func (m model) formView() string {
	if m.formKind == formAgent {
		return m.agentFormView()
	}
	return ""
}

// handleFormUpdate dispatches to the appropriate form update handler.
func (m model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.formKind == formAgent {
		return m.handleAgentFormUpdate(msg)
	}
	return m, nil
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
