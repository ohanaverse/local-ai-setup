package configeditor

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeview"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// serverHints are the Model server tab's key hints for the phase showing,
// fullest first. Only keys that act are named (T5), read from the same
// predicates the handlers use: the mode keys once an observation is in and
// nothing is out (the form is inert while busy), down where the chosen mode
// has fields, a while a write may run
// (serverWriteBlocked), s/x/R for local (serverControlBlocked). c and r both
// read the machine again, c to check after a change elsewhere, r to read a
// config.toml repaired by hand.
func (m *model) serverHints() []string {
	st := &m.server
	switch st.phase {
	case serverEdit:
		return []string{"up/down field · enter next · esc done · ctrl+c quit", "up/down · enter · esc · ctrl+c quit"}
	case serverApplying:
		return []string{"tab next · q quit (after this finishes)", "tab · q quit"}
	case serverMenu:
		return m.serverMenuHints()
	case serverConfirm:
		switch st.prompt {
		case promptRemoveAgent:
			return []string{"y remove · n keep · esc cancel · ctrl+c quit", "y · n · esc · ctrl+c quit"}
		case promptSaveAnyway:
			return []string{"y save anyway · n do not · esc cancel · ctrl+c quit", "y · n · esc · ctrl+c quit"}
		}
		return []string{"y apply · n/esc cancel · ctrl+c quit", "y · n/esc · ctrl+c quit"}
	}
	type hint struct{ key, label string }
	var mode, keys []hint
	if st.loaded && !st.busy {
		mode = append(mode, hint{"left/right", "mode"})
		if len(st.visibleFields()) > 0 {
			mode = append(mode, hint{"down", "fields"})
		}
	}
	if m.serverWriteBlocked() == "" {
		keys = append(keys, hint{"a", "apply"})
	}
	if m.serverLiteLLMBlocked() == "" {
		keys = append(keys, hint{"k", "key"})
	}
	if m.serverTightenBlocked() == "" {
		keys = append(keys, hint{"t", "tighten"})
	}
	keys = append(keys, hint{"c", "check"}, hint{"r", "re-read"})
	if m.serverControlBlocked() == "" {
		keys = append(keys, hint{"s/x/R", "start/stop/restart"})
	}
	keys = append(keys, hint{"tab", "next"})
	join := func(hs []hint, labels bool) []string {
		var out []string
		for _, h := range hs {
			if labels {
				out = append(out, h.key+" "+h.label)
			} else {
				out = append(out, h.key)
			}
		}
		return out
	}
	full := append(join(mode, true), join(keys, true)...)
	short := append(join(mode, false), join(keys, false)...)
	least := join(keys, false)
	return []string{
		strings.Join(append(full, "q quit"), " · "),
		strings.Join(append(short, "q quit"), " · "),
		strings.Join(append(least, "q quit"), " · "),
	}
}

func pairLine(p modelsetup.PairState) editorkit.Line {
	if p.Err != "" {
		return editorkit.Line{Label: "pair", Text: "unreadable: " + editorkit.Scrub(p.Err), Tone: editorkit.ToneBad}
	}
	url := p.URL
	switch {
	case url == "":
		url = "url unset"
	case modelclient.HasUserinfo(url):
		// Observe hands over the placeholder already; a url with an '@'
		// from anywhere else is hidden whole too, since scrub only finds
		// one after "://" (#640).
		url = modelclient.HiddenURL
	}
	key := "key unset"
	if p.KeySet {
		key = "key set"
	}
	l := editorkit.Line{Label: "pair", Text: "LiteLLM " + editorkit.Scrub(url) + ", " + key, Tone: editorkit.ToneNone}
	if p.URL == "" || !p.KeySet {
		l.Tone = editorkit.ToneWarn
	}
	return l
}

func agentLines(st modelsetup.State) []editorkit.Line {
	a := st.Agent
	var facts []string
	if !a.Present {
		facts = append(facts, "no plist")
	} else {
		facts = append(facts, "present")
		if a.Managed {
			facts = append(facts, "managed by wt")
		} else {
			facts = append(facts, "not managed by wt")
		}
		if a.Job.Loaded {
			facts = append(facts, "loaded")
		} else {
			facts = append(facts, "not loaded")
		}
		if a.Job.Running {
			facts = append(facts, fmt.Sprintf("running (pid %d)", a.Job.PID))
		} else {
			facts = append(facts, "not running")
		}
	}
	lines := []editorkit.Line{{Label: "agent", Text: modelsetup.AgentLabel + ": " + strings.Join(facts, ", "), Tone: editorkit.ToneNone}}
	if a.Err != "" {
		lines = append(lines, editorkit.Line{Label: "", Text: "problem: " + editorkit.Scrub(a.Err), Tone: editorkit.ToneBad})
	}
	if l := st.Listener; l.PID != 0 && !l.Ours {
		lines = append(lines, editorkit.Line{Label: "", Text: fmt.Sprintf("port %d is held by pid %d, not the agent", l.Port, l.PID), Tone: editorkit.ToneWarn})
	}
	return lines
}

