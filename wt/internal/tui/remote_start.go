package tui

// The start and launch flow on the model server (the API path, m.client set):
// Enter on a row registers the client session, runs a start job when the row
// needs one (modelclient.RunStart, in a command) and launches the agent on the
// gateway. The registry path's flow (start_flow.go, launch.go) is untouched.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// launchHooks are the per-model test seams over the launch builds. The zero
// value is the real builds, so production never sets one; a test sets a hook
// on the model it constructs, and it travels with the model through bubbletea's
// value copies.
type launchHooks struct {
	// gateway replaces agents.BuildLaunchCmdGateway, the agent command against
	// the model server's gateway (cmd/wt-session's `hooks.build` on the
	// non-TUI path).
	gateway gatewayBuildFunc
	// slot replaces agents.BuildSlotLaunchCmdGateway, the slot-profile build
	// (launchRemoteSlot).
	slot slotBuildFunc
}

// gatewayBuildFunc is the gateway agent build (agents.BuildLaunchCmdGateway's
// shape); a nil hook means the real agents builder.
type gatewayBuildFunc func(agent string, mdl config.Model, path string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error)

// slotBuildFunc is the slot-profile agent build (agents.BuildSlotLaunchCmdGateway's
// shape); a nil hook means the real agents builder.
type slotBuildFunc func(profiles.SlotLaunch, string, bool, agents.Gateway, []string) (*exec.Cmd, error)

// buildLaunch is the agent build: the hook, or the real one when unset.
func (h launchHooks) buildLaunch() gatewayBuildFunc {
	if h.gateway != nil {
		return h.gateway
	}
	return agents.BuildLaunchCmdGateway
}

// buildSlotLaunch is the slot build: the hook, or the real one when unset.
func (h launchHooks) buildSlotLaunch() slotBuildFunc {
	if h.slot != nil {
		return h.slot
	}
	return agents.BuildSlotLaunchCmdGateway
}

// clientNotes collects what the API path has to say while the alt screen is
// up and stderr is not the user's: a register that failed for a launch row,
// the client session's heartbeat notes and the launch's pre-launch notes. It
// is printed with the route notes at both emit points (flushRouteNotes) and
// released to the terminal once the program is over. Reset by Run.
var clientNotes = &startOutput{what: "client session"}

// clientHold is the picker's client session with the model server: created on
// the first model chosen, it holds every model a start or a launch is for and
// ends when the picker's run ends (exitTail). Every method that talks to the
// server runs in a command, never in Update; it is safe for concurrent use.
type clientHold struct {
	client ModelClient

	mu   sync.Mutex
	sess ClientSession
	held map[string]bool
	// registered is true once a register succeeded: an End after only
	// refused registers is refused too, which is not news (cmd/wt-session's rule).
	registered bool
	ended      bool
}

// hold puts id into the client session, registering it on the first call
// (which starts its heartbeat) and re-registering the set after. It returns
// the note for a register that failed, "" otherwise: a failed register never
// refuses the start or the launch (client sessions are advisory).
func (h *clientHold) hold(ctx context.Context, id string) string {
	return h.holdAll(ctx, []string{id})
}

