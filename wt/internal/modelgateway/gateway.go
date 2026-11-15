// Package modelgateway is the wt-model server's streaming reverse proxy: it
// authenticates a caller with the shared token and forwards /v1/* to the
// host's LiteLLM proxy under the host's LiteLLM key, which the client never
// sees.
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
			_ = json.NewEncoder(w).Encode(modelapi.APIError{
				Error: "model proxy is restarting", Code: "proxy_unavailable",
			})
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
			_ = json.NewEncoder(w).Encode(modelapi.APIError{Error: "unauthorized", Code: "unauthorized"})
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
