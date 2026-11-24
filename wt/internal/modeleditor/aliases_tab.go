package modeleditor

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The Aliases tab (#688): the registry's aliases as a table, with an add and
// edit form and a remove prompt over the same checks `wt-model alias set|rm`
// run. Decisions, recorded here because the issue left them open:
//
//   - It is a tab of its own, not a mode of the Models table (models_tab.go is
//     untouched).
//   - The form takes the targets as free text in the command's own syntax
//     ("model[:order], model[:order]"), validated by the real checks
//     (modeladmin.ParseAliasTargetsText, CheckAlias), not a picker.
//   - A save that makes the window smaller or unknown asks first, in the
//     command's own sentence (modeladmin.WindowChange.ShrinkSentence); only y
//     writes.
//   - A remove is refused, with no prompt, while a client session uses the
//     alias or the sessions cannot be read. There is no override here:
//     `wt-model alias rm --force-in-use` is the way.
//   - Like the Models tab it only writes registry.toml; the one route sync is
//     the caller's, on quit (Result.RegistryChanged).

// The Aliases tab's columns, left to right.
const (
	acAlias = iota
	acTargets
	acContext
	acRouted
	acSessions
)

// aliasesDropOrder is what a narrow terminal gives up, first to go first.
// ALIAS, TARGETS and CONTEXT are never dropped; the detail block shows the
// rest of the selected row.
var aliasesDropOrder = []int{acSessions, acRouted}

// aliasesMinTargets is the narrowest the TARGETS column is ever cut to.
const aliasesMinTargets = 12

// aliasesTableMin is the height the fuller layouts leave the table.
const aliasesTableMin = 5

// aliasesPhase is what the Aliases tab is showing.
type aliasesPhase int

const (
	aliasesList    aliasesPhase = iota
	aliasesForm                 // add / edit
	aliasesConfirm              // the shrink question over a save
	aliasesRemove               // the remove prompt
)

// aliasesTab is the Aliases tab's state. Its rows come from one read run in a
// command (loadAliasesCmd); nothing here reads a file or dials a server in
// Update.
type aliasesTab struct {
	phase aliasesPhase
	list  list.Model
	cols  *tuilayout.Columns
	// tgtWidth is the longest targets text, the TARGETS column's width when
	// nothing is cut.
	tgtWidth int
	// cfg and ix are what the last read loaded; the form validates against
	// them (the write checks again under the registry lock). Never nil once
	// loaded.
	cfg   *config.Config
	ix    tiers.Index
	views []modeladmin.AliasView
	// routed is nil when the database was not read; routeWarn says why.
	routed    map[string]bool
	routeWarn string
	// sessions is the running server's per-alias count: nil with no server,
	// and sessErr set when a server runs whose sessions could not be read.
	sessions map[string]int
	sessErr  error

	asked, loaded bool
	gen, built    int
	busy, writing bool
	status        string
	loadErr       string
	selectID      string

	form *aliasForm
	// pending is the save the shrink question is about; wc its window change.
	pending modeladmin.AliasRequest
	wc      modeladmin.WindowChange
	// remove is the alias the remove prompt is about.
	remove string
}

// aliasesLoadedMsg carries one read of everything the tab shows.
type aliasesLoadedMsg struct {
	gen       int
	cfg       *config.Config
	err       error
	ix        tiers.Index
	routed    map[string]bool
	routeWarn string
	sessions  map[string]int
	sessErr   error
}

// aliasSavedMsg reports a SetAlias the form's save started.
type aliasSavedMsg struct {
	id      string
	changed bool
	err     error
}

// aliasRemovedMsg reports a RemoveAliases the remove prompt confirmed.
type aliasRemovedMsg struct {
	id  string
	err error
}

// aliasRow adapts an alias view to a table row.
type aliasRow struct {
	view    modeladmin.AliasView
	targets string
	cells   []string
	cols    *tuilayout.Columns
}

func (r aliasRow) FilterValue() string              { return r.view.ID }
func (r aliasRow) Description() string              { return "" }
func (r aliasRow) TableColumns() *tuilayout.Columns { return r.cols }

// Title is the row's line; the TARGETS cell is drawn at the column's width
// of the moment, so a resize rebuilds nothing.
func (r aliasRow) Title() string {
	cells := slices.Clone(r.cells)
	w := r.cols.Widths[acTargets]
	cells[acTargets] = tuilayout.PadRunes(endCut(r.targets, w), w)
	return r.cols.Line(cells)
}

// visible is registry text with every control character replaced, so that
// no sequence in an id reaches the terminal.
func visible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// targetsText is a view's targets as the column shows them.
func targetsText(v modeladmin.AliasView) string {
	parts := make([]string, 0, len(v.Targets))
	for _, t := range v.Targets {
		parts = append(parts, strconv.Itoa(t.Order)+":"+t.Model)
	}
	return visible(strings.Join(parts, ","))
}

// loadAliasesCmd reads the config, the tiers index, LiteLLM's routes and the
// server's sessions once, off the update loop.
func (m *model) loadAliasesCmd() tea.Cmd {
	al := &m.aliases
	al.gen++
	al.asked, al.busy = true, true
	gen := al.gen
	deps, load := m.opts.Aliases.withDefaults(), m.opts.Models.withDefaults().Load
	return func() tea.Msg {
		ctx := context.Background()
		cfg, err := load()
		if cfg == nil {
			cfg = &config.Config{}
		}
		msg := aliasesLoadedMsg{gen: gen, cfg: cfg, err: err, ix: deps.Index()}
		msg.routed, msg.routeWarn = deps.Routed(ctx, cfg)
		msg.sessions, msg.sessErr = deps.Sessions(ctx)
		return msg
	}
}

func buildAliasesList(theme themes.Theme, views []modeladmin.AliasView, cells [][]string, targets []string) (list.Model, *tuilayout.Columns, int) {
	aW, tW, cW, rW, sW := len("ALIAS"), len("TARGETS"), len("CONTEXT"), len("ROUTED"), len("SESSIONS")
	for i := range views {
		aW = tuilayout.MaxRunes(aW, cells[i][acAlias])
		tW = tuilayout.MaxRunes(tW, targets[i])
		cW = tuilayout.MaxRunes(cW, cells[i][acContext])
	}
	cols := tuilayout.NewColumns(
		[]string{tuilayout.PadRunes("ALIAS", aW), tuilayout.PadRunes("TARGETS", tW), tuilayout.PadRunes("CONTEXT", cW), tuilayout.PadRunes("ROUTED", rW), "SESSIONS"},
		[]int{aW, tW, cW, rW, sW}, 0, aliasesDropOrder,
	)
	items := make([]list.Item, len(views))
	for i, v := range views {
		c := cells[i]
		items[i] = aliasRow{view: v, targets: targets[i], cols: cols, cells: []string{
			tuilayout.PadRunes(c[acAlias], aW), "", tuilayout.PadRunes(c[acContext], cW), tuilayout.PadRunes(c[acRouted], rW), c[acSessions],
		}}
	}
	delegate := themes.ListDelegate(theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	l := list.New(items, delegate, 0, 0)
	l.Title = cols.Header()
	tuilayout.StyleTableTitle(&l, theme.Token(themes.TokenDim))
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	// No filter: a registry holds a handful of aliases, and the hints are
	// short enough at 40 columns without it.
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()
	return l, cols, tW
}

// selectedAlias is the row under the cursor.
func (m *model) selectedAlias() (modeladmin.AliasView, bool) {
	if !m.aliases.loaded {
		return modeladmin.AliasView{}, false
	}
	it, ok := m.aliases.list.SelectedItem().(aliasRow)
	return it.view, ok
}

const aliasesReading = "reading aliases..."

// applyAliases takes a read's result: the rows replace the table with the
// cursor kept on the same alias (or moved to the one a save just wrote).
func (m *model) applyAliases(msg aliasesLoadedMsg) {
	al := &m.aliases
	if msg.gen != al.gen {
		return // a later read is on its way
	}
	keep := ""
	if v, ok := m.selectedAlias(); ok {
		keep = v.ID
	}
	if al.selectID != "" {
		keep = al.selectID
	}
	al.cfg, al.ix, al.routed, al.routeWarn, al.sessions, al.sessErr = msg.cfg, msg.ix, msg.routed, msg.routeWarn, msg.sessions, msg.sessErr
	al.loaded, al.busy, al.selectID = true, false, ""
	al.status = strings.TrimSuffix(strings.TrimSuffix(al.status, aliasesReading), "\n")
	al.views = modeladmin.AliasViews(al.cfg, al.ix)
	cells := make([][]string, len(al.views))
	targets := make([]string, len(al.views))
	for i, v := range al.views {
		window, sess, route := "-", "-", "?"
		if v.Context > 0 {
			window = strconv.Itoa(v.Context)
		}
		switch {
		case al.sessErr != nil:
			sess = "?"
		case al.sessions != nil:
			sess = strconv.Itoa(al.sessions[v.ID])
		}
		if al.routed != nil {
			route = "no"
			if al.routed[v.ID] {
				route = "yes"
			}
		}
		cells[i] = []string{visible(v.ID), "", window, route, sess}
		targets[i] = targetsText(v)
	}
	al.list, al.cols, al.tgtWidth = buildAliasesList(m.theme, al.views, cells, targets)
	al.built++
	for i, it := range al.list.Items() {
		if it.(aliasRow).view.ID == keep {
			al.list.Select(i)
			break
		}
	}
	al.loadErr = ""
	if msg.err != nil {
		al.loadErr = "config load error: " + msg.err.Error()
		if hint := config.RegistryFixHint(msg.err); hint != "" {
			al.loadErr += " (" + hint + ")"
		}
	}
}

// aliasesStatus is the tab's status block: the host's load banner (unless
// the read has an error of its own), what the last action said, and the note
// that the LiteLLM routes are owed a sync once the registry was written.
func (m *model) aliasesStatus() string {
	al := &m.aliases
	banner := m.status
	if al.loadErr != "" {
		banner = ""
	}
	var parts []string
	for _, s := range []string{banner, al.loadErr, al.status} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	switch {
	case m.quitPending && al.writing:
		parts = append(parts, "quitting when the registry write is done...")
	case m.registryChanged:
		parts = append(parts, routesPending)
	}
	return editorkit.WrapText(strings.Join(parts, "\n"), m.width)
}

// aliasDetail is the block under the table: the selected alias's id, whole,
// the redirect it is, its targets whole, why the registry cannot use it, and
// what the columns could not say (sessions unreadable, window unknown). short
// keeps the first line only.
func (m *model) aliasDetail(short bool) []string {
	al := &m.aliases
	v, ok := m.selectedAlias()
	if !ok {
		if al.loaded && len(al.views) == 0 {
			return []string{"no aliases: n adds one"}
		}
		return nil
	}
	head := []string{visible(v.ID)}
	if v.RedirectTo != "" {
		head = append(head, "redirects to "+visible(v.RedirectTo))
	}
	if al.sessErr != nil {
		head = append(head, "sessions unreadable")
	}
	lines := editorkit.Flow(head, " · ", m.width)
	if short {
		return lines[:1]
	}
	if v.Context <= 0 && v.Limiting != "" {
		lines = append(lines, editorkit.Flow([]string{"window unknown:", visible(v.Limiting)}, " ", m.width)...)
	}
	parts := make([]string, 0, len(v.Targets))
	for _, t := range v.Targets {
		parts = append(parts, strconv.Itoa(t.Order)+":"+visible(t.Model))
	}
	if len(parts) > 0 {
		lines = append(lines, editorkit.Flow(slices.Concat([]string{"targets"}, parts), " ", m.width)...)
	}
	if a, found := al.cfg.AliasByID(v.ID); found {
		if err := config.AliasFault(al.cfg, a); err != nil {
			lines = append(lines, editorkit.Flow(strings.Fields("unusable: "+visible(err.Error())), " ", m.width)...)
		}
	}
	return lines
}

// aliasesHints are the tab's key hints, fullest first.
var aliasesHints = []string{
	"n add · enter edit · d remove · r refresh · tab · q quit",
	"n · enter · d · r · tab · q quit",
}

// aliasesFrames are the tab's layouts, fullest first: the tab bar, the
// status, the table, the selected row's detail, the routes warning and the
// key hints. What a short terminal gives up, in order: the blank lines, the
// routes warning, all but the detail's first line, the detail, the hints. The
// tab bar, the status and the table are never given up by choice.
func (m *model) aliasesFrames() []tuilayout.ListFrame {
	status := m.aliasesStatus()
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	warn := ""
	if m.aliases.routeWarn != "" {
		warn = editorkit.WrapText(m.aliases.routeWarn, m.width)
	}
	build := func(spaced bool, detail []string, warn string, withHints bool) tuilayout.ListFrame {
		return func(listView string) string {
			gap := "\n"
			if spaced {
				gap = "\n\n"
			}
			out := tabBar(m.theme, TabAliases) + gap
			if status != "" {
				out += status + gap
			}
			out += listView
			if len(detail) > 0 {
				out += "\n" + dim.Render(strings.Join(detail, "\n"))
			}
			if warn != "" {
				out += "\n" + dim.Render(warn)
			}
			if withHints {
				out += "\n" + dim.Render(editorkit.FitHints(m.width, aliasesHints))
			}
			return out
		}
	}
	all, one := m.aliasDetail(false), m.aliasDetail(true)
	var frames []tuilayout.ListFrame
	for _, f := range []tuilayout.ListFrame{build(true, all, warn, true), build(false, all, warn, true)} {
		if m.height <= 0 || m.height-(lipgloss.Height(f(""))-1) >= aliasesTableMin {
			frames = append(frames, f)
		}
	}
	return append(frames,
		build(false, all, "", true),
		build(false, one, "", true),
		build(false, nil, "", true),
		build(false, nil, "", false),
	)
}

func setTargetsColumn(cols *tuilayout.Columns, w int) {
	cols.Widths[acTargets] = w
	cols.Heads[acTargets] = tuilayout.PadRunes("TARGETS", w)
}

// fitAliases sizes the table to the room its frame leaves; Update calls it
// after every message, and it measures the list, so it runs only while the
// table is what is showing. The columns that fit are chosen with TARGETS at
// its full width; when the three that are never dropped are still wider than
// the list, TARGETS is cut to what is left (the detail block shows the whole
// text).
func (m *model) fitAliases() {
	al := &m.aliases
	if m.tab != TabAliases || al.phase != aliasesList || m.width <= 0 || m.height <= 0 || !al.loaded {
		return
	}
	setTargetsColumn(al.cols, al.tgtWidth)
	tuilayout.FitTo(&al.list, m.width, m.height, m.aliasesFrames()...)
	if len(al.list.Items()) == 0 {
		// FitTo fits the columns through the rows; with none the header
		// still has to fit.
		al.cols.Fit(al.list.Width())
	}
	need := max(al.cols.Width(), utf8.RuneCountInString(al.cols.Header())+tuilayout.TitleRoom)
	if over := need - al.list.Width(); over > 0 {
		setTargetsColumn(al.cols, max(al.tgtWidth-over, aliasesMinTargets))
	}
	al.list.Title = al.cols.Header()
}

// aliasesView renders the Aliases tab.
func (m *model) aliasesView() string {
	al := &m.aliases
	switch al.phase {
	case aliasesRemove:
		return m.aliasPromptView("Remove "+visible(al.remove)+" from the registry? Its LiteLLM routes go with it. [y/N]", aliasesPromptHints)
	case aliasesConfirm:
		return m.aliasPromptView(m.aliasShrinkQuestion(), aliasesPromptHints)
	case aliasesForm:
		return m.aliasFormView()
	}
	if m.width <= 0 || m.height <= 0 {
		return tabBar(m.theme, TabAliases)
	}
	if !al.loaded {
		return tabBar(m.theme, TabAliases) + "\n\n" + "Reading aliases..."
	}
	view := tuilayout.DrawnFrame(&al.list, m.height, m.aliasesFrames()...)(al.list.View())
	status := m.aliasesStatus()
	if status == "" || lipgloss.Height(view) <= m.height {
		return view
	}
	// A status taller than the screen has it to itself, as on the Models tab.
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return tuilayout.MessageAlone(tabBar(m.theme, TabAliases), status, editorkit.FitHints(m.width, aliasesHints), m.width, m.height, dim)
}

// aliasesPromptHints are the key hints under a y/N question.
var aliasesPromptHints = []string{"y yes · any other key no", "y · n"}

// aliasPromptView is a question with the screen to itself.
func (m *model) aliasPromptView(question string, hints []string) string {
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return tuilayout.MessageAlone(tabBar(m.theme, TabAliases), editorkit.WrapText(question, m.width), editorkit.FitHints(m.width, hints), m.width, m.height, dim)
}

// aliasShrinkQuestion is the question over a save that makes the window
// smaller or unknown: the command's own sentence, then the ask.
func (m *model) aliasShrinkQuestion() string {
	al := &m.aliases
	count := -1
	if al.sessErr == nil && al.sessions != nil {
		count = al.sessions[al.pending.ID]
	}
	return al.wc.ShrinkSentence(visible(al.pending.ID), count) + ". Set it anyway? [y/N]"
}

// noteAliasesBusy says why a key that needs the table did nothing: a read or a
// write is out.
func (m *model) noteAliasesBusy() {
	al := &m.aliases
	switch {
	case al.writing || strings.HasSuffix(al.status, aliasesReading):
	case al.status == "":
		al.status = aliasesReading
	default:
		al.status += "\n" + aliasesReading
	}
}

// updateAliases handles a message while the Aliases tab is showing.
func (m *model) updateAliases(msg tea.Msg) (tea.Model, tea.Cmd) {
	al := &m.aliases
	switch al.phase {
	case aliasesForm:
		return m.updateAliasForm(msg)
	case aliasesConfirm:
		return m.updateAliasConfirm(msg)
	case aliasesRemove:
		return m.updateAliasRemove(msg)
	}
	key, isKey := msg.(tea.KeyMsg)
	if !isKey {
		return m, nil
	}
	if !al.loaded {
		switch key.String() {
		case "q", "ctrl+c":
			return m.quit()
		case "tab":
			return m, m.switchTab()
		}
		return m, nil
	}
	if !al.busy {
		al.status = ""
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "tab":
		return m, m.switchTab()
	case "r":
		if al.busy {
			m.noteAliasesBusy()
			return m, nil
		}
		al.status = aliasesReading
		return m, m.loadAliasesCmd()
	case "n":
		if al.busy {
			m.noteAliasesBusy()
			return m, nil
		}
		m.openAliasForm(nil)
		return m, nil
	case "enter":
		if al.busy {
			m.noteAliasesBusy()
			return m, nil
		}
		if v, ok := m.selectedAlias(); ok {
			if v.RedirectTo != "" {
				// The writer's own refusal, before a form that could not save.
				al.status = modeladmin.CheckAlias(al.cfg, modeladmin.AliasRequest{ID: v.ID, Targets: v.Targets}).Error()
				return m, nil
			}
			m.openAliasForm(&v)
		}
		return m, nil
	case "d":
		v, ok := m.selectedAlias()
		switch {
		case al.busy:
			m.noteAliasesBusy()
		case !ok:
		case al.sessErr != nil:
			al.status = "not removed: the running model server's client sessions could not be read, so " + v.ID + " may be in use; `wt-model alias rm --force-in-use` overrides"
		case al.sessions[v.ID] > 0:
			al.status = "not removed: " + v.ID + " is used by " + strconv.Itoa(al.sessions[v.ID]) + " client session(s); `wt-model alias rm --force-in-use` overrides"
		default:
			al.phase, al.remove = aliasesRemove, v.ID
		}
		return m, nil
	}
	var cmd tea.Cmd
	al.list, cmd = al.list.Update(msg)
	return m, cmd
}

// updateAliasRemove handles the remove prompt: y removes, anything else goes
// back.
func (m *model) updateAliasRemove(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	al := &m.aliases
	switch key.String() {
	case "y", "Y":
		id := al.remove
		al.phase, al.busy, al.writing, al.status = aliasesList, true, true, "removing "+id+"..."
		return m, func() tea.Msg {
			// A registry write takes a file lock, so it is not for Update.
			_, err := modeladmin.RemoveAliases([]string{id})
			return aliasRemovedMsg{id: id, err: err}
		}
	case "ctrl+c":
		return m.quit()
	}
	al.phase = aliasesList
	return m, nil
}

// updateAliasConfirm handles the shrink question over a save: only y writes;
// any other key goes back to the form.
func (m *model) updateAliasConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	al := &m.aliases
	switch key.String() {
	case "y", "Y":
		al.phase = aliasesForm
		return m, m.startAliasWrite(al.pending)
	case "ctrl+c":
		return m.quit()
	}
	al.phase = aliasesForm
	return m, nil
}

// startAliasWrite writes req in a command: SetAlias checks it again under the
// registry lock. The form stays open meanwhile, so a refusal is read on it.
func (m *model) startAliasWrite(req modeladmin.AliasRequest) tea.Cmd {
	al := &m.aliases
	al.form.saving, al.form.err = true, ""
	al.busy, al.writing = true, true
	return func() tea.Msg {
		changed, err := modeladmin.SetAlias(req)
		return aliasSavedMsg{id: req.ID, changed: changed, err: err}
	}
}

// applyAliasSaved takes a save's outcome and re-reads. A refusal leaves the
// form open with the reason; a quit that was waiting for the write happens on
// success.
func (m *model) applyAliasSaved(msg aliasSavedMsg) tea.Cmd {
	al := &m.aliases
	al.busy, al.writing = false, false
	if msg.err != nil {
		if al.form != nil {
			al.form.saving, al.form.err = false, "not saved: "+msg.err.Error()
		}
		m.refuseHeldQuit()
		// The usual reason is another program changing the registry since the
		// read: read it again, so the next save is judged on what is there.
		return m.loadAliasesCmd()
	}
	al.phase, al.form, al.selectID = aliasesList, nil, msg.id
	if msg.changed {
		m.registryChanged = true
		al.status = "set " + msg.id
		// The Models tab's rows are stale now (a model's alias guard): its
		// next visit reads again.
		m.models.asked = false
	} else {
		al.status = "no change: " + msg.id
	}
	if m.quitPending {
		m.quitPending = false
		_, cmd := m.quit()
		return cmd
	}
	return m.loadAliasesCmd()
}

// applyAliasRemoved takes a removal's outcome and re-reads, a refusal too: the
// usual reason is that another program took the alias first, and the table
// would go on showing it.
func (m *model) applyAliasRemoved(msg aliasRemovedMsg) tea.Cmd {
	al := &m.aliases
	al.busy, al.writing = false, false
	if msg.err != nil {
		al.status = "not removed: " + msg.err.Error()
		m.refuseHeldQuit()
		return m.loadAliasesCmd()
	}
	m.registryChanged = true
	m.models.asked = false
	al.status = "removed " + msg.id
	if m.quitPending {
		m.quitPending = false
		_, cmd := m.quit()
		return cmd
	}
	return m.loadAliasesCmd()
}
