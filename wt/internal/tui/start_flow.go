package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// startModel is a test seam: production starts the model through the lifecycle
// engine.
var startModel = lifecycle.Start

// waitPendingRoutes is a test seam over lifecycle.WaitPendingRoutes. The route
// hook's LiteLLM proxy restart runs asynchronously, and both of its callers
// here hand straight off to the launch flow, whose agent dials the model
// THROUGH the proxy: launching while the restart is in flight can meet a
// refused connection or the pre-restart route table. Neither waits on the
// Bubble Tea update goroutine, which is the one that repaints the screen and
// answers keys: a start waits on its own goroutine (runStart), behind the
// start screen's routing stage, and the launch-time route check
// (checkLaunchRoute) waits in a command, behind phaseRouting. Of a start's
// waits this is only the one after a start that failed; a start that
// succeeded waits inside settleStart.
var waitPendingRoutes = lifecycle.WaitPendingRoutes

// settleStart is a test seam over lifecycle.SettleStart: what follows a start
// the engine reported done — the wait for the proxy restart, the one probe
// after it, so a model another terminal stopped during that wait is reported
// instead of launched on (#343), and the wait for the restart that probe's
// route removal starts. It waits, asks the provider's server and, when the
// server is gone, writes config.yaml, so it is runStart's goroutine that
// calls it, never the update goroutine. It is handed the start's own writer:
// under the alt screen stderr is hidden.
var settleStart = lifecycle.SettleStart

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
//   - Free lock, nothing written, no restart in flight: there is nothing to
//     wait for and the launch continues in this same Update — no routing
//     screen, not for a frame.
//   - Free lock, nothing written, but a restart started earlier in this session
//     is still in flight (a profile dropped on the replace dialog, a start
//     backed out of: settleRoutesNow): the proxy refuses connections while it
//     drains, for up to three minutes, so the launch waits for it behind
//     phaseRouting, as `wt` does before a launch (ensureRouteBeforeLaunch).
//   - Free lock, route written: the proxy is restarting, which takes 10–20 s,
//     and up to three minutes more when it has a request in flight to finish.
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
	try, ensure, wait, pending := tryEnsureModelRoute, ensureModelRoute, waitPendingRoutes, routesPending
	cfg := m.cfg
	// A pointer: the command below outlives this Update and writes to it.
	captured := &noticeBuffer{}
	changed, done := try(captured, cfg, mdl)
	if done && !changed && !pending() {
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
	head := fmt.Sprintf("Updating the LiteLLM route for %s — restarting the proxy (%s)", m.routing.modelID, elapsed)
	return withDrain(head, m.width) + "\n\n[ctrl+c] quit wt"
}

