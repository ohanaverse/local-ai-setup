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

// TestAskHintsMatchTheHandlers: the popup's footer names the answer shortcuts
// updateAsk answers — enter, y, n, esc — worded as what each key does on this
// popup, and the n label follows the same predicate the handler reads
// (skip != nil): the gate's n declines, the profile popup's n launches
// unprofiled. It matters because the footer is the only in-UI discovery of
// the y/n shortcuts (style guide T5: a key is shown exactly when pressing it
// would act), and a stale label would re-teach the raw prompt's "n = no" on
// the popup that deliberately answers it differently.
func TestAskHintsMatchTheHandlers(t *testing.T) {
	cases := map[string]struct {
		skip    bool   // the popup offers the third answer
		nLabel  string // the footer's n hint
		nAnswer string // what pressing n answers
	}{
		"gate":    {false, "[n] decline", "no"},
		"profile": {true, "[n] launch w/o profile", "skip"},
	}
	for name, tc := range cases {
		var answered string
		bind := func(mm model) (model, tea.Cmd) { answered = "yes"; return mm, nil }
		decline := func(mm model) (model, tea.Cmd) { answered = "no"; return mm, nil }
		skipFn := func(mm model) (model, tea.Cmd) { answered = "skip"; return mm, nil }
		m := model{theme: themes.Theme{}, width: 80, height: 24}
		var s *askState
		if tc.skip {
			s = newAskProfileState(m.theme, 80, 24, "apply profile?", []string{"omlx/qwen3.8"}, phaseModel, bind, skipFn, decline)
		} else {
			s = newAskState(m.theme, 80, 24, "launch here?", []string{"root is on feat"}, phaseModel, bind, decline)
		}
		next, _ := m.seedAsk(s)

		// The footer is the first frame's last line.
		frame := next.askFrames()[0]("LIST")
		footer := frame[strings.LastIndex(frame, "\n")+1:]
		for _, hint := range []string{"[enter] choose", "[y] ", "[n] ", "[esc] back", tc.nLabel} {
			if !strings.Contains(footer, hint) {
				t.Errorf("%s: footer %q missing %q", name, footer, hint)
			}
		}

		// Every key the footer names answers the popup with what its hint
		// promises, on this popup. updateAsk is called directly (not through
		// the whole Update): it is the handler the footer documents, and a
		// hand-seeded model's other lists are not sized for Update.
		want := map[string]string{"enter": "yes", "y": "yes", "n": tc.nAnswer, "esc": "no"}
		for k, wantAnswer := range want {
			answered = ""
			after, _ := next.updateAsk(askKey(k))
			if answered != wantAnswer || after.phase == phaseAsk {
				t.Errorf("%s: key %s answered %q (phase %v); want %q with the popup closed", name, k, answered, after.phase, wantAnswer)
			}
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
