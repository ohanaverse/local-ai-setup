// The usage table's text rendering: borderless and width-aware.
package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
)

// usageColumns are the usage table's columns: the model id, then five
// right-aligned numbers.
var usageColumns = []cli.PlainColumn{
	{Head: "MODEL"}, {Head: "LAUNCHES", Right: true}, {Head: "REQUESTS", Right: true},
	{Head: "PROMPT", Right: true}, {Head: "COMPLETION", Right: true}, {Head: "SPEND", Right: true},
}

// usageCells are one row's six cells. The four spend cells are "-" when
// the report has no spend data.
func usageCells(r usageRow) [6]string {
	c := [6]string{cli.VisibleID(r.Model), formatCount(int64(r.Launches)), "-", "-", "-", "-"}
	if r.Spend != nil {
		c[2] = formatCount(r.Spend.Requests)
		c[3] = formatCount(r.Spend.PromptTokens)
		c[4] = formatCount(r.Spend.CompletionTokens)
		c[5] = fmt.Sprintf("$%.4f", r.Spend.Spend)
	}
	return c
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

// renderUsageTable lays the rows out with cli.RenderPlainTable: MODEL
// left-aligned, the five number columns right-aligned and sized to their
// widest cell. No id is ever truncated: the id is what a reader copies into
// `wt -M` or `--model`. (cli.VisibleID spells out control and invisible
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
	return cli.RenderPlainTable(usageColumns, cells, width)
}
