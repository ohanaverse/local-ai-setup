package tui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The row list (layout A of the cleanup design, unified by the user-driven
// cleanup design's "The list"): the report's header, then every row grouped
// by kind (cleanup.KindGroups: WORKTREES, LOCAL BRANCHES, REMOTE BRANCHES),
// each row carrying its verdict (proven / less sure / unique work / in use),
// its cleanup.RowTags (a TagWarn tag in the warning colour) and its
// sentence, and a detail line under the list that explains the highlighted
// row. The words — headings, tags, the evidence sentence, the header lines,
// which rows are tickable — are all internal/cleanup/present.go's; this file
// only lays them out and styles them.
//
// One component, two modes (rowListOpts.Mode):
//   - manage (`wt cleanup`): a checkbox column; space ticks, `a` ticks every
//     proven row, enter removes the ticked rows, `r` hands a row to rescue.
//   - launch (the picker): a first item "+ New worktree…" (enter or `n`
//     emits newWorktreeChosenMsg), enter on any row emits rowChosenMsg —
//     the root, an in-use or held row too: launching warns, it never blocks
//     — and nothing is ticked. Rows carry a "current" and a "default" tag,
//     `/` filters the rows by name (row_list_launch.go), and the PR tags
//     arrive later (withReport).
//
// In both modes `x` removes the ticked rows, or the highlighted row when
// nothing is ticked (always, in launch mode), through the same confirm flow.
//
// A row's text is never cut: when it does not fit beside the name it wraps
// onto continuation lines under the text column, so a row is as many lines
// as its text needs. That is why this screen is a small list of its own
// rather than a bubbles list, whose delegate gives every item the same
// height. The name column is what gives way to the width.
//
// Removal goes through a confirm screen and the removal engine
// (cleanup_confirm.go); this file runs no git and removes nothing.

// listMode is which entry point the list serves.
type listMode int

const (
	modeManage listMode = iota // `wt cleanup`: tick and remove
	modeLaunch                 // the picker: choose a row to launch in
)

// rowListOpts configures newRowList.
type rowListOpts struct {
	Mode  listMode
	Theme themes.Theme
	Now   time.Time // the header's clock
}

// rowChosenMsg is launch mode's enter on a row: launch there.
type rowChosenMsg struct{ Row cleanup.Row }

// newWorktreeChosenMsg is launch mode's enter on "+ New worktree…", or `n`.
type newWorktreeChosenMsg struct{}

// newWorktreeLabel is launch mode's first item.
const newWorktreeLabel = "+ New worktree…"

// cleanupItem is one entry of the list: a group heading, a row, or launch
// mode's new-worktree item.
type cleanupItem struct {
	heading     string // set on a heading; the rest is then unset
	newWorktree bool   // launch mode's "+ New worktree…"; the rest is unset
	row         cleanup.Row
	text        string        // cleanup.RowText for the row
	tags        []cleanup.Tag // the tags drawn for the row (rowTags)
	locked      bool          // can never be ticked or removed: drawn [-], space does nothing
}

// cleanupLocked reports whether a row can never be ticked: an in-use row, a
// row with any hard blocker, and a remote row unless it is a proven origin
// branch (the engine removes nothing else on a remote: only origin, only
// proven). Every other row may be ticked by the user; only Tickable local
// rows are ticked for them (by default and by `a`): an origin row is never
// ticked for the user, who must space each one, because deleting it is
// visible to everyone with access to the repository. A ticked row that is not
// proven, or loses anything, goes through the typed confirmation
// (cleanup_confirm.go) before anything is removed; an origin row through its
// own phrase.
func cleanupLocked(r cleanup.Row) bool {
	if r.Kind == cleanup.KindRemote && (!r.Tickable() || r.Remote != "origin") {
		return true
	}
	if r.Status == cleanup.StatusInUse || r.HardStop != "" {
		return true
	}
	// Only a hard stop locks: held and default_branch are floors the user
	// may tick past (the engine then asks --accept-loss's typed confirmation).
	for _, b := range r.Blockers {
		if cleanup.IsHardStop(b.Code, r.Kind) {
			return true
		}
	}
	return false
}

// rowTags are the tags the list draws for a row: cleanup.RowTags without
// "you are here", which RowTags gives every worktree holding the caller's
// directory; the row's sentence (cleanup.RowText) already says it, on the
// innermost one only.
func rowTags(r cleanup.Row) []cleanup.Tag {
	var out []cleanup.Tag
	for _, t := range cleanup.RowTags(r) {
		if t.Text != "you are here" {
			out = append(out, t)
		}
	}
	return out
}

// verdictText is the row's status in words: what the status headings said
// before the list was grouped by kind.
func verdictText(s cleanup.Status) string {
	return strings.ReplaceAll(string(s), "_", " ")
}

// unit is one unbreakable piece of a row's text: the verdict, a tag, a word
// of the sentence or of the age. sep goes before it when it follows another
// unit on the same line.
type unit struct {
	sep, text string
	warn      bool // drawn in the warning style (a TagWarn tag)
}

// rowUnits is a row's text as units: verdict · tags · sentence  age.
func rowUnits(it cleanupItem) []unit {
	out := []unit{{text: verdictText(it.row.Status)}}
	for _, t := range it.tags {
		out = append(out, unit{sep: " · ", text: t.Text, warn: t.Severity == cleanup.TagWarn})
	}
	for k, w := range strings.Fields(it.text) {
		sep := " "
		if k == 0 {
			sep = " · "
		}
		out = append(out, unit{sep: sep, text: w})
	}
	for k, w := range strings.Fields(cleanup.AgeText(it.row)) {
		sep := " "
		if k == 0 {
			sep = "  "
		}
		out = append(out, unit{sep: sep, text: w})
	}
	return out
}

// unitsText is units on one line.
func unitsText(us []unit) string {
	var b strings.Builder
	for k, u := range us {
		if k > 0 {
			b.WriteString(u.sep)
		}
		b.WriteString(u.text)
	}
	return b.String()
}

// flowUnits lays units out on lines of at most width runes, breaking only
// between units (tuilayout.Flow's rule, with each unit's own separator); a
// unit longer than a whole line is cut at the line's end. width <= 0 is one
// line.
func flowUnits(us []unit, width int) [][]unit {
	if width <= 0 {
		return [][]unit{us}
	}
	var lines [][]unit
	var cur []unit
	curW := 0
	for _, u := range us {
		w := utf8.RuneCountInString(u.text)
		if len(cur) > 0 {
			if curW+utf8.RuneCountInString(u.sep)+w <= width {
				cur, curW = append(cur, u), curW+utf8.RuneCountInString(u.sep)+w
				continue
			}
			lines, cur, curW = append(lines, cur), nil, 0
		}
		runes := []rune(u.text)
		for len(runes) > width {
			lines = append(lines, []unit{{text: string(runes[:width]), warn: u.warn}})
			runes = runes[width:]
		}
		u.text = string(runes)
		cur, curW = []unit{u}, len(runes)
	}
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	return lines
}

// Row geometry: "> " cursor, "[x] " box (manage mode only), the kind padded
// to 8, then the name and the text, two spaces apart. Names stay whole while
// that leaves the text cleanupRoomyTextW columns (the text wraps instead: a
// wrapped text loses nothing, a cut name hides which row it is). Otherwise
// the name column narrows to cleanupMinNameW so texts fit on one line, and
// below that (a terminal too narrow to give the text cleanupMinTextW columns
// beside it) to cleanupFloorNameW.
const (
	cleanupKindW      = 8
	cleanupBoxW       = 4
	cleanupFixedW     = 2 + cleanupBoxW + cleanupKindW + 2 + 2
	cleanupMinNameW   = 16
	cleanupMinTextW   = 24
	cleanupRoomyTextW = 40
	cleanupFloorNameW = 4
)

// cleanupListModel is the row list's Bubble Tea model.
type cleanupListModel struct {
	opts          rowListOpts
	report        cleanup.Report
	theme         themes.Theme
	now           time.Time
	items         []cleanupItem
	ticked        map[int]bool // by item index
	cursor        int          // item index of the highlighted item; -1 with none
	offset        int          // the first list line on screen
	rows          int          // number of row items (headings and the new-worktree item excluded)
	maxName       int          // the longest display name
	maxText       int          // the longest row text (verdict, tags, sentence, age)
	width, height int
	done          bool

	// The removal flow (cleanup_confirm.go).
	rm      RemoveFuncs
	phase   cleanupPhase
	status  string          // one line under the list ("nothing ticked", "cancelled...")
	pending int             // rows being previewed
	conf    *cleanupConfirm // the confirm screen; nil outside it
	run     *cleanupRun     // the last run; set from the progress screen on
	log     []removal.Outcome
	exitErr error // returned by RunCleanupList (the re-read failed)
	// quitAfterRun: a signal came during a run; the program quits when the
	// run ends (Ruling T).
	quitAfterRun bool
	// rescueRow is the row `r` chose: the list quits and RunCleanupList
	// hands it to RemoveFuncs.Rescue once the alt screen is gone.
	rescueRow *cleanup.Row
	clock     func() time.Time // the list's clock on a rebuild

	// Launch mode's type-to-filter (row_list_launch.go): filter is the
	// query, filtering is true while it is being typed (every printable key
	// goes into it then).
	filter    string
	filtering bool
}

// newCleanupList is the manage-mode list `wt cleanup` shows.
func newCleanupList(r cleanup.Report, th themes.Theme, now time.Time) cleanupListModel {
	return newRowList(r, rowListOpts{Mode: modeManage, Theme: th, Now: now})
}

// newRowList builds the list for r in o.Mode: launch mode's new-worktree
// item first, then every kind group's heading and rows in
// cleanup.KindGroups' order. In manage mode Tickable local rows start
// ticked; in launch mode nothing is. The cursor starts on the first item.
func newRowList(r cleanup.Report, o rowListOpts) cleanupListModel {
	m := cleanupListModel{opts: o, report: r, theme: o.Theme, now: o.Now, ticked: map[int]bool{}, cursor: -1, clock: time.Now}
	here := deepestCurrent(r)
	if o.Mode == modeLaunch {
		m.items = append(m.items, cleanupItem{newWorktree: true})
		m.cursor = 0
		m.maxName = lipgloss.Width(newWorktreeLabel)
	}
	for _, g := range cleanup.KindGroups(r) {
		m.items = append(m.items, cleanupItem{heading: g.Heading})
		for _, row := range g.Rows {
			it := cleanupItem{row: row, text: cleanup.RowText(r, row), tags: rowTags(row), locked: cleanupLocked(row)}
			if o.Mode == modeLaunch {
				it.tags = append(launchTags(r, row, here), it.tags...)
			}
			if o.Mode == modeManage && row.Tickable() && !it.locked && row.Kind != cleanup.KindRemote {
				m.ticked[len(m.items)] = true
			}
			if m.cursor < 0 {
				m.cursor = len(m.items)
			}
			m.maxName = max(m.maxName, lipgloss.Width(cleanup.DisplayName(row)))
			m.maxText = max(m.maxText, lipgloss.Width(unitsText(rowUnits(it))))
			m.items = append(m.items, it)
			m.rows++
		}
	}
	return m
}

// launch reports whether the list is in launch mode.
func (m *cleanupListModel) launch() bool { return m.opts.Mode == modeLaunch }

// empty reports whether the list has nothing to highlight: a manage list
// with no rows (a launch list always has its new-worktree item).
func (m *cleanupListModel) empty() bool { return m.cursor < 0 }

// RunCleanupList shows r as the tick list on the alt screen. enter removes
// the ticked rows through rm after a confirmation; once the screen is gone,
// every outcome of the session is printed (what was removed and each
// deleted branch's commit outlive the alt screen).
func RunCleanupList(r cleanup.Report, th themes.Theme, rm RemoveFuncs) error {
	m := newCleanupList(r, th, time.Now())
	m.rm = rm
	final, stop, err := runCleanupProgram(m)
	defer stop()
	fm, ok := final.(cleanupListModel)
	if ok && fm.exitMessage() != "" {
		if _, werr := fmt.Fprintln(os.Stdout, fm.exitMessage()); werr != nil && err == nil {
			err = werr
		}
	}
	if err == nil && ok {
		err = fm.exitErr
	}
	// The rescue asks and prints on the real terminal, so it runs here, after
	// the alt screen has closed (and after the session's removal log).
	if err == nil && ok {
		err = runRescue(fm, rm, stop)
	}
	return err
}

// runCleanupProgram is the test seam over the Bubble Tea program: it runs the
// list on the alt screen and returns the final model and the function that
// stops the signal forwarding (idempotent). A test swaps it to drive
// RunCleanupList's after-the-screen steps without a terminal.
var runCleanupProgram = realCleanupProgram

func realCleanupProgram(m cleanupListModel) (tea.Model, func(), error) {
	// Bubble Tea's own signal handler quits on SIGINT/SIGTERM in any phase,
	// mid-removal too; the model handles them instead (Ruling T). The
	// forwarding stops only after the exit message is printed.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithoutSignalHandler())
	return guardedRun(forwardSignals(p.Send), p.Run)
}

// guardedRun runs the program and returns its result with stop, which the
// caller calls once it is done with the forwarding. A panic in run releases
// the forwarding itself before it propagates: the caller never got stop.
func guardedRun(stop func(), run func() (tea.Model, error)) (tea.Model, func(), error) {
	returned := false
	defer func() {
		if !returned {
			stop()
		}
	}()
	final, err := run()
	returned = true
	return final, stop, err
}

// runRescue hands the `r` key's row to rm.Rescue, if there is one. It stops
// the signal forwarding first: the program has finished, so a forwarded
// signal would go to a dead p.Send and be dropped, leaving Ctrl-C at the
// rescue's y/N prompt (or during a long copy) with no effect. With the
// forwarding stopped Go's default handling ends the process again.
func runRescue(fm cleanupListModel, rm RemoveFuncs, stop func()) error {
	if fm.rescueRow == nil || rm.Rescue == nil {
		return nil
	}
	stop()
	return rm.Rescue(*fm.rescueRow)
}

// startRescue handles `r`: on a row a rescue is offered for, quit and
// remember the row; otherwise stay and say why on the status line.
func (m cleanupListModel) startRescue() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	if offered, why := rescue.Offered(row); !offered {
		m.status = "rescue: " + why
		return m, nil
	}
	if m.rm.Rescue == nil {
		m.status = "rescue is not available here"
		return m, nil
	}
	m.rescueRow = &row
	m.done = true
	return m, tea.Quit
}

