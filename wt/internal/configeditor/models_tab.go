package configeditor

import (
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The Models tab's columns, left to right.
const (
	mcFamily = iota
	mcModel
	mcLoc
	mcStatus
	mcRunning
	mcSize
)

// modelsDropOrder is what a narrow terminal gives up, first to go first.
// MODEL, STATUS and RUNNING say what a row is and are never dropped; the
// detail block under the table still shows the rest for the selected row.
var modelsDropOrder = []int{mcSize, mcLoc, mcFamily}

// modelsMinID is the narrowest the MODEL column is ever cut to: room for a
// provider, an ellipsis and the end of the name.
const modelsMinID = 12

// modelsTableMin is the height the tab's fuller layouts leave the table: its
// header and the blank line under it, three rows, and the two lines bubbles
// keeps for the page dots.
const modelsTableMin = 7

// modelsPhase is what the Models tab is showing.
type modelsPhase int

const (
	modelsList modelsPhase = iota
	modelsRemove
	modelsForm
)

// modelsTab is the Models tab's state. Its rows come from one probe run in a
// command (probeCmd); nothing here reads a file or dials a server.
type modelsTab struct {
	phase modelsPhase
	list  list.Model
	// cols is the table's layout, shared by its header and its rows; idWidth
	// is the longest id, the MODEL column's width when nothing is cut.
	cols    *tuilayout.Columns
	idWidth int
	// rows are the last probe's rows, what the table was built from; cfg is
	// the config that probe read, which the form opens from (the provider
	// choices, the families to suggest, a row's own keys). Never nil once
	// loaded.
	rows []modeladmin.Row
	cfg  *config.Config
	// asked is true once the first probe has been dispatched; loaded once
	// its result is in.
	asked  bool
	loaded bool
	// gen numbers the probes: a result that is not the latest's is dropped,
	// so a slow probe cannot overwrite the rows of a later one. built counts
	// the tables built, so a filter result for an older table is dropped.
	gen   int
	built int
	// busy is true while a probe or a registry write is in flight; keys that
	// would start another are ignored. writing is true for the write alone:
	// a quit waits for it (leave), so the caller always learns of the change.
	busy    bool
	writing bool
	// status is what the last action said; the next key on the table clears
	// it. loadErr is why the registry did not load, and stays until a probe
	// reads it again.
	status  string
	loadErr string
	// selectID is the id the cursor goes to when the next rows arrive: the
	// model a form just saved.
	selectID string
	// remove is the row the remove prompt is about.
	remove modeladmin.Row
	// form is the add / register / edit form while it is open.
	form *modelForm
}

// modelsLoadedMsg carries one probe's rows and the config they were built
// from, which is never nil.
type modelsLoadedMsg struct {
	gen  int
	cfg  *config.Config
	rows []modeladmin.Row
	err  error
}

// modelRemovedMsg reports a removal the remove prompt confirmed.
type modelRemovedMsg struct {
	id   string
	note string
	err  error
}

// modelRow adapts a modeladmin.Row to a table row of the tab's list.
type modelRow struct {
	row   modeladmin.Row
	cells []string
	cols  *tuilayout.Columns
}

func (r modelRow) FilterValue() string              { return r.row.ID + " " + r.row.Family }
func (r modelRow) Description() string              { return "" }
func (r modelRow) TableColumns() *tuilayout.Columns { return r.cols }

// Title is the row's line. The MODEL cell is drawn at the column's width of
// the moment (fitModels narrows it when even the columns that are never
// dropped do not fit), so nothing is rebuilt when the terminal is resized.
func (r modelRow) Title() string {
	cells := slices.Clone(r.cells)
	w := r.cols.Widths[mcModel]
	cells[mcModel] = tuilayout.PadRunes(middleCut(r.row.ID, w), w)
	return r.cols.Line(cells)
}

// middleCut shortens s to w runes by taking out its middle: an id keeps its
// provider and the end of its name, which is where two variants of one model
// differ (…-4bit, …-6bit). A string that fits is returned as it is.
func middleCut(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w < 3 {
		return string(runes[:max(w, 0)])
	}
	tail := (w - 1) / 3
	return string(runes[:w-1-tail]) + "…" + string(runes[len(runes)-tail:])
}

// flow and wrapText are tuilayout's (the launcher wraps a status with the
// same two), under the names this package's code and tests use.
func flow(units []string, sep string, width int) []string {
	return tuilayout.Flow(units, sep, width)
}

func wrapText(s string, width int) string { return tuilayout.WrapText(s, width) }

// userHome is the home directory paths are abbreviated against. A seam: the
// package's TestMain names one, so no test reads the developer's.
var userHome = os.UserHomeDir

// tildePath writes a path under the home directory with ~ for the home, the
// way the registry's own model_dir does: shorter, and still a path a shell
// takes.
func tildePath(path string) string {
	home, err := userHome()
	if err != nil || home == "" || home == "/" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		return "~" + rest
	}
	return path
}

