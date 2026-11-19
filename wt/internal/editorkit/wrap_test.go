package editorkit

import "testing"

// TestWrapTextWithoutAWidth verifies text is left as written when the width
// is not known yet (a view drawn before the first WindowSizeMsg), and that a
// line that fits keeps its own spacing. Wrapping to a width of 0 put one
// rune on each line.
func TestWrapTextWithoutAWidth(t *testing.T) {
	text := "one two  three\n\n/a/path with spaces"
	if got := WrapText(text, 0); got != text {
		t.Errorf("WrapText at width 0 = %q, want the text unchanged", got)
	}
	if got := WrapText(text, 80); got != text {
		t.Errorf("WrapText at 80 = %q, want the text unchanged", got)
	}
	if got := WrapText("one two  three", 7); got != "one two\nthree" {
		t.Errorf("WrapText at 7 = %q", got)
	}
}

// TestWrapIndentedHangsUnderTheStepNumber verifies a plan's step keeps its
// indent and hangs its continuation lines under the text after the step
// number, so the steps stay apart when wrapped; that a width not known yet
// leaves the text alone; and that a terminal too narrow for the hang wraps
// the line plainly rather than hanging text into nothing.
func TestWrapIndentedHangsUnderTheStepNumber(t *testing.T) {
	if got, want := WrapIndented("  1. aaa bbb ccc ddd eee fff ggg hhh iii jjj", 24), "  1. aaa bbb ccc ddd\n     eee fff ggg hhh iii\n     jjj"; got != want {
		t.Errorf("WrapIndented at 24 = %q, want %q", got, want)
	}
	if got, want := WrapIndented("  - x", 0), "  - x"; got != want {
		t.Errorf("WrapIndented at 0 = %q, want %q", got, want)
	}
	if got, want := WrapIndented("  1. aaa bbb ccc ddd eee", 20), WrapText("  1. aaa bbb ccc ddd eee", 20); got != want {
		t.Errorf("WrapIndented at 20 = %q, want the plain wrap %q", got, want)
	}
}