func (m cleanupListModel) Init() tea.Cmd { return nil }

func (m cleanupListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case cleanupPreviewMsg:
		return m.gotPreviews(msg)
	case cleanupProgressMsg:
		return m.gotProgress(msg)
	case cleanupRunDoneMsg:
		return m.gotRunDone(msg)
	case cleanupRefreshMsg:
		return m.gotRefresh(msg)
	case cleanupSignalMsg:
		return m.signalled()
	case tea.KeyMsg:
		if m.done {
			return m, nil
		}
		switch m.phase {
		case cleanupPhaseConfirm:
			return m.confirmKey(msg)
		case cleanupPhaseResults:
			return m.resultsKey(msg)
		case cleanupPhaseRunning:
			// A removal in flight is never abandoned: keys wait.
			return m, nil
		case cleanupPhasePreviewing, cleanupPhaseRefreshing:
			// Nothing is being removed: ctrl+c may leave.
			if msg.String() == "ctrl+c" {
				m.done = true
				return m, tea.Quit
			}
			return m, nil
		}
		m.status = ""
		if m.launch() {
			if next, cmd, ok := m.filterKey(msg); ok {
				return next, cmd
			}
		}
		if m.empty() {
			// "nothing to clean up": any key leaves.
			m.done = true
			return m, tea.Quit
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.done = true
			return m, tea.Quit
		case "enter":
			if m.launch() {
				return m, m.choose()
			}
			return m.startRemoval()
		case "x":
			return m.removeKey()
		case "up", "k":
			m.move(-1, 1)
		case "down", "j":
			m.move(1, 1)
		case "pgup", "b":
			m.move(-1, max(1, m.layout().listH/2))
		case "pgdown", "f":
			m.move(1, max(1, m.layout().listH/2))
		case "home", "g":
			m.move(-1, len(m.items))
		case "end", "G":
			m.move(1, len(m.items))
		default:
			if m.launch() {
				if msg.String() == "n" {
					return m, emit(newWorktreeChosenMsg{})
				}
				break
			}
			switch msg.String() {
			case "r":
				return m.startRescue()
			case " ":
				if it := m.items[m.cursor]; !it.locked {
					m.ticked[m.cursor] = !m.ticked[m.cursor]
				}
			case "a":
				for i, it := range m.items {
					if it.heading == "" && !it.locked && it.row.Tickable() && it.row.Kind != cleanup.KindRemote {
						m.ticked[i] = true
					}
				}
			}
		}
	}
	if !m.empty() {
		m.offset = m.layout().offset
	}
	return m, nil
}

