package tuilayout

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MessageAlone draws a message that has the screen to itself — a refusal too
// long to share a short terminal with the form or the table it is about:
// top (the tab bar), the message, already wrapped to the width, and one hint
// line that says how to leave, drawn in dim.
//
// When the three are taller than the terminal the message is the part that
// is cut, and from its middle: its first lines say what was refused, and its
// last lines are the end of the path of the file to fix, which nothing else
// on screen repeats. One marker line stands where the lines were taken
// out and counts them, and the result is exactly height lines. The lines left
// for the message are shared evenly between its start and its end, the odd
// one going to the end.
//
// A terminal too short for that gives up, in order, every line of the
// message, then top, then the marker; the hint is the last line of every
// result and the only one at a height of 1. A height that is not positive
// (no size reported yet) cuts nothing. Every line is cut to width columns
// (Clip), so the result is never wider than the terminal either.
//
// lipgloss's MaxHeight is not a substitute: it drops lines from the bottom,
// which here are the hint and the path's end, and leaves no sign of it.
func MessageAlone(top, message, hint string, width, height int, dim lipgloss.Style) string {
	// Each line is clipped by itself: lipgloss pads the lines of a block it
	// renders to the widest of them.
	split := func(s string) []string {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			lines[i] = Clip(line, width)
		}
		return lines
	}
	var lines []string
	if top != "" {
		lines = split(top)
	}
	msg := split(message)
	hint = dim.Render(Clip(hint, width))
	// room is the lines left for the message once the hint has its own.
	room := height - 1 - len(lines)
	switch {
	case height <= 0 || len(msg) <= room:
		// Everything fits.
	case room < 1:
		// No line for top and a marker both: top goes, and what is left is
		// the marker's — or a message short enough to need none.
		lines, room = nil, height-1
		if len(msg) <= room {
			break
		}
		fallthrough
	default:
		keep := max(room-1, 0)
		tail := (keep + 1) / 2
		head := keep - tail
		marker := Clip(fmt.Sprintf("… %d lines not shown …", len(msg)-keep), width)
		// The marker is structural (content was cut), not instructional —
		// make it more visible than the dim hint.
		marker = lipgloss.NewStyle().Bold(true).Render(marker)
		cut := append([]string{}, msg[:head]...)
		if room >= 1 {
			cut = append(cut, marker)
		}
		msg = append(cut, msg[len(msg)-tail:]...)
	}
	return strings.Join(append(append(lines, msg...), hint), "\n")
}
