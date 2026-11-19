// Small text helpers for command-line output both binaries print.
package cli

import (
	"strconv"
	"strings"
	"unicode"
)

// Dash stands in for an empty table cell: "" becomes "-", anything else is
// returned as it is.
func Dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// VisibleID is a model id made safe to print in a table: each character
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
func VisibleID(id string) string {
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

// invisible reports whether r must be escaped by VisibleID: anything Go
// does not count as printable (letters, marks, numbers, punctuation,
// symbols and the ASCII space are).
func invisible(r rune) bool { return !unicode.IsPrint(r) }