// emit is a command that delivers msg.
func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// choose is launch mode's enter: the new-worktree item or the highlighted
// row, whatever its verdict (launching warns, never blocks).
func (m cleanupListModel) choose() tea.Cmd {
	if m.hidden(m.cursor) || m.items[m.cursor].heading != "" {
		return nil
	}
	if m.items[m.cursor].newWorktree {
		return emit(newWorktreeChosenMsg{})
	}
	return emit(rowChosenMsg{Row: m.items[m.cursor].row})
}

// removeKey is `x`: the ticked rows, or the highlighted row when nothing is
// ticked (always in launch mode, which ticks nothing), go to the removal
// flow; a locked row or the new-worktree item says why not.
func (m cleanupListModel) removeKey() (cleanupListModel, tea.Cmd) {
	rows := m.tickedRows()
	if len(rows) == 0 {
		if m.hidden(m.cursor) {
			return m, nil
		}
		it := m.items[m.cursor]
		switch {
		case it.newWorktree:
			m.status = "x removes the highlighted worktree or branch; this item creates one"
			return m, nil
		case it.locked:
			m.status = "cannot be removed: " + cleanup.OneLine(it.text)
			return m, nil
		}
		rows = []cleanup.Row{it.row}
	}
	return m.startRemovalOf(rows)
}

