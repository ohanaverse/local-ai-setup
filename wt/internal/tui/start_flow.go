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
	// Any refresh still in flight predates this start; finishStart issues its own.
	m.refreshGen++
	id := m.startRun
	ch := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName}, allowReplace, id)
	m.start = &startState{id: id, item: it, began: time.Now(), cancel: cancel, ch: ch}
	m.phase = phaseStarting
	m.status = ""
	return m, tea.Batch(waitForStart(ch), startTick(id))
}

// handleStartKey handles keys in phaseStarting. esc, q and ctrl+c all cancel
// the in-flight run on the first press. Once cancellation is draining, esc and
// q are ignored: the engine still has to tear down whatever it spawned, and
// leaving mid-teardown can orphan it (an mtplx child starts in its own session
// and keeps port 8003). ctrl+c is the deliberate escape hatch, because a cancel
// that never returns would otherwise trap the user on this screen with no key
// that exits. Every other key is ignored.
func (m model) handleStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.start.cancelling {
			return m, tea.Quit
		}
		m.start.cancelling = true
		m.start.cancel()
	case "esc", "q":
		// Ignored once cancellation is draining: these are the keys a user
		// mashes when a start looks stuck, and leaving mid-teardown can orphan
		// a server the engine spawned (an mtplx child keeps port 8003).
		if m.start.cancelling {
			return m, nil
		}
		m.start.cancelling = true
		m.start.cancel()
	}
	return m, nil
}

// handleStartMsg processes the start flow's messages. Its callers route only
// startStageMsg/startTickMsg/startDoneMsg here, so every type it can receive is
// one it handles; a message whose id no longer matches m.start is a stale
// message from a superseded run and is dropped.
func (m model) handleStartMsg(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case startStageMsg:
		if m.start == nil || msg.id != m.start.id {
			return m, nil
		}
		m.start.stage = msg.stage
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
		return m.finishStart(msg)
	}
	return m, nil
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
		return back(lifecycle.StartErrorMessage(st.item.model.ID, msg.err))
	}
	choices := list.New(buildReplaceChoices(), ThemedListDelegate(m.theme), m.width-2, m.height-2)
	choices.Title = title
	m.replace = &replaceState{item: st.item, choices: choices}
	m.phase = phaseReplaceConfirm
	return m, nil
}

func (m model) startingView() string {
	elapsed := time.Since(m.start.began).Round(time.Second)
	if m.start.cancelling {
		// Name only the key that still does something: esc and q are ignored
		// while the engine tears down, so advertising them would be a lie.
		return fmt.Sprintf("Cancelling %s… (%s)\n\nwaiting for the server to stop\n\n[ctrl+c] quit wt",
			m.start.item.model.ID, elapsed)
	}
	return fmt.Sprintf("Starting %s — %s (%s)\n\n[esc] cancel", m.start.item.model.ID, lifecycle.StageLabel(m.start.stage), elapsed)
}
