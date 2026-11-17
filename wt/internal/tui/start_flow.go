package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// recordRouteNotes keeps a note for the terminal (the model server's notes
// about the client session): it appends the text to pendingRouteNotes, which is printed on the real terminal once the
// alt screen is released — above the agent's output, or after wt exits when
// the launch did not happen. It does not go in the status line: the launch
// follows at once, so the only picker screen the user can land on next is the
// one a failed launch returns to, whose status is that failure.
func (m *model) recordRouteNotes(notes string) {
	pendingRouteNotes += notes
}

// startState is the in-flight start job (phaseStarting, remote_start.go):
// which row, when it began, how to cancel it, and the channel carrying its
// messages.
type startState struct {
	id   int
	item *modelItem
	// allow and others are what this attempt was given (AllowReplace and the
	// others plan), so a refusal's dialog knows which questions are still
	// open and what its retry carries.
	allow      bool
	others     othersPlan
	began      time.Time
	cancel     context.CancelFunc
	cancelling bool
	ch         <-chan tea.Msg
	// quitting is set once a key asked wt to quit. tea.Quit is a command:
	// its QuitMsg comes back through the message channel, so the start's
	// startDoneMsg can still be delivered in between, and a successful one
	// would launch. A quitting start drops it.
	quitting bool

	// The job the server runs. jobID is "" until the first poll names it,
	// and only then can the job be detached from or cancelled
	// (canCancelJob). job is the last poll's view, cancelSent a cancel on its
	// way, cancelErr a cancel the server refused.
	jobID      string
	job        *modelclient.JobView
	cancelSent bool
	// cancelAsked is set by c and never cleared, not even when the server
	// refuses the cancel: a start that then succeeds launches nothing.
	cancelAsked bool
	cancelErr   string
}

// newReplaceChoices is the replace dialog's list of choices. The list's own
// title is one line, cut at the terminal's edge, so the question is the
// frame's header instead (replaceFrames), wrapped; the header takes the lines
// the title and the item count took, so a short terminal still fits it.
func newReplaceChoices(theme themes.Theme, width, height int, items []list.Item) list.Model {
	choices := list.New(items, themes.ListDelegate(theme), width, height)
	choices.SetShowTitle(false)
	choices.SetShowStatusBar(false)
	return choices
}

// replaceState is the replace-confirm dialog for the row that could not start.
type replaceState struct {
	item *modelItem
	// header is the dialog's question, one line per occupant and per other
	// provider's model, built once with the dialog; replaceFrames wraps it to
	// the terminal on each render.
	header  string
	choices list.Model
	// others is the plan "Replace and start" retries with: the refused
	// attempt's own, or the stop --others=stop gave (#412).
	others othersPlan
	// allow is the AllowReplace "keep" and "stop" retry with: the refused
	// attempt's own, or true when the dialog showed same-family occupants,
	// which choosing either confirms.
	allow bool
	// stop are the ids of the other providers' models the dialog showed that
	// wt can stop: exactly what "stop" approves. The stop choice is offered
	// only while the question shows in full (syncStopChoice).
	stop []string
	// What buildOthersChoices was given, kept so the choices can be rebuilt
	// with or without stop as the terminal's size changes.
	id                       string
	sameFamily, othersListed bool
}

// syncStopChoice offers the stop choice only while the dialog shows its
// question in full (whole): a stop approves every stoppable model listed, so
// it is never offered while some of them are cut off the screen. Withdrawn
// under the cursor, the cursor goes back to Cancel. A dialog with nothing to
// stop is left alone.
func (rs *replaceState) syncStopChoice(whole bool) bool {
	if len(rs.stop) == 0 {
		return false
	}
	stoppable := 0
	if whole {
		stoppable = len(rs.stop)
	}
	items := buildOthersChoices(rs.id, rs.sameFamily, rs.othersListed, stoppable)
	if len(items) == len(rs.choices.Items()) {
		return false
	}
	if rs.choices.Index() >= len(items) {
		rs.choices.Select(0)
	}
	rs.choices.SetItems(items)
	return true
}

type startStageMsg struct {
	id int
	// job is a poll's view of the start job.
	job *modelclient.JobView
}
type startDoneMsg struct {
	id int
	// err is what became of the start job on the model server (nil: done).
	err error
	// note is the API path's register note: the client session the start
	// registered first was refused ("" when it was taken).
	note string
}
type startTickMsg struct{ id int }

type replaceChoice int

const (
	replaceCancelChoice replaceChoice = iota // first: the default cursor position
	replaceProceedChoice
	othersKeepChoice // start and keep other providers' models (#412)
	othersStopChoice // stop the ones wt can stop, then start
)

