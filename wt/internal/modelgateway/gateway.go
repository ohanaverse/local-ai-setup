// Package modelgateway is the wt-model server's streaming reverse proxy: it
// authenticates a caller with the shared token and forwards /v1/* to the
// host's LiteLLM proxy under the host's LiteLLM key, which the client never
// sees. A caller presents the token as `Authorization: Bearer TOKEN` or as
// `x-api-key: TOKEN`; a Bearer Authorization header takes precedence, so when
// one is present it is the only credential checked and a wrong one is refused
// even beside a valid x-api-key. Only the paths of Config.AllowPaths are
// forwarded (the server passes InferencePaths); any other is a 404.
//
// A request that carries the x-wt-model-token header (TokenHeader) takes a
// second branch instead, the header mode (#597): the header holds the token,
// and the client's own claude.ai login in Authorization is forwarded to
// LiteLLM, which is authenticated by x-litellm-api-key. It needs
// Config.SubscriptionPassthrough and a trusted peer (peerTrusted), and
// forwards only a `Bearer sk-ant-oat…` Authorization. A request without the
// header is the token swap. On both branches every LiteLLM credential header
// the client sent (upstreamAuthHeaders) is dropped before the gateway sets
// its own. Nothing in this package logs a request, a header or a body.
package modelgateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// Config configures a gateway.
type Config struct {
	Upstream    *url.URL // the LiteLLM proxy base URL
	UpstreamKey string   // sent upstream as a bearer; never shown to clients
	Token       string   // what a caller must present
	// AllowPaths is the paths served: an entry ending in "/" serves every
	// path below it, any other entry exactly itself. Nil or empty serves
	// nothing, so a caller that forgets it shuts the gateway instead of
	// opening every LiteLLM route; the server passes InferencePaths.
	AllowPaths []string
	// SubscriptionPassthrough turns on the header mode: a request carrying
	// TokenHeader forwards the client's claude.ai login. Off (the default),
	// such a request is a 403 subscription_off.
	SubscriptionPassthrough bool
	// SubscriptionSwitch, when set, is read on every request in place of
	// SubscriptionPassthrough: a server that turns the mode off while it runs
	// (a hazard seen after start, #697) flips it, and the next header-mode
	// request is a 403 subscription_off. The wt token alone (the token swap)
	// never depends on it.
	SubscriptionSwitch *atomic.Bool
	// ErrorLog is the ReverseProxy's error logger (a test seam); nil is the
	// standard logger, as before. The proxy logs only transport errors.
	ErrorLog *log.Logger
}

// subscriptionOn is whether the header mode is on now.
func (c Config) subscriptionOn() bool {
	if c.SubscriptionSwitch != nil {
		return c.SubscriptionSwitch.Load()
	}
	return c.SubscriptionPassthrough
}

// TokenHeader is the header-mode credential: the wt token, sent beside the
// client's own Authorization (Go canonical form X-Wt-Model-Token).
const TokenHeader = "x-wt-model-token"

// oauthPrefix is the only Authorization the header mode forwards: a
// claude.ai OAuth access token as a bearer.
const oauthPrefix = "Bearer sk-ant-oat"

// authKind is how authenticated let a request through; it travels to
// Rewrite in the request context, so Rewrite never re-reads the header.
type authKind int

const (
	authTokenSwap authKind = iota // no header: today's token swap
	authHeader                    // x-wt-model-token: forward the login
)

// authKindKey is the unexported context key of authKind.
type authKindKey struct{}

func kindOf(ctx context.Context) authKind {
	k, _ := ctx.Value(authKindKey{}).(authKind)
	return k
}

// upstreamAuthHeaders are the headers LiteLLM takes a key from
// (user_api_key_auth.get_api_key in 1.109.1), highest precedence first:
// x-litellm-api-key supersedes Authorization, so a client that kept it could
// authenticate with a key of its own choosing beside the wt token (#692); the
// others rank below Authorization today, but are the same kind of credential
// and would win if LiteLLM changed its order. Both branches drop all of them
// and set their own. A host that names its own key header with
// general_settings.litellm_key_header_name (wt never writes it) has one more
// the gateway does not know.
var upstreamAuthHeaders = []string{
	"x-litellm-api-key", "api-key", "x-api-key", "x-goog-api-key", "ocp-apim-subscription-key",
}

