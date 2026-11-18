package configeditor

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
)

// serverPhase is what the Model server tab is showing. Only serverView is
// used so far; the form, the plan's confirm screen and the apply in progress
// come with planning and applying.
type serverPhase int

const (
	serverView serverPhase = iota
	serverEdit
	serverConfirm
	serverApplying
)

// serverTab is the Model server tab's state. Everything it knows of the
// machine comes from one observation run in a command (observeServerCmd)
// through ServerDeps.Observe; nothing here reads a file or dials a server.
type serverTab struct {
	phase serverPhase
	// state is the latest observation; loaded is true once one is in.
	state  modelsetup.State
	loaded bool
	// want is the setup the user is choosing. Only its Mode is set so far,
	// and only on screen: nothing is planned or written from it yet.
	want modelsetup.Desired
	// chosen is true once the user has moved the mode choice; until then a
	// new observation sets the choice to the effective mode.
	chosen bool
	// asked is true once the first observation has been dispatched, so the
	// tab observes on its first visit only (c and r observe again).
	asked bool
	// gen numbers the observations: a result that is not the latest's is
	// dropped, so a slow ping cannot overwrite a later answer.
	gen int
	// busy is true while an observation is out.
	busy bool
	// status is what the tab is doing or what the last key said; the next
	// key clears it when nothing is out.
	status string
}

// serverObservedMsg carries one observation.
type serverObservedMsg struct {
	gen int
	st  modelsetup.State
}

// serverObserveOptions are the options an observation is made with. The
// form that edits the url, token path and listen address comes later; until
// then they are Observe's defaults.
func (m *model) serverObserveOptions() modelsetup.ObserveOptions {
	return modelsetup.ObserveOptions{}
}

// observeServerCmd reads the machine once, off the update loop. The deps,
// the generation and the options are taken here, before the command, so the
// command touches nothing of the model; all I/O is inside it.
func (m *model) observeServerCmd() tea.Cmd {
	m.server.gen++
	m.server.asked, m.server.busy = true, true
	gen, observe, opts := m.server.gen, m.opts.Server.Observe, m.serverObserveOptions()
	return func() tea.Msg {
		return serverObservedMsg{gen: gen, st: observe(context.Background(), opts)}
	}
}

// applyServerObserved takes an observation, unless a later one is out.
func (m *model) applyServerObserved(msg serverObservedMsg) {
	st := &m.server
	if msg.gen != st.gen {
		return // a later observation is on its way
	}
	st.state, st.loaded, st.busy, st.status = msg.st, true, false, ""
	if !st.chosen {
		st.want.Mode = msg.st.Resolution.Mode
	}
}

// switchTab moves to the next tab and, on a tab's first visit, starts what
// fills it: the Models tab's probe, the Model server tab's observation.
func (m *model) switchTab() tea.Cmd {
	m.tab = nextTab(m.tab)
	switch {
	case m.tab == TabModels && !m.models.asked:
		return m.probeCmd()
	case m.tab == TabServer && !m.server.asked:
		return m.observeServerCmd()
	}
	return nil
}

// serverModes are the mode choices in the order they are drawn and cycled.
var serverModes = []modelclient.Mode{modelclient.ModeNative, modelclient.ModeLocal, modelclient.ModeRemote}

// modeAllowed is false for local where launchd is not available.
func (st *serverTab) modeAllowed(mode modelclient.Mode) bool {
	return mode != modelclient.ModeLocal || st.state.Platform.OK
}

// stepMode moves the mode choice by step (+1 right, -1 left), round the
// ends and over a choice that is not allowed. From no choice, right is the
// first and left the last.
func (st *serverTab) stepMode(step int) {
	i := -1
	for j, mode := range serverModes {
		if mode == st.want.Mode {
			i = j
		}
	}
	if i < 0 && step < 0 {
		i = 0
	}
	n := len(serverModes)
	for range n {
		i = ((i+step)%n + n) % n
		if st.modeAllowed(serverModes[i]) {
			break
		}
	}
	st.want.Mode, st.chosen = serverModes[i], true
}

// updateServer handles a message while the Model server tab is showing. It
// never calls a dep: observing is a command (observeServerCmd).
func (m *model) updateServer(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.server
	if !st.busy {
		st.status = ""
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "tab":
		return m, m.switchTab()
	case "c":
		// Observe runs every check, the ping included, and writes nothing.
		st.status = "checking the model server"
		return m, m.observeServerCmd()
	case "r":
		// The same read; the one before it, if still out, is dropped. This is
		// how a config.toml repaired by hand is read again.
		st.status = "re-reading the model server's state"
		return m, m.observeServerCmd()
	case "left":
		if st.loaded {
			st.stepMode(-1)
		}
	case "right", " ":
		if st.loaded {
			st.stepMode(1)
		}
	case "up", "down":
		// Reserved for moving between the form's fields.
	}
	// ctrl+s is the Agents tab's save and does nothing here.
	return m, nil
}
