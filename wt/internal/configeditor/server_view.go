package configeditor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
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

// serverLabelW is the width of a status line's label column ("problems"
// and two spaces).
const serverLabelW = 10

// tone is how a status line is coloured; the words on it always say the
// same (V2), so a terminal without colour loses nothing.
type tone int

const (
	toneNone tone = iota
	toneDim
	toneWarn
	toneBad
)

// serverLine is one entry of the status block: a label (empty for a
// continuation entry under the previous label), its text and its tone.
type serverLine struct {
	label, text string
	tone        tone
}

// userinfoRE finds a user:password in a url inside free text, through the
// last @ before the host: a password may hold an @ itself.
var userinfoRE = regexp.MustCompile(`://[^/\s]*@`)

// scrub takes any url userinfo out of text the tab draws: Observe already
// strips it from what it stores, but a message a dep composed may still
// carry one, and nothing the tab shows may hold a credential.
//
// Deliberately greedy: an @ anywhere before the first / or space ends the
// userinfo, so "https://h:1?x=a@b" loses its host too. That errs on the safe
// side (a credential is never shown); TestScrubTakesTheHostWithAnAtInTheQuery
// pins it.
func scrub(s string) string { return userinfoRE.ReplaceAllString(s, "://") }

// homePath writes a path under home with ~ for the home.
//
// The token file is shown in this one form whatever the config wrote: Observe
// hands back the expanded path (State.Token.Path), which is the file a
// session reads, while [model-server] token_file may say ~/... or the same
// path in full. Abbreviating the expanded path against the home Observe used
// gives the ~ form a user writes, is the same file the line's ok / loose /
// missing is about, and stays one form when the value came from
// WT_MODEL_TOKEN_FILE or the agent's plist instead of the table.
func homePath(path, home string) string {
	if home == "" || home == "/" {
		return tildePath(path)
	}
	if rest, ok := strings.CutPrefix(path, home); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		return "~" + rest
	}
	return path
}

// modeSource says where the effective mode came from.
func modeSource(r modelclient.Resolution) string {
	switch r.Source {
	case modelclient.SourceEnvMode:
		return "from WT_MODEL_MODE"
	case modelclient.SourceEnvEndpoint:
		return "from WT_MODEL_URL with a token"
	case modelclient.SourceStored:
		return "from config.toml"
	case modelclient.SourceInferred:
		return "inferred: a url or token is set"
	}
	return ""
}

// envNames splits State.EnvOverride into the variable names.
func envNames(st modelsetup.State) []string {
	if st.EnvOverride == "" {
		return nil
	}
	return strings.Split(st.EnvOverride, ", ")
}

func hasEnv(st modelsetup.State, name string) bool {
	for _, n := range envNames(st) {
		if n == name {
			return true
		}
	}
	return false
}

// tokenLine is the token file and its state, as a word.
func tokenLine(st modelsetup.State) serverLine {
	t := st.Token
	if t.Path == "" {
		if hasEnv(st, "WT_MODEL_TOKEN") {
			return serverLine{"token", "from WT_MODEL_TOKEN (not shown)", toneNone}
		}
		return serverLine{"token", "none set", toneDim}
	}
	path := homePath(t.Path, st.Host.Home)
	switch {
	case t.Loose:
		return serverLine{"token", path + "  loose: chmod 600 " + path, toneBad}
	case t.Err != "":
		return serverLine{"token", path + "  problem: " + t.Err, toneBad}
	case !t.Exists:
		return serverLine{"token", path + "  missing", toneBad}
	}
	return serverLine{"token", path + "  ok", toneNone}
}

func pairLine(p modelsetup.PairState) serverLine {
	if p.Err != "" {
		return serverLine{"pair", "unreadable: " + scrub(p.Err), toneBad}
	}
	url := p.URL
	if url == "" {
		url = "url unset"
	}
	key := "key unset"
	if p.KeySet {
		key = "key set"
	}
	l := serverLine{"pair", "LiteLLM " + scrub(url) + ", " + key, toneNone}
	if p.URL == "" || !p.KeySet {
		l.tone = toneWarn
	}
	return l
}

