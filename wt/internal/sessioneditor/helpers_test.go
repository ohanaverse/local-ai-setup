package sessioneditor

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The helpers below are copies of the ones the Models tab's tests define
// (internal/modeleditor): a test file cannot be shared across packages.

// send delivers msg and then runs every command it leads to, the way the
// Bubble Tea runtime would, until none is left. A quit ends the walk.
func send(t *testing.T, m *model, msg tea.Msg) *model {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(*model)
	for cmd != nil {
		out := cmd()
		cmd = nil
		switch out := out.(type) {
		case nil, tea.QuitMsg:
		case tea.BatchMsg:
			for _, c := range out {
				if c != nil {
					m = send(t, m, c())
				}
			}
		default:
			next, cmd = m.Update(out)
			m = next.(*model)
		}
	}
	return m
}

func keys(t *testing.T, m *model, ks ...string) *model {
	t.Helper()
	for _, k := range ks {
		m = sendKey(t, m, keyMsg(k))
	}
	return m
}

// keyMsg is the key message for a key's name, or for one typed character.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// sendKey delivers one key and runs what it leads to.
func sendKey(t *testing.T, m *model, msg tea.KeyMsg) *model {
	t.Helper()
	return send(t, m, msg)
}

// tabSizes are the terminal sizes wt supports, widths 40/80/120 by heights
// 12/24/50, for a test that measures one screen at each.
var tabSizes = func() (sizes [][2]int) {
	for _, w := range []int{40, 80, 120} {
		for _, h := range []int{12, 24, 50} {
			sizes = append(sizes, [2]int{w, h})
		}
	}
	return sizes
}()

// assertFits fails when view is taller or wider than the terminal. Bubble Tea
// drops a too-tall view's top lines (the tab bar and the status) and cuts a
// too-wide line at the edge.
func assertFits(t *testing.T, name, view string, width, height int) {
	t.Helper()
	if h := lipgloss.Height(view); h > height {
		t.Errorf("%s at %dx%d: the view is %d lines tall:\n%s", name, width, height, h, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("%s at %dx%d: a line is %d columns wide: %q", name, width, height, w, line)
		}
	}
}

// flat is a view with its line breaks and padding taken out, to look for text
// the view wraps at the terminal's width.
func flat(view string) string { return strings.Join(strings.Fields(view), "") }

// stillCursors stops the cursor of the agents list's filter input blinking.
// A blink is a command that sleeps for half a second, and send runs every
// command a key leads to; call it before typing a filter.
func stillCursors(m *model) *model {
	m.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	return m
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// scratchConfigHome points XDG_CONFIG_HOME at a temporary directory of this
// test (restored afterwards), as the Models tab's test machine used to for
// every editor a test built: config.Path() then names a file of the test's.
func scratchConfigHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}
