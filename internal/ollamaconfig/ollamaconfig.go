package ollamaconfig

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/tui"
)

// phase identifies which screen the TUI is showing.
type phase int

const (
	phaseList    phase = iota // union list with status indicators
	phaseEdit                 // field-editing form
	phaseResolve              // pull / delete / cancel choice for missing models
)

// resolveChoice identifies a choice in the resolve prompt.
type resolveChoice int

const (
	resolvePullChoice resolveChoice = iota
	resolveDeleteChoice
	resolveCancelChoice
)

// resolveItem adapts a resolve choice to list.Item.
type resolveItem struct {
	choice resolveChoice
	title  string
	desc   string
}

func (r resolveItem) FilterValue() string { return r.title }
func (r resolveItem) Title() string       { return r.title }
func (r resolveItem) Description() string { return r.desc }

// syncItem adapts a syncEntry to list.Item for the union list.
type syncItem struct {
	entry syncEntry
}

func (s syncItem) FilterValue() string { return s.entry.model.ModelName }
func (s syncItem) Title() string {
	family := s.entry.model.Family
	if family == "" {
		family = "-"
	}
	loc := string(s.entry.model.Location)
	if loc == "" {
		loc = "-"
	}
	tags := tagsToString(s.entry.model.Tags)
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%s / %s  %s  %s  %s",
		family,
		s.entry.model.ModelName,
		strings.ToUpper(s.entry.Status()),
		loc,
		tags,
	)
}

// Description returns empty — the compact single-line format lives in
// Title() so each row is one line.
func (s syncItem) Description() string { return "" }

// buildSyncList constructs the bubbles/list for the union list.
func buildSyncList(entries []syncEntry, theme themes.Theme, width, height int) list.Model {
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, syncItem{entry: e})
	}
	l := list.New(items, tui.ThemedListDelegate(theme), width, height)
	l.Title = "Ollama Model Sync"
	l.SetShowStatusBar(false)
	return l
}

// buildResolveChoices creates the resolve prompt list items.
func buildResolveChoices(modelName string) []list.Item {
	return []list.Item{
		resolveItem{choice: resolvePullChoice, title: "Pull with ollama", desc: "download " + modelName + " via `ollama pull`"},
		resolveItem{choice: resolveDeleteChoice, title: "Delete from config", desc: "remove this model from config.toml"},
		resolveItem{choice: resolveCancelChoice, title: "Cancel", desc: "return to list"},
	}
}

// loadedMsg carries the union entries to Update.
type loadedMsg struct {
	entries     []syncEntry
	ollamaFound bool
	err         error
}

// pulledMsg is emitted after an ollama pull completes.
type pulledMsg struct {
	err error
}

// savedMsg is emitted after a config save completes.
type savedMsg struct {
	err error
}

// model holds the entire UI state.
type model struct {
	theme  themes.Theme
	width  int
	height int

	phase phase
	ready bool

	list    list.Model // union list
	resolve list.Model // resolve choice list

	// edit screen state
	editModel    config.Model // model being edited
	editIsNew    bool         // true when adding an untracked model
	editCursor   int          // 0=family, 1=location, 2=tags
	familyInput  textinput.Model
	tagsInput    textinput.Model
	editLocation config.Location // current location value in edit screen
	editError    string

	// status messages
	status    string // shown above the list
	listError string // shown above the list after a failed action

	// current entries (for refresh and lookup)
	entries []syncEntry

	// saving guards against duplicate submits: true from the moment
	// handleEditSave/handleDelete dispatch an async saveCmd until the
	// matching savedMsg lands, so a second Enter press before the
	// round-trip completes is a no-op instead of re-running the save.
	saving bool
}

// newModel constructs the initial model.
func newModel(theme themes.Theme) model {
	return model{
		theme:        theme,
		familyInput:  textinput.New(),
		tagsInput:    textinput.New(),
		editLocation: config.LocationLocal,
	}
}

// Init returns the initial command: load config + ollama list.
func (m model) Init() tea.Cmd {
	return loadCmd()
}

// ollamaInstalled reports whether the ollama binary is on $PATH.
func ollamaInstalled() bool {
	_, err := exec.LookPath("ollama")
	return err == nil
}

