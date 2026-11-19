// Package urlrule is the one url @ rule: the rule a model-server url follows
// (internal/modelclient for a session, internal/modelsetup's Model server tab
// for setup) and the LiteLLM pair url follows (internal/config) — no parsing,
// no partial echo. A url has no legitimate '@', so any url holding an ASCII
// '@' anywhere is read as carrying a user:password and refused before it is
// written or a request is built, and nothing derived from it (no host, no
// user, no scheme, no piece) is ever printed. Finding the credential span in
// an arbitrary string was tried and bypassed three times (a leading "//", a
// password holding '/', '?', '#' or "://", a schemeless url that url.Parse
// reads as an opaque one); this rule has nothing to find.
package urlrule

import "strings"

// Hidden stands in for a url holding an '@' wherever one would be shown. It
// holds an '@' itself, so it is refused like the url it stands for.
const Hidden = "(hidden: the url contains an @)"

// HasUserinfo reports whether raw holds an ASCII '@' anywhere: every such url
// is read as carrying a user:password. Fullwidth U+FF20 and %40 are not '@'
// (no dialler reads them as the user separator).
func HasUserinfo(raw string) bool {
	return strings.Contains(raw, "@")
}

// Shown is the form of a url that may be printed: raw when it holds no '@',
// else Hidden, whole — never a piece of an '@' url.
func Shown(raw string) string {
	if HasUserinfo(raw) {
		return Hidden
	}
	return raw
}