func agentLines(st modelsetup.State) []serverLine {
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
	lines := []serverLine{{"agent", modelsetup.AgentLabel + ": " + strings.Join(facts, ", "), toneNone}}
	if a.Err != "" {
		lines = append(lines, serverLine{"", "problem: " + scrub(a.Err), toneBad})
	}
	if l := st.Listener; l.PID != 0 && !l.Ours {
		lines = append(lines, serverLine{"", fmt.Sprintf("port %d is held by pid %d, not the agent", l.Port, l.PID), toneWarn})
	}
	return lines
}

// serverKindWord names how a ping failed.
func serverKindWord(k modelsetup.ServerKind) string {
	switch k {
	case modelsetup.KindUnauthorized:
		return "token refused"
	case modelsetup.KindNotWtModel:
		return "not a wt-model server"
	case modelsetup.KindSkew:
		return "api version differs"
	case modelsetup.KindSchema:
		return "schema differs"
	}
	return "unreachable"
}

func serverCheckLine(st modelsetup.State) serverLine {
	s := st.Server
	switch {
	case st.Resolution.Mode == modelclient.ModeNative:
		return serverLine{"server", "none in native mode", toneDim}
	case !s.Checked && s.Message != "":
		return serverLine{"server", "not checked: " + scrub(s.Message), toneDim}
	case !s.Checked:
		return serverLine{"server", "not checked: " + missingForCheck(st), toneDim}
	case s.OK:
		return serverLine{"server", fmt.Sprintf("ok: wt-model answers at %s (api %d, schema %d)", scrub(s.URL), s.API, s.Schema), toneNone}
	}
	return serverLine{"server", "problem: " + serverKindWord(s.Kind) + ": " + scrub(s.Message), toneBad}
}

// missingForCheck says what a check of the server lacked: the url, the token
// or both. A local server's url is the agent's listen address, never missing.
func missingForCheck(st modelsetup.State) string {
	noURL := st.Resolution.Mode != modelclient.ModeLocal && st.Config.Table.URL == "" && st.Server.URL == "" && !hasEnv(st, "WT_MODEL_URL")
	t := st.Token
	noToken := !(t.Path != "" && t.Exists && t.Err == "") && !hasEnv(st, "WT_MODEL_TOKEN")
	switch {
	case noURL && noToken:
		return "no url and no token"
	case noURL:
		return "no url"
	case noToken:
		return "no token"
	}
	return "no url or no token"
}

// checkWord is a LiteLLM check's status as a word, and its tone.
func checkWord(c modelsetup.Check) (string, tone) {
	switch c.Status {
	case modelsetup.CheckOK:
		return "ok", toneNone
	case modelsetup.CheckWarn:
		return "warning", toneWarn
	case modelsetup.CheckFail:
		return "problem", toneBad
	}
	return "skipped", toneDim
}

func litellmLines(st modelsetup.State) []serverLine {
	l := st.LiteLLM
	if !l.Checked {
		if st.Resolution.Mode != modelclient.ModeLocal {
			return []serverLine{{"litellm", "checked in local mode only", toneDim}}
		}
		return []serverLine{{"litellm", "not checked", toneDim}}
	}
	checks := []struct {
		name string
		c    modelsetup.Check
	}{
		{"reachable", l.Reachable}, {"database", l.Database}, {"key", l.KeyAccepted},
		{"routes", l.RoutesInSync}, {"restart", l.RestartOwed}, {"port", l.PortMatches}, {"plist mode", l.PlistMode},
	}
	var lines []serverLine
	for i, ch := range checks {
		word, tn := checkWord(ch.c)
		text := ch.name + " " + word
		if ch.c.Detail != "" {
			text += ": " + scrub(ch.c.Detail)
		}
		label := ""
		if i == 0 {
			label = "litellm"
		}
		lines = append(lines, serverLine{label, text, tn})
	}
	// What the checks cannot see, said rather than guessed (the spec's
	// "LiteLLM: scope and guide").
	return append(lines, serverLine{"", "the salt key and provider keys are not checked", toneDim})
}

