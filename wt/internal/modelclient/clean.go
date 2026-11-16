package modelclient

import (
	"strings"
	"unicode"
)

// clean is s as one printable line: every control and bidi rune is dropped (a
// tab, newline or carriage return becomes a space) and whitespace runs
// collapse to one space. The server is a remote party (style guide M11), so
// every string it sends passes through here before wt shows it. It mirrors
// cleanup.Flatten, which this package cannot import (cleanup pulls in git);
// a test in modelserver keeps the two equal.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// CleanForTest exposes clean to the test that compares it with cleanup.Flatten.
func CleanForTest(s string) string { return clean(s) }

func cleanAll(ss []string) []string {
	for i, s := range ss {
		ss[i] = clean(s)
	}
	return ss
}
