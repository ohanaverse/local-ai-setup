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
	// cfg is the config the rows were built from, re-read on every probe.
	cfg  *config.Config
	rows []modeladmin.Row
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
	// selectID is the row the cursor goes to when the next rows arrive.
	selectID string
	// remove is the row the remove prompt is about.
	remove modeladmin.Row
}

// modelsLoadedMsg carries one probe's rows.
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

// flow lays units out on lines of at most width runes, sep between two units
// on a line. A line is broken only between units — never inside one, and so
// never at a hyphen inside an id or a path — except that a unit longer than a
// whole line is cut at the line's end.
func flow(units []string, sep string, width int) []string {
	width = max(width, 1)
	var lines []string
	cur := ""
	for _, u := range units {
		if cur != "" {
			if utf8.RuneCountInString(cur+sep+u) <= width {
				cur += sep + u
				continue
			}
			lines, cur = append(lines, cur), ""
		}
		runes := []rune(u)
		for len(runes) > width {
			lines, runes = append(lines, string(runes[:width])), runes[width:]
		}
		cur = string(runes)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// wrapText wraps free text to width at its spaces, keeping the line breaks
// it has. A path on a line of its own is therefore cut only at the
// terminal's edge and can be copied as one token. A line that fits is kept
// as it was written, its spacing included, and so is every line when the
// width is not known yet (0, before the first WindowSizeMsg), as in fitHints:
// flow would otherwise put one rune on a line.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
			continue
		}
		if utf8.RuneCountInString(line) <= width {
			lines = append(lines, line)
			continue
		}
		lines = append(lines, flow(strings.Fields(line), " ", width)...)
	}
	return strings.Join(lines, "\n")
}

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
// The table stays on screen meanwhile, and r and d wait for the answer; this
// line is the only sign of it. applyModels takes it down.
const modelsProbing = "probing providers..."

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

// modelsDetail is the block under the table, in two parts: the selected
// row's id, whole, with its status and running state (the table may have cut
// the id, and has no column for the rest) and its tags; and the path of its
// weights, on lines of its own.
func (m *model) modelsDetail() (id, path []string) {
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
	if r.Pairing() {
		parts = append(parts, "target "+dash(r.Target), "draft "+dash(r.Draft))
	}
	id = flow(parts, " · ", m.width)
	if r.Path != "" && !r.Pairing() {
		path = flow([]string{tildePath(r.Path)}, "", m.width)
	}
	return id, path
}

// modelsHints are the tab's key hints, fullest first.
var modelsHints = []string{
	"d remove · r refresh · / filter · tab agents · q quit",
	"d · r · / · tab · q quit",
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
// path, table rows down to one, the hints, and last the id line. The tab bar,
// the status and the table are never given up by choice.
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
	return append(frames, build(false, id, true), build(false, id, false), build(false, nil, false))
}

// fitModels sizes the table to the room its frame leaves. Update calls it
// after every message, like fitList for the Agents tab.
//
// The columns that fit are chosen with MODEL at its full width, the longest
// id (tuilayout.FitTo). When MODEL, STATUS and RUNNING — the three that are
// never dropped — are still wider than the list, MODEL is cut to what is
// left, and the ids longer than that lose their middle (middleCut); the
// detail block shows the selected one whole. One long id must not push
// STATUS and RUNNING off every row.
func (m *model) fitModels() {
	mt := &m.models
	if m.width <= 0 || m.height <= 0 || !mt.loaded {
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
	if m.models.phase == modelsRemove {
		return m.modelsRemoveView()
	}
	if !m.models.loaded {
		return tabBar(m.theme, TabModels) + "\n\n" + "Probing providers..."
	}
	return tuilayout.DrawnFrame(&m.models.list, m.height, m.modelsFrames()...)(m.models.list.View())
}

// applyModels takes a probe's result: the rows replace the table, with the
// cursor kept on the row it was on (or moved to selectID after a write) and
// an applied filter kept.
func (m *model) applyModels(msg modelsLoadedMsg) {
	mt := &m.models
	if msg.gen != mt.gen {
		return // a later probe is on its way
	}
	keep := mt.selectID
	if r, ok := m.selectedModel(); ok && keep == "" {
		keep = r.ID
	}
	filter := ""
	if mt.loaded && mt.list.FilterState() == list.FilterApplied {
		filter = mt.list.FilterValue()
	}
	mt.cfg, mt.rows, mt.loaded, mt.busy, mt.selectID = msg.cfg, msg.rows, true, false, ""
	if mt.status == modelsProbing {
		mt.status = ""
	}
	mt.list, mt.cols, mt.idWidth = buildModelsList(m.theme, msg.rows)
	mt.built++
	if filter != "" {
		mt.list.SetFilterText(filter)
	}
	for i, it := range mt.list.VisibleItems() {
		if it.(modelRow).row.ID == keep {
			mt.list.Select(i)
		}
	}
	mt.loadErr = ""
	if msg.err != nil {
		mt.loadErr = "registry: " + msg.err.Error()
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
	if mt.phase == modelsRemove {
		return m.updateModelsRemove(msg)
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
			return m, nil
		}
		mt.status = modelsProbing
		return m, m.probeCmd()
	case "d":
		r, ok := m.selectedModel()
		switch {
		case !ok || mt.busy:
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
func (m *model) modelsRemoveView() string {
	r := m.models.remove
	text := "Remove " + r.ID + " from the registry? [y/N]\n\n"
	if lines := weightsLines(r); lines != nil {
		text += strings.Join(lines, "\n") + "\n"
	}
	text += "wt removes the registry entry only; it never deletes weights."
	return lipgloss.NewStyle().MaxHeight(max(m.height, 1)).Render(tabBar(m.theme, TabModels) + "\n\n" + wrapText(text, m.width))
}

// applyModelRemoved takes a removal's outcome and re-probes: the rows are
// rebuilt from the registry as it now is. A quit that was waiting for the
// write happens now, with the change recorded for the caller; after a
// refusal it does not, because the refusal has to be read — on this tab,
// which is where it is drawn, so a quit typed from the Agents tab comes back
// here. A refusal re-probes too: the usual reason is that another program
// took the row first, and the table would go on showing it.
func (m *model) applyModelRemoved(msg modelRemovedMsg) tea.Cmd {
	mt := &m.models
	mt.busy, mt.writing = false, false
	if msg.err != nil {
		mt.status = "not removed: " + msg.err.Error()
		if m.quitPending {
			m.quitPending, m.tab = false, TabModels
		}
		return m.probeCmd()
	}
	m.registryChanged = true
	if m.quitPending {
		return tea.Quit
	}
	mt.status = "removed " + msg.id
	if msg.note != "" {
		// The path again: the prompt that showed it is gone.
		mt.status += "; " + msg.note
	}
	return m.probeCmd()
}
