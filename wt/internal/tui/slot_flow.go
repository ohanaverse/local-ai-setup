package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
)

// buildSlotGatewayLaunch is the slot build (launchRemoteSlot): against the
// model-server gateway. A test seam: tests wrap it to see the build.
var buildSlotGatewayLaunch = agents.BuildSlotLaunchCmdGateway

// slotAbortMsg: the applier failed after the terminal was released. ids are
// the profile's slot models the client session holds, which nothing will use
// now and Update takes out of it.
type slotAbortMsg struct {
	err error
	ids []string
}

// runSlotAndWaitCmd is runAndWaitCmd for a slot-profile launch. It releases
// the terminal, prints the route notes, applies the profile's args and
// wrapper through slotApplier, records the launch, runs the agent and
// captures the summary. It never calls profileApplier: an automatic profile
// is not applied to a slot-profile launch. An applier error, or no applier,
// runs nothing and records nothing, and comes back as a slotAbortMsg. gate is
// the launch's one question at the commit point (runRecordAndWaitWith's); a
// decline runs nothing and records nothing. The client session holds the
// slot models, and the post-exit tail ends it.
func runSlotAndWaitCmd(cmd *exec.Cmd, agent string, row catalog.Row, gate func() (bool, error)) tea.Cmd {
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
			return slotAbortMsg{err: err, ids: slotModelIDs(row)}
		}
		if cleanup == nil {
			cleanup = func() error { return nil }
		}
		// The launch gate asks before anything is recorded, so declining
		// there leaves no trace of the launch — while a creation that failed
		// after the person continued travels out as the run's error (a
		// GateError: main's one line, non-zero, never a quiet decline and
		// never a quiet agent exit), and a "no" ends the run with
		// errLaunchDeclined, exit 3). Either way the profile
		// tail's state stays: the standard stop picker offers whatever the
		// start flow left loaded (nothing auto-stops), as after a real exit.
		if gate != nil {
			if ok, gerr := gate(); !ok {
				_ = cleanup()
				shown := config.Model{ID: row.Model.ID}
				pendingSurveyState = pendingSurvey{agent: agent, m: shown, profile: true}
				gerr = gateEndError(gerr)
				return launchDoneMsg{err: gerr}
			}
		}
		// The commit point: the applier succeeded and the agent runs next.
		// Both writes are best-effort, as launchAndRecord's are.
		if err := rotation.New().RecordProfile(agent, row.Model.ID); err != nil {
			fmt.Fprintf(os.Stderr, "note: %v\n", err)
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		duration, runErr := agents.RunAndCleanup(cmd, cleanup)
		releaseRootClaim()
		shown := config.Model{ID: row.Model.ID}
		pendingSummary = agents.Summary(agent, shown, duration)
		pendingSurveyState = pendingSurvey{agent: agent, m: shown, profile: true}
		return launchDoneMsg{err: runErr}
	}
}

// remoteSlot is a slot-profile launch on the API path between its prep and
// the agent's build: the profile row Enter was pressed on, and the row as the
// prep resolved it against the server's catalog.
type remoteSlot struct {
	item *modelItem
	row  catalog.Row
}

// remoteSlotPreparedMsg is a finished slot prep on the API path: the profile
// resolved and its models held in the client session, or why not.
type remoteSlotPreparedMsg struct {
	id   int
	row  catalog.Row
	err  error
	note string // a register that failed (client sessions are advisory)
}

