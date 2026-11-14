package tui

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
)

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
// key, so it shows only what it answers to.
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
	} else {
		if onRow && !it.locked {
			out = append(out, keyHint{"space", "space tick", "space"})
		}
		out = append(out, keyHint{"a", "a proven", "a"}, keyHint{"enter", "enter remove ticked", "enter"})
	}
	if row, ok := m.selectedRow(); ok {
		removal := m.rm.Off == "" && m.rm.Preview != nil
		if removal && !it.locked {
			out = append(out, keyHint{"x", "x remove", "x"}, keyHint{"d", "d +branch", "d"})
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
