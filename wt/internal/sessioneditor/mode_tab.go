package sessioneditor

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// localPointerNote is what the Mode tab says about local: the session only
// points at wt-model on this machine; `wt-model setup` sets it up.
const localPointerNote = "run `wt-model setup` to set up wt-model on this machine"

// runSetupQuestion is asked after local was applied, when wt-model is on
// PATH. Default No (P1).
const runSetupQuestion = "wt-model is on PATH: run `wt-model setup` now? [y/N]"

// ModeDeps are the Mode tab's ways out of the program. Each is called only
// from a command, never from Update. A nil func is filled from
// ModeDepsFromEnv(modelmode.DefaultEnv()) (withDefaults).
type ModeDeps struct {
	Observe func(ctx context.Context, o modelmode.ObserveOptions) modelmode.State
	Plan    func(st modelmode.State, want modelmode.Desired) (modelmode.Plan, error)
	Apply   func(ctx context.Context, p modelmode.Plan, progress func(string)) modelmode.Result
}

// ModeDepsFromEnv builds the three functions over env, so a caller can
// inject fakes of the seams instead of the functions.
func ModeDepsFromEnv(env modelmode.Env) ModeDeps {
	return ModeDeps{
		Observe: func(ctx context.Context, o modelmode.ObserveOptions) modelmode.State {
			return modelmode.Observe(ctx, env, o)
		},
		Plan: modelmode.BuildPlan,
		Apply: func(ctx context.Context, p modelmode.Plan, progress func(string)) modelmode.Result {
			return modelmode.Apply(ctx, env, p, progress)
		},
	}
}

// withDefaults keeps every func the caller set and fills the rest from the
// real machine.
func (d ModeDeps) withDefaults() ModeDeps {
	def := ModeDepsFromEnv(modelmode.DefaultEnv())
	if d.Observe == nil {
		d.Observe = def.Observe
	}
	if d.Plan == nil {
		d.Plan = def.Plan
	}
	if d.Apply == nil {
		d.Apply = def.Apply
	}
	return d
}

// modePhase is what the Mode tab is showing: the status and the mode choice,
// the same with the form's fields taking keys, a yes/no question, or an
// apply in flight.
type modePhase int

const (
	modeView modePhase = iota
	modeEdit
	modeConfirm
	modeApplying
)

// modePrompt is the question the confirm phase asks. Each is default-No
// (P1): only y or Y says yes.
type modePrompt int

const (
	// promptApply is "Apply this plan? [y/N]" under the plan's text.
	promptApply modePrompt = iota
	// promptSaveAnyway is asked before planning after the check of the
	// entered remote url failed; y sets Desired.SaveUnchecked.
	promptSaveAnyway
	// promptRunSetup is asked after local was applied with wt-model on
	// PATH; y ends the editor asking its caller to run `wt-model setup`.
	promptRunSetup
)

// The form's fields: remote alone has them (a url, a token file and a
// pasted token); native and local show none.
const (
	mfURL = iota
	mfToken
	mfPaste
	mfCount
)

// modeFieldLabels name the fields beside their inputs; with the "> " that
// marks the field being edited they fit the status block's label column.
var modeFieldLabels = [mfCount]string{"url", "token", "paste"}

// modeFieldHints are the inputs' placeholders, shown while a field is empty.
var modeFieldHints = [mfCount]string{
	"https://host:8787",
	"path of a 0600 token file",
	"or paste the token (hidden)",
}

