package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startModel is a test seam: production starts the model through the lifecycle
// engine.
var startModel = lifecycle.Start

// waitPendingRoutes is a test seam over lifecycle.WaitPendingRoutes. The route
// hook's LiteLLM proxy restart runs asynchronously, and both of its callers
// here hand straight off to the launch flow, whose agent dials the model
// THROUGH the proxy: launching while the restart is in flight can meet a
// refused connection or the pre-restart route table. finishStart calls it on
// the Bubble Tea update goroutine, so the "Starting …" screen stops updating
// for the length of the restart — that screen already says the routes are
// being updated, and it is the last thing before the agent takes the
// terminal. The launch-time route check (checkLaunchRoute) has no such screen
// to sit behind, so it calls it from a command, behind phaseRouting.
var waitPendingRoutes = lifecycle.WaitPendingRoutes

// routesPending is a test seam over lifecycle.RoutesPending: whether a proxy
// restart is in flight, so that the start screen can say what it is waiting
// for after a start that failed.
var routesPending = lifecycle.RoutesPending

// ensureModelRoute is a test seam over lifecycle.EnsureModelRouteTo, the form
// of the launch-time check that sends its output to a writer of the caller's.
// Production rewrites config.yaml and restarts the LiteLLM proxy; TestMain
// stubs it.
var ensureModelRoute = lifecycle.EnsureModelRouteTo

// tryEnsureModelRoute is a test seam over lifecycle.TryEnsureModelRouteTo, the
// launch-time check's first, non-blocking attempt. See checkLaunchRoute for why
// the check needs two forms and why this one runs on the update goroutine.
var tryEnsureModelRoute = lifecycle.TryEnsureModelRouteTo

// routingState is the in-flight launch-time route check (phaseRouting): the
// route was written and the proxy is restarting.
type routingState struct {
	// id is the run id; a routeDoneMsg or routeTickMsg carrying another one is
	// from a superseded check and is dropped, as startState.id does for starts.
	id      int
	modelID string
	began   time.Time
	// settle finishes the check: it re-runs the route write if the first,
	// non-blocking attempt could not take the config.yaml lock, waits for the
	// proxy restart and returns what the check printed. It runs at most once
	// however many callers there are, and every caller gets the same text: the
	// routing command calls it, and so does Run() when the user quit before
	// that command's message was handled — otherwise the collected lines
	// would be dropped, and wt would exit under a restart still in flight.
	settle func() string
	// settled reports whether settle has finished, so Run() can tell a check
	// it still has to wait for from one that is merely unhandled. It is read
	// from another goroutine than the one settling, hence the atomic.
	settled *atomic.Bool
	// quitting is set once ctrl+c asked wt to quit. tea.Quit is a command:
	// its QuitMsg comes back through the message channel, so the check's
	// routeDoneMsg can still be delivered in between. A quitting check drops
	// it — the user is leaving, and launching an agent now would also record
	// rotation and refcount for a session they did not want. It mirrors
	// startState.cancelling. The state itself is kept so Run() can settle.
	quitting bool
}

type routeDoneMsg struct {
	id    int
	notes string
}
type routeTickMsg struct{ id int }

func routeTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return routeTickMsg{id: id} })
}

