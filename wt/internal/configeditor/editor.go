// Package configeditor is the TUI behind `wt config`: an Agents tab that
// edits the agent section of wt's config.toml, and a Models tab that manages
// the models in the shared registry.toml (through internal/modeladmin).
package configeditor

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
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
	phase phase
	// tab is the tab showing; opts are what the editor was opened with.
	tab  Tab
	opts Options
	// models is the Models tab's state. registryChanged is true once that
	// tab has written registry.toml (Result.RegistryChanged).
	models          modelsTab
	registryChanged bool
	// quitPending is true when the user asked to leave while a registry
	// write was in flight: the editor quits when the write's result is in.
	quitPending bool
	theme       themes.Theme
	cfg         *config.Config
	dirty       bool
	width       int
	height      int
	ready       bool
	status      string // shown above the list
	saving      bool   // prevents duplicate save dispatches
	cfgErr      error  // captured at construction for the initial loadedMsg

	// cachedStatusBlock caches the rendered status block to avoid double rendering
	// and re-creating lipgloss.Style on every call.
	cachedStatusBlock string
	// cachedStatusWidth is the width at which cachedStatusBlock was rendered;
	// it is invalidated when m.width changes.
	cachedStatusWidth int

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
	agInstalledName        string // name we last looked up
	agInstalled            bool   // cached result for agInstalledName
}

func newModel(theme themes.Theme, cfg *config.Config, cfgErr error) *model {
	return &model{theme: theme, cfg: cfg, cfgErr: cfgErr}
}

// Init emits the loaded config immediately. The config is supplied by the
// caller (cmd/wt) rather than reloaded inside the TUI, so a validation error
// can be surfaced without hanging on the "Loading config..." screen.
func (m *model) Init() tea.Cmd {
	loaded := func() tea.Msg {
		// cfg and cfgErr are captured when newModel is called.
		return loadedMsg{cfg: m.cfg, err: m.cfgErr}
	}
	if m.tab == TabModels {
		// Opened on the Models tab (`wt model`): probe at once.
		return tea.Batch(loaded, m.probeCmd())
	}
	return loaded
}

// Update handles a message and then re-fits the list: the status line can
// appear, change length or clear on any message, and the list owes it the
// rows it takes (fitList).
// The inner update always returns *model (the concrete type), so the type
// assertion is safe. Using a pointer receiver for update and returning
// *model makes this explicit and avoids a panic if a future change returns
// a different tea.Model implementation.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(*model); ok && nm.ready {
		nm.fitList()
		nm.fitModels()
		return nm, cmd
	}
	return next, cmd
}

// statusBlock is the status as it is drawn: wrapped to the terminal's width,
// "" when there is none. A terminal cuts a too-wide line at its right edge,
// and what a status ends with is often the part that matters — a location
// error ends with the file to fix (#209).
// The rendered block is cached and only re-rendered when width or status changes.
func (m *model) statusBlock() string {
	if m.status == "" {
		m.cachedStatusBlock = ""
		m.cachedStatusWidth = m.width
		return ""
	}
	if m.width <= 0 {
		m.cachedStatusBlock = m.status
		m.cachedStatusWidth = m.width
		return m.status
	}
	// Re-render only if width or status changed
	if m.cachedStatusWidth != m.width {
		m.cachedStatusBlock = lipgloss.NewStyle().Width(m.width).Render(m.status)
		m.cachedStatusWidth = m.width
	}
	return m.cachedStatusBlock
}