// loadCmd reads config.toml and runs ollama list, returning a loadedMsg
// with the computed union.
func loadCmd() tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load()
		if err != nil {
			return loadedMsg{err: err}
		}
		discovered, err := registry.Ollama{}.Discover()
		if err != nil {
			return loadedMsg{err: err}
		}
		entries := computeUnion(cfg.Models, discovered)
		return loadedMsg{
			entries:     entries,
			ollamaFound: len(discovered) > 0 || ollamaInstalled(),
		}
	}
}

// pullCmd runs `ollama pull <modelName>`. It releases the terminal so
// the user sees native ollama progress output, then restores the TUI.
// It is a package-level var so tests can override it to avoid running a
// real subprocess.
var pullCmd = func(modelName string) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
		}
		cmd := exec.Command("ollama", "pull", modelName)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if currentProgram != nil {
			currentProgram.RestoreTerminal()
		}
		return pulledMsg{err: err}
	}
}

// saveCmd writes the config to disk and returns a savedMsg.
func saveCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		return savedMsg{err: config.Save(cfg)}
	}
}

// isTyping reports whether a bubbles/list incremental filter is active
// (in the union list or the resolve choice list), in which case global
// key shortcuts must not fire.
func (m model) isTyping() bool {
	if m.phase == phaseList && m.ready && m.list.FilterState() == list.Filtering {
		return true
	}
	return m.phase == phaseResolve && m.resolve.FilterState() == list.Filtering
}

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseResolve {
			m.resolve.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseEdit {
			m.familyInput.Width = msg.Width - 20
			m.tagsInput.Width = msg.Width - 20
		}
		return m, nil

	case loadedMsg:
		if msg.err != nil {
			// Build an empty list rather than leaving m.list at its zero
			// value, and set m.ready so the list phase (and its 'r'
			// refresh / 'q' quit keys) is reachable instead of getting
			// stuck on a bare error screen forever.
			m.listError = msg.err.Error()
			m.status = ""
			m.entries = nil
			m.list = buildSyncList(nil, m.theme, m.width-2, m.height-2)
			m.ready = true
			m.phase = phaseList
			return m, nil
		}
		m.listError = ""
		m.entries = msg.entries
		m.list = buildSyncList(msg.entries, m.theme, m.width-2, m.height-2)
		m.ready = true
		m.phase = phaseList
		if !msg.ollamaFound {
			m.status = "ollama not found — showing config models only"
		} else {
			m.status = ""
		}
		return m, nil

	case pulledMsg:
		if msg.err != nil {
			m.listError = "pull failed: " + msg.err.Error()
		}
		return m, loadCmd()

	case savedMsg:
		m.saving = false
		if msg.err != nil {
			if m.phase == phaseEdit {
				m.editError = "save failed: " + msg.err.Error()
				return m, nil
			}
			m.listError = "save failed: " + msg.err.Error()
			m.phase = phaseList
			return m, nil
		}
		return m, loadCmd()

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// While the list's incremental filter is active, q/enter/esc/r
		// must be typed as characters into the filter instead of being
		// treated as global shortcuts — otherwise 'q' or Esc quits the
		// whole TUI mid-search. Mirrors internal/tui/app.go's isTyping
		// guard for the same bubbles/list filtering behavior.
		if !m.isTyping() {
			switch msg.String() {
			case "q":
				if m.phase == phaseList && m.ready {
					return m, tea.Quit
				}
			case "esc":
				if m.phase == phaseEdit {
					m.phase = phaseList
					m.editError = ""
					return m, nil
				}
				if m.phase == phaseResolve {
					m.phase = phaseList
					return m, nil
				}
				if m.phase == phaseList && m.ready {
					return m, tea.Quit
				}
			case "enter":
				return m.handleEnter()
			case "r":
				if m.phase == phaseList && m.ready {
					return m, loadCmd()
				}
			case "tab":
				if m.phase == phaseEdit {
					m.editCursor = (m.editCursor + 1) % 3
					m.focusEditInput()
					return m, nil
				}
			case "shift+tab":
				if m.phase == phaseEdit {
					m.editCursor = (m.editCursor + 2) % 3
					m.focusEditInput()
					return m, nil
				}
			}
		}
	}

	// Delegate to the active list/textinput.
	if m.phase == phaseList && m.ready {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	if m.phase == phaseResolve {
		var cmd tea.Cmd
		m.resolve, cmd = m.resolve.Update(msg)
		return m, cmd
	}
	if m.phase == phaseEdit {
		return m.handleEditUpdate(msg)
	}
	return m, nil
}

