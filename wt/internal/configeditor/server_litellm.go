package configeditor

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// guide14Path is the LiteLLM guide the menu's reasons point at.
const guide14Path = "docs/guides/14-litellm-install-and-configuration.md"

// menuOption is one choice of the LiteLLM key menu.
type menuOption struct {
	action modelsetup.LiteLLMAction
	label  string
}

// plistLoose is true for a LiteLLM plist that exists and is readable by
// group or others.
func plistLoose(p modelsetup.LiteLLMPlistState) bool {
	return p.Present && p.Mode.Perm()&0o077 != 0
}

// serverLiteLLMMenu is what the k menu offers for a state, and the reasons
// for what it does not (each a line of its own). probeSkipped says the
// observation skipped the LiteLLM probe, so its binary and port are unknown,
// not absent. It reads State only: the
// rules mirror modelsetup.BuildPlan's refusals so the menu never offers an
// action the plan would refuse for a reason the observation already shows;
// a refusal for anything else still arrives as the plan's own text.
func serverLiteLLMMenu(s modelsetup.State, probeSkipped bool) (opts []menuOption, reasons []string) {
	p, inst := s.LiteLLMPlist, s.LiteLLMInstall
	where := p.Path
	if where == "" {
		where = "the LiteLLM plist"
	}
	if p.Present && p.HasMasterKey {
		opts = append(opts, menuOption{modelsetup.LiteLLMUseExistingKey, "use the plist's key"})
	}
	switch {
	case !p.Present:
	case p.Binary:
		reasons = append(reasons, "a new key is not offered: "+where+" is a binary property list; run `plutil -convert xml1 "+where+"`, then press r ("+guide14Path+")")
	case p.Err != "":
		reasons = append(reasons, "a new key is not offered: "+where+" cannot be read as an XML plist ("+guide14Path+")")
	case !p.HasMasterKey:
		reasons = append(reasons, "a new key is not offered: "+where+" has no LITELLM_MASTER_KEY to replace ("+guide14Path+")")
	case p.HasDatabaseURL && !p.HasSaltKey:
		reasons = append(reasons, "a new key is not offered: "+where+" sets DATABASE_URL but no LITELLM_SALT_KEY, so a new master key could make LiteLLM's stored credentials unreadable; first set LITELLM_SALT_KEY to the current master key ("+guide14Path+")")
	default:
		opts = append(opts, menuOption{modelsetup.LiteLLMRotateKey, "generate a new key"})
	}
	if p.Present {
		return opts, reasons
	}
	var why []string
	switch {
	case probeSkipped:
		// The read shown never looked for the binary or at the port.
		why = append(why, "the LiteLLM probe was not run; press r, then k again")
	case inst.Binary == "":
		why = append(why, "no litellm binary was found, and wt never installs LiteLLM")
	}
	if !inst.ConfigExists {
		why = append(why, "LiteLLM's config.yaml "+orNoneWord(inst.ConfigPath)+" does not exist, and wt never writes a first one")
	}
	port := inst.Port
	if port == 0 {
		port = modelsetup.DefaultLiteLLMPort
	}
	switch {
	case probeSkipped:
	case !inst.ListenerChecked:
		why = append(why, "port "+strconv.Itoa(port)+" was not checked; press r, then k again")
	case inst.ListenerPID != 0:
		why = append(why, fmt.Sprintf("port %d is in use by pid %d, so a new agent could not listen there", port, inst.ListenerPID))
	}
	if len(why) == 0 {
		return append(opts, menuOption{modelsetup.LiteLLMCreate, "create the LiteLLM agent"}), reasons
	}
	return opts, append(reasons, "create is not offered: "+strings.Join(why, "; ")+" ("+guide14Path+")")
}

func orNoneWord(s string) string {
	if s == "" {
		return "(no path)"
	}
	return s
}

