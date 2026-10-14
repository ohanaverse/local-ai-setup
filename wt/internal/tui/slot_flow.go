package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// Test seams of the slot-profile launch. Production writes config.yaml,
// restarts and probes the LiteLLM proxy and looks the agent binary up;
// TestMain makes each fail closed. The flow's wait for a restart in flight is
// the start flow's seam, waitPendingRoutes.
var (
	ensureSlotRoutes = lifecycle.EnsureModelRoutes
	restartProxyNow  = lifecycle.RestartProxyTo
	probeProxyNow    = lifecycle.ProbeProxy
	settleRoutesNow  = lifecycle.SettleRoutesTo
	buildSlotCmd     = agents.BuildSlotLaunchCmd
)

// settleOut collects what a settling restart prints when the flow starts it
// and goes back to the picker (dropSlot, finishStart): the alt screen is up,
// and that restart runs on for as long as the proxy has a request in flight
// to finish, printing from its own goroutine (#377). A line sent to stderr
// there lands on the picker and is gone at the next repaint. What is kept is
// printed with the route notes, on the real terminal (flushRouteNotes); once
// the alt screen is gone for good the rest is written straight through
// (flushRouteNotesAfterRun), since main() waits for that restart. The settles
// made after the alt screen (settleQuitSlot) print to stderr as they happen.
// Reset by Run() before launch.
var settleOut = &startOutput{}

// slotResult is what one off-goroutine stage of a profile launch found.
type slotResult struct {
	row   catalog.Row // the profile row as re-resolved in step 1
	owed  bool        // a proxy restart is still owed
	err   error
	notes string // what the route writes printed
}

// slotFlow is a profile launch between Enter and the agent's start.
type slotFlow struct {
	// id is the run id; a message carrying another one is from a superseded
	// stage and is dropped, as routingState.id does for route checks.
	id   int
	item *modelItem  // the highlighted profile row
	row  catalog.Row // set once the first stage reported
	// owed says config.yaml was changed for this launch and the proxy has
	// not been restarted for it yet.
	owed  bool
	began time.Time
	// settle runs the stage in flight, at most once however many callers
	// there are: the stage's command calls it, and so does Run() when the
	// user quit before that command's message was handled. nil between
	// stages (while the local model starts, or on the replace dialog).
	settle func() slotResult
	// settled reports whether settle has finished. It is read from another
	// goroutine than the one settling, hence the atomic.
	settled *atomic.Bool
	// quitting is set once ctrl+c asked wt to quit. The stage's message can
	// still arrive before the QuitMsg; a quitting flow drops it, so no agent
	// starts and nothing is recorded after the user left.
	quitting bool
}

type slotPreparedMsg struct { // steps 1-3 done, and 5-7 for a launch row
	id  int
	res slotResult
}
type slotSettledMsg struct { // steps 5-7 done after a start
	id  int
	res slotResult
}
type slotTickMsg struct{ id int }
type slotAbortMsg struct{ err error } // the applier failed after the terminal was released

func slotTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return slotTickMsg{id: id} })
}

// slotDeps are the seams one stage uses. They are read on the update
// goroutine and handed to the command, so a test's seam swap cannot race it.
type slotDeps struct {
	inventory func(*config.Config) localmodels.Snapshot
	ensure    func(context.Context, io.Writer, *config.Config, []config.Model, bool) (bool, error)
	restart   func(context.Context, io.Writer, *config.Config) error
	probe     func(context.Context, *config.Config) error
	wait      func()
}

// prepareSlot is the first stage: steps 1 to 3 of the launch flow and, for a
// row that needs no start, steps 5 to 7 (finishSlotRoutes). It runs in a
// command; nothing in it touches the model.
//
//  1. Re-resolve the profile against a fresh probe.
//  2. Refuse a passthrough flag that would replace the slot mapping.
//  3. Write the cloud slots' missing routes, restart deferred.
//
// A start row stops after step 3: the start that follows writes the local
// route and restarts the proxy itself, and is told a restart is owed.
func prepareSlot(d slotDeps, cfg *config.Config, agent string, extraArgs []string, check profiles.SlotCheck) slotResult {
	buf := &bytes.Buffer{}
	inv := d.inventory(cfg)
	row := catalog.ResolveSlotProfile(cfg, &inv, check)
	res := slotResult{row: row}
	if row.Action() == catalog.ActionBlock {
		res.err = errors.New(row.BlockReason())
		return res
	}
	if err := agents.CheckSlotPassthrough(agent, extraArgs); err != nil {
		res.err = err
		return res
	}
	owed, err := d.ensure(context.Background(), buf, cfg, row.Profile.Cloud(), false)
	if err != nil {
		return slotRouteFailed(d, res, buf, err)
	}
	if row.Action() == catalog.ActionStart {
		res.owed, res.notes = owed, buf.String()
		return res
	}
	return finishSlotRoutes(d, cfg, res, buf, owed)
}