// weightsLines is modeladmin.WeightsNote for the tab: the same words, with
// the path abbreviated and on a line of its own, so that wrapping never
// breaks it in the middle. Nil for a row with nothing on this machine.
func weightsLines(r modeladmin.Row) []string {
	r.Path = tildePath(r.Path)
	note := modeladmin.WeightsNote(r)
	switch {
	case note == "":
		return nil
	case r.Path != "" && strings.HasSuffix(note, " "+r.Path):
		return []string{strings.TrimSuffix(note, " "+r.Path), r.Path}
	}
	return []string{note}
}

// probeCmd reads the config and probes the providers once, off the update
// loop, and returns the rows.
func (m *model) probeCmd() tea.Cmd {
	m.models.gen++
	m.models.asked, m.models.busy = true, true
	gen, deps := m.models.gen, m.opts.Models
	return func() tea.Msg {
		cfg, err := deps.Load()
		if cfg == nil {
			cfg = &config.Config{}
		}
		return modelsLoadedMsg{gen: gen, cfg: cfg, err: err, rows: modeladmin.Rows(cfg, deps.Probe(cfg))}
	}
}

// buildModelsList lays rows out as a table: one shared column layout, one
// item per row, the header as the list's title. It returns the layout and
// the longest id's width with it.
func buildModelsList(theme themes.Theme, rows []modeladmin.Row) (list.Model, *tuilayout.Columns, int) {
	famW, idW, stW := len("FAMILY"), len("MODEL"), len("STATUS")
	sizes := make([]string, len(rows))
	for i, r := range rows {
		sizes[i] = modeladmin.FormatSize(r.Size)
		famW = tuilayout.MaxRunes(famW, dash(r.Family))
		idW = tuilayout.MaxRunes(idW, r.ID)
		stW = tuilayout.MaxRunes(stW, string(r.Status))
	}
	const locW, runW, sizeW = 5, 7, 8
	cols := tuilayout.NewColumns(
		[]string{
			tuilayout.PadRunes("FAMILY", famW), tuilayout.PadRunes("MODEL", idW), tuilayout.PadRunes("LOC", locW),
			tuilayout.PadRunes("STATUS", stW), tuilayout.PadRunes("RUNNING", runW), "SIZE",
		},
		[]int{famW, idW, locW, stW, runW, sizeW},
		0, modelsDropOrder,
	)
	items := make([]list.Item, len(rows))
	for i, r := range rows {
		// The MODEL cell is drawn by modelRow.Title, at the column's width.
		items[i] = modelRow{row: r, cols: cols, cells: []string{
			tuilayout.PadRunes(dash(r.Family), famW), "", tuilayout.PadRunes(dash(r.Location), locW),
			tuilayout.PadRunes(string(r.Status), stW), tuilayout.PadRunes(r.Running, runW), sizes[i],
		}}
	}
	delegate := tui.ThemedListDelegate(theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	// No mark on a filter's matches: bubbles marks the runes it matched in
	// FilterValue (the id and the family), which are not where they are in
	// the row's line.
	delegate.Styles.FilterMatch = lipgloss.NewStyle()
	l := list.New(items, delegate, 0, 0)
	l.Title = cols.Header()
	tuilayout.StyleTableTitle(&l, theme.Token(themes.TokenDim))
	l.SetShowStatusBar(false)
	// The tab prints its own key hints: the list's help line does not know
	// the tab's keys and costs a row.
	l.SetShowHelp(false)
	// q and esc are the list's own quit keys, and its quit goes round the
	// editor's (the unsaved-changes prompt, a write in flight). q is handled
	// by updateModels; esc only clears a filter.
	l.DisableQuitKeybindings()
	return l, cols, idW
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// setModelColumn gives the MODEL column a width.
func setModelColumn(cols *tuilayout.Columns, w int) {
	cols.Widths[mcModel] = w
	cols.Heads[mcModel] = tuilayout.PadRunes("MODEL", w)
}

// selectedModel is the row under the cursor.
func (m *model) selectedModel() (modeladmin.Row, bool) {
	if !m.models.loaded {
		return modeladmin.Row{}, false
	}
	it, ok := m.models.list.SelectedItem().(modelRow)
	return it.row, ok
}

// modelsProbing is the status while a refresh the user asked for is out.
// The table stays on screen meanwhile, and n, enter, r and d wait for the
// answer; this line is the only sign of it. applyModels takes it down.
const modelsProbing = "probing providers..."

// noteProbeOut says why a key that needs the table did nothing: every save
// and removal is followed by a probe, so n, enter, d or r typed straight
// after one arrives while it is out. The line goes under what the status
// already says ("added ...") and is not added twice; a write in flight has
// its own line ("removing ...").
func (m *model) noteProbeOut() {
	mt := &m.models
	switch {
	case mt.writing || strings.HasSuffix(mt.status, modelsProbing):
	case mt.status == "":
		mt.status = modelsProbing
	default:
		mt.status += "\n" + modelsProbing
	}
}

// routesPending is the note the tab shows once it has written the registry.
const routesPending = "LiteLLM routes pending (sync on quit)"

// modelsStatus is the tab's status block: why the registry did not load,
// what the last action said, and a note that the LiteLLM routes are owed a
// sync once the registry has been written. It is wrapped, not cut: a
// removal's status ends with the path of the weights left behind.
func (m *model) modelsStatus() string {
	var parts []string
	for _, s := range []string{m.models.loadErr, m.models.status} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	switch {
	case m.quitPending:
		parts = append(parts, "quitting when the registry write is done...")
	case m.registryChanged:
		parts = append(parts, routesPending)
	}
	return wrapText(strings.Join(parts, "\n"), m.width)
}

// malformedLine says what the loader tolerated in a row's fetch and draft, in
// config.Model.Malformed's own phrases — "fetch is not a table; read as
// absent" — or "" for a row with nothing malformed. `wt model list` prints
// the same phrases on stderr, with the file to fix.
func malformedLine(r modeladmin.Row) string {
	if len(r.Malformed) == 0 {
		return ""
	}
	return strings.Join(r.Malformed, ", ") + "; read as absent"
}

// modelsDetail is the block under the table, in two parts: the selected
// row's id, whole, with its status and running state (the table may have cut
// the id, and has no column for the rest), its tags and, for a pairing, its
// target and draft, then a line for a malformed fetch or draft, which is why
// such a row has no path or reads "missing"; and the path of its weights, on
// lines of its own.
func (m *model) modelsDetail() (id, path []string) {
	return m.modelsDetailLines(true)
}

// modelsDetailLines is modelsDetail, with a pairing's target and draft left
// out of the id part when sides is false: the two are the longest text the
// tab shows (a repo id and a directory), and a short terminal gives them up
// before it gives up the id (modelsFrames).
func (m *model) modelsDetailLines(sides bool) (id, path []string) {
	r, ok := m.selectedModel()
	if !ok {
		return nil, nil
	}
	parts := []string{r.ID, string(r.Status)}
	if running := map[string]string{modeladmin.RunningRun: "running", modeladmin.RunningLoad: "loading", modeladmin.RunningUnknown: "running?"}[r.Running]; running != "" {
		parts = append(parts, running)
	}
	if !strings.HasPrefix(r.ID, r.ProviderID+"/") {
		// Only when the id does not already say it (an omlx-6bit row's id
		// starts omlx/; --id can be anything).
		parts = append(parts, "provider "+r.ProviderID)
	}
	if len(r.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(r.Tags, ","))
	}
	if r.Pairing() && sides {
		parts = append(parts, "target "+dash(r.Target), "draft "+dash(r.Draft))
	}
	id = flow(parts, " · ", m.width)
	if line := malformedLine(r); line != "" {
		// Wrapped between words, like the rest of the block.
		id = append(id, flow(strings.Fields(line), " ", m.width)...)
	}
	if r.Path != "" && !r.Pairing() {
		path = flow([]string{tildePath(r.Path)}, "", m.width)
	}
	return id, path
}

// modelsHints are the tab's key hints, fullest first.
var modelsHints = []string{
	"enter edit · n add · d remove · r refresh · / filter · tab agents · q quit",
	"enter · n · d · r · / · tab · q quit",
}

// fitHints is the fullest of hints that fits width; the last one, cut, when
// none does.
func fitHints(width int, hints []string) string {
	for _, h := range hints {
		if width <= 0 || utf8.RuneCountInString(h) <= width {
			return h
		}
	}
	return tuilayout.Clip(hints[len(hints)-1], width)
}

// modelsFrames are the tab's layouts, fullest first: the tab bar, the status,
// the table, the selected row's detail block and the key hints.
//
// The first three are used only where they leave the table modelsTableMin
// lines: blank lines round the status, and the path, are not worth a table
// of one row. What a short terminal gives up, in order: the blank lines, the
// path, table rows down to one, a pairing's target and draft (two long
// values that can take four lines of a 40-column terminal by themselves, and
// more than the hints are worth), the hints, and last the id line with the
// malformed line under it. The tab bar, the status and the table are never
// given up by choice; a status too long to leave the table a row is drawn
// without the table (modelsView).
func (m *model) modelsFrames() []tuilayout.ListFrame {
	id, path := m.modelsDetail()
	status := m.modelsStatus()
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	build := func(spaced bool, detail []string, withHints bool) tuilayout.ListFrame {
		return func(listView string) string {
			gap := "\n"
			if spaced {
				gap = "\n\n"
			}
			out := tabBar(m.theme, TabModels) + gap
			if status != "" {
				out += status + gap
			}
			out += listView
			if len(detail) > 0 {
				out += "\n" + dim.Render(strings.Join(detail, "\n"))
			}
			if withHints {
				out += "\n" + dim.Render(fitHints(m.width, modelsHints))
			}
			return out
		}
	}
	whole := slices.Concat(id, path)
	var frames []tuilayout.ListFrame
	for _, f := range []tuilayout.ListFrame{build(true, whole, true), build(false, whole, true)} {
		// An empty list view still occupies one line, hence the -1.
		if m.height <= 0 || m.height-(lipgloss.Height(f(""))-1) >= modelsTableMin {
			frames = append(frames, f)
		}
	}
	frames = append(frames, build(false, id, true))
	if r, ok := m.selectedModel(); ok && r.Pairing() {
		short, _ := m.modelsDetailLines(false)
		return append(frames, build(false, short, true), build(false, short, false), build(false, nil, false))
	}
	return append(frames, build(false, id, false), build(false, nil, false))
}

// fitModels sizes the table to the room its frame leaves. Update calls it
// after every message, like fitList for the Agents tab. It measures the list
// (up to sixteen probe renders), so it runs only while the table is what is
// showing — the tab is the one on screen, and neither the form nor the remove
// prompt is over it: only modelsView's table draws this list, and the message
// that brings the table back is fitted on its way out of Update.
//
// The columns that fit are chosen with MODEL at its full width, the longest
// id (tuilayout.FitTo). When MODEL, STATUS and RUNNING — the three that are
// never dropped — are still wider than the list, MODEL is cut to what is
// left, and the ids longer than that lose their middle (middleCut); the
// detail block shows the selected one whole. One long id must not push
// STATUS and RUNNING off every row.
func (m *model) fitModels() {
	mt := &m.models
	if m.tab != TabModels || mt.phase != modelsList || m.width <= 0 || m.height <= 0 || !mt.loaded {
		return
	}
	setModelColumn(mt.cols, mt.idWidth)
	tuilayout.FitTo(&mt.list, m.width, m.height, m.modelsFrames()...)
	need := max(mt.cols.Width(), utf8.RuneCountInString(mt.cols.Header())+tuilayout.TitleRoom)
	if over := need - mt.list.Width(); over > 0 {
		setModelColumn(mt.cols, max(mt.idWidth-over, modelsMinID))
	}
	mt.list.Title = mt.cols.Header()
}

// modelsView renders the Models tab.
func (m *model) modelsView() string {
	switch m.models.phase {
	case modelsRemove:
		return m.modelsRemoveView()
	case modelsForm:
		return m.modelFormView()
	}
	if m.width <= 0 || m.height <= 0 {
		// No size yet: the first WindowSizeMsg is still on its way, and
		// fitModels has sized nothing. A frame measured against no width
		// draws the selected row's detail one rune to a line (flow), and
		// nothing is measured until there is a terminal to measure — which,
		// for a stdout that is not one, is never. Bubble Tea draws again
		// when a size arrives.
		return tabBar(m.theme, TabModels)
	}
	if !m.models.loaded {
		return tabBar(m.theme, TabModels) + "\n\n" + "Probing providers..."
	}
	view := tuilayout.DrawnFrame(&m.models.list, m.height, m.modelsFrames()...)(m.models.list.View())
	status := m.modelsStatus()
	if status == "" || m.height <= 0 || lipgloss.Height(view) <= m.height {
		return view
	}
	// The status is so long that even the sparest frame is taller than the
	// terminal (a refusal that ends with the registry's path, at 40x12).
	// Bubble Tea would drop the view's top lines: the tab bar and the start
	// of the very status that has to be read. So the status has the screen
	// to itself, under the tab bar, until the next key takes it down; the
	// hints join it when there is a line left for them.
	out := tabBar(m.theme, TabModels) + "\n" + status
	if lipgloss.Height(out) < m.height {
		out += "\n" + lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim)).Render(fitHints(m.width, modelsHints))
	}
	return lipgloss.NewStyle().MaxHeight(m.height).Render(out)
}