// checkLaunchRoute makes sure the launched model has its LiteLLM route
// before the launch (#192), then continues to launchSelected. A local model wt
// did not start has no route until something writes one, and a cloud model's
// route can be missing from a config.yaml that lost it; either way the agent
// launched on it would get "Invalid model name" from the proxy. The check writes that one
// route if it is missing and removes nothing, so a running sibling's route
// survives. It never fails the launch. mdl must be a row the probe reported
// running: this never starts a model. The caller has already established that
// the launch goes through LiteLLM.
//
// The check runs on the update goroutine, so it must not wait: it takes the
// config.yaml lock only if it is free right now (TryEnsureModelRouteTo), and a
// lock another wt holds is handed to the returned command, behind phaseRouting.
// EnsureModelRoute would instead block this goroutine for up to
// ensureRouteLockTimeout (10s) — frozen with no repaint and no key, ctrl+c
// included, and *before* the routing screen had been entered, so the one screen
// built to cover a route wait would be the one thing the wait prevented.
//
// What follows depends on what that first attempt could establish:
//
//   - Free lock, nothing written: nothing was restarted, so there is nothing to
//     wait for and the launch continues in this same Update — no routing
//     screen, not for a frame.
//   - Free lock, route written: the proxy is restarting, which takes 10–20 s.
//     Waiting for it here would freeze the picker with no status, so the model
//     enters phaseRouting and the wait runs in the returned command.
//   - Free lock, but a cloud model's row is missing: building it resolves the
//     provider's secret_ref, and an exec: helper runs synchronously, for up to
//     15 s (#253) — a wait this goroutine cannot pay either, so the check
//     defers the build (and reports it unfinished) and the command behind
//     phaseRouting makes it again where waiting is affordable. Same shape as a
//     contended lock, for the same reason.
//   - Contended lock: nothing was written and nothing is yet known, so the
//     command re-runs the whole check with the wait it is allowed to pay there,
//     then waits for the restart that check may start.
//
// Everything the check prints is collected rather than written to stderr,
// where the alt screen would hide it; recordRouteNotes keeps it for the real
// terminal. The check is handed the buffer itself, so only its own lines land
// there — the restart a failed start is still settling prints where it always
// did, and cannot turn up as this launch's note. The buffer is read at once
// when the check finished and nothing changed (nothing was started that could
// still write to it), and only after the wait otherwise: the restart's
// warnings are written by a goroutine, and the wait is what rejoins it.
//
// The seams and m.cfg are read HERE, on the update goroutine, and the command
// closes over the values, so a test's seam swap cannot race the command — the
// same reason runStart reads startModel before starting its goroutine.
func (m model) checkLaunchRoute(mdl config.Model) (model, tea.Cmd) {
	try, ensure, wait := tryEnsureModelRoute, ensureModelRoute, waitPendingRoutes
	cfg := m.cfg
	// A pointer: the command below outlives this Update and writes to it.
	captured := &bytes.Buffer{}
	changed, done := try(captured, cfg, mdl)
	if done && !changed {
		m.recordRouteNotes(captured.String())
		return m.launchSelected()
	}
	var once sync.Once
	var notes string
	settled := &atomic.Bool{}
	settle := func() string {
		once.Do(func() {
			if !done {
				// The lock was held, so the check above wrote nothing and got
				// no further than that. Run it again, this time willing to wait
				// for the lock: a retry that still fails reports itself, and
				// until it has run, whether the proxy is about to restart is
				// simply unknown — which is why this path is behind the screen
				// even though it may end up changing nothing.
				ensure(captured, cfg, mdl)
			}
			wait()
			// Only now: the restart's goroutine writes its warnings to the
			// buffer until the wait has returned.
			notes = captured.String()
			settled.Store(true)
		})
		return notes
	}
	m.routeRun++
	id := m.routeRun
	m.routing = &routingState{id: id, modelID: mdl.ID, began: time.Now(), settle: settle, settled: settled}
	m.phase = phaseRouting
	m.status = ""
	return m, tea.Batch(func() tea.Msg { return routeDoneMsg{id: id, notes: settle()} }, routeTick(id))
}

// recordRouteNotes keeps what a launch-time route check printed: it appends
// the text to pendingRouteNotes, which is printed on the real terminal once the
// alt screen is released — above the agent's output, or after wt exits when
// the launch did not happen. It does not go in the status line: the launch
// follows at once, so the only picker screen the user can land on next is the
// one a failed launch returns to, whose status is that failure.
func (m *model) recordRouteNotes(notes string) {
	pendingRouteNotes += notes
}

// handleRouteKey handles keys in phaseRouting. The route is already written
// and the proxy restart cannot be called off, so there is nothing to cancel:
// ctrl+c quits wt (Run() still collects the check's output on the way out) and
// every other key is ignored — including esc and q, which elsewhere would quit
// or pop back to a picker whose launch is still about to happen. ctrl+c also
// marks the check as quitting, so its routeDoneMsg cannot launch an agent in
// the gap before the QuitMsg is handled (see routingState.quitting).
func (m model) handleRouteKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		// A copy, not a write through the pointer: Update returns a new model
		// and must leave the one it was given as it found it. settle and
		// settled are shared by the copy, which is what keeps them once-only.
		quitting := *m.routing
		quitting.quitting = true
		m.routing = &quitting
		return m, tea.Quit
	}
	return m, nil
}