// dropUpstreamAuth removes every client-sent LiteLLM credential from h.
func dropUpstreamAuth(h http.Header) {
	for _, name := range upstreamAuthHeaders {
		h.Del(name)
	}
}

// InferencePaths is what an agent needs from LiteLLM: the Anthropic and
// OpenAI inference endpoints and the model list, plus the paths below
// /v1/responses/ and /v1/models/ (one response, one model). LiteLLM's other
// /v1 routes (files, batches, model info) are not served.
var InferencePaths = []string{
	"/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/completions",
	"/v1/responses", "/v1/embeddings", "/v1/models",
	"/v1/responses/", "/v1/models/",
}

// New returns a handler serving the allowed /v1/* paths only. A caller is
// authenticated before the path is checked, so a wrong token is a 401 on any
// path.
func New(c Config) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(c.Upstream)
			if kindOf(pr.In.Context()) == authHeader {
				// authenticated checked Authorization: absent, or exactly one
				// `Bearer sk-ant-oat…` value, forwarded as sent.
				pr.Out.Header.Del(TokenHeader)
				dropUpstreamAuth(pr.Out.Header)
				pr.Out.Header.Set("x-litellm-api-key", c.UpstreamKey)
				return
			}
			pr.Out.Header.Del("Authorization")
			dropUpstreamAuth(pr.Out.Header)
			pr.Out.Header.Set("Authorization", "Bearer "+c.UpstreamKey)
		},
		// Flush every write: agent streams are server-sent events.
		FlushInterval: -1,
		ErrorLog:      c.ErrorLog,
		// A dial timeout only: no response-header, idle or body limits,
		// because agent streams run for hours.
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the client went away; nobody to answer
			}
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, modelapi.GatewayErrorObject{
				Message: modelapi.MsgProxyUnavailable, Type: modelapi.TypeAPI, Code: modelapi.CodeProxyUnavailable,
			})
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", authenticated(c, allowlisted(c.AllowPaths, proxy)))
	return mux
}

// authenticated runs the check order of the plan's decision 4. Without
// TokenHeader it is today's validToken and nothing else. With it: the header
// is exactly one value equal to the token (401), the mode is on (403
// subscription_off), the peer is trusted (403 subscription_insecure),
// Authorization is not the wt token (401) and, when present, is one
// `Bearer sk-ant-oat…` value (403 subscription_no_login); then the request
// goes on marked authHeader.
func authenticated(c Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr, header := r.Header[http.CanonicalHeaderKey(TokenHeader)]
		if !header {
			if !validToken(r, c.Token) {
				writeUnauthorized(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if len(hdr) != 1 || !tokenEqual(hdr[0], c.Token) {
			writeUnauthorized(w)
			return
		}
		if !c.subscriptionOn() {
			writeForbidden(w, modelapi.MsgSubscriptionOff, modelapi.CodeSubscriptionOff)
			return
		}
		if !peerTrusted(r) {
			writeForbidden(w, modelapi.MsgSubscriptionInsecure, modelapi.CodeSubscriptionInsecure)
			return
		}
		auth, hasAuth := r.Header["Authorization"]
		for _, v := range auth {
			bearer, _ := strings.CutPrefix(v, "Bearer ")
			if tokenEqual(v, c.Token) || tokenEqual(bearer, c.Token) {
				writeUnauthorized(w)
				return
			}
		}
		if hasAuth && (len(auth) != 1 || !strings.HasPrefix(auth[0], oauthPrefix)) {
			writeForbidden(w, modelapi.MsgSubscriptionNoLogin, modelapi.CodeSubscriptionNoLogin)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKindKey{}, authHeader)))
	})
}

func writeUnauthorized(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, modelapi.GatewayErrorObject{
		Message: modelapi.MsgUnauthorized, Type: modelapi.TypeAuthentication, Code: modelapi.CodeUnauthorized,
	})
}

func writeForbidden(w http.ResponseWriter, msg, code string) {
	writeError(w, http.StatusForbidden, modelapi.GatewayErrorObject{
		Message: msg, Type: modelapi.TypePermission, Code: code,
	})
}