// checkWord is a LiteLLM check's status as a word, and its tone.
func checkWord(c modelsetup.Check) (string, editorkit.Tone) {
	switch c.Status {
	case modelsetup.CheckOK:
		return "ok", editorkit.ToneNone
	case modelsetup.CheckWarn:
		return "warning", editorkit.ToneWarn
	case modelsetup.CheckFail:
		return "problem", editorkit.ToneBad
	}
	return "skipped", editorkit.ToneDim
}

func litellmLines(st modelsetup.State) []editorkit.Line {
	l := st.LiteLLM
	if !l.Checked {
		if st.Resolution.Mode != modelclient.ModeLocal {
			return []editorkit.Line{{Label: "litellm", Text: "checked in local mode only", Tone: editorkit.ToneDim}}
		}
		return []editorkit.Line{{Label: "litellm", Text: "not checked", Tone: editorkit.ToneDim}}
	}
	checks := []struct {
		name string
		c    modelsetup.Check
	}{
		{"reachable", l.Reachable}, {"database", l.Database}, {"key", l.KeyAccepted},
		{"routes", l.RoutesInSync}, {"restart", l.RestartOwed}, {"port", l.PortMatches}, {"plist mode", l.PlistMode},
	}
	var lines []editorkit.Line
	for i, ch := range checks {
		word, tn := checkWord(ch.c)
		text := ch.name + " " + word
		if ch.c.Detail != "" {
			text += ": " + editorkit.Scrub(ch.c.Detail)
		}
		label := ""
		if i == 0 {
			label = "litellm"
		}
		lines = append(lines, editorkit.Line{Label: label, Text: text, Tone: tn})
	}
	// What the checks cannot see, said rather than guessed (the spec's
	// "LiteLLM: scope and guide").
	return append(lines, editorkit.Line{Label: "", Text: "the salt key and provider keys are not checked", Tone: editorkit.ToneDim})
}

// serverStatusLines is the status block: the effective mode and where it
// came from, the url, the token file, the pair, the agent, the server check,
// the LiteLLM checks, the env-override line and the problems.
func serverStatusLines(st modelsetup.State, home string) []editorkit.Line {
	lines := []editorkit.Line{modeview.ModeLine(st.State), modeview.URLLine(st.State)}
	pair := pairLine(st.Pair)
	if st.Config.ParseErr != "" {
		pair = editorkit.Line{Label: "pair", Text: "not read", Tone: editorkit.ToneDim}
	}
	lines = append(lines, modeview.TokenLine(st.State, home), pair)
	lines = append(lines, agentLines(st)...)
	lines = append(lines, modeview.ServerCheckLine(st.State))
	lines = append(lines, litellmLines(st)...)
	lines = append(lines, plistModeLines(st, home)...)
	if env, ok := modeview.EnvLine(st.State); ok {
		lines = append(lines, env)
	}
	return append(lines, modeview.ProblemLines(st.State)...)
}

// modeChoice is the mode choice: (x) on the chosen one, (-) on local where
// it cannot be set up.
func (m *model) modeChoice() (radios []string, notes [][]string) {
	st := &m.server
	var opts []string
	for _, mode := range serverModes {
		mark := "( )"
		switch {
		case !st.modeAllowed(mode):
			mark = "(-)"
		case mode == st.want.Mode:
			mark = "(x)"
		}
		opts = append(opts, mark+" "+string(mode))
	}
	lines := editorkit.ChoiceLines("choose", opts, m.width)
	if !st.state.Platform.OK {
		why := "local needs launchd (macOS)"
		if st.state.Platform.Reason != "" {
			why += ": " + st.state.Platform.Reason
		}
		notes = append(notes, editorkit.RenderLine(editorkit.Line{Text: why}, m.width))
		if st.want.Mode == modelclient.ModeLocal {
			// No option is marked: say why rather than leave the choice blank.
			notes = append(notes, editorkit.RenderLine(editorkit.Line{Text: "local is set but cannot run here; choose native or remote"}, m.width))
		}
	}
	return lines, notes
}

// serverFieldLines are the chosen mode's fields, one line each: the label
// (marked "> " while being edited) and the input. The secrets are drawn by
// their inputs masked; the url is scrubbed like every other url.
func (m *model) serverFieldLines() []string {
	st := &m.server
	var lines []string
	for _, f := range st.visibleFields() {
		label := serverFieldLabels[f]
		if st.phase == serverEdit && st.field == f {
			label = "> " + label
		}
		view := st.fields[f].View()
		if f == sfURL {
			view = editorkit.Scrub(view)
		}
		lines = append(lines, tuilayout.PadRunes(label, editorkit.LabelW)+view)
	}
	return lines
}