// handleRouteMsg processes the routing phase's messages. Its caller routes
// only routeDoneMsg/routeTickMsg here. A message whose id does not match the
// in-flight check, or that arrives outside phaseRouting, is stale and is
// dropped: acting on it would launch an agent the user did not ask for. So is
// any message for a check the user quit out of (routingState.quitting); its
// captured output is not lost, because Run() settles the check and prints it.
func (m model) handleRouteMsg(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case routeTickMsg:
		if m.routing == nil || m.phase != phaseRouting || msg.id != m.routing.id || m.routing.quitting {
			return m, nil
		}
		return m, routeTick(msg.id)
	case routeDoneMsg:
		if m.routing == nil || m.phase != phaseRouting || msg.id != m.routing.id || m.routing.quitting {
			return m, nil
		}
		m.routing = nil
		// Back to the picker's phase first, as finishStart does: every way
		// launchSelected can fail to launch returns to the model picker.
		m.phase = phaseModel
		// A window size that arrived during routing never reached the model
		// list, which is fitted only while its own phase is showing; Update's
		// fitLists does that once this message has been handled.
		m.recordRouteNotes(msg.notes)
		return m.launchSelected()
	}
	return m, nil
}

// routingView is phaseRouting's screen. It names no cancel key because there
// is none: see handleRouteKey.
func (m model) routingView() string {
	elapsed := time.Since(m.routing.began).Round(time.Second)
	return fmt.Sprintf("Updating the LiteLLM route for %s — restarting the proxy (%s)\n\n[ctrl+c] quit wt", m.routing.modelID, elapsed)
}

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
	// out is what the engine has printed so far (lifecycle.Options.Out).
	out *startOutput
}

// pastCancel reports whether the start has got past anything a cancel could
// call off: the engine reported the routing stage, which follows a model
// that is loaded (or, after a failed start that had displaced a model, the
// flow reported it for the proxy restart it waits for). What is left is a
// route write and a proxy restart, neither of which can be taken back, and
// since the wait moved to the start's goroutine (#275) it is 10 to 20
// seconds in which keys are handled. A cancel there would report "cancelled"
// over a model that is running and routed, or over the failure the start
// ended in. A cancel already draining when the stage arrives is in the same
// place: esc and q were ignored and ctrl+c quit there already.
func (s *startState) pastCancel() bool {
	return s.stage == lifecycle.StageRouting
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
	// out is every line the engine printed for this start, as it printed
	// them, set whether or not the start succeeded. It is complete: the
	// goroutine that sends this message has already waited for the proxy
	// restart the start may have left running.
	out string
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
// result, on the returned channel (closed after the result). What the engine
// prints is collected in the returned startOutput (#275).
//
// The goroutine also waits for the proxy restart the start may have left
// running, before it reports. Two things rest on that: the launch that
// follows a successful start dials the model through the proxy, and the
// restart prints its warnings into the same output, so the result carries
// all of it. The wait used to be finishStart's, on the update goroutine,
// where it froze the screen for as long as the restart took. After a start
// that failed there is a restart to wait for only when the start had already
// displaced a model; the stage then says so.
//
// The seams are read HERE, on the caller's goroutine, so a test seam swap
// never races the goroutine's late first read.
func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow bool, id int) (<-chan tea.Msg, *startOutput) {
	ch := make(chan tea.Msg, 16)
	start, wait, pending := startModel, waitPendingRoutes, routesPending
	out := &startOutput{}
	go func() {
		defer close(ch)
		stage := func(s lifecycle.Stage) {
			select {
			case ch <- startStageMsg{id: id, stage: s}:
			case <-ctx.Done():
			}
		}
		err := start(ctx, cfg, t, lifecycle.Options{AllowReplace: allow, Progress: stage, Out: out})
		if err != nil && pending() {
			// Not through stage: a cancelled start has a done context, and
			// the screen still has to say what it is waiting for.
			ch <- startStageMsg{id: id, stage: lifecycle.StageRouting}
		}
		wait()
		ch <- startDoneMsg{id: id, err: err, out: out.text()}
	}()
	return ch, out
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
	ch, out := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName, ModelID: it.model.ID}, allowReplace, id)
	m.start = &startState{id: id, item: it, began: time.Now(), cancel: cancel, ch: ch, out: out}
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
//
// From the routing stage on there is nothing left to cancel (pastCancel):
// esc and q are ignored and ctrl+c quits wt, as in phaseRouting.
func (m model) handleStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if m.start.pastCancel() {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
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

// statusNote is the engine's lines as the status shows them: each without
// the "wt: " it begins with, and the text without its final newline. "" when
// the engine printed nothing. The words are the engine's own — what omlx
// unloaded and whether wt saw it coming, a route that could not be written,
// a proxy restart that failed — so the picker and `wt start` say one thing.
func statusNote(out string) string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "wt: ")
	}
	return strings.Join(lines, "\n")
}