// peerTrusted is the header mode's guard (decision 3). Forwarding headers
// are only ever read to refuse: every X-Forwarded-Proto value and every
// Forwarded proto= element (each element of each value) must be "https".
// Then a direct TLS connection is trusted as the real peer (its client
// address headers are not read). Otherwise RemoteAddr must parse to a
// loopback address (127.0.0.0/8, ::1, ::ffff:127.x; unparseable or empty is
// false), and when any element of any X-Forwarded-For or X-Real-IP value or
// any Forwarded for= element names a non-loopback (or unparseable) client,
// the request is trusted only when at least one proto indicator is present
// (all of them already said https).
func peerTrusted(r *http.Request) bool {
	protos, clients := forwardingElements(r.Header)
	for _, p := range protos {
		if p != "https" {
			return false
		}
	}
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !peer.Unmap().IsLoopback() {
		return false
	}
	for _, cl := range clients {
		if !clientLoopback(cl) {
			return len(protos) > 0
		}
	}
	return true
}

// forwardingElements splits every forwarding header into its elements: the
// proto indicators (X-Forwarded-Proto values, Forwarded proto=) and the
// client addresses (X-Forwarded-For and X-Real-IP values, Forwarded for=),
// each trimmed and unquoted. The split is deliberately naive: a malformed
// value yields more or odder elements, which can only refuse.
func forwardingElements(h http.Header) (protos, clients []string) {
	list := func(name string) []string {
		var out []string
		for _, v := range h.Values(name) {
			for _, e := range strings.Split(v, ",") {
				out = append(out, unquote(strings.TrimSpace(e)))
			}
		}
		return out
	}
	protos = list("X-Forwarded-Proto")
	clients = append(list("X-Forwarded-For"), list("X-Real-Ip")...)
	for _, v := range h.Values("Forwarded") {
		for _, elem := range strings.Split(v, ",") {
			for _, pair := range strings.Split(elem, ";") {
				k, val, ok := strings.Cut(pair, "=")
				if !ok {
					continue
				}
				val = unquote(strings.TrimSpace(val))
				switch strings.ToLower(strings.TrimSpace(k)) {
				case "proto":
					protos = append(protos, val)
				case "for":
					clients = append(clients, val)
				}
			}
		}
	}
	return protos, clients
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// clientLoopback reports whether a forwarded client element names a
// loopback address: a bare address, an address with a port, or a bracketed
// IPv6 address. Anything else (unknown, an obfuscated _name) is not.
func clientLoopback(s string) bool {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().IsLoopback()
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().IsLoopback()
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return a.Unmap().IsLoopback()
		}
	}
	return false
}

// allowlisted serves a request only when pathAllowed lets its path through;
// any other is a 404 that never reaches next.
func allowlisted(allow []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !pathAllowed(allow, r.URL) {
			writeError(w, http.StatusNotFound, modelapi.GatewayErrorObject{
				Message: modelapi.MsgPathNotServed, Type: modelapi.TypeInvalidRequest, Code: modelapi.CodeNotFound,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// pathAllowed matches u's path, never its query, against allow. It refuses a
// path that is not already clean (dot segments, doubled or trailing slashes)
// and one sent in a non-canonical encoding (RawPath set, as for %2e%2e or an
// escaped slash): the upstream may resolve or decode either into a path the
// list does not hold, so only the spelling the list names is forwarded.
func pathAllowed(allow []string, u *url.URL) bool {
	p := u.Path
	if u.RawPath != "" || path.Clean(p) != p {
		return false
	}
	for _, a := range allow {
		if p == a || (strings.HasSuffix(a, "/") && len(p) > len(a) && strings.HasPrefix(p, a)) {
			return true
		}
	}
	return false
}

// writeError writes an OpenAI-style error the gateway itself authors.
func writeError(w http.ResponseWriter, status int, e modelapi.GatewayErrorObject) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(modelapi.GatewayError{Error: e})
}

// validToken accepts the token as a bearer or as x-api-key, in constant time.
func validToken(r *http.Request, token string) bool {
	got := r.Header.Get("x-api-key")
	if rest, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = rest
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// tokenEqual compares a header-mode value with the token in constant time;
// an empty token matches nothing.
func tokenEqual(got, token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