// serverStatusLines is the status block: the effective mode and where it
// came from, the url, the token file, the pair, the agent, the server check,
// the LiteLLM checks, the env-override line and the problems.
func serverStatusLines(st modelsetup.State) []serverLine {
	var lines []serverLine
	r := st.Resolution
	switch {
	case r.Mode != "":
		lines = append(lines, serverLine{"mode", string(r.Mode) + " (" + modeSource(r) + ")", toneNone})
	case st.Config.ParseErr != "":
		// The banner above says why, once.
		lines = append(lines, serverLine{"mode", "unknown: config.toml is not readable", toneWarn})
	case r.Problem != "":
		lines = append(lines, serverLine{"mode", "problem: " + scrub(r.Problem), toneBad})
	default:
		lines = append(lines, serverLine{"mode", "unset: choose one below", toneWarn})
	}
	url := st.Config.Table.URL
	if url == "" {
		url = "none set"
	}
	lines = append(lines, serverLine{"url", scrub(url), toneNone})
	pair := pairLine(st.Pair)
	if st.Config.ParseErr != "" {
		pair = serverLine{"pair", "not read", toneDim}
	}
	lines = append(lines, tokenLine(st), pair)
	lines = append(lines, agentLines(st)...)
	lines = append(lines, serverCheckLine(st))
	lines = append(lines, litellmLines(st)...)
	if names := envNames(st); len(names) > 0 {
		lines = append(lines, serverLine{"env", strings.Join(names, ", ") + " set: they override config.toml", toneWarn})
	}
	var problems []string
	if st.Config.TableErr != "" {
		problems = append(problems, "[model-server]: "+st.Config.TableErr)
	}
	problems = append(problems, st.Config.Problems...)
	for i, p := range problems {
		label := ""
		if i == 0 {
			label = "problems"
		}
		lines = append(lines, serverLine{label, scrub(p), toneBad})
	}
	return lines
}

// renderServerLine lays one entry out at width: the label padded to its
// column and the text flowed between words beside it, continuation lines
// indented under the text. A terminal too narrow for the column flows the
// whole entry instead. No line is wider than width.
func renderServerLine(l serverLine, width int) []string {
	pad := tuilayout.PadRunes(l.label, serverLabelW)
	if width <= 0 {
		return []string{pad + l.text}
	}
	if width-serverLabelW < 16 {
		return flow(strings.Fields(l.label+" "+l.text), " ", width)
	}
	text := flow(strings.Fields(l.text), " ", width-serverLabelW)
	out := make([]string, len(text))
	for i, t := range text {
		if i == 0 {
			out[i] = pad + t
		} else {
			out[i] = strings.Repeat(" ", serverLabelW) + t
		}
	}
	return out
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
	var lines []string
	switch {
	case m.width <= 0:
		lines = []string{tuilayout.PadRunes("choose", serverLabelW) + strings.Join(opts, "  ")}
	case m.width-serverLabelW >= 16:
		for i, l := range flow(opts, "  ", m.width-serverLabelW) {
			lead := strings.Repeat(" ", serverLabelW)
			if i == 0 {
				lead = tuilayout.PadRunes("choose", serverLabelW)
			}
			lines = append(lines, lead+l)
		}
	default:
		lines = flow(opts, "  ", m.width)
	}
	if !st.state.Platform.OK {
		why := "local needs launchd (macOS)"
		if st.state.Platform.Reason != "" {
			why += ": " + st.state.Platform.Reason
		}
		notes = append(notes, renderServerLine(serverLine{text: why}, m.width))
		if st.want.Mode == modelclient.ModeLocal {
			// No option is marked: say why rather than leave the choice blank.
			notes = append(notes, renderServerLine(serverLine{text: "local is set but cannot run here; choose native or remote"}, m.width))
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
			view = scrub(view)
		}
		lines = append(lines, tuilayout.PadRunes(label, serverLabelW)+view)
	}
	return lines
}