func buildReplaceChoices() []list.Item {
	return []list.Item{
		choiceItem{choice: replaceCancelChoice, title: "Cancel", desc: "Return to the model screen"},
		choiceItem{choice: replaceProceedChoice, title: "Replace and start", desc: "Stop the running model, then start this one"},
	}
}

// buildOthersChoices is the dialog's choices when other providers' models are
// loaded (#412): Cancel first, the default; keep; and stop, only when
// stoppable of the models shown can be stopped. With same-family occupants as
// well (sameFamily) the two actions say they replace. Keep's description
// says what it does to the models listed: loads beside them, replaces the
// will-stop ones first, or, with no model listed (othersListed false: only
// families wt cannot read), stops nothing.
func buildOthersChoices(id string, sameFamily, othersListed bool, stoppable int) []list.Item {
	keep, stop := "Start and keep them", "Stop them, then start"
	keepDesc := "Load " + id + " beside the models above"
	switch {
	case sameFamily:
		keep, stop = "Replace, keep the others", "Replace and stop the others"
		keepDesc = "Replace the models listed first, keep the other providers' models"
	case !othersListed:
		keepDesc = "Load " + id + " without stopping anything"
	}
	items := []list.Item{
		choiceItem{choice: replaceCancelChoice, title: "Cancel", desc: "Return to the model screen"},
		choiceItem{choice: othersKeepChoice, title: keep, desc: keepDesc},
	}
	if stoppable > 0 {
		items = append(items, choiceItem{choice: othersStopChoice, title: stop, desc: fmt.Sprintf("Stop %d model(s) of other providers first", stoppable)})
	}
	return items
}

// retryStart starts the refused row again with the answers the user gave.
func (m model) retryStart(it *modelItem, allow bool, others othersPlan) (model, tea.Cmd) {
	return m.beginRemoteStart(it, allow, others)
}

// waitForStart turns the next channel message into a tea message; it must be
// re-armed after every stage message until startDoneMsg arrives.
func waitForStart(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func startTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return startTickMsg{id: id} })
}

// othersPlan is how one start attempt answers the question about other
// providers' models loaded beside the model it starts (#412).
type othersPlan struct {
	// answer is what the engine is given (Options.Others); its zero value
	// asks, so the engine refuses with what is loaded and the dialog asks.
	answer modelapi.OthersDecision
	// stopAll is --others=stop for the pinned row: the engine's refusal is
	// answered, without the dialog, by stopping every resident it listed
	// that wt can stop, as the non-TUI start does (decideOthers).
	stopAll bool
}

// othersFor is a first attempt's plan. --others answers it only for the -M
// pinned row (pinned), by --replace's rule: keep on the first attempt, stop
// once the engine has said what is loaded. Every other start asks.
func (m model) othersFor(pinned bool) othersPlan {
	if !pinned {
		return othersPlan{}
	}
	switch m.othersMode {
	case "keep":
		return othersPlan{answer: modelapi.OthersDecision{Decided: true}}
	case "stop":
		return othersPlan{stopAll: true}
	}
	return othersPlan{}
}

// handleStartKey handles keys in phaseStarting: the start job's own keys
// (handleRemoteStartKey: detach, cancel, quit).
func (m model) handleStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	return m.handleRemoteStartKey(msg)
}

// handleStartMsg processes the start flow's messages. Its callers route only
// startStageMsg/startTickMsg/startDoneMsg here, so every type it can receive is
// one it handles; a message whose id no longer matches m.start is a stale
// message from a superseded run and is dropped. So is every message for a
// start the user quit out of (startState.quitting): its result would launch.
func (m model) handleStartMsg(msg tea.Msg) (model, tea.Cmd) {
	if m.start != nil && m.start.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case startStageMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil
		}
		if msg.job != nil {
			m = m.remoteProgress(msg.job)
		}
		return m, waitForStart(m.start.ch)
	case startTickMsg:
		if m.start == nil || msg.id != m.start.id || m.phase != phaseStarting {
			return m, nil
		}
		return m, startTick(msg.id)
	case startDoneMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil
		}
		return m.finishRemoteStart(msg)
	}
	return m, nil
}

// withNote puts note on lines of its own above status: the note is the
// engine's lines, one each, and the status is what became of the start or of
// the launch after it.
func withNote(note, status string) string {
	if note == "" {
		return status
	}
	return note + "\n" + status
}

// startingView is phaseStarting's screen: the start job's progress
// (remoteStartingView).
func (m model) startingView() string {
	return m.remoteStartingView()
}
