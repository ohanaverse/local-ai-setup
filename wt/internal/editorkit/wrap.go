package editorkit

import (
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// Flow and WrapText are tuilayout's: the launcher's wrapping (it wraps a
// status with the same two), under the names both editors use.
func Flow(units []string, sep string, width int) []string {
	return tuilayout.Flow(units, sep, width)
}

func WrapText(s string, width int) string { return tuilayout.WrapText(s, width) }

// WrapIndented wraps each line of a plan's text by itself, keeping its
// indent and hanging its continuation lines under the text after the step
// number or the bullet ("  1. ", "  - "), so the steps stay apart. A
// terminal too narrow for the hang wraps the line plainly.
func WrapIndented(text string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		body := strings.TrimLeft(line, " ")
		lead := strings.Repeat(" ", len(line)-len(body))
		hang := lead
		if mark, _, ok := strings.Cut(body, " "); ok && lead != "" && (mark == "-" || strings.HasSuffix(mark, ".")) {
			hang += strings.Repeat(" ", len(mark)+1)
		}
		if width <= 0 || width-len(hang) < 16 {
			out = append(out, WrapText(line, width))
			continue
		}
		for i, l := range strings.Split(WrapText(body, width-len(hang)), "\n") {
			if i == 0 {
				out = append(out, lead+l)
			} else {
				out = append(out, hang+l)
			}
		}
	}
	return strings.Join(out, "\n")
}
