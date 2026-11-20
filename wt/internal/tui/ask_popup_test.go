package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// askKey builds the KeyMsg a terminal sends for a key name.
func askKey(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// TestAskKeysRoute: enter and y answer yes (the cursor starts on the yes
// item), esc and n answer no, enter after down answers the second item (no),
// and q/ctrl+c quit through the decline. It matters because a popup answered
// the wrong way either launches what the person refused or drops what they
// accepted.
func TestAskKeysRoute(t *testing.T) {
	cases := map[string]struct {
		keys []string
		want string
	}{
		"enter":      {[]string{"enter"}, "yes"},
		"y":          {[]string{"y"}, "yes"},
		"esc":        {[]string{"esc"}, "no"},
		"n":          {[]string{"n"}, "no"},
		"q":          {[]string{"q"}, "quit"},
		"ctrl+c":     {[]string{"ctrl+c"}, "quit"},
		"down+enter": {[]string{"down", "enter"}, "no"},
	}
	for name, tc := range cases {
		var answered string
		bind := func(mm model) (model, tea.Cmd) { answered = "yes"; return mm, nil }
		decline := func(mm model) (model, tea.Cmd) { answered = "no"; return mm, nil }
		m := model{}
		s := newAskState(themes.Theme{}, 80, 24, "launch here?", []string{"root is on feat"}, phaseModel, bind, decline)
		next, _ := m.seedAsk(s)
		var cmd tea.Cmd
		for _, k := range tc.keys {
			next, cmd = updateMsg(next, askKey(k))
		}
		got := answered
		if tc.want == "quit" {
			if got != "no" || cmd == nil {
				t.Errorf("%s: answered %q cmd %v; want the decline then a quit", name, got, cmd)
			}
		} else if got != tc.want {
			t.Errorf("%s: answered %q, want %q", name, got, tc.want)
		}
		if next.phase == phaseAsk || next.ask != nil {
			t.Errorf("%s: still in phaseAsk", name)
		}
	}
}

// TestAskViewShowsTitleBodyAndChoices: the view renders the title, the wrapped
// body and both choices. It matters because the popup is the only place the
// person sees the warnings before the terminal is released.
func TestAskViewShowsTitleBodyAndChoices(t *testing.T) {
	m := model{theme: themes.Theme{}, width: 80, height: 24}
	next, _ := m.seedAsk(newAskState(m.theme, 80, 24, "launch here?", []string{"root is on feat, not main"}, phaseModel, nil, nil))
	v := next.View()
	for _, want := range []string{"launch here?", "root is on feat, not main", "launch anyway", "go back"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}
