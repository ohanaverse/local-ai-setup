// Package modelgateway is the wt-model server's streaming reverse proxy: it
// authenticates a caller with the shared token and forwards /v1/* to the
// host's LiteLLM proxy under the host's LiteLLM key, which the client never
// sees. A caller presents the token as `Authorization: Bearer TOKEN` or as
// `x-api-key: TOKEN`; a Bearer Authorization header takes precedence, so when
// one is present it is the only credential checked and a wrong one is refused
// even beside a valid x-api-key. Every /v1/* path is forwarded, non-inference
// LiteLLM routes included.
package modelgateway

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// Config configures a gateway.
type Config struct {
	Upstream    *url.URL // the LiteLLM proxy base URL
	UpstreamKey string   // sent upstream as a bearer; never shown to clients
	Token       string   // what a caller must present
}

// New returns a handler serving /v1/* only.
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
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(modelapi.GatewayError{Error: modelapi.GatewayErrorObject{
				Message: modelapi.MsgProxyUnavailable, Type: modelapi.TypeAPI, Code: modelapi.CodeProxyUnavailable,
			}})
		},
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", authenticated(c.Token, proxy))
	return mux
}

func authenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validToken(r, token) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(modelapi.GatewayError{Error: modelapi.GatewayErrorObject{
				Message: modelapi.MsgUnauthorized, Type: modelapi.TypeAuthentication, Code: modelapi.CodeUnauthorized,
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validToken accepts the token as a bearer or as x-api-key, in constant time.
func validToken(r *http.Request, token string) bool {
	got := r.Header.Get("x-api-key")
	if rest, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = rest
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