// startState is the in-flight start: which row, the current stage, when it
// began, how to cancel it, and the channel carrying its messages.
type startState struct {
	id   int
	item *modelItem
	// allow and others are what this attempt was given (AllowReplace and the
	// others plan), so a refusal's dialog knows which questions are still
	// open and what its retry carries.
	allow      bool
	others     othersPlan
	stage      lifecycle.Stage
	began      time.Time
	cancel     context.CancelFunc
	cancelling bool
	ch         <-chan tea.Msg
	// out is what the engine has printed so far (lifecycle.Options.Out).
	out *startOutput
	// result is what became of the start, once its goroutine has finished.
	// It is for Run()'s flush after a quit, when the startDoneMsg was dropped
	// or never read.
	result *startResult
	// quitting is set once ctrl+c at the routing stage asked wt to quit.
	// tea.Quit is a command: its QuitMsg comes back through the message
	// channel, so the start's startDoneMsg can still be delivered in between,
	// and a successful one would launch. A quitting start drops it, as
	// routingState.quitting does for the route check. The state itself is
	// kept so Run() can print what the engine printed (startOutput.release).
	quitting bool
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

// startResult holds a start's outcome where the goroutine that ran it and
// the flush after a quit can both reach it. The goroutine sets it before it
// sends its startDoneMsg, so it is set by the time the channel is closed.
type startResult struct {
	mu   sync.Mutex
	done bool
	err  error
}

func (r *startResult) set(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done, r.err = true, err
}

// get returns the start's error and whether the start has finished.
func (r *startResult) get() (err error, done bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err, r.done
}

// sessionCounts is the number of live wt sessions using each model id, read
// once when the replace dialog is built (#412). A test seam; production is
// the non-TUI prompt's source, refcount.SessionCounts.
var sessionCounts = refcount.SessionCounts

// newReplaceChoices is the replace dialog's list of choices. The list's own
// title is one line, cut at the terminal's edge, so the question is the
// frame's header instead (replaceFrames), wrapped; the header takes the lines
// the title and the item count took, so a short terminal still fits it.
func newReplaceChoices(theme themes.Theme, width, height int, items []list.Item) list.Model {
	choices := list.New(items, ThemedListDelegate(theme), width, height)
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
	// wt can stop: exactly what "stop" approves.
	stop []string
}

type startStageMsg struct {
	id    int
	stage lifecycle.Stage
}
type startDoneMsg struct {
	id int
	// err is what became of the start: the engine's error, or for a start the
	// engine reported done, a *lifecycle.StoppedError when its server was
	// gone again by the end of the proxy wait (settleStart, #343).
	err error
	// out is every line the engine printed for this start, as it printed
	// them, set whether or not the start succeeded, and whatever the check
	// after the wait printed. It is complete: the goroutine that sends this
	// message has already waited for every proxy restart the start, or that
	// check's route removal, may have left running.
	out string
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

// stoppableIDs are the model ids of the residents wt can stop, in the
// engine's order: what a stop answer approves.
func stoppableIDs(others []lifecycle.Resident) []string {
	var ids []string
	for _, r := range others {
		if r.Stoppable {
			ids = append(ids, r.Entry.ModelID)
		}
	}
	return ids
}

// retryStart starts the refused row again with the answers the user gave:
// a profile launch's start carries the restart it still owes.
func (m model) retryStart(it *modelItem, allow bool, others othersPlan) (model, tea.Cmd) {
	if m.slot != nil {
		return m.beginStartOwed(it, allow, others, m.slot.owed)
	}
	return m.beginStart(it, allow, others)
}

// runStart runs the engine in a goroutine and streams its stages, then its
// result, on the returned channel (closed after the result). allow and
// others are the attempt's answers (Options.AllowReplace, Options.Others),
// passed as given: a first attempt leaves others unanswered unless --others
// answered it for the pinned row (othersFor), so the engine's refusal lists
// other providers' models for the dialog (#412). What the engine
// prints is collected in the returned startOutput (#275), and the result is
// also kept in the returned startResult.
//
// The goroutine also waits for the proxy restart the start may have left
// running, before it reports. Two things rest on that: the launch that
// follows a successful start dials the model through the proxy, and the
// restart prints its warnings into the same output, so the result carries
// all of it. The wait used to be finishStart's, on the update goroutine,
// where it froze the screen for as long as the restart took. Whenever there
// is a restart to wait for, the goroutine reports the routing stage itself,
// so the screen says so: after a start that failed (one that had already
// displaced a model), and after one that succeeded under a cancel, whose own
// report of the stage went through a context the cancel had ended.
//
// A start the engine reported done is settled with one call (settleStart,
// lifecycle.SettleStart): that wait, then the check that the server is still
// there (#343) — the wait is where another terminal's `wt stop` lands — and,
// when it is not, the wait for the restart the check's route removal starts.
// It belongs here for the reason the wait does — it is a request to the
// provider's server and, when the server is gone, a config.yaml write that
// can wait for the file's lock, none of which the update goroutine can pay —
// and because the result has to carry it: a server that is gone is sent as
// the start's error, so finishStart treats it as any failed start and no
// launch, and no launch-time route check, follows. What the check prints goes
// to the start's own output, complete when the call returns, and wt does not
// go back to the picker under a restart. A start that failed has only the
// wait.
//
// The check runs whether or not the start was cancelled: a cancel that lost
// is about to be reported as "started …", which has to be true as well. The
// start's context is handed over as it is; lifecycle.SettleStart keeps its
// values and drops its cancellation.
//
// The seams are read HERE, on the caller's goroutine, so a test seam swap
// never races the goroutine's late first read.
func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow bool, others lifecycle.OthersAnswer, restartOwed bool, id int) (<-chan tea.Msg, *startOutput, *startResult) {
	ch := make(chan tea.Msg, 16)
	start, wait, pending, settle := startModel, waitPendingRoutes, routesPending, settleStart
	out := &startOutput{}
	result := &startResult{}
	go func() {
		defer close(ch)
		stage := func(s lifecycle.Stage) {
			select {
			case ch <- startStageMsg{id: id, stage: s}:
			case <-ctx.Done():
			}
		}
		err := start(ctx, cfg, t, lifecycle.Options{AllowReplace: allow, Progress: stage, Out: out, RestartOwed: restartOwed, Others: others})
		if pending() {
			// Not through stage: a cancelled start has a done context, and
			// the screen still has to say what it is waiting for.
			ch <- startStageMsg{id: id, stage: lifecycle.StageRouting}
		}
		if err == nil {
			err = settle(ctx, out, cfg, t)
		} else {
			wait()
		}
		result.set(err)
		ch <- startDoneMsg{id: id, err: err, out: out.text()}
	}()
	return ch, out, result
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
	answer lifecycle.OthersAnswer
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
		return othersPlan{answer: lifecycle.OthersAnswer{Decided: true}}
	case "stop":
		return othersPlan{stopAll: true}
	}
	return othersPlan{}
}