// slotRouteFailed ends a stage whose route write failed: the error names the
// slot, and nothing is owed. The ensure settled what was owed with an
// asynchronous restart, which prints its warnings into buf until the wait
// returns.
func slotRouteFailed(d slotDeps, res slotResult, buf *bytes.Buffer, err error) slotResult {
	d.wait()
	res.owed, res.err, res.notes = false, catalog.SlotRouteError(res.row.Profile, err), buf.String()
	return res
}

// finishSlotRoutes is steps 5 to 7 for res.row, whose local model (if it has
// one) is running: the tail of the first stage for a launch row, and the
// whole stage after a start (settleSlotAfterStart).
//
//  5. Write the local model's route when it has none.
//  6. Restart the proxy once if a write changed config.yaml.
//  7. Wait for any proxy restart in flight, then probe the proxy.
//
// owed says an earlier write of this launch changed config.yaml.
func finishSlotRoutes(d slotDeps, cfg *config.Config, res slotResult, buf *bytes.Buffer, owed bool) slotResult {
	ctx := context.Background()
	if p := res.row.Profile; p.Local != nil {
		var err error
		if owed, err = d.ensure(ctx, buf, cfg, []config.Model{p.Local.Model}, owed); err != nil {
			return slotRouteFailed(d, res, buf, err)
		}
	}
	if owed {
		// Whether it succeeds or not, the restart was made: nothing is owed.
		// It prints into buf, not on stderr under the alt screen: it can say
		// that the proxy is finishing a request in flight, which it gives up
		// to three minutes, or that the request was cut off at that bound.
		err := d.restart(ctx, buf, cfg)
		res.notes = buf.String()
		if err != nil {
			res.err = err
			return res
		}
	}
	res.notes = buf.String()
	// A restart started earlier in this session (a start flow backed out
	// of, a profile dropped on the replace dialog) can still be in flight.
	// ProbeProxy does not wait for one: it would answer from the proxy about
	// to be replaced, before that restart's outcome is remembered.
	d.wait()
	if err := d.probe(ctx, cfg); err != nil {
		res.err = err
	}
	return res
}

// runSlotStage puts st in flight behind phaseSlot with stage as its work. The
// stage is wrapped the way checkLaunchRoute wraps its wait: once-only, so the
// command and Run()'s after-quit settle share one run and one result.
func (m model) runSlotStage(st slotFlow, stage func() slotResult, wrap func(id int, res slotResult) tea.Msg) (model, tea.Cmd) {
	var once sync.Once
	var res slotResult
	settled := &atomic.Bool{}
	settle := func() slotResult {
		once.Do(func() {
			res = stage()
			settled.Store(true)
		})
		return res
	}
	m.slotRun++
	id := m.slotRun
	st.id, st.settle, st.settled, st.quitting = id, settle, settled, false
	m.slot = &st
	m.phase = phaseSlot
	m.status = ""
	return m, tea.Batch(func() tea.Msg { return wrap(id, settle()) }, slotTick(id))
}

// beginSlotLaunch starts the launch of the profile row it. The row must not
// be blocked; its callers show a blocked row's reason instead.
func (m model) beginSlotLaunch(it *modelItem) (model, tea.Cmd) {
	if it == nil || it.profile == nil {
		return m, nil
	}
	d := currentSlotDeps()
	cfg, agent, extraArgs, check := m.cfg, m.agent, m.extraArgs, it.profile.Check
	// Any table refresh still in flight predates this launch.
	m.refreshGen++
	return m.runSlotStage(slotFlow{item: it, began: time.Now()},
		func() slotResult { return prepareSlot(d, cfg, agent, extraArgs, check) },
		func(id int, res slotResult) tea.Msg { return slotPreparedMsg{id: id, res: res} })
}

// currentSlotDeps reads the seams a stage uses. It is called on the update
// goroutine.
func currentSlotDeps() slotDeps {
	return slotDeps{inventory: runInventory, ensure: ensureSlotRoutes, restart: restartProxyNow, probe: probeProxyNow, wait: waitPendingRoutes}
}

// settleSlotAfterStart is the stage after the profile's local model started:
// steps 5 to 7 (finishSlotRoutes). The start restarted the proxy, which
// published the cloud routes too, so nothing is owed going in. Its route hook
// never fails the start, though: a route it could not write is only printed.
// So the model's route is checked here as a running model's is, and a failure
// aborts the launch. A row the start wrote compares equal, changes nothing and
// costs no restart.
func (m model) settleSlotAfterStart() (model, tea.Cmd) {
	d, cfg := currentSlotDeps(), m.cfg
	st := *m.slot
	st.owed = false
	row := st.row
	return m.runSlotStage(st,
		func() slotResult { return finishSlotRoutes(d, cfg, slotResult{row: row}, &bytes.Buffer{}, false) },
		func(id int, res slotResult) tea.Msg { return slotSettledMsg{id: id, res: res} })
}