// modeTab is the Mode tab's state. Everything it knows of the machine comes
// from observations run in commands (observeModeCmd), and every plan and
// apply runs in a command too; nothing here reads a file, dials a server or
// calls a ModeDeps func.
type modeTab struct {
	phase modePhase
	// state is the latest observation; loaded is true once one is in.
	state  modelmode.State
	loaded bool
	// want holds the mode choice; the rest of a Desired comes from the
	// fields when it is built (modeDesired).
	want modelmode.Desired
	// chosen is true once the user has moved the mode choice; until then a
	// new observation sets the choice to the effective mode.
	chosen bool
	// asked is true once the first observation has been dispatched, so the
	// tab observes on its first visit only (c and r observe again).
	asked bool
	// gen numbers the observations and plans: a result that is not the
	// latest's is dropped, so a slow ping cannot overwrite a later answer.
	gen int
	// busy is true while an observation, a plan or an apply is out. writing
	// is true for an apply alone: a quit waits for it (leave), so its
	// result is always read.
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
	// pasted token is never filled; it is cleared after a successful apply.
	fields [mfCount]textinput.Model
	inputs bool
	field  int
	edited bool
	// localCheck is the check of the local server made by an observation
	// that asked about no other url (the tab's first read while the stored
	// mode is local): at plan time Server holds the entered remote url's
	// ping, so the plan's state carries this one as State.LocalServer and a
	// leaving-local note can name the server being left behind.
	localCheck modelmode.ServerState
	// planGen is the observation a leads to a plan from (0: none wanted);
	// queue the questions still to ask before planning. planWant is the
	// snapshot of the form a took: the questions' answers go into it and
	// Plan gets it. It holds the pasted token until the plan is dispatched,
	// then is cleared.
	planGen  int
	queue    []modePrompt
	planWant modelmode.Desired
	// planOut is true while modePlanCmd's plan is out, so c or r can say
	// they cancelled it.
	planOut bool
	// prompt is the question the confirm phase asks; plan and planText the
	// plan it is about and the text shown for it.
	prompt   modePrompt
	plan     modelmode.Plan
	planText string
	// applying is the mode of the plan out for Apply, so a successful local
	// apply can offer `wt-model setup`; setupPath is the wt-model found on
	// PATH for that offer.
	applying  modelmode.Mode
	setupPath string
	// result is the result screen after an apply that left notes or
	// recovery commands to read; nil when none is showing.
	result *editorkit.ResultScreen
}

// modeObservedMsg carries one observation, and whether it was made without
// an entered url to check (noCheck: the session url itself was pinged).
type modeObservedMsg struct {
	gen     int
	st      modelmode.State
	noCheck bool
}

// modePlannedMsg carries one plan, or why there is none.
type modePlannedMsg struct {
	gen  int
	plan modelmode.Plan
	err  error
}

// modeAppliedMsg carries an apply's result and the progress lines Apply
// reported, collected inside the command.
type modeAppliedMsg struct {
	res      modelmode.Result
	progress []string
}

// visibleFields are the fields the chosen mode shows, in order: remote's
// alone.
func (st *modeTab) visibleFields() []int {
	if st.want.Mode == modelmode.ModeRemote {
		return []int{mfURL, mfToken, mfPaste}
	}
	return nil
}

// value is a field's text with the spaces round it taken off.
func (st *modeTab) value(f int) string { return strings.TrimSpace(st.fields[f].Value()) }

