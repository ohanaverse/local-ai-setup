package cli

import "testing"

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
		if got := VisibleID(in); got != want {
			t.Errorf("VisibleID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDashStandsInForAnEmptyCell verifies Dash fills only a truly empty cell:
// "" becomes "-", and anything else, a lone space included, is kept as it is.
// A table cell left empty shifts every column after it when a reader splits
// the line on whitespace, while a value that happens to be blank is still a
// value and must not be mistaken for "nothing".
func TestDashStandsInForAnEmptyCell(t *testing.T) {
	for in, want := range map[string]string{"": "-", "x": "x", " ": " "} {
		if got := Dash(in); got != want {
			t.Errorf("Dash(%q) = %q, want %q", in, got, want)
		}
	}
}
