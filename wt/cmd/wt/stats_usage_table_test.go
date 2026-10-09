// Tests for the usage table's text rendering, measured at 80 columns.
package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
)

// realisticRows are usage rows with model ids of the lengths wt really
// routes (16 to 51 characters) and large token counts.
func realisticRows() []usageRow {
	row := func(id string, launches int, s *spend.Row) usageRow {
		return usageRow{Model: id, Launches: launches, Spend: s}
	}
	return []usageRow{
		row("mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", 3, &spend.Row{Requests: 1204, PromptTokens: 12345678, CompletionTokens: 1234567}),
		row("ollama/deepseek-v4-flash:cloud", 17, &spend.Row{}),
		row("ollama/gemma4:9b", 5, &spend.Row{Requests: 4, PromptTokens: 152, CompletionTokens: 630}),
		row("omlx/mlx-community--Qwen3.8-27B-4bit", 2, &spend.Row{Requests: 88, PromptTokens: 912004, CompletionTokens: 40117}),
		row("openrouter/qwen/qwen3.8-27b", 0, &spend.Row{Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 12.3456}),
	}
}

// TestRenderUsageTableFitsEightyColumns is the spec's measurement: with
// real model ids and seven-digit token counts, no line of the usage table
// is wider than an 80-column terminal, no id is truncated, and an id too
// long for the MODEL column either runs into the blank space left of its
// LAUNCHES value (the 30-character id below) or, with no room for that,
// sits on a line of its own with its numbers under the headers on the next
// line (the 36- and 51-character ids). The bordered renderTable the survey
// table uses is len(id)+51 columns wide for these six columns — 81 at a
// 30-character id — and wraps into an unreadable grid.
func TestRenderUsageTableFitsEightyColumns(t *testing.T) {
	got := renderUsageTable(realisticRows(), 80)
	want := strings.Join([]string{
		"MODEL                       LAUNCHES  REQUESTS      PROMPT  COMPLETION     SPEND",
		"mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
		"                                   3     1,204  12,345,678   1,234,567   $0.0000",
		"ollama/deepseek-v4-flash:cloud    17         0           0           0   $0.0000",
		"ollama/gemma4:9b                   5         4         152         630   $0.0000",
		"omlx/mlx-community--Qwen3.8-27B-4bit",
		"                                   2        88     912,004      40,117   $0.0000",
		"openrouter/qwen/qwen3.8-27b        0         5         517       3,135  $12.3456",
	}, "\n")
	if got != want {
		t.Errorf("table at 80 columns =\n%s\nwant\n%s", got, want)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line is %d columns wide, want at most 80: %q", w, line)
		}
	}
	for _, r := range realisticRows() {
		if !strings.Contains(got, r.Model) {
			t.Errorf("model id %s is missing or truncated", r.Model)
		}
	}
}

