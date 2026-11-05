package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The `wt cleanup` tick list (layout A of the design): the report's header,
// its rows in cleanup.Groups' four groups with a checkbox column, and a
// detail line under the list that explains the highlighted row. The words —
// headings, the evidence sentence, the header lines, which rows are tickable
// — are all internal/cleanup/present.go's; this file only lays them out and
// styles them.
//
// It is VIEW ONLY: enter prints what would be removed, after the alt screen
// is gone, and exits. Nothing here runs git or removes anything.

// cleanupItem is one line of the list: a group heading or a row.
type cleanupItem struct {
	heading string // set on a heading line; the rest is then unset
	row     cleanup.Row
	text    string // cleanup.RowText for the row
	locked  bool   // can never be ticked: drawn [-], space does nothing
}

func (cleanupItem) FilterValue() string { return "" }

// cleanupLocked reports whether a row can never be ticked: an in-use row, a
// row with any hard blocker, and a remote row that is not proven (only a
// merged PR puts an origin branch up for removal). Every other row may be
// ticked by the user; only Tickable rows are ticked for them.
func cleanupLocked(r cleanup.Row) bool {
	return r.Status == cleanup.StatusInUse || len(r.Blockers) > 0 ||
		(r.Kind == cleanup.KindRemote && r.Status != cleanup.StatusProven)
}

// Row geometry: "> " cursor, "[x] " box, the kind padded to 8, then the name
// and the evidence, two spaces apart.
const (
	cleanupKindW    = 8
	cleanupFixedW   = 2 + 4 + cleanupKindW + 2 + 2
	cleanupMinNameW = 16
)

// cleanupDelegate draws one item per line. ticked is the model's tick map
// (shared, so a tick shows on the next render).
type cleanupDelegate struct {
	theme   themes.Theme
	ticked  map[int]bool
	maxName int // the longest display name
	maxText int // the longest evidence sentence
}

