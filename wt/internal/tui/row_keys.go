package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// hintSep joins the hints of a footer line.
const hintSep = " · "

// fitHints lays hints out for a terminal width: the long forms on one line
// when they fit; else the long forms on two lines; else the short forms on
// two lines. A hint is shortened before any is dropped; only a terminal too
// narrow for even the short forms on two lines loses the tail, and each line
// is cut at the edge. A width that is not positive gives the long forms on
// one line.
func fitHints(hints []keyHint, width int) string {
	long := make([]string, len(hints))
	short := make([]string, len(hints))
	for i, h := range hints {
		long[i], short[i] = h.long, h.short
	}
	if width <= 0 {
		return strings.Join(long, hintSep)
	}
	for _, forms := range [][]string{long, short} {
		if lines := packHints(forms, width); len(lines) <= 2 {
			return strings.Join(lines, "\n")
		}
	}
	lines := packHints(short, width)
	return strings.Join(lines[:2], "\n")
}

// packHints fills lines greedily with forms, each line no wider than width
// unless one form alone is wider (the caller clips that one).
func packHints(forms []string, width int) []string {
	var lines []string
	cur := ""
	for _, f := range forms {
		switch {
		case cur == "":
			cur = f
		case lipgloss.Width(cur+hintSep+f) <= width:
			cur += hintSep + f
		default:
			lines = append(lines, cur)
			cur = f
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// footer is the screen's key legend for the highlighted row, dim, one or two
// lines no wider than the terminal; "" when there is nothing to show.
func (m *cleanupListModel) footer() string {
	hints := m.rowKeys()
	if len(hints) == 0 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	lines := strings.Split(fitHints(hints, m.width), "\n")
	for i, line := range lines {
		lines[i] = dim.Render(clip(line, m.width))
	}
	return strings.Join(lines, "\n")
}

// keyHint is one entry of the worktree screens' footer: the key as the
// handlers see it, the wording shown when there is room (long) and the
// shorter wording for a narrow terminal (short).
type keyHint struct{ key, long, short string }

// rowKeys are the keys that work on the highlighted row right now, in display
// order: movement, the screen's own keys, the row keys, then F and q. It
// reads the same predicates the handlers read (cleanupLocked, originTarget,
// rescue.Offered, rootmove.Offered, the wired RemoveFuncs), so a key is
// listed exactly when its handler would not refuse it; TestRowKeysMatchTheHandlers
// holds the two together. The `/` input, while open, owns every printable
// key, so it shows only what it answers to; a query kept with the input
// closed lists esc, which clears it then and no other time (with no query,
// esc falls through to the quit binding). The long form of d follows
// startRowAction's seeding: `d +branch` on a worktree row, `d remove`
// elsewhere, where the branch is the row itself.
func (m *cleanupListModel) rowKeys() []keyHint {
	if m.empty() {
		return nil
	}
	move := keyHint{"↑/↓", "↑/↓ move", "↑/↓"}
	if m.filtering {
		return []keyHint{
			move,
			{"enter", "enter keep filter", "enter"},
			{"esc", "esc clear filter", "esc"},
			{"ctrl+c", "ctrl+c quit", "ctrl+c"},
		}
	}
	out := []keyHint{move}
	it := m.items[m.cursor]
	onRow := !m.hidden(m.cursor) && it.heading == ""
	if m.launch() {
		if onRow && (it.newWorktree || !it.row.Bare) {
			out = append(out, keyHint{"enter", "enter launch", "enter"})
		}
		out = append(out, keyHint{"n", "n new worktree", "n new"}, keyHint{"/", "/ filter", "/"})
		if m.filter != "" {
			out = append(out, keyHint{"esc", "esc clear filter", "esc"})
		}
	} else {
		if onRow && !it.locked {
			out = append(out, keyHint{"space", "space tick", "space"})
		}
		out = append(out, keyHint{"a", "a proven", "a"}, keyHint{"enter", "enter remove ticked", "enter"})
	}
	if row, ok := m.selectedRow(); ok {
		removal := m.rm.Off == "" && m.rm.Preview != nil
		if removal && !it.locked {
			// The branch toggle is seeded only for a worktree row
			// (startRowAction), so `d +branch` is its long form; elsewhere d
			// is x, the branch being the row itself.
			dLong := "d remove"
			if it.row.Kind == cleanup.KindWorktree {
				dLong = "d +branch"
			}
			out = append(out, keyHint{"x", "x remove", "x"}, keyHint{"d", dLong, "d"})
		}
		if target, has := m.originTarget(row); removal && has && !cleanupLocked(target) {
			out = append(out, keyHint{"o", "o delete origin", "o"})
		}
		if ok, _ := rescue.Offered(row); ok && m.rm.Rescue != nil {
			out = append(out, keyHint{"r", "r rescue", "r"})
		}
		if ok, _ := rootmove.Offered(row); ok && m.rm.RestoreRoot != nil {
			out = append(out, keyHint{"R", "R restore root", "R restore"})
		}
	}
	fetch := "F refresh"
	if m.launch() {
		fetch = "F fetch"
	}
	return append(out, keyHint{"F", fetch, "F"}, keyHint{"q", "q quit", "q"})
}
