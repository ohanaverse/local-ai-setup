package sessioneditor

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// filterMatchesMsg is the agents list's filter result. The list ranks its
// items in a command and takes the result as a message; wrapping it keeps
// the result for the list it was ranked for even after the Models tab, whose
// table had a filter of its own, moved to internal/modeleditor.
type filterMatchesMsg struct {
	matches list.FilterMatchesMsg
}

// tagFilter wraps a command the agents list returned, so that a filter result
// in what it produces comes back as a filterMatchesMsg.
func tagFilter(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch msg := cmd().(type) {
		case list.FilterMatchesMsg:
			return filterMatchesMsg{matches: msg}
		case tea.BatchMsg:
			for i := range msg {
				msg[i] = tagFilter(msg[i])
			}
			return msg
		default:
			return msg
		}
	}
}

// applyFilterMatches hands a filter result to the agents list.
func (m *model) applyFilterMatches(msg filterMatchesMsg) tea.Cmd {
	var cmd tea.Cmd
	if m.ready {
		m.list, cmd = m.list.Update(msg.matches)
	}
	return cmd
}
