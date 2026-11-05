package cleanup

import (
	"errors"
	"strings"
	"unicode"
)

// noPRLookupDetail is the detail of a PR-lookup reason when the lookup left
// no error text. The header's gh line leaves it out beside a real cause.
const noPRLookupDetail = "no PR lookup for this branch"

// fetchErrMax caps a fetch error's length in runes.
const fetchErrMax = 200

// gitErrMax caps, in runes, git's stderr in a failed git command's error
// (the command and exit code before it are never cut), and any other error
// text a row detail shows.
const gitErrMax = 200

// gitErrText is error text that did not come from gitFailure as a row
// detail shows it: one sanitised line (flat), at most gitErrMax runes.
func gitErrText(s string) string {
	return capRunes(flat(s), gitErrMax)
}

// gitError is a gitFailure: its text is already one sanitised line with
// only git's stderr capped, so it is never cut again.
type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }

// gitDetail is err's text as a row detail (GatesErr, ClaimsErr, the
// unpushed texts) shows it. Text holding a gitFailure is only sanitised
// (flat): capping it again would cut the exit code and stderr off a long
// command line. Any other text is sanitised and capped (gitErrText).
func gitDetail(err error) string {
	var ge *gitError
	if errors.As(err, &ge) {
		return flat(err.Error())
	}
	return gitErrText(err.Error())
}

// printable is s with a tab, newline or carriage return turned into a space
// and every other control rune (C0, C1, ESC, BEL) and bidi control (U+202A–
// U+202E, U+2066–U+2069, U+200E/U+200F, U+061C) dropped. Tool stderr shown
// on the header must not drive the terminal (retitle it, recolour it,
// reorder the line). It mirrors tui's printable; tui imports this package,
// so it cannot be shared.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, s)
}

// flat is s made printable with its whitespace runs collapsed to single
// spaces: every line of s joined into one.
func flat(s string) string {
	return strings.Join(strings.Fields(printable(s)), " ")
}

// capRunes cuts s to max runes, ending in "…" when it was longer.
func capRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// fetchErrText is a failed fetch's text as the header and JSON fetch.error
// show it: all of git's lines on one sanitised line, at most fetchErrMax
// runes. git's later lines ("Could not read from remote repository") often
// say more than its first, so they are joined, not dropped.
func fetchErrText(s string) string {
	return capRunes(flat(s), fetchErrMax)
}