// holdAll is hold for several models at once (a slot profile's distinct
// models): one register, or one re-register, carrying every id not yet held.
func (h *clientHold) holdAll(ctx context.Context, ids []string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ended {
		return ""
	}
	var fresh []string
	for _, id := range ids {
		if !h.held[id] && !slices.Contains(fresh, id) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 {
		return ""
	}
	var err error
	if h.sess == nil {
		h.sess = h.client.NewSession(fresh, modelclient.SessionOptions{Note: func(s string) {
			fmt.Fprintf(clientNotes, "wt: note: %s\n", cleanup.Flatten(s))
		}})
		err = h.sess.Start(ctx)
	} else {
		err = h.sess.Add(ctx, fresh...)
	}
	if h.held == nil {
		h.held = map[string]bool{}
	}
	for _, id := range fresh {
		h.held[id] = true
	}
	if err != nil {
		return fmt.Sprintf("the model server did not register this client session (%s) — the launch goes on, but the server may stop %s once it is idle", cleanup.Flatten(err.Error()), strings.Join(fresh, ", "))
	}
	h.registered = true
	return ""
}

// drop takes id out of the client session (a re-register without it):
// nothing will use the model.
func (h *clientHold) drop(ctx context.Context, id string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ended || !h.held[id] {
		return
	}
	delete(h.held, id)
	_ = h.sess.Remove(ctx, id)
}

// end ends the client session once, on a context nothing cancels, and says
// so on w only when it fails after a register the server took.
func (h *clientHold) end(w io.Writer) {
	if h == nil {
		return
	}
	h.mu.Lock()
	sess, registered, ended := h.sess, h.registered, h.ended
	h.ended = true
	h.mu.Unlock()
	if ended || sess == nil {
		return
	}
	if err := sess.End(context.Background()); err != nil && registered {
		fmt.Fprintf(w, "wt: note: could not end the client session on the model server (%s) — it forgets it within a minute\n", cleanup.Flatten(err.Error()))
	}
}

// exitTail is what the post-exit flow does after the agent: end the client
// session (and nothing else; the server's idle grace stops models). A program
// with no model server (a pinned command agent) has nothing to end.
type exitTail struct {
	end func()
	// priceNote prints the stale-pricing notice from the model server's
	// catalog (the registry's stamps are not read on this path).
	priceNote func()
}

// exitTailOf is the post-exit tail for the program's final model.
func exitTailOf(m model) exitTail {
	if m.client == nil {
		return exitTail{}
	}
	h := m.hold
	client, pricing, seen := m.client, m.catPricing, m.catSeen
	return exitTail{
		end:       func() { h.end(os.Stderr) },
		priceNote: func() { agents.PrintRemotePriceNotice(context.Background(), client, pricing, seen) },
	}
}

// release is the tail's first step: the client session's end.
func (t exitTail) release() {
	if t.end != nil {
		t.end()
	}
}

// wireOthers is an others plan as the start request carries it: nil asks,
// an empty stop keeps them all.
func wireOthers(p othersPlan) *modelapi.OthersAnswer {
	if !p.answer.Decided {
		return nil
	}
	stop := p.answer.Stop
	if stop == nil {
		stop = []string{}
	}
	return &modelapi.OthersAnswer{Stop: stop}
}

// chooseRemote is Enter (or a -M pin) on a row of the API table: a blocked
// row says why; a row the server must start runs the start flow; any other
// launches. pinned grants --replace and --others, the pin's permissions.
func (m model) chooseRemote(it *modelItem, pinned bool) (model, tea.Cmd) {
	if it.blocked != "" {
		m.status = it.blocked
		return m, nil
	}
	if m.hold == nil {
		m.hold = &clientHold{client: m.client}
	}
	// A slot profile is its own launch unit (slot_flow.go); any earlier
	// profile launch this row supersedes is forgotten.
	m.rslot = nil
	if it.profile != nil {
		return m.beginRemoteSlotLaunch(it)
	}
	row, ok := m.wire[it.model.ID]
	if ok && catalog.NeedsStart(row) {
		return m.beginRemoteStart(it, m.allowReplace && pinned, m.othersFor(pinned))
	}
	return m.launchRemote(it)
}

// beginRemoteStart runs a start job for it on the model server, behind the
// start screen. The command registers the client session first, so the
// server's idle scan sees the model before it exists.
func (m model) beginRemoteStart(it *modelItem, allow bool, others othersPlan) (model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.startRun++
	id := m.startRun
	req := modelapi.StartRequest{Model: it.model.ID, AllowReplace: allow, Others: wireOthers(others)}
	ch, _ := runRemoteStart(ctx, m.client, m.hold, req, id)
	m.start = &startState{id: id, item: it, allow: allow, others: others, began: time.Now(), cancel: cancel, ch: ch}
	m.phase = phaseStarting
	m.status = ""
	return m, waitForStart(ch)
}

// runRemoteStart registers the model in the client session and runs the
// start job in a goroutine, streaming each poll's view (startStageMsg) and
// then its result (startDoneMsg) on the returned channel, closed after the
// result. Questions are not answered here (no Answerer): the job's problem
// comes back in the result and the dialog asks it, then starts again with the
// answer. A start that fails re-registers the session without the model; one
// whose ctx was cancelled (a detach, a quit) leaves that to its canceller.
func runRemoteStart(ctx context.Context, c ModelClient, h *clientHold, req modelapi.StartRequest, id int) (<-chan tea.Msg, <-chan struct{}) {
	ch := make(chan tea.Msg, 16)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer close(ch)
		note := h.hold(ctx, req.Model)
		prog := func(v modelclient.JobView) {
			select {
			case ch <- startStageMsg{id: id, job: &v}:
			case <-ctx.Done():
			}
		}
		_, err := c.RunStart(ctx, req, nil, prog)
		if err != nil && ctx.Err() == nil {
			h.drop(ctx, req.Model)
		}
		done := startDoneMsg{id: id, err: err, note: note}
		if ctx.Err() != nil {
			// Detached or quit: nothing may read the channel again, so a
			// full one must not hold this goroutine forever.
			select {
			case ch <- done:
			default:
			}
			return
		}
		ch <- done
	}()
	return ch, finished
}

