package configeditor

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeview"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// serverPhase is what the Model server tab is showing: the status and the
// mode choice, the same with the form's fields taking keys, a yes/no question
// (the plan's confirm or one asked before planning), or an apply or a
// start/stop in flight.
type serverPhase int

const (
	serverView serverPhase = iota
	serverEdit
	serverConfirm
	serverApplying
	// serverMenu is the LiteLLM key menu k opens.
	serverMenu
)

// serverPrompt is the question the confirm phase asks. Each is default-No
// (P1): only y or Y says yes.
type serverPrompt int

const (
	// promptApply is "Apply this plan? [y/N]" under the plan's text.
	promptApply serverPrompt = iota
	// promptRemoveAgent is asked before planning when the choice leaves
	// local with wt's LaunchAgent installed; y sets Desired.RemoveAgent.
	promptRemoveAgent
	// promptSaveAnyway is asked before planning after the check of the
	// entered remote url failed; y sets Desired.SaveUnchecked.
	promptSaveAnyway
)

// The form's fields. Each mode shows its own (visibleFields): remote the url,
// a token file and a pasted token; local the listen address, its token file
// and, when no key is on record, the LiteLLM master key.
const (
	sfURL = iota
	sfRemoteToken
	sfPaste
	sfListen
	sfLocalToken
	sfMaster
	sfCount
)

// serverFieldLabels name the fields beside their inputs; with the "> " that
// marks the field being edited they fit the status block's label column.
var serverFieldLabels = [sfCount]string{"url", "token", "paste", "listen", "token", "master"}

// serverFieldHints are the inputs' placeholders, shown while a field is
// empty: what goes there, or what an empty field means.
var serverFieldHints = [sfCount]string{
	"https://host:8787",
	"path of a 0600 token file",
	"or paste the token (hidden)",
	"empty: " + modelsetup.DefaultListen,
	"empty: the default token file",
	"LiteLLM master key (hidden)",
}

// serverTab is the Model server tab's state. Everything it knows of the
// machine comes from observations run in commands (observeServerCmd), and
// every plan, apply and start/stop runs in a command too; nothing here reads
// a file, dials a server or calls a ServerDeps func.
type serverTab struct {
	phase serverPhase
	// state is the latest observation; loaded is true once one is in.
	state  modelsetup.State
	loaded bool
	// want holds the mode choice; the rest of a Desired comes from the
	// fields when it is built (serverDesired).
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
	// pasted token and the master key are never filled; they are cleared
	// after a successful apply.
	fields [sfCount]textinput.Model
	inputs bool
	field  int
	edited bool
	// planGen is the observation a leads to a plan from (0: none wanted);
	// queue the questions still to ask before planning. planWant is the
	// snapshot of the form a took (serverDesired, with the options that read
	// used built from the same fields): the questions' answers go into it
	// and Plan gets it, so what is planned is what was checked even if a
	// field changed since. It holds the secrets until the plan is
	// dispatched, then is cleared.
	planGen  int
	queue    []serverPrompt
	planWant modelsetup.Desired
	// planOut is true while serverPlanCmd's plan is out, so c or r can say
	// they cancelled it (it is dropped by its generation when it lands).
	planOut bool
	// prompt is the question the confirm phase asks; plan and planText the
	// plan it is about and the text shown for it.
	prompt   serverPrompt
	plan     modelsetup.Plan
	planText string
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
	// native to remote (which passes over local) is not stopped halfway.
	probeRead bool
}

// serverObservedMsg carries one observation.
type serverObservedMsg struct {
	gen int
	st  modelsetup.State
	// skippedProbe is whether the observation left out the LiteLLM probe
	// (its options' LiteLLMProbeSkipped for the mode it resolved).
	skippedProbe bool
}

// serverPlannedMsg carries one plan, or why there is none.
type serverPlannedMsg struct {
	gen  int
	plan modelsetup.Plan
	err  error
}

// serverAppliedMsg carries an apply's result and the progress lines Apply
// reported, collected inside the command.
type serverAppliedMsg struct {
	res      modelsetup.Result
	progress []string
}

// serverControlledMsg carries a start, stop or restart's outcome.
type serverControlledMsg struct {
	action modelsetup.ControlAction
	err    error
}