// handleEnter dispatches Enter based on the current phase and the
// selected entry's status.
func (m model) handleEnter() (tea.Model, tea.Cmd) {
	switch m.phase {
	case phaseList:
		if !m.ready {
			return m, nil
		}
		item, ok := m.list.SelectedItem().(syncItem)
		if !ok {
			return m, nil
		}
		entry := item.entry
		switch entry.Status() {
		case "synced":
			m.enterEdit(entry.model, false)
			return m, nil
		case "missing":
			m.enterResolve(entry.model)
			return m, nil
		case "untracked":
			m.enterEdit(entry.model, true)
			return m, nil
		}
	case phaseResolve:
		item, ok := m.resolve.SelectedItem().(resolveItem)
		if !ok {
			return m, nil
		}
		switch item.choice {
		case resolvePullChoice:
			modelName := m.editModel.ModelName
			m.phase = phaseList
			return m, pullCmd(modelName)
		case resolveDeleteChoice:
			return m.handleDelete()
		case resolveCancelChoice:
			m.phase = phaseList
			return m, nil
		}
	case phaseEdit:
		return m.handleEditSave()
	}
	return m, nil
}

// enterResolve sets up the resolve choice list for a missing model.
func (m *model) enterResolve(mod config.Model) {
	m.editModel = mod
	m.resolve = list.New(buildResolveChoices(mod.ModelName), tui.ThemedListDelegate(m.theme), m.width-2, m.height-2)
	m.resolve.Title = "Resolve: " + mod.ModelName + " (not in ollama list)"
	m.resolve.SetShowStatusBar(false)
	m.phase = phaseResolve
}

// enterEdit sets up the edit screen for a model. isNew is true for
// untracked models being added to config.
func (m *model) enterEdit(mod config.Model, isNew bool) {
	m.editModel = mod
	m.editIsNew = isNew
	m.editError = ""
	m.editCursor = 0

	// Pre-fill inputs.
	m.familyInput = textinput.New()
	m.familyInput.SetValue(mod.Family)
	m.familyInput.Focus()
	m.familyInput.Width = m.width - 20

	m.tagsInput = textinput.New()
	m.tagsInput.SetValue(tagsToString(mod.Tags))
	m.tagsInput.Width = m.width - 20

	m.editLocation = mod.Location
	if m.editLocation == "" {
		m.editLocation = config.LocationLocal
	}

	m.phase = phaseEdit
}

// focusEditInput focuses/blurs textinputs based on editCursor.
func (m *model) focusEditInput() {
	switch m.editCursor {
	case 0:
		m.familyInput.Focus()
		m.tagsInput.Blur()
	case 2:
		m.familyInput.Blur()
		m.tagsInput.Focus()
	default:
		m.familyInput.Blur()
		m.tagsInput.Blur()
	}
}

