// The usage table's text rendering: borderless and width-aware.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// usageColumns are the usage table's columns: the model id, then five
// right-aligned numbers.
var usageColumns = []plainColumn{
	{head: "MODEL"}, {head: "LAUNCHES", right: true}, {head: "REQUESTS", right: true},
	{head: "PROMPT", right: true}, {head: "COMPLETION", right: true}, {head: "SPEND", right: true},
}

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

// visibleID is a model id made safe to print in a table: each character
// that is not a visible one is spelled as Go would write it in a string
// literal. That is every control character (a newline, an escape), and
// every character that takes no space or moves the text around it: the
// bidi overrides and isolates (U+202E, U+2066), zero-width characters
// (U+200B, U+FEFF), the line and paragraph separators (U+2028, U+2029), and
// spaces other than the ASCII one. Launched ids are wt's own, but a
// spend-only id is whatever the proxy logged as model_group: a raw escape
// sequence there would repaint the terminal and throw off the column
// widths, and a bidi override would show the row's numbers in another
// order than they were printed. --json prints ids through encoding/json,
// which escapes them by its own rules.
//
// A backslash already in an id is left as it is — an id with none of these
// characters prints unchanged, which is what makes it safe to copy — so the
// spelling cannot be reversed: it is for reading, not for parsing.
func visibleID(id string) string {
	if strings.IndexFunc(id, invisible) < 0 {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		if invisible(r) {
			q := strconv.QuoteRune(r) // '\n', '\x1b', '\u202e'
			b.WriteString(q[1 : len(q)-1])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// invisible reports whether r must be escaped by visibleID: anything Go
// does not count as printable (letters, marks, numbers, punctuation,
// symbols and the ASCII space are).
func invisible(r rune) bool { return !unicode.IsPrint(r) }

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

// renderUsageTable lays the rows out with renderPlainTable: MODEL
// left-aligned, the five number columns right-aligned and sized to their
// widest cell. No id is ever truncated: the id is what a reader copies into
// `wt -M` or `--model`. (visibleID spells out control and invisible
// characters; that is the only change an id undergoes.)
//
// The five number columns need about 54 columns with seven-digit token
// counts; on a terminal narrower than those plus "MODEL", the lines are
// longer than the terminal and it wraps them.
func renderUsageTable(rows []usageRow, width int) string {
	cells := make([][]string, len(rows))
	for i, r := range rows {
		c := usageCells(r)
		cells[i] = c[:]
	}
	return renderPlainTable(usageColumns, cells, width)
}