// move moves the cursor n items in direction dir, skipping headings and
// stopping at either end.
func (m *cleanupListModel) move(dir, n int) {
	for ; n > 0; n-- {
		next := -1
		for j := m.cursor + dir; j >= 0 && j < len(m.items); j += dir {
			if m.items[j].heading == "" && !m.hidden(j) {
				next = j
				break
			}
		}
		if next < 0 {
			return
		}
		m.cursor = next
	}
}

// fixedW is the row's fixed columns: cursor, box (manage mode), kind, gaps.
func (m *cleanupListModel) fixedW() int {
	if m.launch() {
		return cleanupFixedW - cleanupBoxW
	}
	return cleanupFixedW
}

// nameWidth is the name column's width: as wide as the longest name when
// that leaves the text cleanupRoomyTextW columns or every row's text fits
// beside it on one line, otherwise narrower, down to
// cleanupMinNameW (texts longer than that leaves wrap); on a terminal too
// narrow to leave a text cleanupMinTextW columns, narrower still.
func (m *cleanupListModel) nameWidth() int {
	if m.width <= 0 {
		return m.maxName
	}
	if m.width-m.fixedW()-m.maxName >= cleanupRoomyTextW {
		return m.maxName
	}
	nw := max(cleanupMinNameW, m.width-m.fixedW()-m.maxText)
	if m.width-m.fixedW()-nw < cleanupMinTextW {
		nw = max(cleanupFloorNameW, m.width-m.fixedW()-cleanupMinTextW)
	}
	return min(m.maxName, nw)
}