// applyModels takes a probe's result: the rows replace the table, with the
// cursor kept on the row it was on — or moved to the model a form just saved
// (selectID) — and an applied filter kept. A row the new rows no longer hold
// leaves the cursor at their top, which is where a table a removal shrank
// starts.
func (m *model) applyModels(msg modelsLoadedMsg) {
	mt := &m.models
	if msg.gen != mt.gen {
		return // a later probe is on its way
	}
	// keep is the id the cursor was on, and was where it stood: two rows can
	// share an id (a registry Validate refuses, which the tab lists so that
	// it can be repaired), and then the id alone does not say which of them
	// the cursor was on.
	keep, was := "", -1
	if r, ok := m.selectedModel(); ok {
		keep, was = r.ID, mt.list.Index()
	}
	if mt.selectID != "" {
		// A saved add is a row that was not there, so there is no "where the
		// cursor stood": the first row with the id.
		keep, was = mt.selectID, -1
	}
	filter := ""
	if mt.loaded && mt.list.FilterState() == list.FilterApplied {
		filter = mt.list.FilterValue()
	}
	mt.cfg, mt.rows, mt.loaded, mt.busy, mt.selectID = msg.cfg, msg.rows, true, false, ""
	if mt.cfg == nil {
		mt.cfg = &config.Config{}
	}
	// The probe has answered: its line goes, and what a save or a removal
	// said above it stays.
	mt.status = strings.TrimSuffix(strings.TrimSuffix(mt.status, modelsProbing), "\n")
	mt.list, mt.cols, mt.idWidth = buildModelsList(m.theme, msg.rows)
	mt.built++
	if filter != "" {
		mt.list.SetFilterText(filter)
	}
	sel := -1
	for i, it := range mt.list.VisibleItems() {
		// The first row with the id, unless the row where the cursor stood
		// has it too.
		if it.(modelRow).row.ID == keep && (sel < 0 || i == was) {
			sel = i
		}
	}
	if sel >= 0 {
		mt.list.Select(sel)
	}
	mt.loadErr = ""
	if msg.err != nil {
		// Not "registry: ": the probe loads the whole config, and a
		// config.toml that does not parse fails it too. The error names what
		// failed, and the hint is there only when the repair is the registry's.
		mt.loadErr = "config load error: " + msg.err.Error()
		// The repair, worded by the one function that words it (as on the
		// Agents tab's status): a file this tab cannot read is not one it
		// can fix.
		if hint := config.RegistryFixHint(msg.err); hint != "" {
			mt.loadErr += " (" + hint + ")"
		}
	}
}