// slotStageDone takes a finished stage's result into the flow: the row, what
// is owed, and the stage's notes, recorded once. It replaces the state with a
// copy, because Update must leave the model it was given as it found it.
func (m model) slotStageDone(res slotResult) model {
	st := *m.slot
	st.row, st.owed = res.row, res.owed
	st.settle, st.settled = nil, nil
	m.slot = &st
	m.recordRouteNotes(res.notes)
	return m
}

// handleSlotMsg processes the slot flow's messages. A message whose id is not
// the flow's, that arrives outside phaseSlot, or that belongs to a flow the
// user quit out of is dropped: acting on it would start an agent nobody asked
// for.
func (m model) handleSlotMsg(msg tea.Msg) (model, tea.Cmd) {
	live := func(id int) bool {
		return m.slot != nil && m.phase == phaseSlot && id == m.slot.id && !m.slot.quitting
	}
	switch msg := msg.(type) {
	case slotTickMsg:
		if !live(msg.id) {
			return m, nil
		}
		return m, slotTick(msg.id)
	case slotPreparedMsg:
		if !live(msg.id) {
			return m, nil
		}
		m = m.slotStageDone(msg.res)
		if msg.res.err != nil {
			return m.abortSlot(msg.res.err.Error())
		}
		if msg.res.row.Action() != catalog.ActionStart {
			return m.launchSlot()
		}
		// Step 4: the existing start flow, for the profile's one local model.
		p := msg.res.row.Profile
		if p == nil || p.Local == nil {
			return m.abortSlot(msg.res.row.Model.ID + " has no local model to start")
		}
		local := p.Local.Model
		// --replace is the -M pin's permission, as for a model row.
		allow := m.allowReplace && m.slot.item.model.ID == m.pinnedModel
		return m.beginStartOwed(&modelItem{model: local, line: local.ID}, allow, msg.res.owed)
	case slotSettledMsg:
		if !live(msg.id) {
			return m, nil
		}
		m = m.slotStageDone(msg.res)
		if msg.res.err != nil {
			return m.abortSlot(msg.res.err.Error())
		}
		return m.launchSlot()
	}
	return m, nil
}

// handleSlotKey handles keys in phaseSlot by handleRouteKey's rule: the
// routes are being written and a proxy restart cannot be called off, so
// ctrl+c quits wt (Run() settles the stage on the way out) and every other
// key is ignored.
func (m model) handleSlotKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		// A copy, not a write through the pointer. settle and settled are
		// shared by the copy, which is what keeps them once-only.
		quitting := *m.slot
		quitting.quitting = true
		m.slot = &quitting
		return m, tea.Quit
	}
	return m, nil
}

// slotView is phaseSlot's screen. It names no cancel key because there is
// none: see handleSlotKey.
func (m model) slotView() string {
	if m.slot == nil {
		return ""
	}
	elapsed := time.Since(m.slot.began).Round(time.Second)
	return fmt.Sprintf("Preparing %s — updating LiteLLM routes (%s)\n\n[ctrl+c] quit wt", m.slot.item.model.ID, elapsed)
}

// abortSlot ends the flow with no launch: back to the picker with reason as
// its status, over a re-probed table.
func (m model) abortSlot(reason string) (model, tea.Cmd) {
	m.slot = nil
	m.status = reason
	m.phase = phaseModel
	return m.refreshTable()
}

// dropSlot ends a flow the user backed out of on the replace dialog. A
// restart still owed by the cloud-route write is settled here, so the written
// routes go live; SettleRoutes only starts the restart, so this does not wait.
// The picker is what the user sees next, so what the restart prints is kept
// (settleOut).
func (m model) dropSlot() model {
	if m.slot == nil {
		return m
	}
	if m.slot.owed {
		settleRoutesNow(context.Background(), settleOut, m.cfg)
	}
	m.slot = nil
	return m
}

// launchSlot is steps 8 to 10: build the command through the driver's slot
// build and hand it to runSlotAndWaitCmd. It runs on the update goroutine and
// waits for nothing.
func (m model) launchSlot() (model, tea.Cmd) {
	row := m.slot.row
	cmd, err := buildSlotCmd(row.Profile.Launch(), m.selectedPath, m.yolo, m.cfg, m.extraArgs)
	if err != nil {
		return m.abortSlot("launch failed: " + err.Error())
	}
	queueLaunchNotes(m.agent, cmd, m.extraArgs)
	m.slot = nil
	// Back to the picker's phase first, as handleRouteMsg does: View can be
	// called once more before the terminal is released, and every way the
	// run command can fail returns to the model picker.
	m.phase = phaseModel
	return m, runSlotAndWaitCmd(cmd, m.agent, row)
}