// handleEditUpdate delegates key handling to the focused textinput or
// handles the location toggle.
func (m model) handleEditUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.editCursor == 1 {
		// Location field: any key toggles. Tab/enter/esc are handled
		// by the parent Update, so we only get here for other keys.
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "up", "down", "left", "right":
				return m, nil
			}
			m.editLocation = toggleLocation(m.editLocation)
			return m, nil
		}
		return m, nil
	}
	// Delegate to the focused textinput.
	if m.editCursor == 0 {
		var cmd tea.Cmd
		m.familyInput, cmd = m.familyInput.Update(msg)
		return m, cmd
	}
	if m.editCursor == 2 {
		var cmd tea.Cmd
		m.tagsInput, cmd = m.tagsInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleEditSave validates and saves the edited model to config.toml.
func (m model) handleEditSave() (tea.Model, tea.Cmd) {
	if m.saving {
		return m, nil
	}
	cfg, err := config.Load()
	if err != nil {
		m.editError = "config load failed: " + err.Error()
		return m, nil
	}
	updated := m.editModel
	updated.Family = m.familyInput.Value()
	updated.Tags = parseTags(m.tagsInput.Value())
	updated.Location = m.editLocation
	if m.editIsNew {
		updated.ID = "ollama/" + updated.ModelName
		updated.ProviderID = "ollama"
		updated.Source = config.SourceCurated
	}
	if !saveModelToConfig(cfg, updated, m.editIsNew) {
		// The model was removed from config.toml (e.g. by a concurrent
		// `wt config ollama` session or a manual edit) between the list
		// load and this save — surface it instead of silently dropping
		// the edit.
		m.editError = "model no longer exists in config.toml (removed concurrently) — press esc, then r to refresh"
		return m, nil
	}
	if err := cfg.Validate(); err != nil {
		m.editError = "validation: " + err.Error()
		return m, nil
	}
	m.saving = true
	return m, saveCmd(cfg)
}

// handleDelete removes the current model from config.toml.
func (m model) handleDelete() (tea.Model, tea.Cmd) {
	if m.saving {
		return m, nil
	}
	cfg, err := config.Load()
	if err != nil {
		m.listError = "config load failed: " + err.Error()
		return m, loadCmd()
	}
	deleteModelFromConfig(cfg, m.editModel.ID)
	m.saving = true
	return m, saveCmd(cfg)
}

// View renders the screen as a string.
func (m model) View() string {
	if !m.ready && m.phase == phaseList {
		if m.status != "" {
			return m.status
		}
		return "loading ollama models..."
	}
	switch m.phase {
	case phaseEdit:
		return m.viewEdit()
	case phaseResolve:
		if m.width <= 0 || m.height <= 0 {
			return "resolve prompt (waiting for window size)"
		}
		return m.resolve.View() + "\n[enter] choose   [esc] back"
	default:
		return m.viewList()
	}
}

// viewList renders the union list screen.
func (m model) viewList() string {
	if m.width <= 0 || m.height <= 0 {
		return "ollama model sync (waiting for window size)"
	}
	var header string
	if m.listError != "" {
		header = tui.ErrorStyle(m.theme).Render("error: "+m.listError) + "\n"
	} else if m.status != "" {
		dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
		header = dimStyle.Render(m.status) + "\n"
	}
	footer := "\n[enter] edit / resolve   [r] refresh   [q] quit"
	return header + m.list.View() + footer
}

// viewEdit renders the edit screen.
func (m model) viewEdit() string {
	if m.width <= 0 || m.height <= 0 {
		return "edit model (waiting for window size)"
	}
	dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	accentStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent))
	readOnly := func(label, value string) string {
		return fmt.Sprintf("  %-12s %s  %s", label, value, dimStyle.Render("(read-only)"))
	}
	editable := func(label, value string, cursor bool) string {
		marker := ""
		if cursor {
			marker = "  " + accentStyle.Render("← cursor")
		}
		return fmt.Sprintf("  %-12s [%s]%s", label, value, marker)
	}
	locDisplay := editable("location", string(m.editLocation), m.editCursor == 1)
	s := fmt.Sprintf("  Edit Model: %s\n\n", m.editModel.ModelName)
	idDisplay := m.editModel.ID
	if m.editIsNew {
		idDisplay = "ollama/" + m.editModel.ModelName + "  (auto-generated)"
	}
	s += readOnly("id", idDisplay) + "\n"
	s += readOnly("model_name", m.editModel.ModelName) + "\n"
	s += readOnly("provider", "ollama") + "\n"
	s += editable("family", m.familyInput.View(), m.editCursor == 0) + "\n"
	s += locDisplay + "\n"
	s += editable("tags", m.tagsInput.View(), m.editCursor == 2) + "\n"
	if m.editError != "" {
		s += "\n" + tui.ErrorStyle(m.theme).Render(m.editError) + "\n"
	}
	s += "\n  [tab/shift+tab] next/prev field   [enter] save   [esc] cancel"
	return s
}

// currentProgram holds the running tea.Program so pullCmd can
// release/restore the terminal.
var currentProgram *tea.Program

// Run starts the TUI in alternate-screen mode and returns when it quits.
func Run(theme themes.Theme) error {
	m := newModel(theme)
	p := tea.NewProgram(m, tea.WithAltScreen())
	currentProgram = p
	_, err := p.Run()
	return err
}