// beginStart starts it through the engine and enters phaseStarting.
func (m model) beginStart(it *modelItem, allowReplace bool, others othersPlan) (model, tea.Cmd) {
	return m.beginStartOwed(it, allowReplace, others, false)
}

// beginStartOwed is beginStart for a caller that already owes a proxy
// restart: a profile launch whose cloud-route write changed config.yaml. The
// engine's one restart then publishes that write too (Options.RestartOwed).
func (m model) beginStartOwed(it *modelItem, allowReplace bool, others othersPlan, restartOwed bool) (model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.startRun++
	// Any refresh still in flight predates this start; finishStart issues its own.
	m.refreshGen++
	id := m.startRun
	ch, out, result := runStart(ctx, m.cfg, lifecycle.Target{ProviderID: it.model.ProviderID, ModelName: it.model.ModelName, ModelID: it.model.ID}, allowReplace, others.answer, restartOwed, id)
	m.start = &startState{id: id, item: it, allow: allowReplace, others: others, began: time.Now(), cancel: cancel, ch: ch, out: out, result: result}
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
// esc and q are ignored and ctrl+c quits wt, as in phaseRouting. That ctrl+c
// also marks the start as quitting, so its startDoneMsg cannot launch an
// agent in the gap before the QuitMsg is handled (see startState.quitting).
func (m model) handleStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if m.start.pastCancel() {
		if msg.String() == "ctrl+c" {
			// A copy, not a write through the pointer, as in handleRouteKey:
			// Update must leave the model it was given as it found it.
			quitting := *m.start
			quitting.quitting = true
			m.start = &quitting
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

// withNote puts note on lines of its own above status: the note is the
// engine's lines, one each, and the status is what became of the start or of
// the launch after it.
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
		if m.slot != nil {
			// This start belonged to a profile launch, which ends here. The
			// engine settled the owed restart on every error but a refusal;
			// a refusal can only reach this way out under a cancel. The
			// picker follows, so what the restart prints is kept (settleOut).
			if m.slot.owed && lifecycle.RestartStillOwed(msg.err) {
				settleRoutesNow(context.Background(), settleOut, m.cfg)
			}
			m.slot = nil
		}
		m.status = withNote(note, status)
		if msg.out != "" {
			// The next key or launch replaces the status, and one too tall
			// for the terminal is cut in its middle (statusAlone). The real
			// terminal keeps the lines whole, with what became of the start
			// under them, the way `wt start` would have printed it.
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
	var stopped *lifecycle.StoppedError
	if st.cancelling && !errors.As(msg.err, &stopped) {
		return back("cancelled")
	}
	// A start that was cancelled, succeeded anyway and was then found
	// stopped is reported as what it is, below: "cancelled" would name the
	// cancel as what stopped it, and nothing else would say that the model
	// the screen showed loading is not running.
	if msg.err == nil {
		m.phase = phaseModel
		if m.slot != nil {
			// The started model is a profile's local model. Its start wrote
			// the route and restarted the proxy, which published the profile's
			// cloud routes with it. The launch that follows is the profile's,
			// never proceedToLaunch's single-model one: probe, then build.
			if note != "" {
				m.recordRouteNotes(msg.out)
			}
			return m.settleSlotAfterStart()
		}
		// The launch that follows routes through LiteLLM. The route hook's
		// proxy restart is over and the server was still there after it:
		// runStart's goroutine waited for the one and asked about the other
		// (settleStart) before it sent this message, so nothing here
		// waits. A server that was gone came as an error instead, and never
		// reaches proceedToLaunch's route check, which would write the route
		// of a row the table still shows as started straight back.
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
	rs := &replaceState{item: st.item, others: st.others, allow: st.allow}
	items := buildReplaceChoices()
	switch {
	case errors.As(msg.err, &occ):
		// The engine lists occupants only while AllowReplace is false, and
		// other providers' models only while the others question is open.
		sameFamily := len(occ.Occupants) > 0
		othersAsked := (len(occ.Others) > 0 || len(occ.OthersUnknown) > 0) && !st.others.answer.Decided
		if othersAsked && st.others.stopAll {
			// --others=stop answers for the pinned row, as the non-TUI start
			// does (decideOthers): every resident wt can stop.
			rs.others = othersPlan{answer: lifecycle.OthersAnswer{Decided: true, Stop: stoppableIDs(occ.Others)}}
			othersAsked = false
			if !sameFamily {
				m.recordRouteNotes(msg.out)
				return m.retryStart(st.item, st.allow, rs.others)
			}
		}
		if !sameFamily && !othersAsked {
			// A refusal that asks only what was answered: report it rather
			// than ask again.
			return back(lifecycle.StartErrorMessage(st.item.model.ID, msg.err))
		}
		rs.header = refusalHeader(m.cfg, st.item.model.ID, occ, sameFamily, othersAsked)
		if othersAsked {
			rs.allow = st.allow || sameFamily
			rs.stop = stoppableIDs(occ.Others)
			items = buildOthersChoices(st.item.model.ID, sameFamily, len(occ.Others) > 0, len(rs.stop))
		}
	case errors.As(msg.err, &unk):
		rs.header = fmt.Sprintf("Cannot tell whether %s at %s is already serving a model", unk.ProviderID, unk.Origin)
	default:
		return back(lifecycle.StartErrorMessage(st.item.model.ID, msg.err))
	}
	// Nothing is touched before either refusal, so the engine has normally
	// printed nothing. If it has, the dialog has no status line for it: it
	// goes where it can be read later.
	m.recordRouteNotes(msg.out)
	rs.choices = newReplaceChoices(m.theme, m.width-2, m.height-2, items)
	m.replace = rs
	m.phase = phaseReplaceConfirm
	return m, nil
}

// refusalHeader is the dialog's question for an *OccupiedError: the
// same-family occupants the start would stop (sameFamily), then the other
// providers' models it would load beside (othersAsked, #412), each with the
// memory it holds as wt knows it, and a line per family wt cannot read. Each
// model some wt session uses is named with its count, in the non-TUI
// prompt's words; the counts are read once, here. The picker has recorded no
// session of its own yet (launchAndRecord comes after), so a count is other
// sessions only. One model per line: a count ends its line, so on one shared
// line it is what the terminal's edge would cut.
func refusalHeader(cfg *config.Config, id string, occ *lifecycle.OccupiedError, sameFamily, othersAsked bool) string {
	ids := occ.IDs()
	if othersAsked {
		for _, r := range occ.Others {
			ids = append(ids, r.Entry.ModelID)
		}
	}
	counts := map[string]int{}
	if len(ids) > 0 {
		counts = sessionCounts(ids)
	}
	inUse := func(oid string) string {
		if n := counts[oid]; n > 0 {
			return fmt.Sprintf(" (in use by %d wt session(s))", n)
		}
		return ""
	}
	var lines []string
	if sameFamily {
		lines = append(lines, fmt.Sprintf("Starting %s will stop:", id))
		for _, oid := range occ.IDs() {
			lines = append(lines, "  "+oid+inUse(oid))
		}
	}
	if othersAsked {
		lines = append(lines, "Also loaded, by other providers:")
		for _, r := range occ.Others {
			lines = append(lines, "  "+r.Label()+" — "+r.Figure()+inUse(r.Entry.ModelID))
		}
		for _, f := range occ.OthersUnknown {
			lines = append(lines, fmt.Sprintf("  cannot tell whether %s at %s is serving a model", f, localmodels.FamilyOrigin(cfg, f)))
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) startingView() string {
	elapsed := time.Since(m.start.began).Round(time.Second)
	if m.start.cancelling {
		// Name only the key that still does something: esc and q are ignored
		// while the engine tears down, so advertising them would be a lie.
		waiting := "waiting for the server to stop"
		if m.start.stage == lifecycle.StageRouting {
			// The engine has returned; what is left is the proxy restart:
			// for the route of a model the start had displaced, or for the
			// started model's own when the cancel lost.
			waiting = "waiting for the LiteLLM proxy restart"
		}
		return withDrain(tuilayout.WrapText(fmt.Sprintf("Cancelling %s… (%s)\n\n%s", m.start.item.model.ID, elapsed, waiting), m.width), m.width) +
			"\n\n[ctrl+c] quit wt"
	}
	hint := "[esc] cancel"
	if m.start.pastCancel() {
		// Name only the key that still does something (handleStartKey).
		hint = "[ctrl+c] quit wt"
	}
	head := "Starting " + m.start.item.model.ID
	stage := fmt.Sprintf("%s (%s)", lifecycle.StageLabel(m.start.stage), elapsed)
	line := head + " — " + stage
	if m.width > 0 && lipgloss.Width(line) > m.width {
		// The stage is what the screen is for, and from the routing stage
		// on it is the only reason given for a key that stopped working: on
		// a line of its own it is not what the terminal's edge cuts.
		line = head + "\n" + stage
	}
	return withDrain(tuilayout.WrapText(line, m.width), m.width) + "\n\n" + hint
}
