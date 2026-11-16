package modelclient

import (
	"strings"
	"testing"
	"unicode"
)

// TestCleanDropsControlsAndBidi pins the decode boundary's one rule: text a
// remote server sent comes back as one printable line, so it cannot forge
// lines, retitle the terminal or reorder a line (style guide M11).
func TestCleanDropsControlsAndBidi(t *testing.T) {
	cases := map[string]string{
		"plain":             "plain",
		"a\nb":              "a b",
		"a\r\n\t b":         "a b",
		"x\x1b[31mred\x07":  "x[31mred",
		"‮evil‬":            "evil",
		"  spaced   out  ":  "spaced out",
		"c1\u0085ctl\u009b": "c1ctl",
		"bidi⁦i⁩‏":          "bidii",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
		for _, r := range clean(in) {
			if unicode.IsControl(r) {
				t.Errorf("clean(%q) kept control %U", in, r)
			}
		}
	}
	if strings.Contains(clean("a\nb"), "\n") {
		t.Error("newline survived")
	}
}