// ensureModeInputs makes the fields once. The pasted token is masked
// (EchoPassword); no "> " prompt, the label is beside each.
func (m *model) ensureModeInputs() {
	st := &m.mode
	if st.inputs {
		return
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	for f := range st.fields {
		in := editorkit.NewTextInput("", modeFieldHints[f])
		in.Prompt = ""
		in.PlaceholderStyle = dim
		if f == mfPaste {
			in.EchoMode = textinput.EchoPassword
		}
		st.fields[f] = in
	}
	st.inputs = true
	m.resizeModeInputs()
}

// resizeModeInputs fits the inputs to the columns beside the label column,
// less one for the cursor.
func (m *model) resizeModeInputs() {
	st := &m.mode
	if !st.inputs {
		return
	}
	for f := range st.fields {
		st.fields[f].Width = max(m.width-editorkit.LabelW-1, 3)
	}
}

// fillModeFields sets the url and token file from what is stored when the
// stored mode is remote, unless the user has edited a field.
func (st *modeTab) fillModeFields() {
	if st.edited {
		return
	}
	t := st.state.Config.Table
	stored, _ := modelmode.ParseMode(t.Mode)
	var url, tok string
	if stored == modelmode.ModeRemote {
		url, tok = t.URL, t.TokenFile
	}
	st.fields[mfURL].SetValue(url)
	st.fields[mfToken].SetValue(tok)
}

// modeObserveOptions are the options an observation is made with: for
// remote, a typed token file is described and the url entered is pinged with
// the token entered (pasted, else the typed file). Native and local add
// nothing.
func (m *model) modeObserveOptions() modelmode.ObserveOptions {
	st := &m.mode
	var o modelmode.ObserveOptions
	if st.want.Mode != modelmode.ModeRemote {
		return o
	}
	tok := st.value(mfToken)
	if tok != "" {
		o.TokenPaths = []string{tok}
	}
	if url := st.value(mfURL); url != "" {
		o.CheckURL, o.CheckToken, o.CheckTokenFile = url, modelmode.Secret(st.fields[mfPaste].Value()), tok
	}
	return o
}

// modeDesired is the setup the user asked for: the mode and, for remote,
// its fields. Local carries the mode alone (the pointer plan fills the url
// and token file itself), so an entered remote url never reaches it. A
// secret becomes a modelmode.Secret here and nowhere earlier.
func (m *model) modeDesired() modelmode.Desired {
	st := &m.mode
	d := modelmode.Desired{Mode: st.want.Mode}
	if st.want.Mode == modelmode.ModeRemote {
		d.URL, d.TokenFile = st.value(mfURL), st.value(mfToken)
		d.PastedToken = modelmode.Secret(st.fields[mfPaste].Value())
	}
	return d
}

// observeModeCmd reads the machine once, off the update loop. The deps,
// the generation and the options are taken here, before the command.
func (m *model) observeModeCmd() tea.Cmd {
	m.mode.gen++
	m.mode.asked, m.mode.busy = true, true
	gen, observe, opts := m.mode.gen, m.opts.Mode.Observe, m.modeObserveOptions()
	return func() tea.Msg {
		return modeObservedMsg{gen: gen, st: observe(context.Background(), opts), noCheck: opts.CheckURL == ""}
	}
}

// applyModeObserved takes an observation, unless a later one is out. The
// observation a asked for goes on to the questions and the plan.
func (m *model) applyModeObserved(msg modeObservedMsg) tea.Cmd {
	st := &m.mode
	if msg.gen != st.gen {
		return nil // a later observation is on its way
	}
	st.state, st.loaded, st.busy = msg.st, true, false
	if msg.noCheck && msg.st.Resolution.Mode == modelmode.ModeLocal {
		// The session url pinged was the local server's: remember that check
		// for the plan, whose own read may ping an entered remote url instead.
		st.localCheck = msg.st.Server
	}
	if st.pending {
		st.status, st.pending = "", false
	}
	if !st.chosen {
		st.want.Mode = msg.st.Resolution.Mode
	}
	m.ensureModeInputs()
	st.fillModeFields()
	if st.planGen != msg.gen {
		return nil
	}
	st.planGen = 0
	if why := m.modeWriteBlocked(); why != "" {
		// The fresh read found what blocks a write (a parse error).
		st.planWant, st.queue = modelmode.Desired{}, nil
		st.status = why
		return nil
	}
	s, want := st.state, st.planWant
	if want.Mode == modelmode.ModeRemote && want.URL != "" && s.Server.Checked && !s.Server.OK {
		st.queue = append(st.queue, promptSaveAnyway)
	}
	return m.nextModeStep()
}

// nextModeStep asks the next question queued before planning, or plans
// when none is left.
func (m *model) nextModeStep() tea.Cmd {
	st := &m.mode
	if len(st.queue) > 0 {
		st.prompt, st.queue = st.queue[0], st.queue[1:]
		st.phase = modeConfirm
		return nil
	}
	return m.modePlanCmd()
}

// modePlanCmd plans from the latest observation and the snapshot a took,
// in a command: Plan is a dep like the others, so it is not called from
// Update.
func (m *model) modePlanCmd() tea.Cmd {
	st := &m.mode
	st.gen++
	st.phase, st.busy, st.status, st.pending = modeView, true, "planning…", true
	want := st.planWant
	st.planWant, st.planOut = modelmode.Desired{}, true
	state := st.state
	state.LocalServer = st.localCheck
	gen, plan := st.gen, m.opts.Mode.Plan
	return func() tea.Msg {
		p, err := plan(state, want)
		return modePlannedMsg{gen: gen, plan: p, err: err}
	}
}

// applyModePlanned shows a plan on the confirm screen, or why there is
// none: a refusal in its own words (each reason already says nothing was
// changed).
func (m *model) applyModePlanned(msg modePlannedMsg) {
	st := &m.mode
	if msg.gen != st.gen {
		return
	}
	st.planOut = false
	st.busy, st.status, st.pending = false, "", false
	if msg.err != nil {
		var ref *modelmode.RefusalError
		if errors.As(msg.err, &ref) {
			st.status = editorkit.Scrub(ref.Error())
		} else {
			st.status = "no plan: " + editorkit.Scrub(msg.err.Error()) + "; nothing was changed"
		}
		return
	}
	st.plan, st.planText = msg.plan, modePlanText(st.state, msg.plan)
	st.phase, st.prompt = modeConfirm, promptApply
}

// modePlanText is the text the user confirms: the plan's Format, and a note
// when the plan writes a url (Plan.WritesURL) over a stored one that held an
// '@' (ConfigState.URLHadCredentials), since the url written replaces it and
// its credentials are gone. Both are data, not wording.
func modePlanText(s modelmode.State, p modelmode.Plan) string {
	text := editorkit.Scrub(p.Format())
	if s.Config.URLHadCredentials && p.WritesURL() {
		text += "the stored url's credentials are not kept: it contained an @, and the url above replaces it\n"
	}
	return text
}

// modeApplyCmd runs the confirmed plan in a command. The deps and the plan
// are taken here; the progress lines are gathered inside the command, in a
// slice nothing else sees, and handed over in the result message.
func (m *model) modeApplyCmd() tea.Cmd {
	st := &m.mode
	st.phase, st.busy, st.writing, st.status, st.pending = modeApplying, true, true, "applying the plan…", false
	apply, plan := m.opts.Mode.Apply, st.plan
	st.applying = plan.Want.Mode
	st.plan, st.planText = modelmode.Plan{}, ""
	return func() tea.Msg {
		var lines []string
		res := apply(context.Background(), plan, func(line string) { lines = append(lines, line) })
		return modeAppliedMsg{res: res, progress: lines}
	}
}

// stepKinds lists steps by kind, for a status line.
func stepKinds(steps []modelmode.Step) string {
	var kinds []string
	for _, s := range steps {
		kinds = append(kinds, string(s.Kind))
	}
	return strings.Join(kinds, ", ")
}

// modeResultText says what an apply did: the progress lines, then what ran
// or where it stopped and why, then the warnings.
func modeResultText(res modelmode.Result, progress []string) string {
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
		if res.Err != nil {
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

// applyModeApplied takes an apply's result: it says what ran, clears the
// pasted token after a success, and reads the machine again. A successful
// local apply then points at `wt-model setup`, offering to run it when
// wt-model is on PATH.
func (m *model) applyModeApplied(msg modeAppliedMsg) tea.Cmd {
	st := &m.mode
	st.status = modeResultText(msg.res, msg.progress)
	st.result = editorkit.NewResultScreen(msg.res.Recovery, msg.res.Notes, st.status)
	ok := msg.res.Failed == nil && msg.res.Err == nil
	if ok {
		st.fields[mfPaste].SetValue("")
		st.edited = false
	}
	local := ok && st.applying == modelmode.ModeLocal
	st.applying = ""
	quitting := m.quitPending
	cmd := m.finishModeWrite(ok)
	if local && !quitting {
		m.offerModelSetup()
	}
	return cmd
}

// offerModelSetup follows a successful local apply: with wt-model on PATH
// the confirm phase asks whether to run `wt-model setup` (default No);
// without it the status points at the command.
func (m *model) offerModelSetup() {
	st := &m.mode
	if look := m.opts.LookPath; look != nil {
		if path, err := look("wt-model"); err == nil && path != "" {
			st.setupPath = path
			st.phase, st.prompt = modeConfirm, promptRunSetup
			return
		}
	}
	st.status = localPointerNote
}

// finishModeWrite ends an apply: the tab takes keys again, a quit that
// waited for it happens now (through quit, so agent edits still get their
// prompt) unless it failed, when the quit is called off here where the
// failure is drawn; otherwise the machine is read again. The status says
// what happened and stays through that read.
func (m *model) finishModeWrite(ok bool) tea.Cmd {
	st := &m.mode
	st.phase, st.busy, st.writing = modeView, false, false
	if m.quitPending {
		held := st.result != nil
		if ok && held {
			// Notes or recovery to read: the quit waits.
			st.status = "quit held back so the notes can be read; press q to quit"
			st.result.Text += "\n" + st.status
		}
		if !ok || held {
			m.quitPending, m.tab = false, TabMode
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
	return m.observeModeCmd()
}

// switchTab moves to the next tab and, on the Mode tab's first visit,
// starts the observation that fills it.
func (m *model) switchTab() tea.Cmd {
	m.tab = nextTab(m.tab)
	if m.tab == TabMode && !m.mode.asked {
		return m.observeModeCmd()
	}
	return nil
}

// modeChoices are the mode choices in the order they are drawn and cycled.
// All three are allowed on every platform: local is a pointer here.
var modeChoices = []modelmode.Mode{modelmode.ModeNative, modelmode.ModeLocal, modelmode.ModeRemote}

// stepMode moves the mode choice by step (+1 right, -1 left), round the
// ends. From no choice, right is the first and left the last.
func (st *modeTab) stepMode(step int) {
	i := -1
	for j, mode := range modeChoices {
		if mode == st.want.Mode {
			i = j
		}
	}
	if i < 0 && step < 0 {
		i = 0
	}
	n := len(modeChoices)
	i = ((i+step)%n + n) % n
	st.want.Mode, st.chosen = modeChoices[i], true
}

// modeWriteBlocked is why a (the one write key) does nothing now, or ""
// when it may act. The hints and the handler both read it (T5).
func (m *model) modeWriteBlocked() string {
	st := &m.mode
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

// focusModeField gives the focus to the field being edited, with the
// cursor at the end of its text.
func (st *modeTab) focusModeField() {
	for f := range st.fields {
		st.fields[f].Blur()
	}
	st.fields[st.field].Focus()
	st.fields[st.field].CursorEnd()
}

// updateMode handles a message while the Mode tab is showing. It never calls
// a dep: observing, planning and applying are commands.
func (m *model) updateMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.mode.phase {
	case modeEdit:
		return m.updateModeEdit(msg)
	case modeConfirm:
		return m.updateModeConfirm(msg)
	case modeApplying:
		return m.updateModeApplying(msg)
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.mode
	if st.result != nil && m.updateModeResult(key.String()) {
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
		st.status, st.pending = m.cancelModePlan()+"checking the model server", true
		return m, m.observeModeCmd()
	case "r":
		// The same read; the one before it, if still out, is dropped. This is
		// how a config.toml repaired by hand is read again.
		st.status, st.pending = m.cancelModePlan()+"re-reading the model server's state", true
		return m, m.observeModeCmd()
	case "left", "right", " ", "down", "enter":
		// The form is inert while anything is out (busy): a read, a plan or
		// an apply. So a plan is always of the form a saw, and no question
		// can appear under a field being typed into (a y in a url must
		// never confirm). These keys only say why they wait.
		if st.busy {
			st.status = "wait: the form does not change while the Mode tab is busy"
			return m, nil
		}
		m.updateModeForm(key.String())
	case "a":
		return m, m.startModePlan()
	}
	// ctrl+s is the Agents tab's save and does nothing here, nor do the
	// admin keys of `wt-model setup` (k, t, s, x, R).
	return m, nil
}

// cancelModePlan drops a plan whose read c or r has just replaced. The
// snapshot, secrets included, goes now; the result is the words to put
// before the new status ("planning cancelled; "), empty if no plan was
// waiting.
func (m *model) cancelModePlan() string {
	st := &m.mode
	if st.planGen == 0 && !st.planOut {
		return ""
	}
	st.planGen, st.queue, st.planWant, st.planOut = 0, nil, modelmode.Desired{}, false
	return "planning cancelled; "
}

// updateModeForm moves the mode choice (left, right, space) or goes into
// the chosen mode's fields (down, enter), once an observation is in.
func (m *model) updateModeForm(key string) {
	st := &m.mode
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
			st.phase, st.field = modeEdit, fields[0]
			st.focusModeField()
		}
	}
}

// startModePlan is a: it reads the machine again with the fields' values
// (always: a remote check of another url), and that read goes on to the
// questions and the plan (applyModeObserved).
func (m *model) startModePlan() tea.Cmd {
	st := &m.mode
	if why := m.modeWriteBlocked(); why != "" {
		st.status = why
		return nil
	}
	// One snapshot of the form for the read and the plan alike.
	st.planWant, st.queue = m.modeDesired(), nil
	st.status, st.pending = "reading the machine again before planning…", true
	cmd := m.observeModeCmd()
	st.planGen = st.gen
	return cmd
}

// updateModeEdit handles the fields: up and down (and enter, tab) move
// between them, past either end back to the mode choice; esc leaves them;
// every other key is the field's. Nothing here acts on the machine.
func (m *model) updateModeEdit(msg tea.Msg) (tea.Model, tea.Cmd) {
	st := &m.mode
	fields := st.visibleFields()
	at := -1
	for i, f := range fields {
		if f == st.field {
			at = i
		}
	}
	leave := func() (tea.Model, tea.Cmd) {
		st.fields[st.field].Blur()
		st.phase = modeView
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
			st.focusModeField()
			return m, nil
		case "down", "enter", "tab":
			if at == len(fields)-1 {
				return leave()
			}
			st.field = fields[at+1]
			st.focusModeField()
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

// updateModeConfirm answers the question showing. Default No (P1): only y
// or Y says yes; n or N says no; esc cancels whatever is being planned;
// every other key is ignored and the question stays (enter too, except on
// the run-setup question, where it is a no). ctrl+c quits.
func (m *model) updateModeConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.mode
	if st.prompt == promptRunSetup {
		switch key.String() {
		case "ctrl+c":
			return m.quit()
		case "y", "Y":
			m.result = Result{RunModelSetup: true, ModelSetup: st.setupPath}
			st.phase = modeView
			return m.quit()
		case "n", "N", "esc", "enter":
			st.phase, st.setupPath, st.status = modeView, "", localPointerNote
		}
		return m, nil
	}
	var yes bool
	switch key.String() {
	case "ctrl+c":
		return m.quit()
	case "y", "Y":
		yes = true
	case "n", "N":
	case "esc":
		st.phase, st.queue, st.plan, st.planText, st.planWant = modeView, nil, modelmode.Plan{}, "", modelmode.Desired{}
		st.status = "not applied; nothing was changed"
		return m, nil
	default:
		return m, nil
	}
	if st.prompt == promptSaveAnyway {
		st.planWant.SaveUnchecked = yes
		return m, m.nextModeStep()
	}
	if yes {
		return m, m.modeApplyCmd()
	}
	st.phase, st.plan, st.planText = modeView, modelmode.Plan{}, ""
	st.status = "not applied; nothing was changed"
	return m, nil
}

// updateModeApplying takes keys while an apply is out: a quit (held until
// the result is in, leave) and tab; nothing else acts.
func (m *model) updateModeApplying(msg tea.Msg) (tea.Model, tea.Cmd) {
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

// updateModeResult takes keys while the result screen shows: up and down
// scroll, esc and enter leave it; q, ctrl+c and tab work as ever. Every
// other key is eaten. Reports whether the key was handled here.
func (m *model) updateModeResult(key string) bool {
	st := &m.mode
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	warn := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenWarning))
	handled, done := st.result.Key(key, m.width, m.height, lipgloss.Height(tabBar(m.theme, TabMode)), warn, dim)
	if done {
		st.result = nil
	}
	return handled
}