// runSlotAndWaitCmd is runAndWaitCmd for a slot-profile launch. It releases
// the terminal, prints the route notes, applies the profile's args and
// wrapper through slotApplier, records the launch, runs the agent and
// captures the summary. It never calls profileApplier: an automatic profile
// is not applied to a slot-profile launch. An applier error, or no applier,
// runs nothing and records nothing, and comes back as a slotAbortMsg.
func runSlotAndWaitCmd(cmd *exec.Cmd, agent string, row catalog.Row) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
			defer func() {
				if currentProgram != nil {
					_ = currentProgram.RestoreTerminal()
				}
			}()
		}
		flushRouteNotes(os.Stderr)
		var cleanup func() error
		err := errors.New("no slot-profile applier")
		if slotApplier != nil {
			cleanup, err = slotApplier(cmd, agent, row)
		}
		if err != nil {
			// The terminal is released here, so this reaches the user's
			// stderr; the picker's status repeats it.
			fmt.Fprintf(os.Stderr, "wt: %v\n", err)
			return slotAbortMsg{err: err}
		}
		if cleanup == nil {
			cleanup = func() error { return nil }
		}
		// The commit point: the applier succeeded and the agent runs next.
		// Both writes are best-effort, as launchAndRecord's are.
		if err := rotation.New().RecordProfile(agent, row.Model.ID); err != nil {
			fmt.Fprintf(os.Stderr, "note: %v\n", err)
		}
		var ids []string
		for _, sm := range row.Profile.Distinct() {
			ids = append(ids, sm.ID)
		}
		if err := refcount.NewStore().RecordAll(os.Getpid(), ids); err != nil {
			fmt.Fprintf(os.Stderr, "note: refcount state not saved: %v\n", err)
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		duration, runErr := agents.RunAndCleanup(cmd, cleanup)
		shown := config.Model{ID: row.Model.ID}
		pendingSummary = agents.Summary(agent, shown, duration)
		pendingSurveyState = pendingSurvey{agent: agent, m: shown, profile: true}
		return launchDoneMsg{err: runErr}
	}
}

// settleQuitSlot is the slot flow's part of flushRouteNotesAfterRun: what wt
// still owes a profile launch the user quit out of, once the alt screen is
// gone. Every way out that leaves a restart owed ends in one settleRoutesNow,
// so no route stays written and unpublished; main() waits for that restart
// before the process exits.
//
//   - A stage in flight is settled first, announced when there is really
//     something to wait for, and its notes join the pending ones. A restart
//     it then still owes is started: a start row's first stage left it to a
//     start that will not happen now.
//   - On the replace dialog the refused start touched nothing, so the
//     restart is started.
//   - While the local model starts, the engine holds the owed restart. A quit
//     while a cancel drains does not wait for that start, so whether its
//     engine ever restarts is unknown (a refusal never does): the restart is
//     started here. If the engine makes its own as well, the proxy restarts
//     twice for a launch nobody is waiting for.
//   - A quit at the start's routing stage is waited for, and is settled after
//     that wait by settleQuitSlotStart, which knows how the start ended.
func settleQuitSlot(fm model, w io.Writer) {
	st := fm.slot
	if st == nil {
		return
	}
	owed := st.owed
	switch {
	case st.settle != nil:
		if !st.settled.Load() {
			fmt.Fprintln(w, "wt: waiting for the LiteLLM proxy restart…")
		}
		res := st.settle()
		pendingRouteNotes += res.notes
		owed = res.owed
	case fm.phase == phaseReplaceConfirm:
	case fm.start != nil && !fm.start.quitting:
	default:
		return
	}
	if owed {
		settleRoutesNow(context.Background(), nil, fm.cfg)
	}
}

// settleQuitSlotStart is settleQuitSlot for a profile whose local model's
// start the user quit out of at the routing stage. flushRouteNotesAfterRun
// calls it once that start's goroutine has finished (settleQuitStart). The
// start was told a restart is owed, and its result was dropped by the
// quitting flow, so finishStart never settled a refusal: the restart is
// started here when the start left it (lifecycle.RestartStillOwed). Every
// other outcome made the one restart itself. A start that somehow has not
// finished is treated as one that left it.
func settleQuitSlotStart(fm model) {
	if fm.slot == nil || !fm.slot.owed || fm.start == nil || fm.start.result == nil {
		return
	}
	if err, done := fm.start.result.get(); done && !lifecycle.RestartStillOwed(err) {
		return
	}
	settleRoutesNow(context.Background(), nil, fm.cfg)
}
