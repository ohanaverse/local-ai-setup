// Package editorkit is the UI plumbing the `wt config` and `wt-model` editors
// share: scrubbing, path and text fitting, the status and prompt layouts, the
// result screen, form inputs and the tab bar. It knows no model-server type.
package editorkit

import "regexp"

// userinfoRE finds a user:password in a url inside free text, through the
// last @ before the host: a password may hold an @ itself.
var userinfoRE = regexp.MustCompile(`://[^/\s]*@`)

// keyRE finds a LiteLLM-style key (sk- and a long run of key characters) that
// does not continue a longer word: group 1 is the character before it (RE2
// has no lookbehind), kept by the replacement.
var keyRE = regexp.MustCompile(`(^|[^A-Za-z0-9_-])sk-[A-Za-z0-9_-]{16,}`)

// Scrub takes any url userinfo and any sk- key out of text the tab draws:
// Observe already strips them from what it stores, but a message a dep
// composed may still carry one, and nothing the tab shows may hold a
// credential.
//
// Deliberately greedy: an @ anywhere before the first / or space ends the
// userinfo, so "https://h:1?x=a@b" loses its host too. That errs on the safe
// side (a credential is never shown); TestScrubTakesTheHostWithAnAtInTheQuery
// pins it.
func Scrub(s string) string {
	return keyRE.ReplaceAllString(userinfoRE.ReplaceAllString(s, "://"), "${1}sk-<hidden>")
}