// filterMatchesMsg is a bubbles list's filter result with the list it is
// for. The list ranks its items in a command and takes the result as a
// message; that message says nothing of which list sent it, and the editor
// has two. tab names the list, and built the Models table it was ranked
// over, so that a result is never applied to the other tab's list or to a
// table that has been rebuilt since.
type filterMatchesMsg struct {
	tab     Tab
	built   int
	matches list.FilterMatchesMsg
}

// tagFilter wraps a command a list returned, so that a filter result in what
// it produces comes back as a filterMatchesMsg.
func tagFilter(cmd tea.Cmd, tab Tab, built int) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch msg := cmd().(type) {
		case list.FilterMatchesMsg:
			return filterMatchesMsg{tab: tab, built: built, matches: msg}
		case tea.BatchMsg:
			for i := range msg {
				msg[i] = tagFilter(msg[i], tab, built)
			}
			return msg
		default:
			return msg
		}
	}
}

// applyFilterMatches hands a filter result to the list it was ranked for.
func (m *model) applyFilterMatches(msg filterMatchesMsg) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case msg.tab == TabAgents && m.ready:
		m.list, cmd = m.list.Update(msg.matches)
	case msg.tab == TabModels && m.models.loaded && msg.built == m.models.built:
		m.models.list, cmd = m.models.list.Update(msg.matches)
	}
	return cmd
}

