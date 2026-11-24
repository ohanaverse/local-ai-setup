package modeleditor

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeview"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// remoteElsewhere is the line under the mode when the machine's mode is
// remote: this screen sets up local (or native); remote is the session's
// choice, made in the session editor.
const remoteElsewhere = "remote mode is set in `wt config` (its Mode tab), not here"

// setupPhase is what the Setup tab is showing: the status and the
// mode choice, the same with the form's fields taking keys, a yes/no question
// (the plan's confirm or one asked before planning), or an apply or a
// start/stop in flight.
type setupPhase int

const (
	setupView setupPhase = iota
	setupEdit
	setupConfirm
	setupApplying
	// setupMenu is the LiteLLM key menu k opens.
	setupMenu
)

// setupPrompt is the question the confirm phase asks. Each is default-No
// (P1): only y or Y says yes.
type setupPrompt int

const (
	// promptApply is "Apply this plan? [y/N]" under the plan's text.
	promptApply setupPrompt = iota
	// promptRemoveAgent is asked before planning when the choice leaves
	// local with wt's LaunchAgent installed; y sets Desired.RemoveAgent.
	promptRemoveAgent
	// promptReplaceRemote is asked before planning when the stored mode is
	// remote: the plan replaces its url and token (G1); n plans nothing.
	promptReplaceRemote
	// promptRunUnused is asked before s or R when the stored mode is not
	// local: no session would use the server started (G2); n starts nothing.
	promptRunUnused
	// promptGateway is asked by g: write the [gateway] subscription switch
	// (#693); y writes it, n writes nothing. Decision: the same confirm phase
	// as the other writes, default-No, naming that serve must be restarted.
	promptGateway
)

// The form's fields, shown for local only (visibleFields): the listen
// address, its token file and, when no key is on record, the LiteLLM master
// key. Native has none.
const (
	sfListen = iota
	sfLocalToken
	sfMaster
	sfCount
)

// setupFieldLabels name the fields beside their inputs; with the "> " that
// marks the field being edited they fit the status block's label column.
var setupFieldLabels = [sfCount]string{"listen", "token", "master"}

// setupFieldHints are the inputs' placeholders, shown while a field is
// empty: what goes there, or what an empty field means.
var setupFieldHints = [sfCount]string{
	"empty: " + modelsetup.DefaultListen,
	"empty: the default token file",
	"LiteLLM master key (hidden)",
}

// setupTab is the Setup tab's state. Everything it knows of the
// machine comes from observations run in commands (observeSetupCmd), and
// every plan, apply and start/stop runs in a command too; nothing here reads
// a file, dials a server or calls a SetupDeps func.
type setupTab struct {
	phase setupPhase
	// state is the latest observation; loaded is true once one is in.
	state  modelsetup.State
	loaded bool
	// want holds the mode choice; the rest of a Desired comes from the
	// fields when it is built (setupDesired).
	want modelsetup.Desired
	// chosen is true once the user has moved the mode choice; until then a
	// new observation sets the choice to the effective mode.
	chosen bool
	// asked is true once the first observation has been dispatched, so the
	// tab observes on its first visit only (c and r observe again).
	asked bool
	// gen numbers the observations and plans: a result that is not the
	// latest's is dropped, so a slow ping cannot overwrite a later answer.
	gen int
	// busy is true while an observation, a plan, an apply or a start/stop is
	// out. writing is true for an apply or a start/stop alone: a quit waits
	// for it (leave), so its result is always read.
	busy    bool
	writing bool
	// status is what the tab is doing or what the last action said; pending
	// is true when it says what is running, and goes when that is done. The
	// next key clears it when nothing is out.
	status  string
	pending bool
	// fields are the form's inputs, made with the first observation
	// (inputs); field is the one being edited. Until the user edits one
	// (edited), each observation fills the fields from what is stored. The
	// master key is never filled; it is cleared after a successful apply.
	fields [sfCount]textinput.Model
	inputs bool
	field  int
	edited bool
	// planGen is the observation a leads to a plan from (0: none wanted);
	// queue the questions still to ask before planning. planWant is the
	// snapshot of the form a took (setupDesired, with the options that read
	// used built from the same fields): the question's answer goes into it
	// and Plan gets it, so what is planned is what was read even if a field
	// changed since. It holds the master key until the plan is dispatched,
	// then is cleared.
	planGen  int
	queue    []setupPrompt
	planWant modelsetup.Desired
	// planOut is true while setupPlanCmd's plan is out, so c or r can say
	// they cancelled it (it is dropped by its generation when it lands).
	planOut bool
	// prompt is the question the confirm phase asks; plan and planText the
	// plan it is about and the text shown for it.
	prompt   setupPrompt
	plan     modelsetup.Plan
	planText string
	// control is the start or restart the unused-server gate (promptRunUnused)
	// was asked for: y runs it, n does not.
	control modelsetup.ControlAction
	// gatewayOn is the value of the subscription switch promptGateway asks
	// for: the opposite of what the row showed when g was pressed.
	gatewayOn bool
	// result is the result screen after an apply that left notes or
	// recovery commands to read; nil when none is showing.
	result *editorkit.ResultScreen
	// menuAt is the menu option the cursor is on.
	menuAt int
	// probeSkipped is true when the observation shown left out the LiteLLM
	// probe, so it holds no litellm binary or port check: choosing local then
	// reads again with the probe (the k menu's create needs it), and the menu
	// says the probe was not run.
	probeSkipped bool
	// probeRead is true while the read the choice of local started, for its
	// LiteLLM probe, is out. Only that read lets the mode keys through, so
	// moving on from local is not held up by it.
	probeRead bool
	// settle is how many more reads the tab makes after a start or restart
	// while the server it started is not answering yet (setupSettleTries at
	// first); settled is the status the action left, kept through them.
	settle  int
	settled string
	// waiting is true while a settle tick is out: the tick of the last try
	// arrives with settle at 0 and must still do the final read, while a tick
	// that r or c outlived must do nothing.
	waiting bool
}

