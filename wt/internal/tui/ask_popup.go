package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// askPurpose says which question an askState asks. The component is the same
// for both; the constructor picks the choices' wording (the gate's two
// answers in newAskStateFor, the profile's three in newAskProfileState) and
// purpose only labels the popup for readers and tests.
type askPurpose int

const (
	askGate    askPurpose = iota // the launch gate: warnings and creation, "launch here?"
	askProfile                   // the local-model profile: "apply?"
)

// askAnswer is the choice item's payload: which answer the item is. The gate
// popup has two (yes, no); the profile popup has a third, skip, so that a
// local model can be launched without its profile instead of only with it or
// not at all.
type askAnswer int

const (
	askYes  askAnswer = iota // bind: launch anyway / apply the profile
	askNo                    // decline: go back to the picker, nothing launched
	askSkip                  // bind with the profile answered no: launch unprofiled
)

// askState is one popup inside the running picker (phaseAsk): a title, body
// lines, and its choices — the gate's two (yes/no), the profile's three
// (yes/skip/no, skip non-nil). bind, skip and decline are the commit site's
// continuations; all receive the model after the popup is gone (phase
// already back to backPhase), so they read current state, never a copy
// captured when the popup opened.
type askState struct {
	purpose   askPurpose
	title     string
	body      []string
	backPhase phase
	choices   list.Model
	bind      func(model) (model, tea.Cmd)
	skip      func(model) (model, tea.Cmd) // nil: the popup offers no third answer
	decline   func(model) (model, tea.Cmd)
}

// newAskState builds a popup. The cursor starts on the yes item, matching the
// default-yes raw prompts it replaces.
func newAskState(theme themes.Theme, width, height int, title string, body []string, backPhase phase, bind, decline func(model) (model, tea.Cmd)) *askState {
	return newAskStateFor(theme, width, height, title, body, backPhase, bind, decline)
}

// newAskStateFor is newAskState with the gate's two answers. Only askGate is
// built here; the profile popup's three answers come from newAskProfileState,
// which labels itself askProfile — a purpose argument would silently hand a
// caller gate wording for the profile question.
func newAskStateFor(theme themes.Theme, width, height int, title string, body []string, backPhase phase, bind, decline func(model) (model, tea.Cmd)) *askState {
	yes := choiceItem{choice: askYes, title: "launch anyway", desc: "Continue with the launch"}
	no := choiceItem{choice: askNo, title: "go back", desc: "Return to the picker; nothing is created or started"}
	s := &askState{
		purpose: askGate, title: title, body: body, backPhase: backPhase,
		choices: newReplaceChoices(theme, width, height, []list.Item{yes, no}),
		bind:    bind, decline: decline,
	}
	// No filtering on a two- or three-choice popup: the list's `/` filter is
	// never wanted here, and its esc/enter bindings would fight updateAsk,
	// which answers esc as go-back and enter as the highlighted choice before
	// the list sees either key.
	s.choices.SetFilteringEnabled(false)
	return s
}

// newAskProfileState is the local-model profile question's popup. It offers
// three answers, not the gate's two: apply the profile, launch this once
// without it (the raw /dev/tty prompt's "n", which is what the picker lost
// when the popup replaced it), or go back. A profile is optional — it
// reshapes the launch, it is not the launch — so declining it must not be
// the same choice as declining the launch.
func newAskProfileState(theme themes.Theme, width, height int, title string, body []string, backPhase phase, bind, skip, decline func(model) (model, tea.Cmd)) *askState {
	s := &askState{
		purpose: askProfile, title: title, body: body, backPhase: backPhase,
		skip: skip,
		choices: newReplaceChoices(theme, width, height, []list.Item{
			choiceItem{choice: askYes, title: "apply profile", desc: "Launch with the local-model profile"},
			choiceItem{choice: askSkip, title: "launch without profile", desc: "Launch this once unprofiled; profiles.toml is untouched"},
			choiceItem{choice: askNo, title: "go back", desc: "Return to the picker; nothing is launched"},
		}),
		bind: bind, decline: decline,
	}
	// No filtering, for the same reason as the gate's popup above: updateAsk
	// answers esc/y/n/enter before the list's filter bindings ever see them.
	s.choices.SetFilteringEnabled(false)
	return s
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

// updateAsk routes a popup's keys: y answers yes, n answers the popup's no
// answer (skipping the profile where one is offered, go back otherwise), esc
// goes back, enter answers the highlighted item, q and ctrl+c decline and
// then quit wt (so a quit never strands what the launch took). Everything
// else is the list's (cursor keys, resize).
func (m model) updateAsk(msg tea.Msg) (model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "y":
			return m.answerAsk(askYes)
		case "n":
			// The profile question reads "apply? [Y/n]" in the raw prompt
			// this popup replaced: there, no meant an unprofiled launch,
			// never an abandoned one. Keep that answer (askSkip); going
			// back stays on esc and on the last item.
			if m.ask.skip != nil {
				return m.answerAsk(askSkip)
			}
			return m.answerAsk(askNo)
		case "esc":
			return m.answerAsk(askNo)
		case "enter":
			a := askYes
			if item, ok := m.ask.choices.SelectedItem().(choiceItem); ok {
				if ans, ok := item.choice.(askAnswer); ok {
					a = ans
				}
			}
			return m.answerAsk(a)
		case "q", "ctrl+c":
			next, c := m.answerAsk(askNo)
			return next, tea.Sequence(c, tea.Quit)
		}
	}
	nextList, cmd := m.ask.choices.Update(msg)
	m.ask.choices = nextList
	return m, cmd
}

// answerAsk closes the popup and runs the continuation its answer names. A
// skip that a popup does not offer (the gate's) declines — decline is the
// only other answer it could mean.
func (m model) answerAsk(a askAnswer) (model, tea.Cmd) {
	next, s := m.leaveAsk()
	switch a {
	case askSkip:
		if s.skip != nil {
			return s.skip(next)
		}
		return s.decline(next)
	case askNo:
		return s.decline(next)
	}
	return s.bind(next)
}

// askHintLine is the popup's key hints: choiceFrame's enter and esc plus the
// y/n shortcuts updateAsk answers, worded as what each key does on this
// popup. The n label reads the same predicate updateAsk reads (skip != nil):
// decline on the gate, launch unprofiled on the profile popup. choiceFrame
// itself stays untouched — it also lays out the replace and ollama dialogs,
// which have no y/n — and the list's own help never names y/n, so this line
// is their only in-UI discovery (style guide T5; TestAskHintsMatchTheHandlers
// holds it to the handlers).
func (m *model) askHintLine() string {
	hints := []string{"[enter] choose", "[y] launch"}
	if m.ask.skip != nil {
		hints = append(hints, "[n] launch w/o profile")
	} else {
		hints = append(hints, "[n] decline")
	}
	return clip(strings.Join(append(hints, "[esc] back"), "   "), m.width)
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
		func(listView string) string { return header + "\n" + listView + "\n" + m.askHintLine() },
		func(listView string) string { return header + "\n" + listView },
	}
}
