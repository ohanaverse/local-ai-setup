package editorkit

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// LabelW is the width of a status line's label column ("problems"
// and two spaces).
const LabelW = 10

// Tone is how a status line is coloured; the words on it always say the
// same (V2), so a terminal without colour loses nothing.
type Tone int

const (
	ToneNone Tone = iota
	ToneDim
	ToneWarn
	ToneBad
)

// Line is one entry of the status block: a label (empty for a
// continuation entry under the previous label), its text and its tone.
type Line struct {
	Label, Text string
	Tone        Tone
}

// RenderLine lays one entry out at width: the label padded to its
// column and the text flowed between words beside it, continuation lines
// indented under the text. A terminal too narrow for the column flows the
// whole entry instead. No line is wider than width.
func RenderLine(l Line, width int) []string {
	pad := tuilayout.PadRunes(l.Label, LabelW)
	if width <= 0 {
		return []string{pad + l.Text}
	}
	if width-LabelW < 16 {
		return Flow(strings.Fields(l.Label+" "+l.Text), " ", width)
	}
	text := Flow(strings.Fields(l.Text), " ", width-LabelW)
	out := make([]string, len(text))
	for i, t := range text {
		if i == 0 {
			out[i] = pad + t
		} else {
			out[i] = strings.Repeat(" ", LabelW) + t
		}
	}
	return out
}

// ChoiceLines lays a choice's options out under label: the label padded to
// its column and the options flowed beside it, continuation lines indented
// under them. A terminal too narrow for the column flows the options alone,
// without the label.
func ChoiceLines(label string, opts []string, width int) []string {
	var lines []string
	switch {
	case width <= 0:
		lines = []string{tuilayout.PadRunes(label, LabelW) + strings.Join(opts, "  ")}
	case width-LabelW >= 16:
		for i, l := range Flow(opts, "  ", width-LabelW) {
			lead := strings.Repeat(" ", LabelW)
			if i == 0 {
				lead = tuilayout.PadRunes(label, LabelW)
			}
			lines = append(lines, lead+l)
		}
	default:
		lines = Flow(opts, "  ", width)
	}
	return lines
}

// Styles is how each tone is drawn in theme.
func Styles(theme themes.Theme) map[Tone]lipgloss.Style {
	return map[Tone]lipgloss.Style{
		ToneNone: lipgloss.NewStyle(),
		ToneDim:  lipgloss.NewStyle().Foreground(theme.Token(themes.TokenDim)),
		ToneWarn: lipgloss.NewStyle().Foreground(theme.Token(themes.TokenWarning)),
		ToneBad:  themes.ErrorStyle(theme),
	}
}

// StatusGroup is one status row with its continuation lines, rendered: the
// unit that is kept or dropped whole.
type StatusGroup struct {
	Label string
	Lines []string
}

// Groups lays the status block out as one group per labelled
// row (an entry without a label belongs to the row before it).
func Groups(lines []Line, width int, styles map[Tone]lipgloss.Style) []StatusGroup {
	var groups []StatusGroup
	for _, l := range lines {
		if l.Label != "" || len(groups) == 0 {
			groups = append(groups, StatusGroup{Label: l.Label})
		}
		g := &groups[len(groups)-1]
		for _, line := range RenderLine(l, width) {
			g.Lines = append(g.Lines, styles[l.Tone].Render(line))
		}
	}
	return groups
}

// Body is what FitBody lays out, top to bottom: the head (a status line, a
// banner), the status groups, the choice, the notes under it and the fields.
// Fields are drawn as given (a caller wanting a blank line above them
// includes it).
type Body struct {
	Head   []string
	Groups []StatusGroup
	Choice []string
	Notes  [][]string
	Fields []string
}

// FitBody lays b out in at most room lines. The choice (with the fields)
// and the head are kept whole; when the rest does not fit, status groups go
// whole, in dropOrder (first entry first), and one marker at the end of the
// block counts them. Then the notes go, last first; if that is still too
// tall the blank lines go, then the middle of the head.
func FitBody(b Body, room int, dropOrder []string, dim lipgloss.Style) []string {
	head, groups, radios, notes, fieldLines := b.Head, b.Groups, b.Choice, b.Notes, b.Fields

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
			if !dropped[g.Label] {
				out = append(out, g.Lines...)
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
	for _, label := range dropOrder {
		if len(out) <= room {
			break
		}
		for _, g := range groups {
			if g.Label == label {
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
	return out
}

// StatusBlock is the status as it is drawn: wrapped to the terminal's width,
// "" when there is none. A terminal cuts a too-wide line at its right edge,
// and what a status ends with is often the part that matters — a location
// error ends with the file to fix (#209).
func StatusBlock(status string, width int) string {
	if status == "" {
		return ""
	}
	if width <= 0 {
		return status
	}
	return lipgloss.NewStyle().Width(width).Render(status)
}
