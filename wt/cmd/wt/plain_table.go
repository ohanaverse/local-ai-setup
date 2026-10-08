// The borderless, width-aware text table `wt stats` and `wt model list`
// print. One renderer, so the two listings cannot drift apart.
package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// plainColumn is one column of a plain table: its heading, and whether its
// cells are right-aligned (numbers) or left-aligned (text).
type plainColumn struct {
	head  string
	right bool
}

// plainGap separates two columns.
const plainGap = "  "

// plainTableWidth is what renderPlainTable needs to print every row on one
// line: key is the first column at its widest cell, rest is every other
// column at its widest with the gaps between them (the gap after the key
// included). A caller that can drop columns compares key+rest, or rest plus
// the least key width it will accept, with the terminal's width.
func plainTableWidth(cols []plainColumn, rows [][]string) (key, rest int) {
	widths := plainWidths(cols, rows)
	for _, w := range widths[1:] {
		rest += len(plainGap) + w
	}
	return widths[0], rest
}

func plainWidths(cols []plainColumn, rows [][]string) []int {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = lipgloss.Width(c.head)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], lipgloss.Width(c))
		}
	}
	return widths
}

// renderPlainTable lays rows out without borders, each column sized to its
// widest cell. The first column is the row's key (a model id): it is never
// truncated, because it is what a reader copies into another command.
//
// width is the terminal's width, or 0 for no limit. When the key column and
// the others do not fit together, the key column is narrowed to what is
// left. A key wider than that runs on into the blank space left of a
// right-aligned second cell when there is room for it and a gap; otherwise it
// is printed on a line of its own, with the rest of the row on the next line
// under its headers — what df does with a long device name.
//
// When the other columns alone are wider than the terminal, the lines are
// longer than the terminal and it wraps them; a caller that can drop columns
// does so first (plainTableWidth). No line ends in a space.
func renderPlainTable(cols []plainColumn, rows [][]string, width int) string {
	widths := plainWidths(cols, rows)
	rest := 0
	for _, w := range widths[1:] {
		rest += len(plainGap) + w
	}
	if width > 0 && widths[0]+rest > width {
		widths[0] = max(width-rest, lipgloss.Width(cols[0].head))
	}

	var b strings.Builder
	line := func(c []string) {
		var l strings.Builder
		// first is the column the second cell starts at; the key may use
		// everything left of it but the gap.
		first := widths[0]
		if len(c) > 1 {
			first += len(plainGap)
			if cols[1].right {
				first += widths[1] - lipgloss.Width(c[1])
			}
		}
		key := c[0]
		if len(c) > 1 && lipgloss.Width(key)+len(plainGap) > first {
			b.WriteString(key + "\n")
			key = ""
		}
		l.WriteString(key)
		for j := 1; j < len(c); j++ {
			pad := widths[j] - lipgloss.Width(c[j])
			if j == 1 {
				l.WriteString(strings.Repeat(" ", first-lipgloss.Width(key)))
			} else {
				l.WriteString(plainGap)
				if cols[j].right {
					l.WriteString(strings.Repeat(" ", pad))
				}
			}
			l.WriteString(c[j])
			if !cols[j].right {
				l.WriteString(strings.Repeat(" ", pad))
			}
		}
		b.WriteString(strings.TrimRight(l.String(), " ") + "\n")
	}
	heads := make([]string, len(cols))
	for i, c := range cols {
		heads[i] = c.head
	}
	line(heads)
	for _, r := range rows {
		line(r)
	}
	return strings.TrimRight(b.String(), "\n")
}