func (d cleanupDelegate) Height() int                         { return 1 }
func (d cleanupDelegate) Spacing() int                        { return 0 }
func (d cleanupDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// nameWidth is the name column's width at a list width: as wide as the
// longest name when every row's evidence fits beside it, otherwise narrower,
// down to cleanupMinNameW. The name gives way, not the evidence. Only a
// sentence too long even beside the narrowest column is cut in its row (with
// an ellipsis); the detail line under the list shows it whole.
func (d cleanupDelegate) nameWidth(width int) int {
	return min(d.maxName, max(cleanupMinNameW, width-cleanupFixedW-d.maxText))
}

func (d cleanupDelegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
	it, ok := li.(cleanupItem)
	if !ok {
		return
	}
	width := m.Width()
	if it.heading != "" {
		style := lipgloss.NewStyle().Foreground(d.theme.Token(themes.TokenHeader)).Bold(true)
		_, _ = io.WriteString(w, style.Render(clip(it.heading, width)))
		return
	}
	selected := index == m.Index()
	cursor := "  "
	if selected {
		cursor = "> "
	}
	_, _ = io.WriteString(w, d.style(it, selected).Render(cleanupLine(cursor, d.box(index, it), it, d.nameWidth(width), width)))
}

func (d cleanupDelegate) box(index int, it cleanupItem) string {
	switch {
	case it.locked:
		return "[-]"
	case d.ticked[index]:
		return "[x]"
	}
	return "[ ]"
}

func (d cleanupDelegate) style(it cleanupItem, selected bool) lipgloss.Style {
	switch {
	case selected:
		return lipgloss.NewStyle().Foreground(d.theme.Token(themes.TokenAccent)).Bold(true)
	case it.locked:
		return lipgloss.NewStyle().Foreground(d.theme.Token(themes.TokenDim))
	}
	return lipgloss.NewStyle().Foreground(d.theme.Token(themes.TokenUnselected))
}

// cleanupLine is a row's text: cursor, box, kind, the name cut to nameW, and
// the evidence with the age after it when it fits. Only an evidence sentence
// longer than the room left at the narrowest name column is cut here (with
// an ellipsis); the detail line under the list always shows it whole.
func cleanupLine(cursor, box string, it cleanupItem, nameW, width int) string {
	name := cleanup.DisplayName(it.row)
	if lipgloss.Width(name) > nameW {
		name = cutLines(name, nameW)
	}
	prefix := cursor + box + " " + tuilayout.PadRunes(string(it.row.Kind), cleanupKindW) + "  " + padCols(name, nameW) + "  "
	text := it.text
	if age := cleanup.AgeText(it.row); age != "" {
		if with := text + "  " + age; width <= 0 || lipgloss.Width(prefix+with) <= width {
			text = with
		}
	}
	if avail := width - lipgloss.Width(prefix); width > 0 && lipgloss.Width(text) > avail {
		text = cutLines(text, max(1, avail))
	}
	return strings.TrimRight(prefix+text, " ")
}

// padCols pads s with spaces to w display columns.
func padCols(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// cleanupListModel is the tick list's Bubble Tea model.
type cleanupListModel struct {
	report        cleanup.Report
	theme         themes.Theme
	now           time.Time
	list          list.Model
	ticked        map[int]bool // by list index
	rows          int          // number of row items (headings excluded)
	width, height int
	done          bool
	message       string // printed after the program ends; "" for a plain quit
}

// newCleanupList builds the list for r: every group's heading and rows in
// cleanup.Groups' order, Tickable rows ticked, the cursor on the first row.
func newCleanupList(r cleanup.Report, th themes.Theme, now time.Time) cleanupListModel {
	ticked := map[int]bool{}
	d := cleanupDelegate{theme: th, ticked: ticked}
	var items []list.Item
	rows := 0
	for _, g := range cleanup.Groups(r) {
		items = append(items, cleanupItem{heading: g.Heading})
		for _, row := range g.Rows {
			it := cleanupItem{row: row, text: cleanup.RowText(r, row), locked: cleanupLocked(row)}
			if row.Tickable() && !it.locked {
				ticked[len(items)] = true
			}
			d.maxName = max(d.maxName, lipgloss.Width(cleanup.DisplayName(row)))
			d.maxText = max(d.maxText, lipgloss.Width(it.text))
			items = append(items, it)
			rows++
		}
	}
	l := list.New(items, d, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()
	l.KeyMap.ShowFullHelp.SetEnabled(false)
	l.KeyMap.CloseFullHelp.SetEnabled(false)
	m := cleanupListModel{report: r, theme: th, now: now, list: l, ticked: ticked, rows: rows}
	m.skipHeading(1)
	return m
}

// RunCleanupList shows r as the tick list on the alt screen. enter is view
// only: once the screen is gone it prints what would have been removed.
// Nothing is removed and no git command is run.
func RunCleanupList(r cleanup.Report, th themes.Theme) error {
	final, err := tea.NewProgram(newCleanupList(r, th, time.Now()), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(cleanupListModel); ok && fm.message != "" {
		_, err = fmt.Fprintln(os.Stdout, fm.message)
	}
	return err
}

func (m cleanupListModel) Init() tea.Cmd { return nil }

func (m cleanupListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.done {
			return m, nil
		}
		if m.rows == 0 {
			// "nothing to clean up": any key leaves.
			m.done = true
			return m, tea.Quit
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.done = true
			return m, tea.Quit
		case "enter":
			m.done = true
			m.message = m.viewOnlyMessage()
			return m, tea.Quit
		case " ":
			i := m.list.Index()
			if it, ok := m.list.SelectedItem().(cleanupItem); ok && it.heading == "" && !it.locked {
				m.ticked[i] = !m.ticked[i]
			}
		case "a":
			for i, li := range m.list.Items() {
				if it := li.(cleanupItem); it.heading == "" && !it.locked && it.row.Tickable() {
					m.ticked[i] = true
				}
			}
		default:
			before := m.list.Index()
			m.list, cmd = m.list.Update(msg)
			dir := 1
			if m.list.Index() < before {
				dir = -1
			}
			m.skipHeading(dir)
		}
	}
	m.fit()
	return m, cmd
}

// skipHeading moves the cursor off a heading: on in direction dir, else back
// the other way.
func (m *cleanupListModel) skipHeading(dir int) {
	items := m.list.Items()
	isRow := func(i int) bool { return items[i].(cleanupItem).heading == "" }
	i := m.list.Index()
	if i < 0 || i >= len(items) || isRow(i) {
		return
	}
	for _, d := range []int{dir, -dir} {
		for j := i + d; j >= 0 && j < len(items); j += d {
			if isRow(j) {
				m.list.Select(j)
				return
			}
		}
	}
}

// fit sizes the list to the room its frames leave (tuilayout.FitTo). It runs
// after every message: the detail line's height follows the highlighted row.
func (m *cleanupListModel) fit() {
	if m.width <= 0 || m.height <= 0 || m.rows == 0 {
		return
	}
	fitTo(&m.list, m.width, m.height, m.frames()...)
}

// frames are the screen's layouts, fullest first. A short terminal loses
// the report header first, then the title with the key hints; the detail
// line is never given up, since it is the only place a long evidence
// sentence is shown whole.
func (m *cleanupListModel) frames() []listFrame {
	build := func(withTitle, withHeader bool) listFrame {
		return func(listView string) string {
			var b strings.Builder
			if withTitle {
				b.WriteString(clip(m.titleLine(), m.width) + "\n")
			}
			if withHeader {
				b.WriteString(m.headerBlock() + "\n\n")
			}
			b.WriteString(listView)
			if d := m.detail(); d != "" {
				b.WriteString("\n" + d)
			}
			return b.String()
		}
	}
	return []listFrame{build(true, true), build(true, false), build(false, false)}
}

func (m *cleanupListModel) titleLine() string {
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return accent.Render("wt cleanup") + dim.Render("   space tick · a all proven · enter show ticked (view only) · q quit")
}

// headerBlock is cleanup.HeaderLines, wrapped: the fetch failure ends in
// what it means for the list.
func (m *cleanupListModel) headerBlock() string {
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	text := tuilayout.WrapText(strings.Join(cleanup.HeaderLines(m.report, m.now), "\n"), m.width)
	return clip(dim.Render(text), m.width)
}

// detail explains the highlighted row: a rule with its name, then its whole
// evidence sentence (cleanup.RowText) and age, wrapped, never cut. A row
// that can never be ticked says so first.
func (m *cleanupListModel) detail() string {
	it, ok := m.list.SelectedItem().(cleanupItem)
	if !ok || it.heading != "" {
		return ""
	}
	rule := "── " + cleanup.DisplayName(it.row) + " "
	if m.width > 0 {
		rule = cutLines(rule, m.width)
		rule += strings.Repeat("─", max(0, m.width-lipgloss.Width(rule)))
	}
	text := it.text
	if age := cleanup.AgeText(it.row); age != "" {
		text += " [" + age + "]"
	}
	if it.locked {
		text = "cannot be ticked: " + text
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return dim.Render(rule) + "\n" + tuilayout.WrapText(text, m.width)
}

func (m cleanupListModel) View() string {
	if m.done {
		return ""
	}
	if m.rows == 0 {
		body := m.headerBlock() + "\n\nnothing to clean up: no other worktree or branch\n\npress any key to quit"
		return clip(body, m.width)
	}
	return drawnFrame(&m.list, m.height, m.frames()...)(m.list.View())
}

// selectedRow is the highlighted row; false on a heading or an empty list.
func (m cleanupListModel) selectedRow() (cleanup.Row, bool) {
	it, ok := m.list.SelectedItem().(cleanupItem)
	if !ok || it.heading != "" {
		return cleanup.Row{}, false
	}
	return it.row, true
}

// tickedItems are the ticked rows in list order.
func (m cleanupListModel) tickedItems() []cleanupItem {
	var out []cleanupItem
	for i, li := range m.list.Items() {
		if it := li.(cleanupItem); it.heading == "" && m.ticked[i] {
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

// viewOnlyMessage is enter's sentence in PR A: what would be removed.
func (m cleanupListModel) viewOnlyMessage() string {
	items := m.tickedItems()
	if len(items) == 0 {
		return "view only — nothing is removed; nothing ticked"
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = string(it.row.Kind) + " " + cleanup.DisplayName(it.row)
	}
	noun := "items"
	if len(items) == 1 {
		noun = "item"
	}
	return fmt.Sprintf("view only — nothing is removed; would remove %d %s: %s", len(items), noun, strings.Join(names, ", "))
}

// exitMessage is what RunCleanupList prints after the program ends.
func (m cleanupListModel) exitMessage() string { return m.message }