// canCancelJob is the one predicate behind c on the start screen (T5): the
// footer shows it and the key acts exactly while it holds — once the job has
// an id, and not while a cancel is already on its way.
func (s *startState) canCancelJob() bool {
	return s.jobID != "" && !s.cancelSent
}

// jobCancelMsg is the server's answer to c.
type jobCancelMsg struct {
	id  int
	err error
}

// handleRemoteStartKey is the start screen's keyboard on the API path (R8):
// ctrl+c detaches once the job has an id (the job keeps running) and leaves
// wt before it has one (T1); c cancels the job (canCancelJob). Every other
// key does nothing — esc and q included, on purpose: the footer names only
// keys that act (T5), and ctrl+c is the one way off this screen.
func (m model) handleRemoteStartKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.start.jobID == "" {
			// A copy, as handleStartKey's quit: Update leaves the model it was
			// given as it found it. quitting drops the start's late result.
			quitting := *m.start
			quitting.quitting = true
			m.start = &quitting
			return m, tea.Quit
		}
		st := m.start
		st.cancel() // stops the polls, never the job
		m.start = nil
		m.phase = phaseModel
		m.status = fmt.Sprintf("detached — job %s keeps running on the model server", st.jobID)
		ids := m.startDropIDs(st.item.model.ID)
		m.rslot = nil
		return m, dropAllCmd(m.hold, ids)
	case "c":
		if !m.start.canCancelJob() {
			return m, nil
		}
		next := *m.start
		next.cancelSent, next.cancelAsked, next.cancelErr = true, true, ""
		m.start = &next
		c, runID, jobID := m.client, next.id, next.jobID
		return m, func() tea.Msg {
			return jobCancelMsg{id: runID, err: c.CancelJob(context.Background(), jobID)}
		}
	}
	return m, nil
}

// handleJobCancel applies the server's answer to c. A cancel the server
// refused is said on the start screen and c is offered again; an accepted one
// ends the job, whose result returns to the table.
func (m model) handleJobCancel(msg jobCancelMsg) (model, tea.Cmd) {
	if m.start == nil || msg.id != m.start.id || msg.err == nil {
		return m, nil
	}
	next := *m.start
	next.cancelSent = false
	next.cancelErr = "could not cancel job " + next.jobID + ": " + cleanup.Flatten(msg.err.Error())
	m.start = &next
	return m, nil
}

// remoteProgress applies one poll's view to the start screen.
func (m model) remoteProgress(v *modelclient.JobView) model {
	next := *m.start
	view := *v
	next.job = &view
	if id := cleanup.Flatten(v.ID); id != "" {
		next.jobID = id
	}
	m.start = &next
	return m
}