// beginRemoteSlotLaunch is the API path's slot-profile launch, the twin of
// cmd/wt's launchSlotProfileRemote: change both paths together
// (TestSlotFlowParity holds them to the same server calls). Its prep, in a
// command behind the launching screen, reads the catalog once, resolves the
// profile against it (catalog.ResolveSlotProfileWire), refuses a passthrough
// flag that would replace the slot mapping and holds every distinct slot
// model in the client session before any start. handleRemoteSlotPrepared
// then runs the start flow for the one local model when it needs a start,
// and launchRemoteSlot builds the profile's command against the gateway.
// No route is written, no proxy restarted or probed: the server owns LiteLLM.
func (m model) beginRemoteSlotLaunch(it *modelItem) (model, tea.Cmd) {
	if m.hold == nil {
		m.hold = &clientHold{client: m.client}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.prepRun++
	m.prep = &launchPrep{id: m.prepRun, modelID: it.model.ID, cancel: cancel}
	m.rslot = &remoteSlot{item: it}
	m.launchModel = config.Model{ID: it.model.ID}
	m.status = ""
	m.phase = phaseLaunching
	id, client, hold, agent, extraArgs, check := m.prepRun, m.client, m.hold, m.agent, m.extraArgs, it.profile.Check
	return m, func() tea.Msg {
		return prepRemoteSlot(ctx, id, client, hold, agent, extraArgs, check)
	}
}

// prepRemoteSlot is the slot prep's server work (beginRemoteSlotLaunch). A
// prep cancelled after its register takes the models back out.
func prepRemoteSlot(ctx context.Context, id int, client ModelClient, hold *clientHold, agent string, extraArgs []string, check profiles.SlotCheck) tea.Msg {
	cat, err := client.Catalog(ctx)
	if err != nil {
		return remoteSlotPreparedMsg{id: id, err: err}
	}
	if holder := catalog.SlotPrefixHolderWire(cat.Rows); holder != "" {
		return remoteSlotPreparedMsg{id: id, err: errors.New(catalog.SlotPrefixHeldReason(holder))}
	}
	row := catalog.ResolveSlotProfileWire(catalog.WireByID(cat.Rows), check)
	if row.Action() == catalog.ActionBlock {
		return remoteSlotPreparedMsg{id: id, row: row, err: errors.New(row.BlockReason())}
	}
	if err := agents.CheckSlotPassthrough(agent, extraArgs); err != nil {
		return remoteSlotPreparedMsg{id: id, row: row, err: err}
	}
	ids := slotModelIDs(row)
	note := hold.holdAll(ctx, ids)
	if ctx.Err() != nil {
		dropAll(hold, ids)
		return remoteSlotPreparedMsg{id: id, row: row, err: ctx.Err()}
	}
	return remoteSlotPreparedMsg{id: id, row: row, note: note}
}

// slotModelIDs is the profile's distinct slot model ids, the default slot's
// first.
func slotModelIDs(row catalog.Row) []string {
	var ids []string
	for _, sm := range row.Profile.Distinct() {
		ids = append(ids, sm.ID)
	}
	return ids
}

// dropAll takes ids out of the client session: nothing will use them.
func dropAll(h *clientHold, ids []string) {
	for _, id := range ids {
		h.drop(context.Background(), id)
	}
}

// handleRemoteSlotPrepared applies a finished slot prep: a failure returns to
// the picker with the reason; a profile whose local model needs a start runs
// the API path's start flow for it (--replace and --others are the -M pin's
// permissions, as for a model row); any other builds the profile's command. A
// prep the user left (esc, ctrl+c) or one superseded is dropped.
func (m model) handleRemoteSlotPrepared(msg remoteSlotPreparedMsg) (model, tea.Cmd) {
	if m.prep == nil || msg.id != m.prep.id || m.prep.quitting || m.phase != phaseLaunching || m.rslot == nil {
		return m, m.staleSlotDropCmd(msg)
	}
	m.prep.cancel()
	m.prep = nil
	m.phase = phaseModel
	if msg.note != "" {
		m.recordRouteNotes("wt: note: " + msg.note + "\n")
	}
	if msg.err != nil {
		m.rslot = nil
		m.status = withNote(msg.note, cleanup.Flatten(msg.err.Error()))
		return m, nil
	}
	st := *m.rslot
	st.row = msg.row
	m.rslot = &st
	if p := msg.row.Profile; msg.row.Action() == catalog.ActionStart && p.Local != nil {
		pinned := st.item.model.ID == m.pinnedModel
		local := p.Local.Model
		return m.beginRemoteStart(&modelItem{model: local, line: local.ID}, m.allowReplace && pinned, m.othersFor(pinned))
	}
	return m.launchRemoteSlot()
}

// staleSlotDropCmd takes a stale slot prep's models out of the client session,
// the slot twin of staleDropCmd: a prep left with esc may have finished its
// register just after the cancel, and nothing will use the models. Only a
// prep that succeeded holds anything (a failed or cancelled one dropped its
// own), and only while the picker is back at the table with no launch in
// flight: a newer launch of the same models must not lose them.
func (m model) staleSlotDropCmd(msg remoteSlotPreparedMsg) tea.Cmd {
	if msg.err != nil || msg.row.Profile == nil || m.hold == nil || m.prep != nil || m.phase != phaseModel {
		return nil
	}
	return dropAllCmd(m.hold, slotModelIDs(msg.row))
}

// dropRemoteSlot is what a profile launch the user backed out of on the
// replace dialog (a decline or esc) does on the API path: it takes every slot
// model its prep held out of the client session and forgets the launch. The
// local model's own job already dropped it; the other slots were held for
// this launch alone. No profile launch, no command.
func (m model) dropRemoteSlot() (model, tea.Cmd) {
	if m.rslot == nil || m.rslot.row.Profile == nil {
		return m, nil
	}
	ids := slotModelIDs(m.rslot.row)
	m.rslot = nil
	return m, dropAllCmd(m.hold, ids)
}

// dropAllCmd is dropAll as a command, for the update goroutine.
func dropAllCmd(h *clientHold, ids []string) tea.Cmd {
	return func() tea.Msg {
		dropAll(h, ids)
		return nil
	}
}

// startDropIDs is what a start that ends without a launch takes out of the
// client session: the model alone, or, in a slot-profile launch, every slot
// model its prep held (the other slots are held only for this launch).
func (m model) startDropIDs(modelID string) []string {
	if m.rslot != nil && m.rslot.row.Profile != nil {
		return slotModelIDs(m.rslot.row)
	}
	return []string{modelID}
}

// launchRemoteSlot builds the profile's command against the gateway and hands
// it to runSlotAndWaitCmd, which applies the profile's args and wrapper,
// records the launch and runs the agent; the post-exit tail ends the client
// session (exitTail). It runs on the update goroutine and waits for nothing.
func (m model) launchRemoteSlot() (model, tea.Cmd) {
	st := m.rslot
	m.rslot = nil
	m.phase = phaseModel
	row := st.row
	cmd, err := buildSlotGatewayLaunch(row.Profile.Launch(), m.selectedPath, m.yolo, m.client.Gateway(), m.extraArgs)
	if err != nil {
		note := m.startNote
		m.startNote = ""
		m.status = withNote(note, "launch failed: "+cleanup.Flatten(err.Error()))
		h, ids := m.hold, slotModelIDs(row)
		return m, func() tea.Msg {
			dropAll(h, ids)
			return nil
		}
	}
	m.startNote = ""
	queueLaunchNotes(m.agent, cmd, m.extraArgs)
	return m.commitLaunch(runSlotAndWaitCmd(cmd, m.agent, row, m.gateFor()))
}