// setupSettleTries and setupSettleDelay bound the wait after a start or a
// restart: launchd returns when the job is started, a little before serve
// listens, and a read in that gap would show a failure that is gone a
// moment later. About ten seconds in all, the apply path's own wait.
const setupSettleTries = 20

var setupSettleDelay = 500 * time.Millisecond

// setupSettleMsg asks for the next read of the wait after a start or restart.
type setupSettleMsg struct{}

// stillStarting is whether an observation is of a loaded agent whose server
// refused the connection: the one failure a start or restart that just
// succeeded is expected to show for a moment. A refused token or another
// server on the port is an answer, and is shown at once.
func stillStarting(st modelsetup.State) bool {
	return st.Server.Checked && !st.Server.OK && st.Server.Kind == modelsetup.KindUnreachable && st.Agent.Job.Loaded
}

// setupObservedMsg carries one observation.
type setupObservedMsg struct {
	gen int
	st  modelsetup.State
	// skippedProbe is whether the observation left out the LiteLLM probe
	// (its options' LiteLLMProbeSkipped for the mode it resolved).
	skippedProbe bool
}

// setupPlannedMsg carries one plan, or why there is none.
type setupPlannedMsg struct {
	gen  int
	plan modelsetup.Plan
	err  error
}

// setupAppliedMsg carries an apply's result and the progress lines Apply
// reported, collected inside the command.
type setupAppliedMsg struct {
	res      modelsetup.Result
	progress []string
}

// setupControlledMsg carries a start, stop or restart's outcome.
type setupControlledMsg struct {
	action modelsetup.ControlAction
	err    error
}

// setupGatewayMsg carries the outcome of g's write of the subscription switch.
type setupGatewayMsg struct {
	on  bool
	was bool
	res config.GatewayWrite
	err error
}

// needsMasterKey is true when no LiteLLM master key is on record: neither
// wt-model's pair nor the LiteLLM plist holds one.
func needsMasterKey(s modelsetup.State) bool {
	return !s.Pair.KeySet && !s.LiteLLMPlist.HasMasterKey
}

// visibleFields are the fields the chosen mode shows, in order.
func (st *setupTab) visibleFields() []int {
	if st.want.Mode != modelmode.ModeLocal {
		return nil
	}
	if needsMasterKey(st.state) {
		return []int{sfListen, sfLocalToken, sfMaster}
	}
	return []int{sfListen, sfLocalToken}
}

// value is a field's text with the spaces round it taken off.
func (st *setupTab) value(f int) string { return strings.TrimSpace(st.fields[f].Value()) }