// fitList sizes the agents list to the rows left under the title and the
// status. A one-line status fits the two spare rows the list has always left;
// each further line a wrapped status takes comes out of the list, so the view
// is never taller than the terminal — Bubble Tea drops a too-tall view's top
// lines, which here are the title and the status itself.
// Minimum viable list height is 3 rows (1 item + 2 margins) to avoid layout
// issues where the list would render off-screen or Bubble Tea would drop the
// title/status from the top.
func (m *model) fitList() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	h := m.height - 4
	if s := m.statusBlock(); s != "" {
		h -= lipgloss.Height(s) - 1
	}
	// Minimum 3 rows for a viable list: 1 item + top/bottom margin that
	// bubbles list uses internally. If h < 3, the list would be too small
	// and Bubble Tea would drop lines from the top (title/status).
	if h < 3 {
		h = 3
	}
	m.list.SetSize(m.width-2, h)
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
			// This editor writes config.toml. An error whose repair is in
			// registry.toml must say so here of all places: the user opened
			// this screen to fix it.
			if hint := config.RegistryFixHint(msg.err); hint != "" {
				m.status += " (" + hint + ")"
			}
		}
		m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
		// Call fitList immediately after setting status to ensure the list
		// height accounts for any wrapped status (e.g. location error hint).
		// Without this, the first render after loadedMsg could have the list
		// at the wrong height if the hint makes status exceed terminal width.
		m.fitList()
		return m, nil
	case modelsLoadedMsg:
		m.applyModels(msg)
		return m, nil
	case modelRemovedMsg:
		return m, m.applyModelRemoved(msg)
	case filterMatchesMsg:
		return m, m.applyFilterMatches(msg)
	case saveMsg:
		m.saving = false
		if msg.err != nil {
			m.status = "save failed: " + msg.err.Error()
			m.fitList()
			m.quitting = false
			return m, nil
		}
		m.dirty = false
		m.status = "saved"
		m.fitList()
		if m.quitting {
			m.quitting = false
			return m.leave()
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
	if m.tab == TabModels {
		return m.updateModels(msg)
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		// Delegate to the list while it is filtering so single-key global
		// shortcuts (n, d, q) don't intercept filter input.
		if m.ready && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, tagFilter(cmd, TabAgents, 0)
		}

		// Global shortcuts take precedence over list delegation.
		switch msg.String() {
		case "ctrl+s":
			return m.handleSave()
		case "q", "ctrl+c":
			return m.quit()
		case "tab":
			m.tab = TabModels
			if !m.models.asked {
				// The first visit probes; later ones show what is there
				// (r refreshes).
				return m, m.probeCmd()
			}
			return m, nil
		case "d":
			if m.ready {
				if it, ok := m.list.SelectedItem().(agentItem); ok {
					if !it.command && it.configured {
						enterDelete(m, it.agent.Name)
					}
				}
			}
			return m, nil
		case "n":
			if m.ready {
				enterAgentForm(m, config.Agent{}, true)
			}
			return m, nil
		case "enter":
			if m.ready {
				if it, ok := m.list.SelectedItem().(agentItem); ok {
					if it.command {
						return m, nil
					}
					enterAgentForm(m, it.agent, !it.configured)
				}
			}
			return m, nil
		}

		// Not a global shortcut — delegate to the list.
		if m.ready {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			if cmd != nil {
				return m, tagFilter(cmd, TabAgents, 0)
			}
		}
	}
	return m, nil
}

// quit leaves the editor, by way of the unsaved-changes prompt when the
// Agents tab has edits that were not saved. The prompt concerns agent edits
// only: every change on the Models tab is already in registry.toml.
func (m *model) quit() (tea.Model, tea.Cmd) {
	if !m.dirty {
		return m.leave()
	}
	m.phase = phaseQuit
	m.quitting = false
	return m, nil
}

// leave ends the program — once the registry write in flight, if there is
// one, has reported. The write runs in a command; a program that ended before
// its message was handled would return Result{RegistryChanged: false} for a
// registry that did change, and the caller would skip the route sync it owes.
// So the quit is recorded, and applyModelRemoved issues it. A probe in flight
// holds nothing up.
func (m *model) leave() (tea.Model, tea.Cmd) {
	if m.models.writing {
		m.quitPending = true
		m.phase = phaseList
		return m, nil
	}
	return m, tea.Quit
}

// handleQuitUpdate processes keys in the unsaved-changes quit prompt.
func (m *model) handleQuitUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			m.quitting = true
			return m.handleSave()
		case "n", "N":
			return m.leave()
		case "c", "C", "esc":
			m.phase = phaseList
			m.quitting = false
			return m, nil
		}
	}
	return m, nil
}

func (m *model) View() string {
	if !m.ready {
		return "Loading config..."
	}
	switch m.phase {
	case phaseForm:
		return m.formView()
	case phaseDelete:
		return m.deleteView()
	case phaseQuit:
		return "You have unsaved agent changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n"
	default:
		if m.tab == TabModels {
			return m.modelsView()
		}
		var status string
		if s := m.statusBlock(); s != "" {
			status = s + "\n\n"
		}
		return tabBar(m.theme, TabAgents) + "\n\n" + status + m.list.View()
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
func (m *model) formView() string {
	if m.formKind == formAgent {
		return m.agentFormView()
	}
	return ""
}

// handleFormUpdate dispatches to the appropriate form update handler.
func (m *model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.formKind == formAgent {
		return m.handleAgentFormUpdate(msg)
	}
	return m, nil
}

// Run starts the config editor TUI with the config already loaded by the
// caller. A non-nil cfgErr is surfaced as a status message so the user can
// repair the config without the CLI exiting early. The Result says whether
// the Models tab wrote the registry, in which case the caller owes the
// LiteLLM routes one sync; it is reported even when the program ends with an
// error, since the write happened.
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error) {
	m := newModel(theme, cfg, cfgErr)
	o.Models = o.Models.withDefaults()
	m.opts, m.tab = o, o.StartTab
	allOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)
	p := tea.NewProgram(m, allOpts...)
	_, err := p.Run()
	return Result{RegistryChanged: m.registryChanged}, err
}