// serverLiteLLMBlocked is why k and t do nothing now, or "": a write is
// blocked, or the choice is not local.
func (m *model) serverLiteLLMBlocked() string {
	if why := m.serverWriteBlocked(); why != "" {
		return why
	}
	st := &m.server
	switch {
	case st.want.Mode != modelclient.ModeLocal:
		return "k and t change the LiteLLM setup of local mode: choose local first; nothing was changed"
	case !st.state.Platform.OK:
		return "the local model server cannot run here, so k and t do nothing; nothing was changed"
	}
	return ""
}

// serverTightenBlocked is why t does nothing now, or "".
func (m *model) serverTightenBlocked() string {
	if why := m.serverLiteLLMBlocked(); why != "" {
		return why
	}
	if !plistLoose(m.server.state.LiteLLMPlist) {
		return "the LiteLLM plist is not looser than 0600, or there is none; nothing was changed"
	}
	return ""
}

// openServerMenu is k.
func (m *model) openServerMenu() {
	st := &m.server
	if why := m.serverLiteLLMBlocked(); why != "" {
		st.status = why
		return
	}
	st.phase, st.menuAt = serverMenu, 0
}

// updateServerMenu takes keys while the menu shows: up and down move, enter
// chooses (through the same read, plan and confirm as a), esc closes it.
func (m *model) updateServerMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	st := &m.server
	opts, _ := serverLiteLLMMenu(st.state, st.probeSkipped)
	switch key.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		st.phase, st.status = serverView, "no LiteLLM change chosen; nothing was changed"
	case "up":
		if st.menuAt > 0 {
			st.menuAt--
		}
	case "down":
		if st.menuAt < len(opts)-1 {
			st.menuAt++
		}
	case "enter":
		if st.menuAt >= len(opts) {
			return m, nil
		}
		st.phase = serverView
		return m, m.startServerPlanWith(opts[st.menuAt].action)
	}
	return m, nil
}

// serverMenuHints are the menu's hints: only keys that act.
func (m *model) serverMenuHints() []string {
	opts, _ := serverLiteLLMMenu(m.server.state, m.server.probeSkipped)
	if len(opts) == 0 {
		return []string{"esc back · ctrl+c quit", "esc · ctrl+c quit"}
	}
	return []string{"up/down choose · enter select · esc back · ctrl+c quit", "up/down · enter · esc · ctrl+c quit"}
}

// serverMenuView draws the menu: the title and the options always, then the
// reasons as dim lines as far as they fit, behind a marker for the rest.
func (m *model) serverMenuView(bar string, dim lipgloss.Style) string {
	opts, reasons := serverLiteLLMMenu(m.server.state, m.server.probeSkipped)
	lines := []string{"", "LiteLLM master key"}
	if len(opts) == 0 {
		lines = append(lines, "nothing can be done here:")
	}
	for i, o := range opts {
		mark := "  "
		if i == m.server.menuAt {
			mark = "> "
		}
		lines = append(lines, mark+o.label)
	}
	var rl []string
	for _, r := range reasons {
		for _, l := range strings.Split(editorkit.WrapText(editorkit.Scrub(r), m.width), "\n") {
			rl = append(rl, dim.Render(l))
		}
	}
	room := m.height - lipgloss.Height(bar) - 1
	if free := room - len(lines); len(rl) > free {
		keep := max(free-1, 0)
		rl = append(rl[:keep], dim.Render(fmt.Sprintf("... %d lines not shown ...", len(rl)-keep)))
		if free < 1 {
			rl = nil
		}
	}
	return tuilayout.MessageAlone(bar, strings.Join(append(lines, rl...), "\n"), editorkit.FitHints(m.width, m.serverMenuHints()), m.width, m.height, dim)
}

// plistModeLines is the status block's warning for a LiteLLM plist that is
// readable by others: the word warning, the mode, and the fix.
func plistModeLines(st modelsetup.State, home string) []serverLine {
	p := st.LiteLLMPlist
	if !plistLoose(p) {
		return nil
	}
	return []serverLine{{"", fmt.Sprintf("warning: plist mode %04o is readable by others: tighten to 0600 (chmod 600 %s)",
		p.Mode.Perm(), editorkit.HomePath(p.Path, st.Host.Home, home)), toneWarn}}
}