// ensureSetupInputs makes the fields once. The master key is masked
// (EchoPassword); no "> " prompt, the label is beside each.
func (m *model) ensureSetupInputs() {
	st := &m.setup
	if st.inputs {
		return
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	for f := range st.fields {
		in := editorkit.NewTextInput("", setupFieldHints[f])
		in.Prompt = ""
		in.PlaceholderStyle = dim
		if f == sfMaster {
			in.EchoMode = textinput.EchoPassword
		}
		st.fields[f] = in
	}
	st.inputs = true
	m.resizeSetupInputs()
}

// resizeSetupInputs fits the inputs to the columns beside the label column,
// less one for the cursor.
func (m *model) resizeSetupInputs() {
	st := &m.setup
	if !st.inputs {
		return
	}
	for f := range st.fields {
		st.fields[f].Width = max(m.width-editorkit.LabelW-1, 3)
	}
}

// fillSetupFields sets the non-secret fields from what is stored, unless
// the user has edited one: the listen address and token file from wt's
// LaunchAgent, else the stored token file of a stored local mode (a remote
// table's token file is the remote server's, never local's). An empty field
// is a plan's default.
func (st *setupTab) fillSetupFields() {
	if st.edited {
		return
	}
	s := st.state
	t := s.Config.Table
	stored, _ := modelmode.ParseMode(t.Mode)
	var listen, ltok string
	if s.Agent.Managed {
		listen, ltok = s.Agent.Listen, s.Agent.TokenFile
	}
	if ltok == "" && stored == modelmode.ModeLocal {
		ltok = t.TokenFile
	}
	for f, v := range map[int]string{sfListen: listen, sfLocalToken: ltok} {
		st.fields[f].SetValue(v)
	}
}

// setupObserveOptions are the options an observation is made with: the
// fields of the chosen mode. Local looks for a listener on the edited port
// and describes a typed token file; native has no fields.
func (m *model) setupObserveOptions() modelsetup.ObserveOptions {
	st := &m.setup
	var o modelsetup.ObserveOptions
	o.SkipLiteLLMProbe = st.want.Mode != modelmode.ModeLocal
	// Until the user moves the mode choice the mode shown is the one the read
	// resolves, so a config that stores local is probed in that same read.
	o.ProbeLiteLLMWhenLocal = !st.chosen
	if st.want.Mode == modelmode.ModeLocal {
		if _, port, err := net.SplitHostPort(st.value(sfListen)); err == nil {
			if p, err := strconv.Atoi(port); err == nil {
				o.ListenPort = p
			}
		}
		if p := st.value(sfLocalToken); p != "" {
			o.TokenPaths = []string{p}
		}
	}
	return o
}

// setupDesired is the setup the user asked for: the mode and, for local,
// its fields (the question's answer is added to a's snapshot). The master
// key becomes a modelsetup.Secret here and nowhere earlier, and goes in only
// while it is asked for.
func (m *model) setupDesired() modelsetup.Desired {
	st := &m.setup
	var d modelsetup.Desired
	d.Mode = st.want.Mode
	if st.want.Mode == modelmode.ModeLocal {
		d.Listen, d.TokenFile = st.value(sfListen), st.value(sfLocalToken)
		if needsMasterKey(st.state) {
			d.MasterKey = modelsetup.Secret(st.fields[sfMaster].Value())
		}
	}
	return d
}

// observeSetupCmd reads the machine once, off the update loop. The deps,
// the generation and the options are taken here, before the command, so the
// command touches nothing of the model; all I/O is inside it.
func (m *model) observeSetupCmd() tea.Cmd {
	return m.observeSetupCmdWith(m.setupObserveOptions())
}

// observeSetupCmdWith is observeSetupCmd with the options given.
func (m *model) observeSetupCmdWith(opts modelsetup.ObserveOptions) tea.Cmd {
	m.setup.gen++
	m.setup.asked, m.setup.busy, m.setup.probeRead = true, true, false
	gen, observe := m.setup.gen, m.opts.Setup.Observe
	return func() tea.Msg {
		st := observe(context.Background(), opts)
		return setupObservedMsg{gen: gen, st: st, skippedProbe: opts.LiteLLMProbeSkipped(st.Resolution.Mode)}
	}
}

// applySetupObserved takes an observation, unless a later one is out. The
// observation a asked for goes on to the questions and the plan.
func (m *model) applySetupObserved(msg setupObservedMsg) tea.Cmd {
	st := &m.setup
	if msg.gen != st.gen {
		return nil // a later observation is on its way
	}
	if st.settle > 0 && stillStarting(msg.st) {
		// Keep the last state on screen and read again shortly, so the
		// server line never shows the gap between launchd and serve (#710).
		st.settle--
		st.busy, st.waiting = true, true
		st.status = st.settled + "; waiting for wt-model to answer…"
		return tea.Tick(setupSettleDelay, func(time.Time) tea.Msg { return setupSettleMsg{} })
	}
	if st.settle > 0 || st.settled != "" {
		st.settle, st.waiting = 0, false
		st.status, st.settled = st.settled, ""
	}
	st.state, st.loaded, st.busy, st.probeSkipped, st.probeRead = msg.st, true, false, msg.skippedProbe, false
	if st.pending {
		st.status, st.pending = "", false
	}
	if !st.chosen {
		// A remote machine starts the choice on local: remote is set in the
		// session editor, and the status block says so (remoteElsewhere).
		st.want.Mode = msg.st.Resolution.Mode
		if st.want.Mode == modelmode.ModeRemote {
			st.want.Mode = modelmode.ModeLocal
		}
	}
	m.ensureSetupInputs()
	st.fillSetupFields()
	if st.planGen != msg.gen {
		return nil
	}
	st.planGen = 0
	if why := m.setupWriteBlocked(); why != "" {
		// The fresh read found what blocks a write (a parse error).
		st.planWant, st.queue = modelsetup.Desired{}, nil
		st.status = why
		return nil
	}
	s, want := st.state, st.planWant
	if want.LiteLLM != modelsetup.LiteLLMTighten && modelmode.ConfigMode(s.State) == modelmode.ModeRemote {
		// G1: this plan replaces remote's stored url and token.
		st.queue = append(st.queue, promptReplaceRemote)
	}
	if want.Mode != modelmode.ModeLocal && s.Agent.Present && s.Agent.Managed {
		st.queue = append(st.queue, promptRemoveAgent)
	}
	return m.nextSetupStep()
}

// nextSetupStep asks the next question queued before planning, or plans
// when none is left.
func (m *model) nextSetupStep() tea.Cmd {
	st := &m.setup
	if len(st.queue) > 0 {
		st.prompt, st.queue = st.queue[0], st.queue[1:]
		st.phase = setupConfirm
		return nil
	}
	return m.setupPlanCmd()
}

// setupPlanCmd plans from the latest observation and the snapshot a took,
// in a command: Plan is a dep like the others, so it is not called from
// Update. The master key goes only while the fresh read still asks for it.
func (m *model) setupPlanCmd() tea.Cmd {
	st := &m.setup
	st.gen++
	st.phase, st.busy, st.status, st.pending = setupView, true, "planning…", true
	want := st.planWant
	if !needsMasterKey(st.state) {
		want.MasterKey = ""
	}
	st.planWant, st.planOut = modelsetup.Desired{}, true
	gen, plan, state := st.gen, m.opts.Setup.Plan, st.state
	return func() tea.Msg {
		p, err := plan(state, want)
		return setupPlannedMsg{gen: gen, plan: p, err: err}
	}
}

// applySetupPlanned shows a plan on the confirm screen, or why there is
// none: a refusal in its own words (each reason already says nothing was
// changed).
func (m *model) applySetupPlanned(msg setupPlannedMsg) {
	st := &m.setup
	if msg.gen != st.gen {
		return
	}
	st.planOut = false
	st.busy, st.status, st.pending = false, "", false
	if msg.err != nil {
		var ref *modelsetup.RefusalError
		if errors.As(msg.err, &ref) {
			st.status = editorkit.Scrub(ref.Error())
		} else {
			st.status = "no plan: " + editorkit.Scrub(msg.err.Error()) + "; nothing was changed"
		}
		return
	}
	st.plan, st.planText = msg.plan, setupPlanText(st.state, msg.plan)
	st.phase, st.prompt = setupConfirm, promptApply
}

// setupPlanText is the text the user confirms: the plan's Format, and a
// note when the plan writes a url (Plan.WritesURL) over a stored one that
// held an '@' (ConfigState.URLHadCredentials; Observe shows it only as a
// placeholder), since the url written replaces it and its credentials are
// gone. Both are data, not wording.
func setupPlanText(s modelsetup.State, p modelsetup.Plan) string {
	text := editorkit.Scrub(p.Format())
	if s.Config.URLHadCredentials && p.WritesURL() {
		text += "the stored url's credentials are not kept: it contained an @, and the url above replaces it\n"
	}
	return text
}

// setupApplyCmd runs the confirmed plan in a command. The deps and the plan
// are taken here; the progress lines are gathered inside the command, in a
// slice nothing else sees, and handed over in the result message.
func (m *model) setupApplyCmd() tea.Cmd {
	st := &m.setup
	st.phase, st.busy, st.writing, st.status, st.pending = setupApplying, true, true, "applying the plan…", false
	apply, plan := m.opts.Setup.Apply, st.plan
	st.plan, st.planText = modelsetup.Plan{}, ""
	return func() tea.Msg {
		var lines []string
		res := apply(context.Background(), plan, func(line string) { lines = append(lines, line) })
		return setupAppliedMsg{res: res, progress: lines}
	}
}

// stepKinds lists steps by kind, for a status line.
func stepKinds(steps []modelsetup.Step) string {
	var kinds []string
	for _, s := range steps {
		kinds = append(kinds, string(s.Kind))
	}
	return strings.Join(kinds, ", ")
}

// setupResultText says what an apply did: the progress lines, then what
// ran or where it stopped and why, then the warnings. A wait that ran out
// names the url, how the last ping failed and the log to read.
func setupResultText(res modelsetup.Result, progress []string) string {
	var lines []string
	for _, l := range progress {
		lines = append(lines, editorkit.Scrub(l))
	}
	if res.Failed == nil && res.Err == nil {
		if len(res.Done) == 0 {
			lines = append(lines, "applied: nothing needed changing")
		} else {
			lines = append(lines, "applied: "+strconv.Itoa(len(res.Done))+" "+pluralWord(len(res.Done), "step", "steps")+" done ("+stepKinds(res.Done)+")")
		}
	} else {
		line := "not applied"
		if res.Failed != nil {
			line = "stopped at the " + string(res.Failed.Kind) + " step"
		}
		var wait *modelsetup.WaitError
		switch {
		case errors.As(res.Err, &wait):
			line += ": wt-model did not answer at " + editorkit.Scrub(wait.URL) + " (" + modeview.KindWord(wait.Kind) + "); see " + wait.Log
		case res.Err != nil:
			line += ": " + editorkit.Scrub(res.Err.Error())
		}
		if len(res.Done) > 0 {
			line += "; done before it: " + stepKinds(res.Done)
		}
		lines = append(lines, line+"; nothing after it was changed")
	}
	for _, w := range res.Warnings {
		lines = append(lines, "warning: "+editorkit.Scrub(w))
	}
	return strings.Join(lines, "\n")
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// applySetupApplied takes an apply's result: it says what ran, clears the
// secrets after a success, and reads the machine again.
func (m *model) applySetupApplied(msg setupAppliedMsg) tea.Cmd {
	st := &m.setup
	st.status = setupResultText(msg.res, msg.progress)
	st.result = editorkit.NewResultScreen(msg.res.Recovery, msg.res.Notes, st.status)
	ok := msg.res.Failed == nil && msg.res.Err == nil
	if ok {
		st.fields[sfMaster].SetValue("")
		st.edited = false
	}
	return m.finishSetupWrite(ok)
}

// controlWords are an action's -ing and past forms.
var controlWords = map[modelsetup.ControlAction][2]string{
	modelsetup.ControlStart:   {"starting", "started"},
	modelsetup.ControlStop:    {"stopping", "stopped"},
	modelsetup.ControlRestart: {"restarting", "restarted"},
}

// startControl is s and R: gated (G2) when config.toml stores a mode other
// than local, since no session would use the server started. x never gates,
// and a blocked action still says why it is blocked (setupControlCmd runs
// the same check again before acting).
func (m *model) startControl(a modelsetup.ControlAction) tea.Cmd {
	st := &m.setup
	if why := m.setupControlBlocked(); why != "" {
		st.status = why
		return nil
	}
	switch modelmode.ConfigMode(st.state.State) {
	case modelmode.ModeRemote, modelmode.ModeNative:
		st.control, st.prompt, st.phase = a, promptRunUnused, setupConfirm
		return nil
	}
	return m.setupControlCmd(a)
}

// setupControlCmd starts, stops or restarts the LaunchAgent in a command,
// the dep taken here.
func (m *model) setupControlCmd(a modelsetup.ControlAction) tea.Cmd {
	st := &m.setup
	if why := m.setupControlBlocked(); why != "" {
		st.status = why
		return nil
	}
	st.phase, st.busy, st.writing, st.status, st.pending = setupApplying, true, true, controlWords[a][0]+" the LaunchAgent…", false
	control := m.opts.Setup.Control
	return func() tea.Msg {
		return setupControlledMsg{action: a, err: control(context.Background(), a)}
	}
}

// gatewayRestartText is what the tab says after the switch is written: serve
// reads [gateway] once, at start, and a restart that reloads it is a bootout
// and a bootstrap (x then s here), never a kickstart (#693).
const gatewayRestartText = "serve must be restarted: press x then s (bootout/bootstrap)"

// startGateway is g: ask whether to turn the subscription switch the other
// way. A blocked write says why, like a, s, x and R.
func (m *model) startGateway() tea.Cmd {
	st := &m.setup
	if why := m.setupWriteBlocked(); why != "" {
		st.status = why
		return nil
	}
	st.gatewayOn = !st.state.Gateway.SubscriptionPassthrough
	st.prompt, st.phase = promptGateway, setupConfirm
	return nil
}

// setupGatewayCmd writes the switch in a command, the dep taken here.
func (m *model) setupGatewayCmd() tea.Cmd {
	st := &m.setup
	if why := m.setupWriteBlocked(); why != "" {
		st.phase, st.status = setupView, why
		return nil
	}
	on, was := st.gatewayOn, st.state.Gateway.SubscriptionPassthrough
	st.phase, st.busy, st.writing, st.status, st.pending = setupApplying, true, true, "writing the subscription switch…", false
	set := m.opts.Setup.SetGateway
	return func() tea.Msg {
		res, err := set(on)
		return setupGatewayMsg{on: on, was: was, res: res, err: err}
	}
}

// applySetupGateway says how the write went and reads the machine again, so
// the gateway row shows what config.toml now holds.
func (m *model) applySetupGateway(msg setupGatewayMsg) tea.Cmd {
	st := &m.setup
	word := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	switch {
	case msg.err != nil:
		st.status = "subscription switch failed: " + editorkit.Scrub(msg.err.Error()) + "; nothing was changed"
	case !msg.res.Changed:
		st.status = "subscription passthrough is already " + word(msg.on) + "; nothing was changed"
	default:
		st.status = "subscription passthrough " + word(msg.on) + " (was " + word(msg.was) + ")"
		if msg.res.Backup != "" {
			st.status += "; backup " + msg.res.Backup
		}
		st.status += "; " + gatewayRestartText
	}
	return m.finishSetupWrite(msg.err == nil)
}

// applySetupControlled says how a start, stop or restart went and reads the
// machine again.
func (m *model) applySetupControlled(msg setupControlledMsg) tea.Cmd {
	st := &m.setup
	if msg.err != nil {
		st.status = msg.action.String() + " failed: " + editorkit.Scrub(msg.err.Error())
	} else {
		st.status = modelsetup.AgentLabel + " " + controlWords[msg.action][1]
		if msg.action == modelsetup.ControlStart || msg.action == modelsetup.ControlRestart {
			st.settle, st.settled = setupSettleTries, st.status
		}
	}
	return m.finishSetupWrite(msg.err == nil)
}

// finishSetupWrite ends an apply or a start/stop: the tab takes keys again,
// a quit that waited for it happens now (through quit, which still waits for
// a registry write the Models tab has out) unless it failed or left notes to
// read, when the quit is called off here where they are drawn; otherwise the
// machine is read again. The status says what happened and stays through
// that read.
func (m *model) finishSetupWrite(ok bool) tea.Cmd {
	st := &m.setup
	st.phase, st.busy, st.writing = setupView, false, false
	if m.quitPending {
		held := st.result != nil
		if ok && held {
			// Notes or recovery to read (the backup's path): the quit waits.
			st.status = "quit held back so the notes can be read; press q to quit"
			st.result.Text += "\n" + st.status
		}
		if !ok || held {
			m.quitPending, m.tab = false, TabSetup
		} else {
			m.quitPending = false
			if _, cmd := m.quit(); cmd != nil {
				return cmd
			}
		}
	}
	return m.observeSetupCmd()
}

// switchTab moves to the next tab and, on a tab's first visit, starts the
// read that fills it: the Setup tab's observation, the Models tab's probe.
func (m *model) switchTab() tea.Cmd {
	m.tab = nextTab(m.tab)
	switch {
	case m.tab == TabSetup && !m.setup.asked:
		return m.observeSetupCmd()
	case m.tab == TabModels && !m.models.asked:
		return m.probeCmd()
	}
	return nil
}

// setupModes are the mode choices in the order they are drawn and cycled:
// local, the setup this screen is for, then native. Remote is not offered
// here; it is set in `wt config` (remoteElsewhere).
var setupModes = []modelmode.Mode{modelmode.ModeLocal, modelmode.ModeNative}

// modeAllowed is false for local where launchd is not available.
func (st *setupTab) modeAllowed(mode modelmode.Mode) bool {
	return mode != modelmode.ModeLocal || st.state.Platform.OK
}

// stepMode moves the mode choice by step (+1 right, -1 left), round the
// ends and over a choice that is not allowed. From no choice, right is the
// first and left the last.
func (st *setupTab) stepMode(step int) {
	i := -1
	for j, mode := range setupModes {
		if mode == st.want.Mode {
			i = j
		}
	}
	if i < 0 && step < 0 {
		i = 0
	}
	n := len(setupModes)
	for range n {
		i = ((i+step)%n + n) % n
		if st.modeAllowed(setupModes[i]) {
			break
		}
	}
	st.want.Mode, st.chosen = setupModes[i], true
}

// setupWriteBlocked is why a write key (a, s, x, R) does nothing now, or ""
// when it may act. The hints and the handlers both read it (T5).
func (m *model) setupWriteBlocked() string {
	st := &m.setup
	switch {
	case !st.loaded:
		return "the model server's state is still being read; nothing was changed"
	case st.state.Config.ParseErr != "":
		return "config.toml at " + st.state.Config.Path + " does not parse, so nothing is written; fix it, then press r; nothing was changed"
	case st.state.Config.TableErr != "":
		return "[model-server] in " + st.state.Config.Path + " cannot be read, so nothing is written; fix it, then press r; nothing was changed"
	case st.state.Config.RestErr != "":
		return "config.toml at " + st.state.Config.Path + " has a key that cannot be read, so nothing is written; fix it, then press r; nothing was changed"
	case st.busy:
		return "wait: the last action is still running; nothing was changed"
	}
	return ""
}

// setupControlBlocked is why s, x and R do nothing now, or "".
func (m *model) setupControlBlocked() string {
	st := &m.setup
	if why := m.setupWriteBlocked(); why != "" {
		return why
	}
	switch {
	case st.want.Mode != modelmode.ModeLocal:
		return "s, x and R start, stop and restart the local model server's LaunchAgent: choose local first"
	case !st.state.Platform.OK:
		return "the local model server cannot run here, so s, x and R do nothing"
	}
	return ""
}

// focusSetupField gives the focus to the field being edited, with the
// cursor at the end of its text.
func (st *setupTab) focusSetupField() {
	for f := range st.fields {
		st.fields[f].Blur()
	}
	st.fields[st.field].Focus()
	st.fields[st.field].CursorEnd()
}

// updateSetup handles a message while the Setup tab is showing. It
// never calls a dep: observing, planning, applying and start/stop are
// commands.
func (m *model) updateSetup(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.setup.phase {
	case setupEdit:
		return m.updateSetupEdit(msg)
	case setupConfirm:
		return m.updateSetupConfirm(msg)
	case setupApplying:
		return m.updateSetupApplying(msg)
	case setupMenu:
		return m.updateSetupMenu(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.setup
	if st.result != nil && m.updateSetupResult(key.String()) {
		return m, nil
	}
	if !st.busy {
		st.status = ""
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "tab":
		return m, m.switchTab()
	case "c":
		// Observe runs every check, the ping of the entered url included,
		// and writes nothing.
		st.status, st.pending = m.cancelSetupPlan()+"checking the model server", true
		st.settle, st.settled, st.waiting = 0, "", false // its read replaces the wait after a start or restart
		return m, m.observeSetupCmd()
	case "r":
		// The same read; the one before it, if still out, is dropped. This is
		// how a config.toml repaired by hand is read again.
		st.status, st.pending = m.cancelSetupPlan()+"re-reading the model server's state", true
		st.settle, st.settled, st.waiting = 0, "", false // its read replaces the wait after a start or restart
		return m, m.observeSetupCmd()
	case "left", "right", " ", "down", "enter":
		// The form is inert while anything is out (busy): a read, a plan,
		// an apply or a start/stop. So a plan is always of the form a saw,
		// and no question can appear under a field being typed into (a y in
		// a url must never confirm). These keys only say why they wait.
		modeKey := key.String() == "left" || key.String() == "right" || key.String() == " "
		if st.busy && !(st.probeRead && modeKey) {
			st.status = "wait: the form does not change while the model server tab is busy"
			return m, nil
		}
		was := st.want.Mode
		m.updateSetupForm(key.String())
		if st.want.Mode == modelmode.ModeLocal && was != modelmode.ModeLocal && st.probeSkipped {
			// The read shown skipped the LiteLLM probe (its mode was not
			// local), so read again with it, exactly as r does.
			st.status, st.pending = m.cancelSetupPlan()+"re-reading the model server's state", true
			cmd := m.observeSetupCmd()
			st.probeRead = true
			return m, cmd
		}
	case "a":
		return m, m.startSetupPlan()
	case "k":
		m.openSetupMenu()
	case "t":
		if why := m.setupTightenBlocked(); why != "" {
			st.status = why
			return m, nil
		}
		return m, m.startSetupPlanWith(modelsetup.LiteLLMTighten)
	case "s":
		return m, m.startControl(modelsetup.ControlStart)
	case "x":
		return m, m.setupControlCmd(modelsetup.ControlStop)
	case "R":
		return m, m.startControl(modelsetup.ControlRestart)
	case "g":
		return m, m.startGateway()
	}
	// ctrl+s, a save key elsewhere, does nothing here.
	return m, nil
}

// cancelSetupPlan drops a plan whose read c or r has just replaced: the
// older read, or the plan already out after it (planOut), is dropped by its
// generation when it lands, so the plan would silently never come. The
// snapshot, secrets included, goes now; the result is the words to put
// before the new status ("planning cancelled; "), empty if no plan was
// waiting.
func (m *model) cancelSetupPlan() string {
	st := &m.setup
	if st.planGen == 0 && !st.planOut {
		return ""
	}
	st.planGen, st.queue, st.planWant, st.planOut = 0, nil, modelsetup.Desired{}, false
	return "planning cancelled; "
}

// updateSetupForm moves the mode choice (left, right, space) or goes into
// the chosen mode's fields (down, enter), once an observation is in.
func (m *model) updateSetupForm(key string) {
	st := &m.setup
	if !st.loaded {
		return
	}
	switch key {
	case "left":
		st.stepMode(-1)
	case "right", " ":
		st.stepMode(1)
	default:
		if fields := st.visibleFields(); len(fields) > 0 {
			st.phase, st.field = setupEdit, fields[0]
			st.focusSetupField()
		}
	}
}

// startSetupPlan is a: it reads the machine again with the fields' values
// (always: a plan from an older read could act on a port taken since), and
// that read goes on to the question and the plan (applySetupObserved).
func (m *model) startSetupPlan() tea.Cmd {
	return m.startSetupPlanWith(modelsetup.LiteLLMNone)
}

// startSetupPlanWith is a with a LiteLLM action (k's menu, t): the action
// is part of the snapshot, and no entered master key goes with it, since
// the action decides the key itself.
func (m *model) startSetupPlanWith(action modelsetup.LiteLLMAction) tea.Cmd {
	st := &m.setup
	if why := m.setupWriteBlocked(); why != "" {
		st.status = why
		return nil
	}
	// One snapshot of the form for the read and the plan alike.
	st.planWant, st.queue = m.setupDesired(), nil
	if action != modelsetup.LiteLLMNone {
		st.planWant.LiteLLM, st.planWant.MasterKey = action, ""
	}
	st.status, st.pending = "reading the machine again before planning…", true
	cmd := m.observeSetupCmdWith(m.setupObserveOptions())
	st.planGen = st.gen
	return cmd
}

// updateSetupEdit handles the fields: up and down (and enter, tab) move
// between them, past either end back to the mode choice; esc leaves them;
// every other key is the field's. Nothing here acts on the machine.
func (m *model) updateSetupEdit(msg tea.Msg) (tea.Model, tea.Cmd) {
	st := &m.setup
	fields := st.visibleFields()
	at := -1
	for i, f := range fields {
		if f == st.field {
			at = i
		}
	}
	leave := func() (tea.Model, tea.Cmd) {
		st.fields[st.field].Blur()
		st.phase = setupView
		return m, nil
	}
	if at < 0 {
		return leave()
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m.quit()
		case "esc":
			return leave()
		case "up", "shift+tab":
			if at == 0 {
				return leave()
			}
			st.field = fields[at-1]
			st.focusSetupField()
			return m, nil
		case "down", "enter", "tab":
			if at == len(fields)-1 {
				return leave()
			}
			st.field = fields[at+1]
			st.focusSetupField()
			return m, nil
		}
	}
	before := st.fields[st.field].Value()
	var cmd tea.Cmd
	st.fields[st.field], cmd = st.fields[st.field].Update(msg)
	if st.fields[st.field].Value() != before {
		st.edited = true
	}
	return m, cmd
}

// updateSetupConfirm answers the question showing. Default No (P1): only y
// or Y says yes; n or N says no (which cancels the plan's confirm); esc
// cancels whatever is being planned; every other key is ignored and the
// question stays. ctrl+c quits.
func (m *model) updateSetupConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.setup
	var yes bool
	switch key.String() {
	case "ctrl+c":
		return m.quit()
	case "y", "Y":
		yes = true
	case "n", "N":
	case "esc":
		st.phase, st.queue, st.plan, st.planText, st.planWant = setupView, nil, modelsetup.Plan{}, "", modelsetup.Desired{}
		st.status = "not applied; nothing was changed"
		if st.prompt == promptGateway {
			st.status = "subscription switch not written; nothing was changed"
		}
		if st.prompt == promptRunUnused {
			// The unused-server gate starts nothing, so it cancels in its own
			// words, exactly as its n does (G2N).
			st.status = "not " + controlWords[st.control][1] + "; nothing was changed"
		}
		return m, nil
	default:
		return m, nil
	}
	switch st.prompt {
	case promptGateway:
		if yes {
			return m, m.setupGatewayCmd()
		}
		st.phase = setupView
		st.status = "subscription switch not written; nothing was changed"
		return m, nil
	case promptRunUnused:
		if yes {
			return m, m.setupControlCmd(st.control)
		}
		st.phase = setupView
		st.status = "not " + controlWords[st.control][1] + "; nothing was changed"
		return m, nil
	case promptReplaceRemote:
		if !yes {
			st.phase, st.queue, st.planWant = setupView, nil, modelsetup.Desired{}
			st.status = "not applied; nothing was changed"
			return m, nil
		}
		// y: go on to the plan and its own confirm.
		return m, m.nextSetupStep()
	case promptRemoveAgent:
		st.planWant.RemoveAgent = yes
	default:
		if yes {
			return m, m.setupApplyCmd()
		}
		st.phase, st.plan, st.planText = setupView, modelsetup.Plan{}, ""
		st.status = "not applied; nothing was changed"
		return m, nil
	}
	return m, m.nextSetupStep()
}

// updateSetupApplying takes keys while an apply or a start/stop is out:
// a quit (held until the result is in, leave) and tab; nothing else acts.
func (m *model) updateSetupApplying(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "tab":
		return m, m.switchTab()
	}
	return m, nil
}