// finishRemoteStart applies a start job's result: a success launches the
// model on the gateway; a question (occupied, others, occupancy unknown) not
// yet answered opens the dialog the registry path opens; anything else goes
// back to the table with what happened on the status line.
func (m model) finishRemoteStart(msg startDoneMsg) (model, tea.Cmd) {
	st := m.start
	m.start = nil
	st.cancel()
	note := msg.note
	back := func(status string) (model, tea.Cmd) {
		m.status = withNote(note, status)
		m.phase = phaseModel
		if m.rslot != nil && m.rslot.row.Profile != nil {
			// The job already took its own model out; a slot profile's other
			// models were held for this launch alone.
			ids := slotModelIDs(m.rslot.row)
			m.rslot = nil
			return m, dropAllCmd(m.hold, ids)
		}
		return m, nil
	}
	if msg.err == nil && st.cancelAsked {
		// c lost the race: the job was past cancelling (or the server
		// refused the cancel because it had finished). The model is up, but
		// the user asked for no launch: none, and nothing will use it.
		m.status = withNote(note, fmt.Sprintf("started %s; launch cancelled (job %s)", st.item.model.ID, st.jobID))
		m.phase = phaseModel
		ids := m.startDropIDs(st.item.model.ID)
		m.rslot = nil
		return m, dropAllCmd(m.hold, ids)
	}
	if msg.err == nil {
		m.phase = phaseModel
		if note != "" {
			m.recordRouteNotes("wt: note: " + note + "\n")
			m.startNote = note
		}
		if m.rslot != nil {
			// The start was a slot profile's local model: the profile's
			// command is what launches (slot_flow.go).
			return m.launchRemoteSlot()
		}
		return m.launchRemote(st.item)
	}
	id := st.item.model.ID
	var je *modelclient.JobError
	if !errors.As(msg.err, &je) {
		return back("error: " + cleanup.Flatten(msg.err.Error()))
	}
	p := je.Problem
	message := cleanup.Flatten(p.Message)
	failed := func() (model, tea.Cmd) {
		if message == "" {
			message = "the start of " + id + " ended: " + cleanup.Flatten(p.Code)
		}
		return back("error: " + message)
	}
	rs := &replaceState{item: st.item, others: st.others, allow: st.allow}
	items := buildReplaceChoices()
	switch p.Code {
	case modelapi.CodeCancelled:
		return back(fmt.Sprintf("cancelled — nothing was started (job %s)", st.jobID))
	case modelapi.CodeOccupied, modelapi.CodeOthers:
		// A question this attempt already answered is never asked again: the
		// refusal is reported instead.
		sameFamily := len(p.Occupants) > 0 && !st.allow
		othersAsked := (len(p.Others) > 0 || len(p.OthersUnknown) > 0) && !st.others.answer.Decided
		if othersAsked && st.others.stopAll {
			// --others=stop answers for the pinned row: every resident the
			// server can stop, as the non-TUI start does.
			rs.others = othersPlan{answer: modelapi.OthersDecision{Decided: true, Stop: modelapi.StoppableIDs(p.Others)}}
			othersAsked = false
			if !sameFamily {
				return m.beginRemoteStart(st.item, st.allow, rs.others)
			}
		}
		if !sameFamily && !othersAsked {
			return failed()
		}
		rs.header = problemHeader(id, p, sameFamily, othersAsked, m.clientSessionCounts())
		if othersAsked {
			rs.allow = st.allow || sameFamily
			rs.stop = modelapi.StoppableIDs(p.Others)
			rs.id, rs.sameFamily, rs.othersListed = id, sameFamily, len(p.Others) > 0
			items = buildOthersChoices(rs.id, rs.sameFamily, rs.othersListed, len(rs.stop))
		}
	case modelapi.CodeOccupancyUnknown:
		if st.allow {
			return failed()
		}
		rs.header = message
	default:
		return failed()
	}
	if note != "" {
		m.recordRouteNotes("wt: note: " + note + "\n")
	}
	rs.choices = newReplaceChoices(m.theme, m.width-2, m.height-2, items)
	m.replace = rs
	m.phase = phaseReplaceConfirm
	return m, nil
}

// clientSessionCounts is the number of client sessions using each model the
// table shows, as the catalog said when it was read.
func (m model) clientSessionCounts() map[string]int {
	counts := map[string]int{}
	for id, r := range m.wire {
		counts[id] = r.Sessions
	}
	return counts
}