// needsMasterKey is true when no LiteLLM master key is on record: neither
// wt-model's pair nor the LiteLLM plist holds one.
func needsMasterKey(s modelsetup.State) bool {
	return !s.Pair.KeySet && !s.LiteLLMPlist.HasMasterKey
}

// visibleFields are the fields the chosen mode shows, in order.
func (st *serverTab) visibleFields() []int {
	switch st.want.Mode {
	case modelclient.ModeRemote:
		return []int{sfURL, sfRemoteToken, sfPaste}
	case modelclient.ModeLocal:
		if needsMasterKey(st.state) {
			return []int{sfListen, sfLocalToken, sfMaster}
		}
		return []int{sfListen, sfLocalToken}
	}
	return nil
}

// value is a field's text with the spaces round it taken off.
func (st *serverTab) value(f int) string { return strings.TrimSpace(st.fields[f].Value()) }

// ensureServerInputs makes the fields once. The secrets are masked
// (EchoPassword); no "> " prompt, the label is beside each.
func (m *model) ensureServerInputs() {
	st := &m.server
	if st.inputs {
		return
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	for f := range st.fields {
		in := editorkit.NewTextInput("", serverFieldHints[f])
		in.Prompt = ""
		in.PlaceholderStyle = dim
		if f == sfPaste || f == sfMaster {
			in.EchoMode = textinput.EchoPassword
		}
		st.fields[f] = in
	}
	st.inputs = true
	m.resizeServerInputs()
}

// resizeServerInputs fits the inputs to the columns beside the label column,
// less one for the cursor.
func (m *model) resizeServerInputs() {
	st := &m.server
	if !st.inputs {
		return
	}
	for f := range st.fields {
		st.fields[f].Width = max(m.width-editorkit.LabelW-1, 3)
	}
}

// fillServerFields sets the non-secret fields from what is stored, unless
// the user has edited one: remote's url and token file from [model-server];
// local's listen address and token file from wt's LaunchAgent, else the
// stored token file. An empty field is a plan's default.
func (st *serverTab) fillServerFields() {
	if st.edited {
		return
	}
	s := st.state
	t := s.Config.Table
	stored, _ := modelclient.ParseMode(t.Mode)
	var url, rtok, listen, ltok string
	if stored == modelclient.ModeRemote {
		url, rtok = t.URL, t.TokenFile
	}
	if s.Agent.Managed {
		listen, ltok = s.Agent.Listen, s.Agent.TokenFile
	}
	if ltok == "" && stored == modelclient.ModeLocal {
		ltok = t.TokenFile
	}
	for f, v := range map[int]string{sfURL: url, sfRemoteToken: rtok, sfListen: listen, sfLocalToken: ltok} {
		st.fields[f].SetValue(v)
	}
}

// serverObserveOptions are the options an observation is made with: the
// fields of the chosen mode. Local looks for a listener on the edited port
// and describes a typed token file; remote describes a typed token file and
// pings the url entered with the token entered (pasted, else the typed file).
func (m *model) serverObserveOptions() modelsetup.ObserveOptions {
	st := &m.server
	var o modelsetup.ObserveOptions
	o.SkipLiteLLMProbe = st.want.Mode != modelclient.ModeLocal
	// Until the user moves the mode choice the mode shown is the one the read
	// resolves, so a config that stores local is probed in that same read.
	o.ProbeLiteLLMWhenLocal = !st.chosen
	switch st.want.Mode {
	case modelclient.ModeLocal:
		if _, port, err := net.SplitHostPort(st.value(sfListen)); err == nil {
			if p, err := strconv.Atoi(port); err == nil {
				o.ListenPort = p
			}
		}
		if p := st.value(sfLocalToken); p != "" {
			o.TokenPaths = []string{p}
		}
	case modelclient.ModeRemote:
		tok := st.value(sfRemoteToken)
		if tok != "" {
			o.TokenPaths = []string{tok}
		}
		if url := st.value(sfURL); url != "" {
			o.CheckURL, o.CheckToken, o.CheckTokenFile = url, modelsetup.Secret(st.fields[sfPaste].Value()), tok
		}
	}
	return o
}

// serverDesired is the setup the user asked for: the mode and the chosen
// mode's fields (the questions' answers are added to a's snapshot). A
// secret becomes a modelsetup.Secret here and nowhere earlier; the master
// key goes in only while it is asked for.
func (m *model) serverDesired() modelsetup.Desired {
	st := &m.server
	var d modelsetup.Desired
	d.Mode = st.want.Mode
	switch st.want.Mode {
	case modelclient.ModeLocal:
		d.Listen, d.TokenFile = st.value(sfListen), st.value(sfLocalToken)
		if needsMasterKey(st.state) {
			d.MasterKey = modelsetup.Secret(st.fields[sfMaster].Value())
		}
	case modelclient.ModeRemote:
		d.URL, d.TokenFile = st.value(sfURL), st.value(sfRemoteToken)
		d.PastedToken = modelsetup.Secret(st.fields[sfPaste].Value())
	}
	return d
}

// observeServerCmd reads the machine once, off the update loop. The deps,
// the generation and the options are taken here, before the command, so the
// command touches nothing of the model; all I/O is inside it.
func (m *model) observeServerCmd() tea.Cmd {
	return m.observeServerCmdWith(m.serverObserveOptions())
}

// observeServerCmdWith is observeServerCmd with the options given.
func (m *model) observeServerCmdWith(opts modelsetup.ObserveOptions) tea.Cmd {
	m.server.gen++
	m.server.asked, m.server.busy, m.server.probeRead = true, true, false
	gen, observe := m.server.gen, m.opts.Server.Observe
	return func() tea.Msg {
		st := observe(context.Background(), opts)
		return serverObservedMsg{gen: gen, st: st, skippedProbe: opts.LiteLLMProbeSkipped(st.Resolution.Mode)}
	}
}

// applyServerObserved takes an observation, unless a later one is out. The
// observation a asked for goes on to the questions and the plan.
func (m *model) applyServerObserved(msg serverObservedMsg) tea.Cmd {
	st := &m.server
	if msg.gen != st.gen {
		return nil // a later observation is on its way
	}
	st.state, st.loaded, st.busy, st.probeSkipped, st.probeRead = msg.st, true, false, msg.skippedProbe, false
	if st.pending {
		st.status, st.pending = "", false
	}
	if !st.chosen {
		st.want.Mode = msg.st.Resolution.Mode
	}
	m.ensureServerInputs()
	st.fillServerFields()
	if st.planGen != msg.gen {
		return nil
	}
	st.planGen = 0
	if why := m.serverWriteBlocked(); why != "" {
		// The fresh read found what blocks a write (a parse error).
		st.planWant, st.queue = modelsetup.Desired{}, nil
		st.status = why
		return nil
	}
	s, want := st.state, st.planWant
	if want.Mode != modelclient.ModeLocal && s.Agent.Present && s.Agent.Managed {
		st.queue = append(st.queue, promptRemoveAgent)
	}
	if want.Mode == modelclient.ModeRemote && want.URL != "" && s.Server.Checked && !s.Server.OK {
		st.queue = append(st.queue, promptSaveAnyway)
	}
	return m.nextServerStep()
}

// nextServerStep asks the next question queued before planning, or plans
// when none is left.
func (m *model) nextServerStep() tea.Cmd {
	st := &m.server
	if len(st.queue) > 0 {
		st.prompt, st.queue = st.queue[0], st.queue[1:]
		st.phase = serverConfirm
		return nil
	}
	return m.serverPlanCmd()
}

// serverPlanCmd plans from the latest observation and the snapshot a took,
// in a command: Plan is a dep like the others, so it is not called from
// Update. The master key goes only while the fresh read still asks for it.
func (m *model) serverPlanCmd() tea.Cmd {
	st := &m.server
	st.gen++
	st.phase, st.busy, st.status, st.pending = serverView, true, "planning…", true
	want := st.planWant
	if !needsMasterKey(st.state) {
		want.MasterKey = ""
	}
	st.planWant, st.planOut = modelsetup.Desired{}, true
	gen, plan, state := st.gen, m.opts.Server.Plan, st.state
	return func() tea.Msg {
		p, err := plan(state, want)
		return serverPlannedMsg{gen: gen, plan: p, err: err}
	}
}

// applyServerPlanned shows a plan on the confirm screen, or why there is
// none: a refusal in its own words (each reason already says nothing was
// changed).
func (m *model) applyServerPlanned(msg serverPlannedMsg) {
	st := &m.server
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
	st.plan, st.planText = msg.plan, serverPlanText(st.state, msg.plan)
	st.phase, st.prompt = serverConfirm, promptApply
}

// serverPlanText is the text the user confirms: the plan's Format, and a
// note when the plan writes a url (Plan.WritesURL) over a stored one that
// held an '@' (ConfigState.URLHadCredentials; Observe shows it only as a
// placeholder), since the url written replaces it and its credentials are
// gone. Both are data, not wording.
func serverPlanText(s modelsetup.State, p modelsetup.Plan) string {
	text := editorkit.Scrub(p.Format())
	if s.Config.URLHadCredentials && p.WritesURL() {
		text += "the stored url's credentials are not kept: it contained an @, and the url above replaces it\n"
	}
	return text
}

// serverApplyCmd runs the confirmed plan in a command. The deps and the plan
// are taken here; the progress lines are gathered inside the command, in a
// slice nothing else sees, and handed over in the result message.
func (m *model) serverApplyCmd() tea.Cmd {
	st := &m.server
	st.phase, st.busy, st.writing, st.status, st.pending = serverApplying, true, true, "applying the plan…", false
	apply, plan := m.opts.Server.Apply, st.plan
	st.plan, st.planText = modelsetup.Plan{}, ""
	return func() tea.Msg {
		var lines []string
		res := apply(context.Background(), plan, func(line string) { lines = append(lines, line) })
		return serverAppliedMsg{res: res, progress: lines}
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

// serverResultText says what an apply did: the progress lines, then what
// ran or where it stopped and why, then the warnings. A wait that ran out
// names the url, how the last ping failed and the log to read.
func serverResultText(res modelsetup.Result, progress []string) string {
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

// applyServerApplied takes an apply's result: it says what ran, clears the
// secrets after a success, and reads the machine again.
func (m *model) applyServerApplied(msg serverAppliedMsg) tea.Cmd {
	st := &m.server
	st.status = serverResultText(msg.res, msg.progress)
	st.result = editorkit.NewResultScreen(msg.res.Recovery, msg.res.Notes, st.status)
	ok := msg.res.Failed == nil && msg.res.Err == nil
	if ok {
		st.fields[sfPaste].SetValue("")
		st.fields[sfMaster].SetValue("")
		st.edited = false
	}
	return m.finishServerWrite(ok)
}

// controlWords are an action's -ing and past forms.
var controlWords = map[modelsetup.ControlAction][2]string{
	modelsetup.ControlStart:   {"starting", "started"},
	modelsetup.ControlStop:    {"stopping", "stopped"},
	modelsetup.ControlRestart: {"restarting", "restarted"},
}

// serverControlCmd starts, stops or restarts the LaunchAgent in a command,
// the dep taken here.
func (m *model) serverControlCmd(a modelsetup.ControlAction) tea.Cmd {
	st := &m.server
	if why := m.serverControlBlocked(); why != "" {
		st.status = why
		return nil
	}
	st.phase, st.busy, st.writing, st.status, st.pending = serverApplying, true, true, controlWords[a][0]+" the LaunchAgent…", false
	control := m.opts.Server.Control
	return func() tea.Msg {
		return serverControlledMsg{action: a, err: control(context.Background(), a)}
	}
}

// applyServerControlled says how a start, stop or restart went and reads the
// machine again.
func (m *model) applyServerControlled(msg serverControlledMsg) tea.Cmd {
	st := &m.server
	if msg.err != nil {
		st.status = msg.action.String() + " failed: " + editorkit.Scrub(msg.err.Error())
	} else {
		st.status = modelsetup.AgentLabel + " " + controlWords[msg.action][1]
	}
	return m.finishServerWrite(msg.err == nil)
}

// finishServerWrite ends an apply or a start/stop: the tab takes keys again,
// a quit that waited for it happens now (through quit, so agent edits still
// get their prompt) unless it failed, when the quit is called off here where
// the failure is drawn; otherwise the machine is read again. The status says
// what happened and stays through that read.
func (m *model) finishServerWrite(ok bool) tea.Cmd {
	st := &m.server
	st.phase, st.busy, st.writing = serverView, false, false
	if m.quitPending {
		held := st.result != nil
		if ok && held {
			// Notes or recovery to read (the backup's path): the quit waits.
			st.status = "quit held back so the notes can be read; press q to quit"
			st.result.Text += "\n" + st.status
		}
		if !ok || held {
			m.quitPending, m.tab = false, TabServer
			if m.discardHeld {
				m.discardHeld, m.dirty = false, true
			}
		} else {
			m.quitPending = false // discardHeld stays until the quit happens (leave)
			if _, cmd := m.quit(); cmd != nil {
				return cmd
			}
		}
	}
	return m.observeServerCmd()
}

// switchTab moves to the next tab and, on the Model server tab's first
// visit, starts the observation that fills it.
func (m *model) switchTab() tea.Cmd {
	m.tab = nextTab(m.tab)
	if m.tab == TabServer && !m.server.asked {
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

// serverWriteBlocked is why a write key (a, s, x, R) does nothing now, or ""
// when it may act. The hints and the handlers both read it (T5).
func (m *model) serverWriteBlocked() string {
	st := &m.server
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

// serverControlBlocked is why s, x and R do nothing now, or "".
func (m *model) serverControlBlocked() string {
	st := &m.server
	if why := m.serverWriteBlocked(); why != "" {
		return why
	}
	switch {
	case st.want.Mode != modelclient.ModeLocal:
		return "s, x and R start, stop and restart the local model server's LaunchAgent: choose local first"
	case !st.state.Platform.OK:
		return "the local model server cannot run here, so s, x and R do nothing"
	}
	return ""
}

// focusServerField gives the focus to the field being edited, with the
// cursor at the end of its text.
func (st *serverTab) focusServerField() {
	for f := range st.fields {
		st.fields[f].Blur()
	}
	st.fields[st.field].Focus()
	st.fields[st.field].CursorEnd()
}

// updateServer handles a message while the Model server tab is showing. It
// never calls a dep: observing, planning, applying and start/stop are
// commands.
func (m *model) updateServer(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.server.phase {
	case serverEdit:
		return m.updateServerEdit(msg)
	case serverConfirm:
		return m.updateServerConfirm(msg)
	case serverApplying:
		return m.updateServerApplying(msg)
	case serverMenu:
		return m.updateServerMenu(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.server
	if st.result != nil && m.updateServerResult(key.String()) {
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
		st.status, st.pending = m.cancelServerPlan()+"checking the model server", true
		return m, m.observeServerCmd()
	case "r":
		// The same read; the one before it, if still out, is dropped. This is
		// how a config.toml repaired by hand is read again.
		st.status, st.pending = m.cancelServerPlan()+"re-reading the model server's state", true
		return m, m.observeServerCmd()
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
		m.updateServerForm(key.String())
		if st.want.Mode == modelclient.ModeLocal && was != modelclient.ModeLocal && st.probeSkipped {
			// The read shown skipped the LiteLLM probe (its mode was not
			// local), so read again with it, exactly as r does.
			st.status, st.pending = m.cancelServerPlan()+"re-reading the model server's state", true
			cmd := m.observeServerCmd()
			st.probeRead = true
			return m, cmd
		}
	case "a":
		return m, m.startServerPlan()
	case "k":
		m.openServerMenu()
	case "t":
		if why := m.serverTightenBlocked(); why != "" {
			st.status = why
			return m, nil
		}
		return m, m.startServerPlanWith(modelsetup.LiteLLMTighten)
	case "s":
		return m, m.serverControlCmd(modelsetup.ControlStart)
	case "x":
		return m, m.serverControlCmd(modelsetup.ControlStop)
	case "R":
		return m, m.serverControlCmd(modelsetup.ControlRestart)
	}
	// ctrl+s is the Agents tab's save and does nothing here.
	return m, nil
}

// cancelServerPlan drops a plan whose read c or r has just replaced: the
// older read, or the plan already out after it (planOut), is dropped by its
// generation when it lands, so the plan would silently never come. The
// snapshot, secrets included, goes now; the result is the words to put
// before the new status ("planning cancelled; "), empty if no plan was
// waiting.
func (m *model) cancelServerPlan() string {
	st := &m.server
	if st.planGen == 0 && !st.planOut {
		return ""
	}
	st.planGen, st.queue, st.planWant, st.planOut = 0, nil, modelsetup.Desired{}, false
	return "planning cancelled; "
}

// updateServerForm moves the mode choice (left, right, space) or goes into
// the chosen mode's fields (down, enter), once an observation is in.
func (m *model) updateServerForm(key string) {
	st := &m.server
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
			st.phase, st.field = serverEdit, fields[0]
			st.focusServerField()
		}
	}
}

// startServerPlan is a: it reads the machine again with the fields' values
// (always: a plan from an older read could act on a port taken since, or a
// remote check of another url), and that read goes on to the questions and
// the plan (applyServerObserved).
func (m *model) startServerPlan() tea.Cmd {
	return m.startServerPlanWith(modelsetup.LiteLLMNone)
}

// startServerPlanWith is a with a LiteLLM action (k's menu, t): the action
// is part of the snapshot, and no entered master key goes with it, since
// the action decides the key itself.
func (m *model) startServerPlanWith(action modelsetup.LiteLLMAction) tea.Cmd {
	st := &m.server
	if why := m.serverWriteBlocked(); why != "" {
		st.status = why
		return nil
	}
	// One snapshot of the form for the read and the plan alike.
	st.planWant, st.queue = m.serverDesired(), nil
	if action != modelsetup.LiteLLMNone {
		st.planWant.LiteLLM, st.planWant.MasterKey = action, ""
	}
	st.status, st.pending = "reading the machine again before planning…", true
	cmd := m.observeServerCmdWith(m.serverObserveOptions())
	st.planGen = st.gen
	return cmd
}

// updateServerEdit handles the fields: up and down (and enter, tab) move
// between them, past either end back to the mode choice; esc leaves them;
// every other key is the field's. Nothing here acts on the machine.
func (m *model) updateServerEdit(msg tea.Msg) (tea.Model, tea.Cmd) {
	st := &m.server
	fields := st.visibleFields()
	at := -1
	for i, f := range fields {
		if f == st.field {
			at = i
		}
	}
	leave := func() (tea.Model, tea.Cmd) {
		st.fields[st.field].Blur()
		st.phase = serverView
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
			st.focusServerField()
			return m, nil
		case "down", "enter", "tab":
			if at == len(fields)-1 {
				return leave()
			}
			st.field = fields[at+1]
			st.focusServerField()
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

// updateServerConfirm answers the question showing. Default No (P1): only y
// or Y says yes; n or N says no (which cancels the plan's confirm); esc
// cancels whatever is being planned; every other key is ignored and the
// question stays. ctrl+c quits.
func (m *model) updateServerConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.server
	var yes bool
	switch key.String() {
	case "ctrl+c":
		return m.quit()
	case "y", "Y":
		yes = true
	case "n", "N":
	case "esc":
		st.phase, st.queue, st.plan, st.planText, st.planWant = serverView, nil, modelsetup.Plan{}, "", modelsetup.Desired{}
		st.status = "not applied; nothing was changed"
		return m, nil
	default:
		return m, nil
	}
	switch st.prompt {
	case promptRemoveAgent:
		st.planWant.RemoveAgent = yes
	case promptSaveAnyway:
		st.planWant.SaveUnchecked = yes
	default:
		if yes {
			return m, m.serverApplyCmd()
		}
		st.phase, st.plan, st.planText = serverView, modelsetup.Plan{}, ""
		st.status = "not applied; nothing was changed"
		return m, nil
	}
	return m, m.nextServerStep()
}

// updateServerApplying takes keys while an apply or a start/stop is out:
// a quit (held until the result is in, leave) and tab; nothing else acts.
func (m *model) updateServerApplying(msg tea.Msg) (tea.Model, tea.Cmd) {
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
