package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// askPurpose says which question an askState asks. The component is the same
// for both; the purpose picks the choices' wording.
type askPurpose int

const (
	askGate    askPurpose = iota // the launch gate: warnings and creation, "launch here?"
	askProfile                   // the local-model profile: "apply?"
)

// askAnswer is the choice item's payload: yes is the first item.
type askAnswer bool

// askState is one yes/no popup inside the running picker (phaseAsk): a title,
// body lines, and two choices. bind and decline are the commit site's
// continuations; both receive the model after the popup is gone (phase
// already back to backPhase), so they read current state, never a copy
// captured when the popup opened.
type askState struct {
	purpose   askPurpose
	title     string
	body      []string
	backPhase phase
	choices   list.Model
	bind      func(model) (model, tea.Cmd)
	decline   func(model) (model, tea.Cmd)
}

// newAskState builds a popup. The cursor starts on the yes item, matching the
// default-yes raw prompts it replaces.
func newAskState(theme themes.Theme, width, height int, title string, body []string, backPhase phase, bind, decline func(model) (model, tea.Cmd)) *askState {
	return newAskStateFor(askGate, theme, width, height, title, body, backPhase, bind, decline)
}

// newAskStateFor is newAskState with an explicit purpose.
func newAskStateFor(purpose askPurpose, theme themes.Theme, width, height int, title string, body []string, backPhase phase, bind, decline func(model) (model, tea.Cmd)) *askState {
	yes := choiceItem{choice: askAnswer(true), title: "launch anyway", desc: "Continue with the launch"}
	no := choiceItem{choice: askAnswer(false), title: "go back", desc: "Return to the picker; nothing is created or started"}
	if purpose == askProfile {
		yes = choiceItem{choice: askAnswer(true), title: "apply profile", desc: "Launch with the local-model profile"}
		no = choiceItem{choice: askAnswer(false), title: "don't apply", desc: "Return to the picker; nothing is launched"}
	}
	return &askState{
		purpose: purpose, title: title, body: body, backPhase: backPhase,
		choices: newReplaceChoices(theme, width, height, []list.Item{yes, no}),
		bind:    bind, decline: decline,
	}
}

// seedAsk shows the popup. Like every phase transition it clears the status.
func (m *model) seedAsk(s *askState) (model, tea.Cmd) {
	m.ask = s
	m.phase = phaseAsk
	m.status = ""
	return *m, nil
}

// leaveAsk closes the popup before a continuation runs: the continuation
// decides where the model goes next (a second popup, the run, the picker).
func (m model) leaveAsk() (model, *askState) {
	s := m.ask
	m.ask = nil
	m.phase = s.backPhase
	return m, s
}

// updateAsk routes a popup's keys: y answers yes, n and esc answer no, enter
// answers the highlighted item, q and ctrl+c decline and then quit wt (so a
// quit never strands what the launch took). Everything else is the list's
// (cursor keys, resize).
func (m model) updateAsk(msg tea.Msg) (model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "y":
			next, s := m.leaveAsk()
			return s.bind(next)
		case "n", "esc":
			next, s := m.leaveAsk()
			return s.decline(next)
		case "enter":
			yes := true
			if item, ok := m.ask.choices.SelectedItem().(choiceItem); ok {
				if a, ok := item.choice.(askAnswer); ok {
					yes = bool(a)
				}
			}
			next, s := m.leaveAsk()
			if yes {
				return s.bind(next)
			}
			return s.decline(next)
		case "q", "ctrl+c":
			next, s := m.leaveAsk()
			next, c := s.decline(next)
			return next, tea.Sequence(c, tea.Quit)
		}
	}
	nextList, cmd := m.ask.choices.Update(msg)
	m.ask.choices = nextList
	return m, cmd
}

// askFrames are the popup's layouts: the title and body (wrapped, never
// clipped mid-word, the way replaceFrames wraps its question) above the
// choices, with the key hints; a terminal too short for the hints loses them
// before any line of the question.
func (m *model) askFrames() []listFrame {
	lines := []string{"  " + m.ask.title, ""}
	for _, src := range m.ask.body {
		for k, line := range strings.Split(tuilayout.WrapText(src, m.width-4), "\n") {
			prefix := "    "
			if k == 0 {
				prefix = "  • "
			}
			lines = append(lines, prefix+line)
		}
	}
	header := clip(strings.Join(lines, "\n"), m.width)
	return []listFrame{
		func(listView string) string { return header + "\n" + m.choiceFrame()(listView) },
		func(listView string) string { return header + "\n" + listView },
	}
}