// withClientSessions is line with the client session count of the model it
// names, when there is one (N7).
func withClientSessions(line string, n int) string {
	if n <= 0 {
		return line
	}
	return fmt.Sprintf("%s (in use by %d client session(s))", line, n)
}

// problemHeader is the dialog's question for a start problem, in
// refusalHeader's lines: the occupants the start would stop (sameFamily),
// then the other providers' models it would load beside (othersAsked), each
// with the memory figure the server worded, and a line per family the server
// could not read. Every piece of the problem is made one printable line
// (M11).
func problemHeader(id string, p modelapi.Problem, sameFamily, othersAsked bool, counts map[string]int) string {
	var lines []string
	if sameFamily {
		lines = append(lines, fmt.Sprintf("Starting %s will stop:", id))
		for _, oid := range p.Occupants {
			lines = append(lines, "  "+withClientSessions(cleanup.Flatten(oid), counts[oid]))
		}
	}
	if othersAsked {
		lines = append(lines, "Also loaded, by other providers:")
		for _, r := range p.Others {
			line := cleanup.Flatten(r.Label)
			if f := cleanup.Flatten(r.Figure); f != "" {
				line += " — " + f
			}
			lines = append(lines, "  "+withClientSessions(line, counts[r.ID]))
		}
		for _, f := range p.OthersUnknown {
			lines = append(lines, fmt.Sprintf("  cannot tell whether %s is serving a model", cleanup.Flatten(f)))
		}
	}
	return strings.Join(lines, "\n")
}

// launchPrep is the API path's launch while it prepares on the model server
// (phaseLaunching): the client session's register and, for an agent that
// syncs the gateway's models, the catalog read, then the agent's command.
// Nothing local runs until it is done, so a quit here leaves no agent behind.
type launchPrep struct {
	id      int
	modelID string
	cancel  context.CancelFunc
	// quitting is set once ctrl+c asked wt to quit, as routingState.quitting:
	// the prep's result, which can still be delivered before the QuitMsg, is
	// dropped and launches nothing.
	quitting bool
}

// remotePreparedMsg is a finished prep: the agent's command built against
// the gateway, or why it could not be.
type remotePreparedMsg struct {
	id      int
	modelID string // the model the prep held, for a prep that arrives stale
	cmd     *exec.Cmd
	err     error
}

