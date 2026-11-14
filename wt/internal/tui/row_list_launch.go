package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// The row list's launch-mode extras (cleanup_list.go has the list itself):
// the empty tag set launch mode prepends (the "current" and "default" labels
// are gone), type-to-filter, the cursor placement the picker asks for, and
// swapping in a report whose PR lookups have run.

// deepestCurrent is the path of the innermost worktree holding the caller's
// directory, "" when none does. cleanup marks every containing worktree
// Current (the root contains .worktrees/<x>), so the longest path wins.
func deepestCurrent(r cleanup.Report) string {
	here := ""
	for _, row := range r.Rows {
		if row.Kind == cleanup.KindWorktree && row.Current && len(row.Path) > len(here) {
			here = row.Path
		}
	}
	return here
}

// screenName is the title the removal screens carry: `wt worktrees` in manage
// mode, plain `wt` in the picker.
func (m *cleanupListModel) screenName() string {
	if m.launch() {
		return "wt"
	}
	return "wt worktrees"
}

// chosenWord is how the removal screens name what is being removed: "ticked"
// when the ticks chose the rows (enter, in manage mode), "selected" when the
// cursor did (the row keys `x`, `d`, `o`, and everything in the picker, which
// ticks nothing). A row the ticks did not choose is never called "ticked".
func (m *cleanupListModel) chosenWord() string {
	if m.chosenByTicks {
		return "ticked"
	}
	return "selected"
}

// rowKey identifies a row across two reports of the same repository.
type rowKey struct {
	kind                    cleanup.Kind
	ref, path, remote, name string
}

func keyOf(r cleanup.Row) rowKey {
	return rowKey{kind: r.Kind, ref: r.Ref, path: r.Path, remote: r.Remote, name: r.Name}
}

// withReport is the list rebuilt from rep (the same repository, its PR
// lookups now run) with everything the user did or was shown kept: the size,
// the removal funcs, the filter, the status line, the scroll position, any
// removal phase in progress (its choices and quit state kept) and the
// highlighted item (the same row by kind, ref, path and name; the
// new-worktree item stays itself).
func (m cleanupListModel) withReport(rep cleanup.Report) cleanupListModel {
	next := newRowList(rep, m.opts)
	next.rm, next.width, next.height, next.offset = m.rm, m.width, m.height, m.offset
	next.filter, next.filtering = m.filter, m.filtering
	next.phase, next.conf, next.run, next.log, next.pending = m.phase, m.conf, m.run, m.log, m.pending
	next.rowDetail, next.confFromDetail = m.rowDetail, m.confFromDetail
	next.fillGen = m.fillGen // a swap keeps the fill generation (an accepted fill or refetch spent it)
	next.status, next.exitErr, next.quitAfterRun, next.rescueRow, next.done = m.status, m.exitErr, m.quitAfterRun, m.rescueRow, m.done
	next.restoreRoot, next.chosenByTicks, next.seedBranch, next.pendingRM = m.restoreRoot, m.chosenByTicks, m.seedBranch, m.pendingRM
	next.clock, next.now = m.clock, m.now
	if m.opts.Mode == modeManage {
		// The tick list's own choices survive a re-read (the `F` refresh): a
		// row the user unticked stays unticked, never reset to newRowList's
		// default, or enter would remove what they kept, and a row the user
		// ticked that newRowList never ticks for them (a less-sure or an
		// origin row) keeps that tick, or F would silently drop it. A row the
		// old list never showed keeps its default; one that is no longer
		// tickable — newly locked — stays unticked (newRowList already left
		// it so, and startRemoval refuses a locked tick anyway).
		was := map[rowKey]bool{}
		for i, it := range m.items {
			// A row that was Pending in the old list is unseen: the facts
			// stage never knew whether it would prove, so a tick (or the
			// lack of one) made against that ignorance carries no choice —
			// it takes the new report's default below.
			if !it.newWorktree && !it.row.Pending {
				was[keyOf(it.row)] = m.ticked[i]
			}
		}
		for i, it := range next.items {
			if it.newWorktree {
				continue
			}
			if ticked, seen := was[keyOf(it.row)]; seen {
				switch {
				case ticked && !it.locked:
					next.ticked[i] = true
				case !ticked:
					delete(next.ticked, i)
				}
			}
		}
	}
	if m.cursor >= 0 && m.cursor < len(m.items) {
		cur := m.items[m.cursor]
		switch {
		case cur.newWorktree:
			next.cursor = 0
		default:
			next.selectWhere(func(r cleanup.Row) bool { return keyOf(r) == keyOf(cur.row) })
		}
	}
	next.settleCursor()
	return next
}