// updateModels handles a message while the Models tab is showing.
func (m *model) updateModels(msg tea.Msg) (tea.Model, tea.Cmd) {
	mt := &m.models
	switch mt.phase {
	case modelsRemove:
		return m.updateModelsRemove(msg)
	case modelsForm:
		return m.updateModelForm(msg)
	}
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m, nil
	}
	if !mt.loaded {
		// The first probe is on its way and there is no table to act on,
		// but the user is not held on this tab until a slow provider answers.
		switch key.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "tab":
			m.tab = TabAgents
		}
		return m, nil
	}
	if mt.list.FilterState() == list.Filtering {
		// Every key is text for the filter, q included, but ctrl+c: the list
		// has its own quit keys turned off (buildModelsList), its force-quit
		// with them, and ctrl+c leaves the editor from every screen.
		if key.String() == "ctrl+c" {
			return m.quit()
		}
		var cmd tea.Cmd
		mt.list, cmd = mt.list.Update(msg)
		return m, tagFilter(cmd, TabModels, mt.built)
	}
	// What the last action said has been read: the next key clears it, and
	// gives its lines back to the table. Not while a probe or a write is
	// out: "removing ..." and "probing ..." are why r and d do nothing yet.
	if !mt.busy {
		mt.status = ""
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "tab":
		m.tab = TabAgents
		return m, nil
	case "r":
		if mt.busy {
			m.noteProbeOut()
			return m, nil
		}
		mt.status = modelsProbing
		return m, m.probeCmd()
	case "n":
		// Not over rows that are about to be replaced.
		if mt.busy {
			m.noteProbeOut()
			return m, nil
		}
		m.openModelForm(nil)
		return m, nil
	case "enter":
		// Edit a registry row; register a discovered one.
		if mt.busy {
			m.noteProbeOut()
			return m, nil
		}
		if r, ok := m.selectedModel(); ok {
			m.openModelForm(&r)
		}
		return m, nil
	case "d":
		r, ok := m.selectedModel()
		switch {
		case mt.busy:
			m.noteProbeOut()
		case !ok:
		case !r.Registered:
			mt.status = r.ID + " is not in the registry: there is nothing to remove"
		default:
			mt.phase, mt.remove = modelsRemove, r
		}
		return m, nil
	}
	var cmd tea.Cmd
	mt.list, cmd = mt.list.Update(msg)
	return m, tagFilter(cmd, TabModels, mt.built)
}