// launchRemote launches it on the gateway, the non-TUI launchFilteredRemote's
// steps: the server work (hold the model in the client session, read the
// server's non-native models for an agent that syncs them, build the agent
// against the gateway) runs in a prep command behind the launching screen;
// only once it is done does Update hand the built command to the run command,
// which records the rotation once the launch gate is past. The post-exit flow
// ends the client session (exitTail).
func (m model) launchRemote(it *modelItem) (model, tea.Cmd) {
	mdl := it.model
	if row, ok := m.wire[it.model.ID]; ok {
		mdl = catalog.ToModel(row)
	}
	m.launchModel = mdl
	m.status = ""
	if m.hold == nil {
		m.hold = &clientHold{client: m.client}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.prepRun++
	m.prep = &launchPrep{id: m.prepRun, modelID: mdl.ID, cancel: cancel}
	m.phase = phaseLaunching
	return m, prepRemoteLaunchCmd(ctx, prepInput{
		id: m.prepRun, client: m.client, hold: m.hold, agent: m.agent, model: mdl,
		path: m.selectedPath, yolo: m.yolo, extraArgs: m.extraArgs,
		build: m.hooks.buildLaunch(),
	})
}

// prepInput is what the prep command reads, copied out of the model.
type prepInput struct {
	id        int
	client    ModelClient
	hold      *clientHold
	agent     string
	model     config.Model
	path      string
	yolo      bool
	extraArgs []string
	build     gatewayBuildFunc
}

// prepRemoteLaunchCmd does the launch's server work. Holding the model
// starts the client session's heartbeat when it is the first one, so the
// heartbeat goroutine belongs to this command (the post-exit end stops it),
// never to Update. A prep that fails, or that esc or ctrl+c cancelled, takes
// the model out of the client session and builds nothing more.
func prepRemoteLaunchCmd(ctx context.Context, in prepInput) tea.Cmd {
	return func() tea.Msg {
		bg := context.WithoutCancel(ctx)
		if note := in.hold.hold(ctx, in.model.ID); note != "" && ctx.Err() == nil {
			fmt.Fprintf(clientNotes, "wt: note: %s\n", note)
		}
		fail := func(err error) tea.Msg {
			if ctx.Err() == nil {
				in.hold.drop(bg, in.model.ID)
			}
			// A cancelled prep (esc, ctrl+c) arrives stale: handlePrepared
			// drops its model, unless a newer launch of the same model is
			// under way (an esc and an immediate re-Enter), which a drop here
			// would break.
			return remotePreparedMsg{id: in.id, modelID: in.model.ID, err: err}
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		var models []config.Model
		if _, ok := agents.ByName(in.agent).(agents.GatewaySyncer); ok && !in.model.Native {
			cat, err := in.client.Catalog(ctx)
			if err != nil {
				return fail(err)
			}
			models = catalog.NonNativeModels(cat.Rows)
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		cmd, _, err := in.build(in.agent, in.model, in.path, in.yolo, in.client.Gateway(), models, in.extraArgs)
		if err != nil {
			return fail(err)
		}
		if wd, werr := shellDir(); werr == nil {
			for _, n := range agents.RelativeArgNotes(in.agent, wd, cmd.Dir, in.extraArgs) {
				fmt.Fprintln(clientNotes, n)
			}
		}
		return remotePreparedMsg{id: in.id, modelID: in.model.ID, cmd: cmd}
	}
}

// handlePrepared applies a finished prep: a failure returns to the picker
// with the reason (and the start's note ahead of it), a built command goes to
// the run command. A prep the user left (esc, ctrl+c) or one superseded is
// dropped, and a prep left with esc (it may have held the model, or even built
// its command) takes its model out of the client session, see staleDropCmd.
func (m model) handlePrepared(msg remotePreparedMsg) (model, tea.Cmd) {
	if m.prep == nil || msg.id != m.prep.id || m.prep.quitting || m.phase != phaseLaunching {
		return m, m.staleDropCmd(msg)
	}
	m.prep.cancel()
	m.prep = nil
	m.phase = phaseModel
	note := m.startNote
	m.startNote = ""
	if msg.err != nil {
		m.status = withNote(note, "launch failed: "+cleanup.Flatten(msg.err.Error()))
		return m, nil
	}
	agent, id, h := m.agent, m.launchModel.ID, m.hold
	record := func() string {
		if err := rotation.New().RecordFor(agent, id); err != nil {
			return err.Error()
		}
		return ""
	}
	// A launch that never reached the agent (the profile failed) ends the
	// client session where the registry path releases its wt session.
	launchModel := m.launchModel
	// A decline takes the prep's model out of the client session: nothing
	// will use it.
	return m.commitAsk(launchModel, func(mm model) (model, tea.Cmd) {
		return mm.commitLaunch(runRecordAndWaitWith(msg.cmd, agent, launchModel, record, mm.gateFor(), func() { h.end(os.Stderr) }, mm.profileCarry))
	}, dropAllCmd(h, []string{id}))
}

// staleDropCmd takes a stale prep's model out of the client session: nothing
// will use it. It does so only when the picker is back at the table with no
// launch in flight; while a newer launch (the same model after an esc and an
// immediate re-Enter, or any run) is under way, a drop could break it, so the
// model stays held until the run's end, as before.
func (m model) staleDropCmd(msg remotePreparedMsg) tea.Cmd {
	if msg.modelID == "" || m.hold == nil || m.prep != nil || m.phase != phaseModel {
		return nil
	}
	h, id := m.hold, msg.modelID
	return func() tea.Msg {
		h.drop(context.Background(), id)
		return nil
	}
}

// handleLaunchingKey is the launching screen's keyboard: esc goes back to the
// table and ctrl+c quits wt, both calling the prep off (its result is
// dropped, no agent is built or run); every other key does nothing.
func (m model) handleLaunchingKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.prep.cancel()
		quitting := *m.prep
		quitting.quitting = true
		m.prep = &quitting
		return m, tea.Quit
	case "esc":
		m.prep.cancel()
		m.prep = nil
		m.phase = phaseModel
		m.startNote = ""
		return m, nil
	}
	return m, nil
}

// launchingView is the launching screen: what is being prepared, and the
// keys that act (T5, T6).
func (m model) launchingView() string {
	body := tuilayout.WrapText("Launching "+m.prep.modelID+" — preparing the launch on the model server…", m.width)
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return body + "\n\n" + dim.Render("esc back · ctrl+c quit")
}

// remoteStartingView is the start screen on the API path: the model, what the
// job is doing (queued, the server's step), the client's elapsed time from the
// job's own progress, the job id, and only the keys that act (T5, T6).
func (m model) remoteStartingView() string {
	st := m.start
	what := "asking the model server to start it"
	elapsed := ""
	if v := st.job; v != nil {
		switch {
		case v.Queued:
			what = modelclient.QueuedDetail
		case cleanup.Flatten(v.Step) != "":
			what = cleanup.Flatten(v.Step)
		case cleanup.Flatten(v.State) != "":
			what = cleanup.Flatten(v.State)
		}
		elapsed = fmt.Sprintf(" (%s)", v.Elapsed.Round(time.Second))
	}
	lines := []string{"Starting " + st.item.model.ID + " — " + what + elapsed}
	if st.jobID != "" {
		job := "job " + st.jobID
		if st.job != nil && st.job.Attached {
			job += " (joined a start already running on the model server)"
		}
		lines = append(lines, job)
	}
	if st.cancelSent {
		lines = append(lines, "cancelling job "+st.jobID+"…")
	}
	if st.cancelErr != "" {
		lines = append(lines, "error: "+st.cancelErr)
	}
	body := tuilayout.WrapText(strings.Join(lines, "\n"), m.width)
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	return body + "\n\n" + dim.Render(remoteStartHint(st))
}

// remoteStartHint is the start screen's key line: ctrl+c quits before the job
// has an id and detaches after; c is named exactly while canCancelJob holds.
func remoteStartHint(st *startState) string {
	switch {
	case st.jobID == "":
		return "ctrl+c quit"
	case st.canCancelJob():
		return "ctrl+c detach · c cancel job"
	}
	return "ctrl+c detach"
}

// settleRemoteStart stops waiting on a start the user quit out of: the polls
// end (the job, if it has one, keeps running on the server) and the goroutine
// is waited for, so nothing outlives the program but the job itself.
func settleRemoteStart(final tea.Model) {
	fm, ok := final.(model)
	if !ok {
		return
	}
	if fm.catalogCancel != nil {
		fm.catalogCancel()
	}
	if fm.prep != nil {
		fm.prep.cancel()
	}
	if fm.start == nil {
		return
	}
	fm.start.cancel()
	for range fm.start.ch {
	}
}

// printPendingSummaryAndSurveyTail runs the TUI's post-exit flow once the
// alt-screen is gone, in the same order as the non-TUI path (issues
// #115/#116): the client session ends (exitTail; even when nothing
// launched), then the survey (not for a slot profile), the summary line, the after-survey stats and
// the pricing notice from the model server's catalog. There is no stop
// picker. It is extracted from Run() so this ordering is unit-testable
// without a real tea.Program/TTY.
func printPendingSummaryAndSurveyTail(tail exitTail) {
	launched := pendingSurveyState
	summary := pendingSummary
	pendingSurveyState = pendingSurvey{}
	pendingSummary = ""
	tail.release()
	var stats string
	// A slot profile is not one model: no survey, as no price notice below.
	if launched.agent != "" && !launched.profile {
		stats = runSurvey(launched.agent, launched.m)
	}
	if summary != "" {
		fmt.Println("\n" + summary)
	}
	if stats != "" {
		fmt.Println(stats)
	}
	// A command agent launches no model (m.ID is empty), and a slot profile is
	// not one priced model: neither gets the reminder.
	if tail.priceNote != nil && summary != "" && launched.m.ID != "" && !launched.profile {
		tail.priceNote()
	}
}
