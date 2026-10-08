package tuilayout

import (
	"strings"
	"unicode/utf8"
)

// Free text that must be read whole — a status that ends in the command to
// run — is wrapped, not clipped: Clip ends a line at the terminal's edge with
// no sign that anything was cut. Wrapped text is as many lines as it needs,
// so a frame that wraps is measured like any other (FitList) and a screen
// keeps a clipped frame behind it for a terminal too short.

// Flow lays units out on lines of at most width runes, sep between two units
// on a line. A line is broken only between units — never inside one, and so
// never at a hyphen inside an id or a path — except that a unit longer than a
// whole line is cut at the line's end.
func Flow(units []string, sep string, width int) []string {
	width = max(width, 1)
	var lines []string
	cur := ""
	for _, u := range units {
		if cur != "" {
			if utf8.RuneCountInString(cur+sep+u) <= width {
				cur += sep + u
				continue
			}
			lines, cur = append(lines, cur), ""
		}
		runes := []rune(u)
		for len(runes) > width {
			lines, runes = append(lines, string(runes[:width])), runes[width:]
		}
		cur = string(runes)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// WrapText wraps free text to width at its spaces, keeping the line breaks
// it has. A path on a line of its own is therefore cut only at the
// terminal's edge and can be copied as one token. A line that fits is kept
// as it was written, its spacing included, and so is every line when the
// width is not known yet (0, before the first WindowSizeMsg): Flow would
// otherwise put one rune on a line.
func WrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
			continue
		}
		if utf8.RuneCountInString(line) <= width {
			lines = append(lines, line)
			continue
		}
		lines = append(lines, Flow(strings.Fields(line), " ", width)...)
	}
	return strings.Join(lines, "\n")
}
