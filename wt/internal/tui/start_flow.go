package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startModel is a test seam: production starts the model through the lifecycle
// engine.
var startModel = lifecycle.Start

// startState is the in-flight start: which row, the current stage, when it
// began, how to cancel it, and the channel carrying its messages.
type startState struct {
	id         int
	item       *modelItem
	stage      lifecycle.Stage
	began      time.Time
	cancel     context.CancelFunc
	cancelling bool
	ch         <-chan tea.Msg
}

// replaceState is the replace-confirm dialog for the row that could not start.
type replaceState struct {
	item    *modelItem
	choices list.Model
}

type startStageMsg struct {
	id    int
	stage lifecycle.Stage
}
type startDoneMsg struct {
	id  int
	err error
}
type startTickMsg struct{ id int }

type replaceChoice int

const (
	replaceCancelChoice replaceChoice = iota // first: the default cursor position
	replaceProceedChoice
)

func buildReplaceChoices() []list.Item {
	return []list.Item{
		choiceItem{choice: replaceCancelChoice, title: "Cancel", desc: "Return to the model screen"},
		choiceItem{choice: replaceProceedChoice, title: "Replace and start", desc: "Stop the running model, then start this one"},
	}
}

// runStart runs the engine in a goroutine and streams its stages, then its
// result, on the returned channel (closed after the result). The startModel
// seam is read HERE, on the caller's goroutine, so a test seam swap never
// races the goroutine's late first read.
func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow bool, id int) <-chan tea.Msg {
	ch := make(chan tea.Msg, 16)
	start := startModel
	go func() {
		defer close(ch)
		err := start(ctx, cfg, t, lifecycle.Options{
			AllowReplace: allow,
			Progress: func(s lifecycle.Stage) {
				select {
				case ch <- startStageMsg{id: id, stage: s}:
				case <-ctx.Done():
				}
			},
		})
		ch <- startDoneMsg{id: id, err: err}
	}()
	return ch
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

// beginStart starts it through the engine and enters phaseStarting.
func (m model) beginStart(it *modelItem, allowReplace bool) (model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.startRun++
	id := m.startRun
	ch := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName}, allowReplace, id)
	m.start = &startState{id: id, item: it, began: time.Now(), cancel: cancel, ch: ch}
	m.phase = phaseStarting
	m.status = ""
	return m, tea.Batch(waitForStart(ch), startTick(id))
}

// handleStartKey handles keys in phaseStarting: esc/q/ctrl+c cancel (a further
// press while cancelling quits); every other key is ignored.
func (m model) handleStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		if m.start.cancelling {
			return m, tea.Quit
		}
		m.start.cancelling = true
		m.start.cancel()
	}
	return m, nil
}

// handleStartMsg processes the start flow's messages; ok is false for
// messages that are not the flow's.
func (m model) handleStartMsg(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case startStageMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil, true
		}
		m.start.stage = msg.stage
		return m, waitForStart(m.start.ch), true
	case startTickMsg:
		if m.start == nil || msg.id != m.start.id || m.phase != phaseStarting {
			return m, nil, true
		}
		return m, startTick(msg.id), true
	case startDoneMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil, true
		}
		nm, cmd := m.finishStart(msg)
		return nm, cmd, true
	}
	return m, nil, false
}

func (m model) finishStart(msg startDoneMsg) (model, tea.Cmd) {
	st := m.start
	m.start = nil
	st.cancel()
	back := func(status string) (model, tea.Cmd) {
		m.status = status
		m.phase = phaseModel
		// The table was built before the attempt and a start can have stopped
		// the occupant and then failed, so re-probe instead of showing stale
		// RUNNING.
		return m.refreshTable()
	}
	if st.cancelling {
		return back("cancelled")
	}
	if msg.err == nil {
		m.phase = phaseModel
		return m.proceedToLaunch()
	}
	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	title := ""
	switch {
	case errors.As(msg.err, &occ):
		title = fmt.Sprintf("Starting %s will stop %s, which is running", st.item.model.ID, occ.Occupant.ModelID)
	case errors.As(msg.err, &unk):
		title = fmt.Sprintf("Cannot tell whether %s at %s is already serving a model", unk.ProviderID, unk.Origin)
	default:
		return back(startErrorMessage(st.item.model.ID, msg.err))
	}
	choices := list.New(buildReplaceChoices(), ThemedListDelegate(m.theme), m.width-2, m.height-2)
	choices.Title = title
	m.replace = &replaceState{item: st.item, choices: choices}
	m.phase = phaseReplaceConfirm
	return m, nil
}

// startErrorMessage is the picker status line for a failed start.
func startErrorMessage(id string, err error) string {
	var down *lifecycle.DaemonDownError
	var bin *lifecycle.BinaryMissingError
	var busy *lifecycle.PortBusyError
	switch {
	case errors.As(err, &down):
		return fmt.Sprintf("%s is not answering at %s — start it first", down.Provider, down.Origin)
	case errors.As(err, &bin), errors.As(err, &busy):
		return err.Error()
	}
	return fmt.Sprintf("failed to start %s: %v", id, err)
}

func stageLabel(s lifecycle.Stage) string {
	switch s {
	case lifecycle.StageStoppingOccupant:
		return "stopping the running model"
	case lifecycle.StageStarting:
		return "starting the server"
	case lifecycle.StageWaiting:
		return "waiting for the model to load"
	case lifecycle.StageWarming:
		return "warming the model"
	}
	return "starting"
}

func (m model) startingView() string {
	elapsed := time.Since(m.start.began).Round(time.Second)
	if m.start.cancelling {
		return fmt.Sprintf("Cancelling %s… (%s)\n\n[esc] quit wt", m.start.item.model.ID, elapsed)
	}
	return fmt.Sprintf("Starting %s — %s (%s)\n\n[esc] cancel", m.start.item.model.ID, stageLabel(m.start.stage), elapsed)
}