// itemLines renders item i: a heading or the new-worktree item is one line;
// a row is its first line (cursor, box, kind, the name cut to the name
// column, the text's first part) and the rest of its text wrapped under the
// text column, a warn tag in the warning style.
func (m *cleanupListModel) itemLines(i int) []string {
	if m.hidden(i) {
		return nil
	}
	it := m.items[i]
	if it.heading != "" {
		style := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader)).Bold(true)
		return []string{style.Render(clip(it.heading, m.width))}
	}
	cursor := "  "
	if i == m.cursor {
		cursor = "> "
	}
	style := m.rowStyle(it, i == m.cursor)
	if it.newWorktree {
		return []string{style.Render(clip(cursor+newWorktreeLabel, m.width))}
	}
	nameW := m.nameWidth()
	name := cleanup.DisplayName(it.row)
	if lipgloss.Width(name) > nameW {
		name = cutLines(name, nameW)
	}
	box := ""
	if !m.launch() {
		box = m.box(i) + " "
	}
	prefix := cursor + box + tuilayout.PadRunes(string(it.row.Kind), cleanupKindW) + "  " + padCols(name, nameW) + "  "
	textW := 0 // no width yet: one line
	if m.width > 0 {
		textW = max(1, m.width-lipgloss.Width(prefix))
	}
	warn := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenWarning)).Bold(i == m.cursor)
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	var out []string
	for k, line := range flowUnits(rowUnits(it), textW) {
		lead := indent
		if k == 0 {
			lead = prefix
		}
		plain := strings.TrimRight(lead+unitsText(line), " ")
		if m.width > 0 && lipgloss.Width(plain) > m.width {
			// A terminal narrower than the prefix: clip, one style.
			out = append(out, style.Render(clip(plain, m.width)))
			continue
		}
		var b strings.Builder
		b.WriteString(style.Render(lead))
		for j, u := range line {
			if j > 0 {
				b.WriteString(style.Render(u.sep))
			}
			if u.warn {
				b.WriteString(warn.Render(u.text))
			} else {
				b.WriteString(style.Render(u.text))
			}
		}
		out = append(out, b.String())
	}
	return out
}