// updateModelsRemove handles the remove prompt: y removes, anything that
// means no goes back.
func (m *model) updateModelsRemove(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	mt := &m.models
	switch key.String() {
	case "y", "Y":
		row := mt.remove
		mt.phase, mt.busy, mt.writing, mt.status = modelsList, true, true, "removing "+row.ID+"..."
		return m, func() tea.Msg {
			// A registry write takes a file lock, so it is not for Update.
			return modelRemovedMsg{id: row.ID, note: strings.Join(weightsLines(row), "\n"), err: modeladmin.Remove([]string{row.ID})}
		}
	case "n", "N", "esc", "q":
		mt.phase = modelsList
	case "ctrl+c":
		return m.quit()
	}
	return m, nil
}

// modelsRemoveView is the remove prompt. It says where the weights are before
// the row is gone, because wt deletes none.
//
// A long id and a long path wrap to more lines than a short terminal has (a
// 72-column id and its omlx directory are 13 lines at 40 columns). What the
// prompt gives up, in order: its two blank lines, then the closing sentence —
// the question and the path are what y is answered on. Only a prompt still
// too tall after that is cut at the bottom.
func (m *model) modelsRemoveView() string {
	r := m.models.remove
	question := "Remove " + r.ID + " from the registry? [y/N]"
	weights := strings.Join(weightsLines(r), "\n")
	build := func(gap string, closing bool) string {
		parts := []string{question}
		if weights != "" {
			parts = append(parts, weights)
		}
		text := strings.Join(parts, gap)
		if closing {
			text += "\nwt removes the registry entry only; it never deletes weights."
		}
		return tabBar(m.theme, TabModels) + gap + wrapText(text, m.width)
	}
	view := build("\n\n", true)
	for _, spare := range []string{build("\n", true), build("\n", false)} {
		if m.height <= 0 || lipgloss.Height(view) <= m.height {
			break
		}
		view = spare
	}
	return lipgloss.NewStyle().MaxHeight(max(m.height, 1)).Render(view)
}

