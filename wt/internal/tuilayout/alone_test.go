package tuilayout

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestMessageAloneCutsTheMiddleAndKeepsTheHint verifies the screen a message
// too long for its frame is given (a refusal that ends with the path of the
// file to fix): at every height from 1 up, for messages shorter than, equal
// to and far longer than the room, the result is never taller than the
// terminal, no line is wider than it, and the hint is the last line. A
// message that fits is whole and has no marker; one that does not keeps its
// first and its last lines round one marker line that counts the lines taken
// out, fills the height exactly, and loses its lines before the top line,
// the top line before the marker, and the hint never. Cutting from the
// bottom instead — what lipgloss's MaxHeight does — drops the hint that says
// how to leave the screen and then the end of the path, with nothing on
// screen to say so.
func TestMessageAloneCutsTheMiddleAndKeepsTheHint(t *testing.T) {
	msg := func(n int) string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("m%d", i+1)
		}
		return strings.Join(lines, "\n")
	}
	const top, hint = "TOP", "press a key"
	mark := func(n int) string { return fmt.Sprintf("… %d lines not shown …", n) }
	cases := []struct {
		name          string
		lines, height int
		want          []string
	}{
		{"shorter than the room", 3, 12, []string{top, "m1", "m2", "m3", hint}},
		{"exactly the room", 10, 12, []string{top, "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10", hint}},
		{"one line too many", 11, 12, []string{top, "m1", "m2", "m3", "m4", mark(2), "m7", "m8", "m9", "m10", "m11", hint}},
		{"far too long", 40, 12, []string{top, "m1", "m2", "m3", "m4", mark(31), "m36", "m37", "m38", "m39", "m40", hint}},
		{"an even number of lines left", 40, 7, []string{top, "m1", "m2", mark(36), "m39", "m40", hint}},
		{"one line left goes to the end", 40, 4, []string{top, mark(39), "m40", hint}},
		{"no line left", 40, 3, []string{top, mark(40), hint}},
		{"no room for the top line", 40, 2, []string{mark(40), hint}},
		{"no room for the marker", 40, 1, []string{hint}},
		{"a short message on two lines", 1, 2, []string{"m1", hint}},
		{"a short message on one line", 1, 1, []string{hint}},
		{"a two-line message on three lines", 2, 3, []string{top, mark(2), hint}},
		{"no height reported yet", 40, 0, append(append([]string{top}, strings.Split(msg(40), "\n")...), hint)},
	}
	for _, c := range cases {
		got := MessageAlone(top, msg(c.lines), hint, 40, c.height, lipgloss.NewStyle())
		if want := strings.Join(c.want, "\n"); got != want {
			t.Errorf("%s (%d lines at height %d):\n%s\nwant:\n%s", c.name, c.lines, c.height, got, want)
		}
	}

	// Every height and length: the invariants, whatever the split.
	for height := 1; height <= 30; height++ {
		for lines := 1; lines <= 60; lines++ {
			got := strings.Split(MessageAlone(top, msg(lines), hint, 40, height, lipgloss.NewStyle()), "\n")
			name := fmt.Sprintf("%d lines at height %d", lines, height)
			if len(got) > height {
				t.Errorf("%s: %d lines tall", name, len(got))
			}
			if got[len(got)-1] != hint {
				t.Errorf("%s: the last line = %q, want the hint", name, got[len(got)-1])
			}
			marks := strings.Count(strings.Join(got, "\n"), "not shown")
			fits := 1+lines+1 <= height
			switch {
			case fits && (marks != 0 || len(got) != lines+2):
				t.Errorf("%s: a message that fits should be whole with no marker: %q", name, got)
			case !fits && height >= 3 && (marks != 1 || len(got) != height):
				t.Errorf("%s: want one marker and the height filled: %q", name, got)
			case !fits && height >= 4 && got[len(got)-2] != fmt.Sprintf("m%d", lines):
				t.Errorf("%s: the message's last line should be the one above the hint: %q", name, got)
			case !fits && height >= 5 && got[1] != "m1":
				t.Errorf("%s: the message's first line should be under the top line: %q", name, got)
			}
		}
	}

	// Width: the marker and the hint are cut to the terminal like any line,
	// measured in columns (a wide rune is two).
	for _, width := range []int{5, 12, 40} {
		view := MessageAlone(top, "一二三四五六七八九十\n"+msg(30), "a hint longer than five columns", width, 6, lipgloss.NewStyle())
		for _, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: a line is %d columns wide: %q", width, w, line)
			}
		}
		if h := lipgloss.Height(view); h != 6 {
			t.Errorf("width %d: the view is %d lines tall, want 6", width, h)
		}
	}
}