// selectWhere moves the cursor to the first row pred accepts and reports
// whether there was one.
func (m *cleanupListModel) selectWhere(pred func(cleanup.Row) bool) bool {
	for i, it := range m.items {
		if !it.newWorktree && pred(it.row) {
			m.cursor = i
			return true
		}
	}
	return false
}

// filterText is what the filter matches a row against: its listed name, its
// branch, its drawn tags, every identity tag the list no longer draws (root,
// bare, held by session, locked, +remote) and its PR ("#383", or "PR ?" while
// the lookup is owed). Why the undrawn ones: the filter is a reader of the
// row's vocabulary, not of the line's — a user looking for the worktree a
// session holds should not have to know its name.
func filterText(it cleanupItem) string {
	parts := []string{cleanup.DisplayName(it.row), it.row.Branch}
	for _, t := range it.tags {
		parts = append(parts, t.Text)
	}
	for _, t := range cleanup.RowTags(it.row) {
		parts = append(parts, t.Text)
	}
	switch {
	case it.row.PRUnknown:
		parts = append(parts, "PR ?")
	case it.row.PRTag != nil:
		parts = append(parts, fmt.Sprintf("#%d %s", it.row.PRTag.Number, strings.ToLower(it.row.PRTag.State)))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// hidden reports whether the filter hides item i: a row that does not match
// it, and the new-worktree item while there is a query.
func (m *cleanupListModel) hidden(i int) bool {
	if m.filter == "" || i < 0 || i >= len(m.items) {
		return false
	}
	if m.items[i].newWorktree {
		return true
	}
	return !strings.Contains(filterText(m.items[i]), strings.ToLower(m.filter))
}

// settleCursor puts the cursor on the first visible item when the filter
// hides the one it is on (it stays put when nothing is visible).
func (m *cleanupListModel) settleCursor() {
	if m.cursor >= 0 && !m.hidden(m.cursor) {
		return
	}
	for i := range m.items {
		if !m.hidden(i) {
			m.cursor = i
			return
		}
	}
}

// filterKey is launch mode's filter handling, ahead of every other binding.
// While the query is being typed, every printable key (q, n, x, j... too)
// goes into it, backspace takes one off, up and down still move, enter keeps
// the query and closes the input, esc drops it; ctrl+c quits. Outside it,
// `/` opens the input and esc with a query in place clears it rather than
// quitting. handled is false for a key the list's other bindings take.
func (m cleanupListModel) filterKey(msg tea.KeyMsg) (next cleanupListModel, cmd tea.Cmd, handled bool) {
	if !m.filtering {
		switch msg.String() {
		case "/":
			m.filtering = true
			return m, nil, true
		case "esc":
			if m.filter != "" {
				m.filter = ""
				m.settleCursor()
				return m, nil, true
			}
		}
		return m, nil, false
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		m.done = true
		return m, tea.Quit, true
	case tea.KeyEsc:
		m.filter, m.filtering = "", false
	case tea.KeyEnter:
		m.filtering = false
	case tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case tea.KeyUp:
		m.move(-1, 1, true)
	case tea.KeyDown:
		m.move(1, 1, true)
	case tea.KeyRunes, tea.KeySpace:
		m.filter += string(msg.Runes)
	}
	m.settleCursor()
	return m, nil, true
}

// filterLine shows the query, with a caret while it is being typed; "" with
// none.
func (m *cleanupListModel) filterLine() string {
	if !m.filtering && m.filter == "" {
		return ""
	}
	text := "filter: " + m.filter
	if m.filtering {
		text += "_  (enter keeps it, esc clears it)"
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent))
	return dim.Render(clip(text, m.width)) + "\n"
}
