package tuilayout

import (
	"strings"
	"testing"
)

// TestWrapTextBreaksOnlyBetweenWords verifies the wrap both TUIs put a long
// status through: every line fits the width, no word is split while it fits
// a line by itself, a line that already fits keeps its spacing, and an
// unknown width (0, before the terminal reports one) changes nothing. A wrap
// that broke inside a word would split a model id or a path at a hyphen, and
// one that ran at width 0 would draw one rune a line.
func TestWrapTextBreaksOnlyBetweenWords(t *testing.T) {
	text := "start it with `llmbench provider isolate --solo mlx_lm_server org/Big-4bit --draft org/Small-4bit`"
	got := WrapText(text, 40)
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 40 {
			t.Errorf("line %q is wider than 40", line)
		}
	}
	if strings.Join(strings.Fields(got), " ") != text {
		t.Errorf("wrapped text lost or split a word:\n%s", got)
	}
	if !strings.Contains(got, "org/Big-4bit") || !strings.Contains(got, "mlx_lm_server") {
		t.Errorf("a word was broken inside:\n%s", got)
	}
	if got := WrapText("kept  as   written", 40); got != "kept  as   written" {
		t.Errorf("a line that fits = %q, want it unchanged", got)
	}
	if got := WrapText(text, 0); got != text {
		t.Errorf("width 0 = %q, want the text unchanged", got)
	}
	if got := Flow([]string{"abcdefghij"}, " ", 4); strings.Join(got, "|") != "abcd|efgh|ij" {
		t.Errorf("a word longer than a line = %q, want it cut at the line's end", got)
	}
}