func (m *cleanupListModel) box(i int) string {
	switch {
	case m.items[i].locked:
		return "[-]"
	case m.ticked[i]:
		return "[x]"
	}
	return "[ ]"
}

func (m *cleanupListModel) rowStyle(it cleanupItem, selected bool) lipgloss.Style {
	switch {
	case selected:
		return lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	case it.locked:
		return lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	}
	return lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenUnselected))
}

// padCols pads s with spaces to w display columns.
func padCols(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// cleanupLayout is one frame of the screen: what goes above and below the
// list, how many list lines fit between, and the list's lines and offset.
type cleanupLayout struct {
	top, bottom string
	listH       int // list lines shown, the scroll hint not counted
	lines       []string
	offset      int
	hint        bool // the list is taller than listH: a scroll hint line follows it
}

// layout fits the screen to the terminal. The frames, fullest first: title,
// header and detail; title and detail; detail alone; nothing. The first that
// leaves room for the whole highlighted row is used, so a short terminal
// loses the report header first, then the title, and the detail line only
// when even the row alone barely fits. The offset keeps the whole
// highlighted row on screen (its top line when it is taller than the room),
// with its group heading when it is the group's first row and there is room.
// A list taller than its room gives one of its lines to a scroll hint.
func (m *cleanupListModel) layout() cleanupLayout {
	var lines []string
	start := make([]int, len(m.items)+1)
	for i := range m.items {
		start[i] = len(lines)
		lines = append(lines, m.itemLines(i)...)
	}
	start[len(m.items)] = len(lines)
	cs, ce := start[m.cursor], start[m.cursor+1]

	title := clip(m.titleLine(), m.width) + "\n"
	header := m.headerBlock() + "\n\n"
	detail := "\n" + m.statusBlock() + m.detail()
	frames := [][2]string{{title + header, detail}, {title, detail}, {"", detail}, {"", ""}}
	l := cleanupLayout{lines: lines}
	for k, f := range frames {
		l.top, l.bottom = f[0], f[1]
		if m.height <= 0 {
			l.listH = len(lines)
			break
		}
		avail := m.height - (lipgloss.Height(f[0]+"x"+f[1]) - 1)
		l.hint = len(lines) > avail && avail > 1
		l.listH = avail
		if l.hint {
			l.listH--
		}
		if l.listH >= ce-cs || k == len(frames)-1 {
			l.listH = max(1, l.listH)
			break
		}
	}

	off := min(m.offset, max(0, len(lines)-l.listH))
	if ce > off+l.listH {
		off = ce - l.listH
	}
	if cs < off {
		off = cs
	}
	if m.cursor > 0 && m.items[m.cursor-1].heading != "" {
		if hs := start[m.cursor-1]; hs < off && ce-hs <= l.listH {
			off = hs
		}
	}
	// Never open the list on the tail of a wrapped row: start at the next
	// item instead, while that still shows the highlighted row.
	for k := 0; k < len(m.items); k++ {
		if start[k] < off && off < start[k+1] {
			if start[k+1] <= cs {
				off = start[k+1]
			}
			break
		}
	}
	l.offset = max(0, off)
	return l
}

func (m *cleanupListModel) titleLine() string {
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	if m.launch() {
		return accent.Render("wt") + dim.Render("   enter launch · n new worktree · / filter · q quit")
	}
	return accent.Render("wt cleanup") + dim.Render("   space tick · a all proven · enter remove ticked · r rescue · q quit")
}

// statusBlock is the status, wrapped, with its line break; "" without one.
func (m *cleanupListModel) statusBlock() string {
	st := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent))
	out := m.filterLine()
	if m.status == "" {
		return out
	}
	return out + st.Render(clip(tuilayout.WrapText(m.status, m.width), m.width)) + "\n"
}

