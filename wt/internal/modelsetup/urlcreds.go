package modelsetup

import (
	"net/url"
	"strings"
)

// The one reading of a typed url's user and password, shared by the plan's
// refusal, Apply's config-step backstop and every place a url is shown.

// hasScheme reports whether raw starts with "<scheme>://". Only then is it
// parsed as a url as is; anything else is read as an authority.
func hasScheme(raw string) bool {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return false
	}
	for j, c := range raw[:i] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case j > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// parseTyped parses a typed url. One typed without a scheme ("host:port",
// "user:pass@host:port") is parsed as "//"+raw: url.Parse alone would take
// what precedes the first colon for a scheme and the rest for an opaque
// part, with no user at all. schemeless says it was read that way.
func parseTyped(raw string) (u *url.URL, schemeless bool, err error) {
	if hasScheme(raw) {
		u, err = url.Parse(raw)
		return u, false, err
	}
	u, err = url.Parse("//" + raw)
	return u, true, err
}

// credSpan is the one cut of a typed url's credentials: from just after its
// first "://" (or after a leading "//", or from its start) through its LAST
// '@'. ok is false when raw holds no ASCII '@'. Fail closed: a model-server
// url has no legitimate '@', so whatever precedes the last one is treated as
// a user:password, however the url parses (a leading "//", a password holding
// '/', '?' or '#', or a scheme typo all once parsed with no user at all).
func credSpan(raw string) (start, end int, ok bool) {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return 0, 0, false
	}
	if i := strings.Index(raw, "://"); i >= 0 && i+3 <= at && !strings.Contains(raw[:i+3], "@") {
		start = i + 3
	} else if strings.HasPrefix(raw, "//") {
		start = 2
	}
	return start, at + 1, true
}

// hasUserinfo reports whether a typed url holds an '@' anywhere: every such
// url is refused (wt does not store credentials in config.toml). host names
// the server in the refusal and never quotes what precedes the last '@': the
// parsed host when the url parses with a user, else the host of what follows
// the last '@', else "" (the refusal then says "the url").
func hasUserinfo(raw string) (host string, ok bool) {
	start, end, ok := credSpan(raw)
	if !ok {
		return "", false
	}
	if u, _, err := parseTyped(raw); err == nil && u.User != nil {
		return u.Hostname(), true
	}
	rest := raw[:start] + raw[end:]
	if u, _, err := parseTyped(rest); err == nil && !strings.Contains(u.Hostname(), "@") {
		return u.Hostname(), true
	}
	return "", true
}

// stripUserinfo is the display form of a typed url: everything credSpan
// covers is cut, so no user, password or '@' remains whatever characters the
// password holds. A url with no '@' comes back exactly as typed.
func stripUserinfo(raw string) string {
	start, end, ok := credSpan(raw)
	if !ok {
		return raw
	}
	return raw[:start] + raw[end:]
}

// credWithheld replaces a check message that still quotes part of a typed
// url's credentials after redactUserinfo's scrub.
const credWithheld = "the check failed; its message is not shown because it quoted the url's user:password"

// redactUserinfo removes raw's credentials from text that may quote raw (a
// ping error names the url it dialled): every occurrence of the span
// credSpan covers, as typed and as url-encoded, is cut. Fail closed: if a
// piece of the credentials (two characters or more) still shows after that,
// the whole message is withheld. Text about a url with no '@' comes back
// unchanged.
func redactUserinfo(text, raw string) string {
	start, end, ok := credSpan(raw)
	if !ok {
		return text
	}
	cred := raw[start:end]
	text = strings.ReplaceAll(text, cred, "")
	if u, _, err := parseTyped(raw); err == nil && u.User != nil {
		text = strings.ReplaceAll(text, u.User.String()+"@", "")
	}
	if strings.Contains(text, "@") {
		return credWithheld
	}
	for _, piece := range strings.FieldsFunc(cred, func(r rune) bool { return strings.ContainsRune(":/?#@\\ \t", r) }) {
		if len(piece) >= 2 && strings.Contains(text, piece) {
			return credWithheld
		}
	}
	return text
}
