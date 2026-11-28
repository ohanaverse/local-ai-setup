// Package sessioneditor is the session's TUI behind `wt config`: an Agents
// tab that edits the agent section of wt's config.toml, and a Mode tab that
// chooses how this session reaches a model server (native, remote, or a
// pointer to wt-model on this machine, through internal/modelmode). It is a
// session screen: it reads no registry (the config comes from config.LoadSession and
// a save validates with config.ValidateSession) and links no admin package.
// The models in registry.toml and this machine's model-server setup are
// internal/modeleditor's (`wt-model model`, `wt-model setup`).
package sessioneditor

import (
	"os"
	"os/exec"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

type phase int

const (
	phaseList phase = iota
	phaseForm
	phaseDelete
	phaseQuit
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
	mode modeTab
	// quitPending is true when the user asked to leave while a Mode apply
	// was in flight: the editor quits when its result is in.
	quitPending bool
	// discardHeld is true when that held quit came from "discard and quit"
	// on the unsaved-changes prompt: the edits the answer was about are no
	// longer counted as unsaved (dirty is cleared), so the quit asks again
	// only about edits made while it waited. A failed apply calls the quit
	// off and puts dirty back. It is cleared only when the quit happens
	// (leave returns tea.Quit) or the prompt asks again (quit).
	discardHeld bool
	// result is what Run hands its caller (the run-setup answer).
	result Result
	theme  themes.Theme
	cfg    *config.Config
	dirty  bool
	width  int
	height int
	ready  bool
	status string // shown above the list
	saving bool   // prevents duplicate save dispatches
	cfgErr error  // captured at construction for the initial loadedMsg

	// home is the home directory paths are abbreviated against
	// (Options.UserHome, read once in Run); empty writes every path in full.
	home string

	// cachedStatusBlock caches the rendered status block to avoid double rendering
	// and re-creating lipgloss.Style on every call.
	cachedStatusBlock string
	// cachedStatusWidth and cachedStatusText are the width and the status
	// cachedStatusBlock was rendered for; a change of either invalidates it.
	cachedStatusWidth int
	cachedStatusText  string

	list list.Model

	// delete state
	deleteTarget deleteTarget

	// quit state
	quitting bool // true when waiting for save-before-quit

	// Form state
	formIsNew  bool
	formCursor int
	formError  string

	// Agent form fields
	agEdit          config.Agent
	agName          textinput.Model
	agInstalledName string // name we last looked up
	agInstalled     bool   // cached result for agInstalledName
}

func newModel(theme themes.Theme, cfg *config.Config, cfgErr error) *model {
	return &model{theme: theme, cfg: cfg, cfgErr: cfgErr}
}

// Init emits the loaded config immediately. The config is supplied by the
// caller (cmd/wt-session) rather than reloaded inside the TUI, so a load error
// can be surfaced without hanging on the "Loading config..." screen.
func (m *model) Init() tea.Cmd {
	loaded := func() tea.Msg {
		// cfg and cfgErr are captured when newModel is called.
		return loadedMsg{cfg: m.cfg, err: m.cfgErr}
	}
	if m.tab == TabMode {
		// Opened on the Mode tab: observe at once.
		return tea.Batch(loaded, m.observeModeCmd())
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
		m.cachedStatusBlock, m.cachedStatusWidth, m.cachedStatusText = "", m.width, ""
		return ""
	}
	if m.width <= 0 {
		m.cachedStatusBlock, m.cachedStatusWidth, m.cachedStatusText = m.status, m.width, m.status
		return m.status
	}
	// Re-render only if the width or the status changed. Both: a cache keyed
	// by the width alone went on drawing the status of the first render — at
	// a width that never changed, "save failed: ..." was never shown.
	if m.cachedStatusWidth != m.width || m.cachedStatusText != m.status {
		m.cachedStatusBlock = editorkit.StatusBlock(m.status, m.width)
		m.cachedStatusWidth, m.cachedStatusText = m.width, m.status
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
		m.resizeModeInputs()
	case loadedMsg:
		m.ready = true
		if msg.cfg == nil {
			msg.cfg = &config.Config{DefaultTag: "code"}
		}
		m.cfg = msg.cfg
		if msg.err != nil {
			// A LoadSession error is config.toml's: no registry is read, so
			// there is no registry hint to add.
			m.status = "config load/validation error: " + msg.err.Error()
		}
		m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
		return m, nil
	case modeObservedMsg:
		return m, m.applyModeObserved(msg)
	case modePlannedMsg:
		m.applyModePlanned(msg)
		return m, nil
	case modeAppliedMsg:
		return m, m.applyModeApplied(msg)
	case filterMatchesMsg:
		return m, m.applyFilterMatches(msg)
	case saveMsg:
		m.saving = false
		if msg.err != nil {
			m.status = "save failed: " + msg.err.Error()
			m.showQuitSaveFailure()
			return m, nil
		}
		m.dirty = false
		m.status = "saved"
		if m.quitting {
			m.quitting = false
			return m.leave()
		}
		return m, nil
	}

	if m.phase == phaseForm {
		return m.handleAgentFormUpdate(msg)
	}
	if m.phase == phaseDelete {
		return m.handleDeleteUpdate(msg)
	}
	if m.phase == phaseQuit {
		return m.handleQuitUpdate(msg)
	}
	if m.tab == TabMode {
		return m.updateMode(msg)
	}
	if msg, ok := msg.(tea.KeyMsg); ok {
		// Delegate to the list while it is filtering so single-key global
		// shortcuts (n, d, q) don't intercept filter input.
		if m.ready && m.list.FilterState() == list.Filtering {
			// ctrl+c is the one key that is not filter text: the list's own
			// quit keys are off, its force-quit with them.
			if msg.String() == "ctrl+c" {
				return m.quit()
			}
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, tagFilter(cmd)
		}

		// Global shortcuts take precedence over list delegation.
		switch msg.String() {
		case "ctrl+s":
			return m.handleSave()
		case "q", "ctrl+c":
			return m.quit()
		case "tab":
			// The first visit to the Mode tab fills it; later ones show what
			// is there (r refreshes).
			return m, m.switchTab()
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
				return m, tagFilter(cmd)
			}
		}
	}
	return m, nil
}