// breakPath splits a path into lines no wider than width, breaking only
// after a "/"; a segment wider than the width on its own is cut at the width.
func breakPath(path string, width int) []string {
	if width <= 0 || len(path) <= width {
		return []string{path}
	}
	var segs []string
	for rest := path; rest != ""; {
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			segs, rest = append(segs, rest), ""
			continue
		}
		segs, rest = append(segs, rest[:i+1]), rest[i+1:]
	}
	var out []string
	cur := ""
	for _, seg := range segs {
		for len(seg) > width {
			if cur != "" {
				out, cur = append(out, cur), ""
			}
			out, seg = append(out, seg[:width]), seg[width:]
		}
		if len(cur)+len(seg) > width {
			out, cur = append(out, cur), ""
		}
		cur += seg
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// statusDropOrder is the order a terminal too short gives up status rows:
// first entry first. The mode and the server are the last to go.
var statusDropOrder = []string{"litellm", "agent", "env", "pair", "token", "url", "problems", "server", "mode"}

// statusGroup is one status row with its continuation lines, rendered: the
// unit that is kept or dropped whole.
type statusGroup struct {
	label string
	lines []string
}

// serverStatusGroups lays the status block out as one group per labelled
// row (an entry without a label belongs to the row before it).
func serverStatusGroups(st modelsetup.State, width int, styles map[tone]lipgloss.Style) []statusGroup {
	var groups []statusGroup
	for _, l := range serverStatusLines(st) {
		if l.label != "" || len(groups) == 0 {
			groups = append(groups, statusGroup{label: l.label})
		}
		g := &groups[len(groups)-1]
		for _, line := range renderServerLine(l, width) {
			g.lines = append(g.lines, styles[l.tone].Render(line))
		}
	}
	return groups
}

// serverBody is the tab's body under the tab bar, in at most room lines: a
// status line, the parse error, the status block and the mode choice, styled.
// The mode choice (with the fields) and the status text are kept whole; when
// the rest does not fit, status rows go whole, lowest priority first
// (statusDropOrder), and one marker at the end of the block counts them.
// If that is still too tall the blank lines go, then the middle of the
// banner and status text.
func (m *model) serverBody(room int) string {
	styles := map[tone]lipgloss.Style{
		toneNone: lipgloss.NewStyle(),
		toneDim:  lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim)),
		toneWarn: lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenWarning)),
		toneBad:  themes.ErrorStyle(m.theme),
	}
	var head []string
	if s := m.server.status; s != "" {
		head = append(head, strings.Split(wrapText(s, m.width), "\n")...)
	}
	if m.quitPending && m.server.writing {
		head = append(head, strings.Split(wrapText("wt config quits once this has finished", m.width), "\n")...)
	}
	if !m.server.loaded {
		return strings.Join(append(head, "", wrapText("reading the model server's state", m.width)), "\n")
	}
	st := m.server.state
	if st.Config.ParseErr != "" {
		// The path breaks only after a slash, so wrapping never splits a name.
		lines := []string{"config.toml does not parse:"}
		lines = append(lines, breakPath(homePath(st.Config.Path, st.Host.Home), m.width)...)
		lines = append(lines, strings.Split(wrapText(scrub(st.Config.ParseErr), m.width), "\n")...)
		lines = append(lines, "fix the file, then press r")
		for _, l := range lines {
			head = append(head, styles[toneBad].Render(l))
		}
	}
	groups := serverStatusGroups(st, m.width, styles)
	radios, notes := m.modeChoice()
	var fieldLines []string
	if fields := m.serverFieldLines(); len(fields) > 0 {
		fieldLines = append([]string{""}, fields...)
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))

	dropped := map[string]bool{}
	keepNotes := len(notes)
	build := func(seps bool) []string {
		var out []string
		sep := func() {
			if seps {
				out = append(out, "")
			}
		}
		out = append(out, head...)
		if len(head) > 0 {
			sep()
		}
		kept := 0
		for _, g := range groups {
			if !dropped[g.label] {
				out = append(out, g.lines...)
				kept++
			}
		}
		if n := len(groups) - kept; n > 0 {
			word := "rows"
			if n == 1 {
				word = "row"
			}
			out = append(out, dim.Render(fmt.Sprintf("... %d status %s not shown ...", n, word)))
		}
		if len(groups) > 0 {
			sep()
		}
		out = append(out, radios...)
		for _, n := range notes[:keepNotes] {
			out = append(out, n...)
		}
		return append(out, fieldLines...)
	}
	out := build(true)
	for _, label := range statusDropOrder {
		if len(out) <= room {
			break
		}
		for _, g := range groups {
			if g.label == label {
				dropped[label] = true
			}
		}
		out = build(true)
	}
	// The notes under the mode choice explain it; they go, whole and last
	// first, only once every status row has.
	for len(out) > room && keepNotes > 0 {
		keepNotes--
		out = build(true)
	}
	if len(out) > room {
		out = build(false)
	}
	if over := len(out) - room; over > 0 && len(head) > 2 {
		// Last resort: the middle of the banner or status text, keeping its
		// first line and the line that says what to do. The mode choice is
		// never touched.
		cut := min(over+1, len(head)-2)
		head = append(append(append([]string{}, head[:1]...), dim.Render(fmt.Sprintf("... %d lines not shown ...", cut))), head[len(head)-(len(head)-1-cut):]...)
		out = build(false)
	}
	return strings.Join(out, "\n")
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
	if m.server.phase == serverConfirm {
		return m.serverPromptView(bar, dim)
	}
	// The bar, the blank line under it and the hint line are not the body's.
	body := "\n" + m.serverBody(m.height-lipgloss.Height(bar)-2)
	// Blank filler pins the hints to the last line when the body fits.
	if fill := m.height - lipgloss.Height(bar) - lipgloss.Height(body) - 1; fill > 0 {
		body += strings.Repeat("\n", fill)
	}
	return tuilayout.MessageAlone(bar, body, fitHints(m.width, m.serverHints()), m.width, m.height, dim)
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
		text = "The check of " + scrub(s.URL) + " failed: " + serverKindWord(s.Kind) + ": " + scrub(s.Message) +
			". The server may be off; y stores the setup unchecked."
		question = "Save anyway? [y/N]"
	default:
		question = "Apply this plan? [y/N]"
		body := "\n" + wrapIndented(strings.TrimRight(st.planText, "\n"), m.width) + "\n\n" + wrapText(question, m.width)
		return tuilayout.MessageAlone(bar, body, fitHints(m.width, m.serverHints()), m.width, m.height, dim)
	}
	body := "\n" + wrapText(text, m.width) + "\n\n" + wrapText(question, m.width)
	return tuilayout.MessageAlone(bar, body, fitHints(m.width, m.serverHints()), m.width, m.height, dim)
}

// wrapIndented wraps each line of a plan's text by itself, keeping its
// indent and hanging its continuation lines under the text after the step
// number or the bullet ("  1. ", "  - "), so the steps stay apart. A
// terminal too narrow for the hang wraps the line plainly.
func wrapIndented(text string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		body := strings.TrimLeft(line, " ")
		lead := strings.Repeat(" ", len(line)-len(body))
		hang := lead
		if mark, _, ok := strings.Cut(body, " "); ok && lead != "" && (mark == "-" || strings.HasSuffix(mark, ".")) {
			hang += strings.Repeat(" ", len(mark)+1)
		}
		if width <= 0 || width-len(hang) < 16 {
			out = append(out, wrapText(line, width))
			continue
		}
		for i, l := range strings.Split(wrapText(body, width-len(hang)), "\n") {
			if i == 0 {
				out = append(out, lead+l)
			} else {
				out = append(out, hang+l)
			}
		}
	}
	return strings.Join(out, "\n")
}
