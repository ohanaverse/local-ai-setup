package cleanup

import (
	"strings"
	"testing"
)

// TestFlattenKeepsLengthAndStripsControls checks Flatten joins lines,
// drops ESC and other control runes like OneLine, and never cuts the text,
// while OneLine still caps at gitErrMax. Why: wt restore-root's reasons end
// with the recovery sentence or a remedy line that must reach the user
// whole, yet must not carry terminal escapes.
func TestFlattenKeepsLengthAndStripsControls(t *testing.T) {
	long := "first\nsecond \x1b[31mred\x07 " + strings.Repeat("x", 300) + " END"
	got := Flatten(long)
	if strings.ContainsAny(got, "\n\r\x1b\x07") {
		t.Errorf("Flatten kept a control character: %q", got)
	}
	if !strings.HasPrefix(got, "first second [31mred ") || !strings.HasSuffix(got, " END") {
		t.Errorf("Flatten = %q", got)
	}
	if n := len([]rune(OneLine(long))); n != gitErrMax {
		t.Errorf("OneLine is %d runes, want the %d cap", n, gitErrMax)
	}
}