// quit leaves the editor, by way of the unsaved-changes prompt when the
// Agents tab has edits that were not saved. The prompt concerns agent edits
// only: the Mode tab writes nothing until it is applied.
func (m *model) quit() (tea.Model, tea.Cmd) {
	if !m.dirty {
		return m.leave()
	}
	// The prompt asks about every unsaved edit again, the given-up ones too.
	m.discardHeld = false
	m.phase = phaseQuit
	m.quitting = false
	return m, nil
}

// leave ends the program — once a Mode apply in flight, if there is one,
// has reported: the quit is recorded and finishModeWrite issues it, on the
// Mode tab, which is the one that says what the apply did. (An agents save
// is waited for by the quit prompt itself.)
func (m *model) leave() (tea.Model, tea.Cmd) {
	if m.mode.writing {
		m.quitPending = true
		m.phase, m.tab = phaseList, TabMode
		return m, nil
	}
	// Only now is a discard final: a failed apply while the quit was held
	// puts the edits back.
	m.discardHeld = false
	return m, tea.Quit
}

// showQuitSaveFailure calls off a quit whose save-before-quit failed, and
// brings forward the one place that says why: m.status is the Agents tab's
// status line, and neither the unsaved-changes prompt nor the Mode tab draws
// it. It does nothing for a save that was not a quit's.
func (m *model) showQuitSaveFailure() {
	if !m.quitting {
		return
	}
	m.quitting = false
	m.result = Result{} // the quit that carried the run-setup answer is off
	m.phase, m.tab = phaseList, TabAgents
}

// handleQuitUpdate processes keys in the unsaved-changes quit prompt.
func (m *model) handleQuitUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			m.quitting = true
			return m.handleSave()
		case "n", "N":
			if m.mode.writing {
				// The quit will wait (leave). The answer is kept with it.
				m.discardHeld, m.dirty = true, false
			}
			return m.leave()
		case "c", "C", "esc":
			// The quit is off, and so is the run-setup answer it carried
			// (a y on the offer after local was applied): a later quit
			// must not run `wt-model setup` the user has since declined.
			m.result = Result{}
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
		return m.agentFormView()
	case phaseDelete:
		return m.deleteView()
	case phaseQuit:
		// Wrapped: at 40 columns both lines are wider than the terminal.
		return editorkit.WrapText("You have unsaved agent changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel", m.width) + "\n"
	default:
		if m.tab == TabMode {
			return m.modeTabView()
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
}

// resolveHome is the home directory f names: os.UserHomeDir when f is nil,
// and "" (no path is abbreviated) when it cannot be read.
func resolveHome(f func() (string, error)) string {
	if f == nil {
		f = os.UserHomeDir
	}
	home, err := f()
	if err != nil {
		return ""
	}
	return home
}

// Run starts the session config editor TUI with the config already loaded by the
// caller. A non-nil cfgErr is surfaced as a status message so the user can
// repair the config without the CLI exiting early.
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error) {
	m := newModel(theme, cfg, cfgErr)
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	o.Mode = o.Mode.withDefaults()
	m.opts, m.tab = o, o.StartTab
	m.home = resolveHome(o.UserHome)
	allOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)
	p := tea.NewProgram(m, allOpts...)
	final, err := p.Run()
	if fm, ok := final.(*model); ok {
		return fm.result, err
	}
	return m.result, err
}