// applyModelRemoved takes a removal's outcome and re-probes: the rows are
// rebuilt from the registry as it now is. A quit that was waiting for the
// write happens now, with the change recorded for the caller — through quit,
// like any other: the tabs took keys while it waited, and an agent edit made
// meanwhile gets the unsaved-changes prompt (the table is then read again
// under it, for a user who cancels). Edits the user already chose to discard
// are not asked about twice (discardHeld). After a refusal the quit does not
// happen, because the refusal has to be read — on this tab, which is where
// it is drawn, so a quit typed from the Agents tab comes back here. A refusal
// re-probes too: the usual reason is that another program took the row
// first, and the table would go on showing it.
func (m *model) applyModelRemoved(msg modelRemovedMsg) tea.Cmd {
	mt := &m.models
	mt.busy, mt.writing = false, false
	if msg.err != nil {
		mt.status = "not removed: " + msg.err.Error()
		m.refuseHeldQuit()
		return m.probeCmd()
	}
	m.registryChanged = true
	mt.status = "removed " + msg.id
	if msg.note != "" {
		// The path again: the prompt that showed it is gone.
		mt.status += "; " + msg.note
	}
	if m.quitPending {
		m.quitPending, m.discardHeld = false, false
		if _, cmd := m.quit(); cmd != nil {
			return cmd
		}
		// The unsaved-changes prompt is up; the status written above it is
		// the one behind it when it is answered.
	}
	return m.probeCmd()
}