// withNote puts note on lines of its own above status. The view cuts each
// line of the status at the terminal's width, so on one line a note naming two
// models left no room for the failure after it.
func withNote(note, status string) string {
	if note == "" {
		return status
	}
	return note + "\n" + status
}

func (m model) finishStart(msg startDoneMsg) (model, tea.Cmd) {
	st := m.start
	m.start = nil
	st.cancel()
	// omlx can unload a model and then fail the load, or be cancelled, so the
	// engine's lines are shown on every way out, not only after a success.
	note := statusNote(msg.out)
	back := func(status string) (model, tea.Cmd) {
		m.status = withNote(note, status)
		if msg.out != "" {
			// The status cuts each line at the terminal's edge, and the next
			// key or launch replaces it. The real terminal keeps the lines
			// whole, with what became of the start under them, the way
			// `wt start` would have printed it.
			m.recordRouteNotes(msg.out + "wt: " + status + "\n")
		}
		m.phase = phaseModel
		// The table was built before the attempt and a start can have stopped
		// the occupant and then failed, so re-probe instead of showing stale
		// RUNNING.
		return m.refreshTable()
	}
	if st.cancelling && msg.err == nil {
		// The cancel lost: the engine was past stopping and the model is
		// loaded. The launch is called off, as asked; "cancelled" alone
		// would say the start was.
		return back(fmt.Sprintf("started %s; launch cancelled", st.item.model.ID))
	}
	if st.cancelling {
		return back("cancelled")
	}
	if msg.err == nil {
		m.phase = phaseModel
		// The launch that follows routes through LiteLLM. The route hook's
		// proxy restart is over: runStart's goroutine waited for it before
		// it sent this message, so nothing here waits.
		//
		// The launch follows a successful start, and proceedToLaunch clears
		// the status line, so the engine's lines take the route notes' way
		// out: onto the real terminal, above the agent's output. If that
		// launch fails the picker comes back instead, so launchSelected is
		// also handed the note and puts it ahead of the failure on the
		// status line.
		if note != "" {
			m.recordRouteNotes(msg.out)
			m.startNote = note
		}
		return m.proceedToLaunch()
	}
	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	title := ""
	switch {
	case errors.As(msg.err, &occ):
		title = fmt.Sprintf("Starting %s will stop %s", st.item.model.ID, strings.Join(occ.IDs(), ", "))
	case errors.As(msg.err, &unk):
		title = fmt.Sprintf("Cannot tell whether %s at %s is already serving a model", unk.ProviderID, unk.Origin)
	default:
		return back(lifecycle.StartErrorMessage(st.item.model.ID, msg.err))
	}
	// Nothing is touched before either refusal, so the engine has normally
	// printed nothing. If it has, the dialog has no status line for it: it
	// goes where it can be read later.
	m.recordRouteNotes(msg.out)
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
		waiting := "waiting for the server to stop"
		if m.start.stage == lifecycle.StageRouting {
			// The engine has returned; what is left is the proxy restart
			// that drops the route of a model the start had displaced.
			waiting = "waiting for the LiteLLM proxy restart"
		}
		return fmt.Sprintf("Cancelling %s… (%s)\n\n%s\n\n[ctrl+c] quit wt",
			m.start.item.model.ID, elapsed, waiting)
	}
	hint := "[esc] cancel"
	if m.start.pastCancel() {
		// Name only the key that still does something (handleStartKey).
		hint = "[ctrl+c] quit wt"
	}
	return fmt.Sprintf("Starting %s — %s (%s)\n\n%s", m.start.item.model.ID, lifecycle.StageLabel(m.start.stage), elapsed, hint)
}