// serverResultScreen is what an apply left to read: the commands to run by
// hand and the notes (the backup's path), each scrubbed, and the result's
// own text. It is a screen of its own, scrolled with up and down, because
// a recovery command cut off is useless.
type serverResultScreen struct {
	recovery string
	notes    []string
	text     string
	top      int
}

// newResultScreen is the screen for a result, or nil when it has neither
// notes nor recovery to show.
func newResultScreen(res modelsetup.Result, text string) *serverResultScreen {
	if len(res.Notes) == 0 && res.Recovery == "" {
		return nil
	}
	r := &serverResultScreen{recovery: editorkit.Scrub(res.Recovery), text: text}
	for _, n := range res.Notes {
		r.notes = append(r.notes, editorkit.Scrub(n))
	}
	return r
}

// lines lays the screen out at width: the recovery first (the part that
// must be read), then the notes, then the result's text.
func (r *serverResultScreen) lines(width int, warn, dim lipgloss.Style) []string {
	var out []string
	add := func(s string, style lipgloss.Style) {
		for _, l := range strings.Split(editorkit.WrapText(s, width), "\n") {
			out = append(out, style.Render(l))
		}
	}
	plain := lipgloss.NewStyle()
	if r.recovery != "" {
		add("to put things right by hand:", warn)
		add(r.recovery, plain)
		out = append(out, "")
	}
	for _, n := range r.notes {
		add("note: "+n, plain)
	}
	if r.text != "" {
		if len(r.notes) > 0 || r.recovery != "" {
			out = append(out, "")
		}
		add(r.text, dim)
	}
	return out
}

// resultWindow is the screen's lines, how many rows show and whether it
// scrolls.
func (m *model) resultWindow() (lines []string, rows int, scrolls bool) {
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	warn := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenWarning))
	lines = m.server.result.lines(m.width, warn, dim)
	rows = max(m.height-lipgloss.Height(tabBar(m.theme, TabServer))-1, 1)
	if len(lines) > rows {
		return lines, max(rows-1, 1), true
	}
	return lines, rows, false
}

// updateServerResult takes keys while the result screen shows: up and down
// scroll, esc and enter leave it; q, ctrl+c and tab work as ever. Every other
// key is eaten, c and r included: a check or re-read would replace the
// status, and the Recovery commands and notes of a failed apply could not be
// shown again. Reports whether the key was handled here.
func (m *model) updateServerResult(key string) bool {
	st := &m.server
	lines, rows, _ := m.resultWindow()
	switch key {
	case "up":
		st.result.top = max(st.result.top-1, 0)
	case "down":
		st.result.top = min(st.result.top+1, max(len(lines)-rows, 0))
	case "esc", "enter":
		st.result = nil
	case "q", "ctrl+c", "tab":
		return false
	}
	return true
}

// serverResultView draws the result screen.
func (m *model) serverResultView(bar string, dim lipgloss.Style) string {
	lines, rows, scrolls := m.resultWindow()
	top := min(m.server.result.top, max(len(lines)-rows, 0))
	end := min(top+rows, len(lines))
	shown := append([]string{}, lines[top:end]...)
	if scrolls {
		shown = append(shown, dim.Render(fmt.Sprintf("... lines %d-%d of %d; up/down scroll ...", top+1, end, len(lines))))
	}
	return tuilayout.MessageAlone(bar, strings.Join(shown, "\n"), editorkit.FitHints(m.width, m.serverResultHints(scrolls)), m.width, m.height, dim)
}

// serverResultHints are the result screen's hints.
func (m *model) serverResultHints(scrolls bool) []string {
	scroll, short := "", ""
	if scrolls {
		scroll, short = "up/down scroll · ", "up/down · "
	}
	return []string{scroll + "esc/enter done · tab next · q quit", short + "esc/enter · tab · q quit", "esc · q quit"}
}