// statusDropOrder is the order a terminal too short gives up status rows:
// first entry first. The mode and the server are the last to go.
var statusDropOrder = []string{"litellm", "agent", "env", "pair", "token", "url", "problems", "server", "mode"}

// serverBody is the tab's body under the tab bar, in at most room lines: a
// status line, the parse error, the status block and the mode choice, styled.
// The mode choice (with the fields) and the status text are kept whole; when
// the rest does not fit, status rows go whole, lowest priority first
// (statusDropOrder), and one marker at the end of the block counts them.
// If that is still too tall the blank lines go, then the middle of the
// banner and status text.
func (m *model) serverBody(room int) string {
	styles := editorkit.Styles(m.theme)
	var head []string
	if s := m.server.status; s != "" {
		head = append(head, strings.Split(editorkit.WrapText(s, m.width), "\n")...)
	}
	if m.quitPending && m.server.writing {
		head = append(head, strings.Split(editorkit.WrapText("wt config quits once this has finished", m.width), "\n")...)
	}
	if !m.server.loaded {
		return strings.Join(append(head, "", editorkit.WrapText("reading the model server's state", m.width)), "\n")
	}
	st := m.server.state
	if st.Config.ParseErr != "" {
		// The path breaks only after a slash, so wrapping never splits a name.
		lines := []string{"config.toml does not parse:"}
		lines = append(lines, editorkit.BreakPath(editorkit.HomePath(st.Config.Path, st.Host.Home, m.home), m.width)...)
		lines = append(lines, strings.Split(editorkit.WrapText(editorkit.Scrub(st.Config.ParseErr), m.width), "\n")...)
		lines = append(lines, "fix the file, then press r")
		for _, l := range lines {
			head = append(head, styles[editorkit.ToneBad].Render(l))
		}
	}
	groups := editorkit.Groups(serverStatusLines(st, m.home), m.width, styles)
	radios, notes := m.modeChoice()
	var fieldLines []string
	if fields := m.serverFieldLines(); len(fields) > 0 {
		fieldLines = append([]string{""}, fields...)
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))

	b := editorkit.Body{Head: head, Groups: groups, Choice: radios, Notes: notes, Fields: fieldLines}
	return strings.Join(editorkit.FitBody(b, room, statusDropOrder, dim), "\n")
}

// serverTabView renders the Model server tab: the tab bar, a blank line, the
// body and the hint line on the terminal's last line (L3). A body too tall
// for the terminal gives up status rows, whole and lowest priority first,
// behind a marker at the end of the block that counts them (see serverBody);
// the mode choice and the hints always stay. Before the first WindowSizeMsg it
// is the tab bar alone.
func (m *model) serverTabView() string {
	bar := tabBar(m.theme, TabServer)
	if m.width <= 0 || m.height <= 0 {
		return bar
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	switch {
	case m.server.phase == serverConfirm:
		return m.serverPromptView(bar, dim)
	case m.server.phase == serverMenu:
		return m.serverMenuView(bar, dim)
	case m.server.result != nil && m.server.phase == serverView:
		return m.serverResultView(bar, dim)
	}
	// The bar, the blank line under it and the hint line are not the body's.
	body := "\n" + m.serverBody(m.height-lipgloss.Height(bar)-2)
	// Blank filler pins the hints to the last line when the body fits.
	if fill := m.height - lipgloss.Height(bar) - lipgloss.Height(body) - 1; fill > 0 {
		body += strings.Repeat("\n", fill)
	}
	return tuilayout.MessageAlone(bar, body, editorkit.FitHints(m.width, m.serverHints()), m.width, m.height, dim)
}

// serverPromptView is the confirm phase: what the question is about, then
// the question on the line above the hints. A text too tall for the terminal
// loses lines from its middle (L4); the question is last, so it stays.
func (m *model) serverPromptView(bar string, dim lipgloss.Style) string {
	st := &m.server
	var text, question string
	switch st.prompt {
	case promptRemoveAgent:
		a := st.state.Agent
		where := a.PlistPath
		if a.Job.Running {
			where += " (running)"
		}
		text = "Leaving local: wt's LaunchAgent " + where + " is installed. n keeps it installed; the token file and the LiteLLM pair are kept either way."
		question = "Remove the LaunchAgent? [y/N]"
	case promptSaveAnyway:
		s := st.state.Server
		text = "The check of " + editorkit.Scrub(s.URL) + " failed: " + modeview.KindWord(s.Kind) + ": " + editorkit.Scrub(s.Message) +
			". The server may be off; y stores the setup unchecked."
		question = "Save anyway? [y/N]"
	default:
		return editorkit.PlanPromptView(bar, st.planText, "Apply this plan? [y/N]", m.serverHints(), m.width, m.height, dim)
	}
	return editorkit.PromptView(bar, text, question, m.serverHints(), m.width, m.height, dim)
}
