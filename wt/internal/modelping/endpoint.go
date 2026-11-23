// Package modelping is one ping of the model server and its classification:
// the endpoint it talks to, the typed errors a ping ends in, the address an
// error text shows, and Clean — the one printable line every string a remote
// server can influence is flattened to (style guide M11). It sits below
// modelclient, which aliases these names so existing callers keep their
// types, and imports only stdlib, modelapi and urlrule, so the model side can
// link it without reaching for the session client (TestModeLeavesStayOffTheClient).
package modelping

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// Endpoint is where the model server is and the token that opens it.
// CAFile, when set, is a PEM file of extra trusted roots added to the system
// pool for an https url (decision 14); ServerName, when set, is the name the
// server's certificate is verified as (a host command dialling loopback sets
// it to the certificate's own name, tlsfile.DialName).
type Endpoint struct {
	URL, Token string
	CAFile     string
	ServerName string
}

// Origin is the URL without trailing slashes.
func (e Endpoint) Origin() string { return strings.TrimRight(e.URL, "/") }

// Gateway returns the origin and token a streaming gateway client needs.
func (e Endpoint) Gateway() (origin, token string) { return e.Origin(), e.Token }

// UnparsedAddr stands in for host:port when the url has no host url.Parse
// can find: the raw origin is never echoed in its place.
const UnparsedAddr = "(the configured url, which names no host)"

// UnbuildableRequest is the reason given when net/http cannot build a
// request from the url: its own error quotes the whole url.
const UnbuildableRequest = "the model server url does not parse as a url with a host"

// Addr is host:port of rawURL, as the error texts and the server's --listen
// flag name it. It never echoes the raw url: one holding an '@' is
// urlrule.Hidden whole (no parsing, HasUserinfo), and one url.Parse finds no
// host in is the fixed UnparsedAddr.
func Addr(rawURL string) string {
	if urlrule.HasUserinfo(rawURL) {
		return urlrule.Hidden
	}
	if u, err := url.Parse(Endpoint{URL: rawURL}.Origin()); err == nil && u.Host != "" {
		return u.Host
	}
	return UnparsedAddr
}

// Clean is s as one printable line: every control and bidi rune is dropped (a
// tab, newline or carriage return becomes a space) and whitespace runs
// collapse to one space. The server is a remote party (style guide M11), so
// every string it sends passes through here before wt shows it. It mirrors
// cleanup.Flatten, which this package cannot import (cleanup pulls in git);
// a test in modelserver keeps the two equal.
func Clean(s string) string {
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