// TestRenderUsageTableWithoutAWidthLimit verifies that with no width (a
// pipe or a file, width 0) every model is exactly one line, however long
// its id. `wt stats | grep qwen` and `| awk` depend on one row per line;
// the two-line form is for a terminal only.
func TestRenderUsageTableWithoutAWidthLimit(t *testing.T) {
	rows := realisticRows()
	lines := strings.Split(renderUsageTable(rows, 0), "\n")
	if len(lines) != len(rows)+1 {
		t.Fatalf("%d lines for %d rows, want one line per row plus the header:\n%s", len(lines), len(rows), strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[1], "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality  ") || !strings.HasSuffix(lines[1], "$0.0000") {
		t.Errorf("long-id row = %q, want the id and its numbers on one line", lines[1])
	}
	// A wide terminal behaves the same: nothing needs to move.
	if wide := renderUsageTable(rows, 200); wide != strings.Join(lines, "\n") {
		t.Errorf("at 200 columns the table differs from the unlimited one:\n%s", wide)
	}
}

// TestRenderUsageTableShowsDashesWithoutSpend verifies the degraded table:
// with no spend data the launch count is still there and REQUESTS, PROMPT,
// COMPLETION and SPEND are each "-", never "0" or "$0.0000". A zero would
// claim the proxy logged nothing, when the truth is that nobody asked it.
func TestRenderUsageTableShowsDashesWithoutSpend(t *testing.T) {
	got := renderUsageTable([]usageRow{{Model: "ollama/gemma4:9b", Launches: 5}}, 80)
	want := "MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
		"ollama/gemma4:9b         5         -       -           -      -"
	if got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
}

// TestRenderUsageTableOnANarrowTerminal verifies a terminal too narrow for
// even the number columns (40 columns) still gets every id whole and every
// number, in this exact layout: the MODEL column shrinks to its header, so
// each id is on a line of its own, and the number lines (58 columns) run
// past the edge for the terminal to wrap, still aligned under the headers.
// Truncating an id to fit would print something that cannot be pasted into
// `wt -M`; and a layout that kept ids whole but let the numbers drift from
// their headers would pass a check that only looked for the ids.
func TestRenderUsageTableOnANarrowTerminal(t *testing.T) {
	got := renderUsageTable(realisticRows(), 40)
	want := strings.Join([]string{
		"MODEL  LAUNCHES  REQUESTS      PROMPT  COMPLETION     SPEND",
		"mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality",
		"              3     1,204  12,345,678   1,234,567   $0.0000",
		"ollama/deepseek-v4-flash:cloud",
		"             17         0           0           0   $0.0000",
		"ollama/gemma4:9b",
		"              5         4         152         630   $0.0000",
		"omlx/mlx-community--Qwen3.8-27B-4bit",
		"              2        88     912,004      40,117   $0.0000",
		"openrouter/qwen/qwen3.8-27b",
		"              0         5         517       3,135  $12.3456",
	}, "\n")
	if got != want {
		t.Errorf("table at 40 columns =\n%s\nwant\n%s", got, want)
	}
	// A width below the header's own is the same table: there is nothing
	// left to narrow.
	if tiny := renderUsageTable(realisticRows(), 1); tiny != want {
		t.Errorf("table at 1 column =\n%s\nwant the 40-column table", tiny)
	}
}

// TestRenderUsageTableEscapesControlCharacters verifies a model id holding
// an escape sequence and a newline is printed with both spelled out, on one
// line, and that the launch count gets the same thousands separators as the
// other counts. Spend-only ids come from the proxy's model_group column,
// which wt does not write; printed raw, such an id recolours the terminal
// and splits its own row in two.
func TestRenderUsageTableEscapesControlCharacters(t *testing.T) {
	got := renderUsageTable([]usageRow{{Model: "evil\x1b[31mred\nline2", Launches: 1234, Spend: &spend.Row{Requests: 1}}}, 80)
	want := "MODEL                   LAUNCHES  REQUESTS  PROMPT  COMPLETION    SPEND\n" +
		`evil\x1b[31mred\nline2     1,234         1       0           0  $0.0000`
	if got != want {
		t.Errorf("table =\n%q\nwant\n%q", got, want)
	}
}

// TestVisibleIDEscapesInvisibleCharacters verifies an id is printed with
// every character that is not a visible one spelled out: bidi overrides and
// isolates, zero-width characters, the byte order mark, the line and
// paragraph separators and non-ASCII spaces, as well as control characters.
// A right-to-left override in a proxy-logged model_group shows the numbers
// after it in reverse on a terminal that honours it, and a zero-width space
// makes two different ids look the same. Everything a real id is made of —
// letters in any script, digits, punctuation, the ASCII space, a backslash —
// is left exactly as it is, so a clean id can be copied from the table.
func TestVisibleIDEscapesInvisibleCharacters(t *testing.T) {
	bom := string(rune(0xFEFF))
	for in, want := range map[string]string{
		"ollama/gemma4:9b":             "ollama/gemma4:9b",
		`a b\n/é:漢字-ü_@"'`:             `a b\n/é:漢字-ü_@"'`,
		"evil\u202e9b:4ammeg":          `evil\u202e9b:4ammeg`,
		"iso\u2066x\u2069\u200f\u061c": `iso\u2066x\u2069\u200f\u061c`,
		"zero\u200bwidth\u200d" + bom:  `zero\u200bwidth\u200d\ufeff`,
		"line\u2028para\u2029":         `line\u2028para\u2029`,
		"nb\u00a0sp\u3000":             `nb\u00a0sp\u3000`,
		"ctl\x1b[31m\n\t\x7f":          `ctl\x1b[31m\n\t\x7f`,
	} {
		if got := visibleID(in); got != want {
			t.Errorf("visibleID(%q) = %q, want %q", in, got, want)
		}
	}
	// In the table: one line, and the columns sized to what is printed.
	got := renderUsageTable([]usageRow{{Model: "evil\u202e/x", Launches: 1}}, 80)
	want := "MODEL         LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
		`evil\u202e/x         1         -       -           -      -`
	if got != want {
		t.Errorf("table =\n%q\nwant\n%q", got, want)
	}
}

// TestFormatCount pins the usage table's thousands separators (a comma
// every three digits), at each digit-count edge. Token counts reach nine
// digits in a 30-day window, and an unseparated 123456789 is not readable at
// a glance.
func TestFormatCount(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 12345: "12,345", 123456: "123,456",
		1234567: "1,234,567", 4294967294: "4,294,967,294", -1234: "-1,234",
	} {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}
