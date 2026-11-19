package sessioneditor

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeview"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// modeHints are the Mode tab's key hints for the phase showing, fullest
// first. Only keys that act are named (T5), read from the same predicates
// the handlers use: the mode keys once an observation is in and nothing is
// out (the form is inert while busy), down where the chosen mode has fields,
// a while a write may run (modeWriteBlocked). c and r both read the machine
// again, c to check after a change elsewhere, r to read a config.toml
// repaired by hand.
func (m *model) modeHints() []string {
	st := &m.mode
	switch st.phase {
	case modeEdit:
		return []string{"up/down field · enter next · esc done · ctrl+c quit", "up/down · enter · esc · ctrl+c quit"}
	case modeApplying:
		return []string{"tab next · q quit (after this finishes)", "tab · q quit"}
	case modeConfirm:
		switch st.prompt {
		case promptSaveAnyway:
			return []string{"y save anyway · n do not · esc cancel · ctrl+c quit", "y · n · esc · ctrl+c quit"}
		case promptRunSetup:
			return []string{"y run it · n/enter/esc not now · ctrl+c quit", "y · n · ctrl+c quit"}
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
	if m.modeWriteBlocked() == "" {
		keys = append(keys, hint{"a", "apply"})
	}
	keys = append(keys, hint{"c", "check"}, hint{"r", "re-read"}, hint{"tab", "next"})
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

// modeStatusLines is the status block: the effective mode and where it came
// from, the url, the token file, the server check, the env-override line and
// the problems. No pair, agent, LiteLLM or plist rows: those are
// `wt-model setup`'s.
func modeStatusLines(st modelmode.State, home string) []editorkit.Line {
	lines := []editorkit.Line{modeview.ModeLine(st), modeview.URLLine(st), modeview.TokenLine(st, home), modeview.ServerCheckLine(st)}
	if env, ok := modeview.EnvLine(st); ok {
		lines = append(lines, env)
	}
	return append(lines, modeview.ProblemLines(st)...)
}

// modeChoice is the mode choice, (x) on the chosen one, and under it, while
// local is chosen, the note that wt-model's own setup is `wt-model setup`.
func (m *model) modeChoice() (radios []string, notes [][]string) {
	st := &m.mode
	var opts []string
	for _, mode := range modeChoices {
		mark := "( )"
		if mode == st.want.Mode {
			mark = "(x)"
		}
		opts = append(opts, mark+" "+string(mode))
	}
	lines := editorkit.ChoiceLines("choose", opts, m.width)
	if st.want.Mode == modelmode.ModeLocal {
		notes = append(notes, editorkit.RenderLine(editorkit.Line{Text: localPointerNote}, m.width))
	}
	return lines, notes
}

// modeFieldLines are the chosen mode's fields, one line each: the label
// (marked "> " while being edited) and the input. The pasted token is drawn
// by its input masked; the url is scrubbed like every other url.
func (m *model) modeFieldLines() []string {
	st := &m.mode
	var lines []string
	for _, f := range st.visibleFields() {
		label := modeFieldLabels[f]
		if st.phase == modeEdit && st.field == f {
			label = "> " + label
		}
		view := st.fields[f].View()
		if f == mfURL {
			view = editorkit.Scrub(view)
		}
		lines = append(lines, tuilayout.PadRunes(label, editorkit.LabelW)+view)
	}
	return lines
}

// modeDropOrder is the order a terminal too short gives up status rows:
// first entry first. The mode and the server are the last to go.
var modeDropOrder = []string{"env", "token", "url", "problems", "server", "mode"}

// modeBody is the tab's body under the tab bar, in at most room lines: a
// status line, the parse error, the status block and the mode choice, styled
// and fitted by editorkit.FitBody (status rows go whole, in modeDropOrder).
func (m *model) modeBody(room int) string {
	styles := editorkit.Styles(m.theme)
	var head []string
	if s := m.mode.status; s != "" {
		head = append(head, strings.Split(editorkit.WrapText(s, m.width), "\n")...)
	}
	if m.quitPending && m.mode.writing {
		head = append(head, strings.Split(editorkit.WrapText("wt config quits once this apply has finished", m.width), "\n")...)
	}
	if !m.mode.loaded {
		return strings.Join(append(head, "", editorkit.WrapText("reading the model server's state", m.width)), "\n")
	}
	st := m.mode.state
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
	groups := editorkit.Groups(modeStatusLines(st, m.home), m.width, styles)
	radios, notes := m.modeChoice()
	var fieldLines []string
	if fields := m.modeFieldLines(); len(fields) > 0 {
		fieldLines = append([]string{""}, fields...)
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	b := editorkit.Body{Head: head, Groups: groups, Choice: radios, Notes: notes, Fields: fieldLines}
	return strings.Join(editorkit.FitBody(b, room, modeDropOrder, dim), "\n")
}

// modeTabView renders the Mode tab: the tab bar, a blank line, the body and
// the hint line on the terminal's last line (L3). Before the first
// WindowSizeMsg it is the tab bar alone.
func (m *model) modeTabView() string {
	bar := tabBar(m.theme, TabMode)
	if m.width <= 0 || m.height <= 0 {
		return bar
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	switch {
	case m.mode.phase == modeConfirm:
		return m.modePromptView(bar, dim)
	case m.mode.result != nil && m.mode.phase == modeView:
		warn := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenWarning))
		return m.mode.result.View(bar, modeResultHints, m.width, m.height, warn, dim)
	}
	// The bar, the blank line under it and the hint line are not the body's.
	body := "\n" + m.modeBody(m.height-lipgloss.Height(bar)-2)
	// Blank filler pins the hints to the last line when the body fits.
	if fill := m.height - lipgloss.Height(bar) - lipgloss.Height(body) - 1; fill > 0 {
		body += strings.Repeat("\n", fill)
	}
	return tuilayout.MessageAlone(bar, body, editorkit.FitHints(m.width, m.modeHints()), m.width, m.height, dim)
}

// modeResultHints are the result screen's hints.
func modeResultHints(scrolls bool) []string {
	scroll, short := "", ""
	if scrolls {
		scroll, short = "up/down scroll · ", "up/down · "
	}
	return []string{scroll + "esc/enter done · tab next · q quit", short + "esc/enter · tab · q quit", "esc · q quit"}
}

// modePromptView is the confirm phase: what the question is about, then the
// question on the line above the hints. A text too tall for the terminal
// loses lines from its middle (L4); the question is last, so it stays.
func (m *model) modePromptView(bar string, dim lipgloss.Style) string {
	st := &m.mode
	switch st.prompt {
	case promptSaveAnyway:
		s := st.state.Server
		text := "The check of " + editorkit.Scrub(s.URL) + " failed: " + modeview.KindWord(s.Kind) + ": " + editorkit.Scrub(s.Message) +
			". The server may be off; y stores the setup unchecked."
		return editorkit.PromptView(bar, text, "Save anyway? [y/N]", m.modeHints(), m.width, m.height, dim)
	case promptRunSetup:
		text := st.status + "\n" + localPointerNote + "."
		return editorkit.PromptView(bar, text, runSetupQuestion, m.modeHints(), m.width, m.height, dim)
	}
	return editorkit.PlanPromptView(bar, st.planText, "Apply this plan? [y/N]", m.modeHints(), m.width, m.height, dim)
}
