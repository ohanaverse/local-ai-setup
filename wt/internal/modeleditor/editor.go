// Package modeleditor is the Models screen behind `wt-model model`: one
// Models tab that manages the models in the shared registry.toml (through
// internal/modeladmin). It is an admin screen: it reads and writes the
// registry and probes the providers, so it is linked by the host binary,
// never by the session one.
package modeleditor

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// loadedMsg carries the config after loading.
type loadedMsg struct {
	cfg *config.Config
	err error
}

type model struct {
	// tab is the tab showing; opts are what the screen was opened with.
	tab  Tab
	opts Options
	// models is the Models tab's state. registryChanged is true once that
	// tab has written registry.toml (Result.RegistryChanged).
	models          modelsTab
	registryChanged bool
	// quitPending is true when the user asked to leave while a registry
	// write was in flight: the screen quits when its result is in.
	quitPending bool
	theme       themes.Theme
	cfg         *config.Config
	cfgErr      error // captured at construction for the initial loadedMsg
	width       int
	height      int
	ready       bool
	// status is the load banner: why the config handed to Run did not load
	// or validate. modelsStatus draws it.
	status string

	// home is the home directory paths are abbreviated against
	// (Options.UserHome, read once in Run); empty writes every path in full.
	home string
}

func newModel(theme themes.Theme, cfg *config.Config, cfgErr error) *model {
	return &model{theme: theme, cfg: cfg, cfgErr: cfgErr}
}

// Init emits the loaded config at once and starts the first probe: the
// screen has nothing to show before the providers answer.
func (m *model) Init() tea.Cmd {
	loaded := func() tea.Msg {
		// cfg and cfgErr are captured when newModel is called.
		return loadedMsg{cfg: m.cfg, err: m.cfgErr}
	}
	return tea.Batch(loaded, m.probeCmd())
}

// Update handles a message and then re-fits the table: the status can
// appear, change length or clear on any message.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(*model); ok && nm.ready {
		nm.fitModels()
		return nm, cmd
	}
	return next, cmd
}

func (m *model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeModelForm()
	case loadedMsg:
		m.ready = true
		if msg.cfg == nil {
			msg.cfg = &config.Config{DefaultTag: "code"}
		}
		m.cfg = msg.cfg
		if msg.err != nil {
			m.status = "config load/validation error: " + msg.err.Error()
			// An error whose repair is in registry.toml must say so here
			// of all places: this screen manages that file.
			if hint := config.RegistryFixHint(msg.err); hint != "" {
				m.status += " (" + hint + ")"
			}
		}
		return m, nil
	case modelsLoadedMsg:
		m.applyModels(msg)
		return m, nil
	case modelRemovedMsg:
		return m, m.applyModelRemoved(msg)
	case modelSavedMsg:
		return m, m.applyModelSaved(msg)
	case filterMatchesMsg:
		return m, m.applyFilterMatches(msg)
	}
	return m.updateModels(msg)
}

// quit leaves the screen. There is nothing unsaved to ask about: every
// change is written to registry.toml when it is made.
func (m *model) quit() (tea.Model, tea.Cmd) {
	return m.leave()
}

// leave ends the program — once the registry write in flight, if there is
// one, has reported. The write runs in a command; a program that ended before
// its message was handled would return Result{RegistryChanged: false} for a
// registry that did change, and the caller would skip the route sync it owes.
// So the quit is recorded, and applyModelRemoved or applyModelSaved, whichever
// the write reports to, issues it. The tab says the quit is waiting (the
// table's status, or the form's title when the write is a form's save). A
// probe in flight holds nothing up.
func (m *model) leave() (tea.Model, tea.Cmd) {
	if m.models.writing {
		m.quitPending = true
		return m, nil
	}
	return m, tea.Quit
}

// refuseHeldQuit calls a quit held for a write off: the write was refused,
// and the refusal has to be read.
func (m *model) refuseHeldQuit() {
	m.quitPending = false
}

func (m *model) View() string {
	if !m.ready {
		return "Loading config..."
	}
	return m.modelsView()
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

// Run starts the Models screen with the config already loaded by the caller.
// A non-nil cfgErr is kept as the load banner. The Result says whether the
// screen wrote the registry, in which case the caller owes the LiteLLM routes
// one sync; it is reported even when the program ends with an error, since
// the write happened.
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, o Options, opts ...tea.ProgramOption) (Result, error) {
	m := newModel(theme, cfg, cfgErr)
	o.Models = o.Models.withDefaults()
	m.opts, m.tab = o, o.StartTab
	m.home = resolveHome(o.UserHome)
	allOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)
	p := tea.NewProgram(m, allOpts...)
	_, err := p.Run()
	return Result{RegistryChanged: m.registryChanged}, err
}