// headerBlock is cleanup.HeaderLines, wrapped: the fetch failure ends in
// what it means for the list.
func (m *cleanupListModel) headerBlock() string {
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	text := tuilayout.WrapText(strings.Join(cleanup.HeaderLines(m.report, m.now), "\n"), m.width)
	return clip(dim.Render(text), m.width)
}

// detail explains the highlighted row: a rule with its name, then its whole
// sentence and age, wrapped, never cut. A row that can never be ticked says
// so first.
func (m *cleanupListModel) detail() string {
	if m.hidden(m.cursor) {
		return "no row matches the filter"
	}
	it := m.items[m.cursor]
	name := cleanup.DisplayName(it.row)
	if it.newWorktree {
		name = newWorktreeLabel
	}
	rule := "── " + name + " "
	if m.width > 0 {
		rule = cutLines(rule, m.width)
		rule += strings.Repeat("─", max(0, m.width-lipgloss.Width(rule)))
	}
	text := it.text
	if age := cleanup.AgeText(it.row); age != "" {
		text += " [" + age + "]"
	}
	switch {
	case it.newWorktree:
		text = "create a new branch and worktree, then launch in it"
	case m.launch() && it.locked:
		text = "cannot be removed (launching is fine): " + text
	case m.launch():
	case it.locked && it.row.Kind == cleanup.KindRemote && it.row.Remote != "origin":
		text = "cannot be ticked (wt removes only origin branches): " + text
	case it.locked && it.row.Kind == cleanup.KindRemote:
		text = "cannot be ticked (only a proven origin branch can be removed): " + text
	case it.row.Kind == cleanup.KindRemote:
		text = "origin branch: deleting it is visible to everyone with access to the repository and needs the phrase delete origin; space ticks it: " + text
	case it.locked:
		text = "cannot be ticked: " + text
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	out := dim.Render(rule) + "\n" + clip(tuilayout.WrapText(text, m.width), m.width)
	// The sentence counts the worktrees inside this one; their paths are
	// named here only when they all fit on one line (the JSON has them).
	if paths := cleanup.NestedPaths(it.row); len(paths) > 0 {
		if line := "worktrees inside: " + strings.Join(paths, ", "); m.width <= 0 || lipgloss.Width(line) <= m.width {
			out += "\n" + line
		}
	}
	return out
}

func (m cleanupListModel) View() string {
	if m.done {
		return ""
	}
	switch m.phase {
	case cleanupPhasePreviewing:
		return m.waitView(fmt.Sprintf("%s · re-checking %d %s items; nothing is removed yet (ctrl+c leaves)", m.screenName(), m.pending, m.chosenWord()))
	case cleanupPhaseConfirm:
		return m.confirmView()
	case cleanupPhaseRunning:
		return m.runningView()
	case cleanupPhaseResults:
		return m.resultsView()
	case cleanupPhaseRefreshing:
		return m.waitView(m.screenName() + " · re-reading the repository")
	}
	if m.empty() {
		body := m.headerBlock() + "\n\n" + m.statusBlock() + "nothing to clean up: no other worktree or branch\n\npress any key to quit"
		return capLines(clip(body, m.width), m.height)
	}
	l := m.layout()
	end := min(len(l.lines), l.offset+l.listH)
	view := l.top + strings.Join(l.lines[l.offset:end], "\n")
	if l.hint {
		var more []string
		if l.offset > 0 {
			more = append(more, "↑ more above")
		}
		if end < len(l.lines) {
			more = append(more, "↓ more below")
		}
		dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
		view += "\n" + dim.Render(clip("  "+strings.Join(more, " · "), m.width))
	}
	view += l.bottom
	return capLines(view, m.height)
}

// capLines keeps the first height lines of s: the last resort for a
// terminal too short for even the sparest frame. A height that is not
// positive keeps everything.
func capLines(s string, height int) string {
	if height <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	return strings.Join(lines[:height], "\n")
}

// selectedRow is the highlighted row; false on an empty list, on launch
// mode's new-worktree item, on a heading and on a row the filter hides.
func (m cleanupListModel) selectedRow() (cleanup.Row, bool) {
	if m.cursor < 0 || m.hidden(m.cursor) {
		return cleanup.Row{}, false
	}
	if it := m.items[m.cursor]; it.heading == "" && !it.newWorktree {
		return it.row, true
	}
	return cleanup.Row{}, false
}

// tickedItems are the ticked rows in list order.
func (m cleanupListModel) tickedItems() []cleanupItem {
	var out []cleanupItem
	for i, it := range m.items {
		if it.heading == "" && m.ticked[i] {
			out = append(out, it)
		}
	}
	return out
}

// tickedNames are the ticked rows' display names, in list order.
func (m cleanupListModel) tickedNames() []string {
	var out []string
	for _, it := range m.tickedItems() {
		out = append(out, cleanup.DisplayName(it.row))
	}
	return out
}

// exitMessage is what RunCleanupList prints after the program ends: every
// outcome of the session ("" when nothing ran).
func (m cleanupListModel) exitMessage() string {
	text := logText(m.log)
	if m.phase != cleanupPhaseRunning || m.run == nil {
		return text
	}
	// Ended mid-run anyway: say what has no outcome (Ruling T).
	if text == "" {
		text = "wt cleanup: removed 0 items"
	}
	lines := append([]string{text, "the run did not finish:"}, m.run.unfinishedLines()...)
	return strings.Join(lines, "\n")
}
