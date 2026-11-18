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

// hasUserinfo reports whether a typed url carries a user (and maybe a
// password), scheme or not, and its host for a message that must not quote
// the rest. A url that does not parse but holds an @ before its path counts
// too, with no host.
func hasUserinfo(raw string) (host string, ok bool) {
	if u, _, err := parseTyped(raw); err == nil {
		return u.Hostname(), u.User != nil
	}
	rest := raw
	if hasScheme(raw) {
		_, rest, _ = strings.Cut(raw, "://")
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return "", strings.Contains(rest, "@")
}

// stripUserinfo removes a user:password from a url, scheme or not; a url
// typed without a scheme stays without one. A url that does not parse loses
// everything from just after "://" (or its start) up to its last "@".
func stripUserinfo(raw string) string {
	if u, schemeless, err := parseTyped(raw); err == nil {
		u.User = nil
		if schemeless {
			return strings.TrimPrefix(u.String(), "//")
		}
		return u.String()
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		if j := strings.Index(raw, "://"); j >= 0 && j < i {
			return raw[:j+3] + raw[i+1:]
		}
		return raw[i+1:]
	}
	return raw
}

// redactUserinfo removes raw's user:password from text that may quote raw
// (a ping error names the url it dialled), as typed and as url-encoded.
// Text about a url with no user comes back unchanged.
func redactUserinfo(text, raw string) string {
	if _, ok := hasUserinfo(raw); !ok {
		return text
	}
	auth := raw
	if hasScheme(raw) {
		_, auth, _ = strings.Cut(raw, "://")
	}
	if i := strings.IndexAny(auth, "/?#"); i >= 0 {
		auth = auth[:i]
	}
	if i := strings.LastIndex(auth, "@"); i >= 0 {
		text = strings.ReplaceAll(text, auth[:i+1], "")
	}
	if u, _, err := parseTyped(raw); err == nil && u.User != nil {
		text = strings.ReplaceAll(text, u.User.String()+"@", "")
	}
	return text
}
