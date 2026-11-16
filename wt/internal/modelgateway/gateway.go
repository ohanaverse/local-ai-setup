// Package modelgateway is the wt-model server's streaming reverse proxy: it
// authenticates a caller with the shared token and forwards /v1/* to the
// host's LiteLLM proxy under the host's LiteLLM key, which the client never
// sees. A caller presents the token as `Authorization: Bearer TOKEN` or as
// `x-api-key: TOKEN`; a Bearer Authorization header takes precedence, so when
// one is present it is the only credential checked and a wrong one is refused
// even beside a valid x-api-key. Only the paths of Config.AllowPaths are
// forwarded (the server passes InferencePaths); any other is a 404.
package modelgateway

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
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
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("x-api-key")
			pr.Out.Header.Set("Authorization", "Bearer "+c.UpstreamKey)
		},
		// Flush every write: agent streams are server-sent events.
		FlushInterval: -1,
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
	mux.Handle("/v1/", authenticated(c.Token, allowlisted(c.AllowPaths, proxy)))
	return mux
}

func authenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validToken(r, token) {
			writeError(w, http.StatusUnauthorized, modelapi.GatewayErrorObject{
				Message: modelapi.MsgUnauthorized, Type: modelapi.TypeAuthentication, Code: modelapi.CodeUnauthorized,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
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
