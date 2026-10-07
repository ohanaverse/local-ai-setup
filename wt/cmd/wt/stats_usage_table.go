// The usage table's text rendering: borderless and width-aware.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

var usageHeaders = [6]string{"MODEL", "LAUNCHES", "REQUESTS", "PROMPT", "COMPLETION", "SPEND"}

// usageGap separates two columns.
const usageGap = "  "

// usageCells are one row's six cells. The four spend cells are "-" when
// the report has no spend data.
func usageCells(r usageRow) [6]string {
	c := [6]string{visibleID(r.Model), formatCount(int64(r.Launches)), "-", "-", "-", "-"}
	if r.Spend != nil {
		c[2] = formatCount(r.Spend.Requests)
		c[3] = formatCount(r.Spend.PromptTokens)
		c[4] = formatCount(r.Spend.CompletionTokens)
		c[5] = fmt.Sprintf("$%.4f", r.Spend.Spend)
	}
	return c
}

// visibleID is a model id made safe to print in a table: each control
// character (a newline, an escape) is spelled as Go would write it in a
// string literal. Launched ids are wt's own, but a spend-only id is
// whatever the proxy logged as model_group, and a raw escape sequence there
// would repaint the terminal and throw off the column widths. --json prints
// ids through encoding/json, which escapes them itself.
func visibleID(id string) string {
	if strings.IndexFunc(id, unicode.IsControl) < 0 {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		if unicode.IsControl(r) {
			q := strconv.QuoteRune(r) // '\n', '\x1b'
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// formatCount renders n with thousands separators: 1234567 → "1,234,567".
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, d := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// renderUsageTable lays the rows out without borders: MODEL left-aligned,
// the five number columns right-aligned and sized to their widest cell.
//
// width is the terminal's width, or 0 for no limit. When the longest model
// id and the number columns do not fit together, the MODEL column is
// narrowed to what is left. A model id wider than that runs on into the
// blank space left of its own LAUNCHES value when there is room for it and
// a gap; otherwise it is printed on a line of its own, with its numbers on
// the next line under their headers — what df does with a long device
// name. No id is ever truncated: the id is what a reader copies into
// `wt -M` or `--model`. (visibleID spells out control characters; that is
// the only change an id undergoes.)
//
// The five number columns need about 54 columns with seven-digit token
// counts; on a terminal narrower than those plus "MODEL", the lines are
// longer than the terminal and it wraps them.
func renderUsageTable(rows []usageRow, width int) string {
	cells := make([][6]string, len(rows))
	widths := [6]int{}
	for i, h := range usageHeaders {
		widths[i] = lipgloss.Width(h)
	}
	for i, r := range rows {
		cells[i] = usageCells(r)
		for j, c := range cells[i] {
			widths[j] = max(widths[j], lipgloss.Width(c))
		}
	}
	numbers := 0
	for _, w := range widths[1:] {
		numbers += len(usageGap) + w
	}
	if width > 0 && widths[0]+numbers > width {
		widths[0] = max(width-numbers, lipgloss.Width(usageHeaders[0]))
	}

	var b strings.Builder
	line := func(c [6]string) {
		// first is the column the LAUNCHES cell starts at; the model id may
		// use everything left of it but the gap.
		first := widths[0] + len(usageGap) + widths[1] - lipgloss.Width(c[1])
		model := c[0]
		if lipgloss.Width(model)+len(usageGap) > first {
			b.WriteString(model + "\n")
			model = ""
		}
		b.WriteString(model + strings.Repeat(" ", first-lipgloss.Width(model)) + c[1])
		for j := 2; j < 6; j++ {
			b.WriteString(usageGap + strings.Repeat(" ", widths[j]-lipgloss.Width(c[j])) + c[j])
		}
		b.WriteString("\n")
	}
	line(usageHeaders)
	for _, c := range cells {
		line(c)
	}
	return strings.TrimRight(b.String(), "\n")
}
