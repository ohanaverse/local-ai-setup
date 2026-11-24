package modelgateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// The sentinels of the plan's Global Constraints: fake values that look like
// the real ones, so a leak check can grep for them.
const (
	loginSentinel = "sk-ant-oat01-wtSENTINEL"
	tokenSentinel = "wt-SENTINEL-tok"
)

// headerUpstream is a fake LiteLLM that counts calls and keeps the headers of
// the last one.
type headerUpstream struct {
	srv   *httptest.Server
	calls atomic.Int32
	mu    sync.Mutex
	hdr   http.Header
}

func newHeaderUpstream(t *testing.T) *headerUpstream {
	t.Helper()
	u := &headerUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		u.mu.Lock()
		u.hdr = r.Header.Clone()
		u.mu.Unlock()
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *headerUpstream) last() http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hdr.Clone()
}

func upstreamURL(t *testing.T, srv *httptest.Server) *url.URL {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// subConfig is a gateway Config with the sentinel token and the mode on or off.
func subConfig(t *testing.T, up *httptest.Server, on bool) Config {
	t.Helper()
	return Config{
		Upstream: upstreamURL(t, up), UpstreamKey: testKey, Token: tokenSentinel,
		AllowPaths: InferencePaths, SubscriptionPassthrough: on,
	}
}

// hdrs builds a header from name/value pairs; a repeated name adds a value.
func hdrs(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

// serveDirect drives the handler without a listener, so a test can claim any
// peer address (no test listens on a non-loopback address).
func serveDirect(h http.Handler, path, remote string, hdr http.Header, overTLS bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "http://gw.test"+path, strings.NewReader("{}"))
	req.RemoteAddr = remote
	for k, vs := range hdr {
		req.Header[k] = append([]string(nil), vs...)
	}
	if overTLS {
		req.TLS = &tls.ConnectionState{}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// wantGatewayError checks a gateway-authored OpenAI-style error.
func wantGatewayError(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, want modelapi.GatewayErrorObject) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("%s: status %d, want %d (body %s)", what, rec.Code, status, rec.Body.String())
		return
	}
	var e modelapi.GatewayError
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Errorf("%s: body %q: %v", what, rec.Body.String(), err)
		return
	}
	if e.Error != want {
		t.Errorf("%s: body %+v, want %+v", what, e.Error, want)
	}
}

var (
	errUnauthorized = modelapi.GatewayErrorObject{Message: modelapi.MsgUnauthorized, Type: modelapi.TypeAuthentication, Code: modelapi.CodeUnauthorized}
	errSubOff       = modelapi.GatewayErrorObject{Message: modelapi.MsgSubscriptionOff, Type: modelapi.TypePermission, Code: modelapi.CodeSubscriptionOff}
	errSubInsecure  = modelapi.GatewayErrorObject{Message: modelapi.MsgSubscriptionInsecure, Type: modelapi.TypePermission, Code: modelapi.CodeSubscriptionInsecure}
	errSubNoLogin   = modelapi.GatewayErrorObject{Message: modelapi.MsgSubscriptionNoLogin, Type: modelapi.TypePermission, Code: modelapi.CodeSubscriptionNoLogin}
)

const loopbackPeer = "127.0.0.1:5555"

// TestSubscriptionHeaderForwardsTheLogin pins the header branch's upstream
// headers on every path Claude Code uses: the client's claude.ai login goes
// through byte-equal in Authorization, anthropic-beta is untouched, LiteLLM is
// authenticated by x-litellm-api-key, and neither the wt token header nor
// x-api-key reaches LiteLLM. It matters because this is the one path that
// forwards a client credential, so any extra or missing header either leaks
// the wt token or breaks the subscription request.
func TestSubscriptionHeaderForwardsTheLogin(t *testing.T) {
	up := newHeaderUpstream(t)
	h := New(subConfig(t, up.srv, true))
	for _, p := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/models"} {
		rec := serveDirect(h, p, loopbackPeer, hdrs(
			TokenHeader, tokenSentinel,
			"Authorization", "Bearer "+loginSentinel,
			"anthropic-beta", "oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14",
			"x-api-key", "client-x-api-key",
		), false)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d body %s", p, rec.Code, rec.Body.String())
		}
		got := up.last()
		if v := got.Values("Authorization"); len(v) != 1 || v[0] != "Bearer "+loginSentinel {
			t.Errorf("%s: upstream Authorization %q, want the login byte-equal", p, v)
		}
		if v := got.Get("anthropic-beta"); v != "oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14" {
			t.Errorf("%s: upstream anthropic-beta %q", p, v)
		}
		if v := got.Values("x-litellm-api-key"); len(v) != 1 || v[0] != testKey {
			t.Errorf("%s: upstream x-litellm-api-key %q, want the host key", p, v)
		}
		if v := got.Values(TokenHeader); len(v) != 0 {
			t.Errorf("%s: upstream saw %s %q", p, TokenHeader, v)
		}
		if v := got.Values("x-api-key"); len(v) != 0 {
			t.Errorf("%s: upstream saw x-api-key %q", p, v)
		}
	}
	if n := up.calls.Load(); n != 3 {
		t.Errorf("upstream calls %d, want 3", n)
	}
}

// TestSubscriptionHeaderWithoutAuthorizationAddsNone pins that a header-mode
// request without Authorization reaches LiteLLM without one: the gateway
// never invents a credential (and in particular never puts the host key in
// Authorization on this branch).
func TestSubscriptionHeaderWithoutAuthorizationAddsNone(t *testing.T) {
	up := newHeaderUpstream(t)
	rec := serveDirect(New(subConfig(t, up.srv, true)), "/v1/messages", loopbackPeer, hdrs(TokenHeader, tokenSentinel), false)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if v := up.last().Values("Authorization"); len(v) != 0 {
		t.Errorf("upstream Authorization %q, want none", v)
	}
	if v := up.last().Get("x-litellm-api-key"); v != testKey {
		t.Errorf("upstream x-litellm-api-key %q, want the host key", v)
	}
}

// TestSubscriptionNonOAuthAuthorizationRefused pins steps 4 and 5 of the
// check order: on the header branch an Authorization holding the wt token is
// a 401 (the token would otherwise be forwarded to LiteLLM and Anthropic),
// and any other value that is not a `Bearer sk-ant-oat…` login (an API key, a
// random string, a second value) is a 403 subscription_no_login. None reaches
// the upstream.
func TestSubscriptionNonOAuthAuthorizationRefused(t *testing.T) {
	up := newHeaderUpstream(t)
	h := New(subConfig(t, up.srv, true))
	for _, tc := range []struct {
		name string
		auth []string
		want modelapi.GatewayErrorObject
		code int
	}{
		{"wt token as bearer", []string{"Bearer " + tokenSentinel}, errUnauthorized, 401},
		{"wt token raw", []string{tokenSentinel}, errUnauthorized, 401},
		{"login beside the wt token", []string{"Bearer " + loginSentinel, "Bearer " + tokenSentinel}, errUnauthorized, 401},
		{"api key", []string{"Bearer sk-ant-api03-wtSENTINELkey"}, errSubNoLogin, 403},
		{"random bearer", []string{"Bearer 9f8e7d6c5b4a"}, errSubNoLogin, 403},
		{"random raw", []string{"9f8e7d6c5b4a"}, errSubNoLogin, 403},
		{"login without scheme", []string{loginSentinel}, errSubNoLogin, 403},
		{"two logins", []string{"Bearer " + loginSentinel, "Bearer " + loginSentinel}, errSubNoLogin, 403},
		{"empty", []string{""}, errSubNoLogin, 403},
	} {
		hdr := hdrs(TokenHeader, tokenSentinel)
		hdr["Authorization"] = tc.auth
		wantGatewayError(t, tc.name, serveDirect(h, "/v1/messages", loopbackPeer, hdr, false), tc.code, tc.want)
	}
	if n := up.calls.Load(); n != 0 {
		t.Errorf("upstream calls %d, want 0", n)
	}
}

// TestSubscriptionWrongOrDuplicateHeaderForwardsNothing pins step 1: a
// present x-wt-model-token header must be exactly one non-empty value equal
// to the token. A wrong, empty or two-valued header is the existing 401 even
// beside a valid wt Bearer (no fallback to the token swap), and never reaches
// the upstream.
func TestSubscriptionWrongOrDuplicateHeaderForwardsNothing(t *testing.T) {
	up := newHeaderUpstream(t)
	h := New(subConfig(t, up.srv, true))
	for name, vals := range map[string][]string{
		"wrong":         {"nope"},
		"empty":         {""},
		"two valid":     {tokenSentinel, tokenSentinel},
		"valid + wrong": {tokenSentinel, "nope"},
		"bearer form":   {"Bearer " + tokenSentinel},
	} {
		for _, withBearer := range []bool{false, true} {
			hdr := http.Header{}
			hdr[http.CanonicalHeaderKey(TokenHeader)] = vals
			if withBearer {
				hdr.Set("Authorization", "Bearer "+tokenSentinel)
			}
			what := name
			if withBearer {
				what += " + wt bearer"
			}
			wantGatewayError(t, what, serveDirect(h, "/v1/messages", loopbackPeer, hdr, false), 401, errUnauthorized)
		}
	}
	if n := up.calls.Load(); n != 0 {
		t.Errorf("upstream calls %d, want 0", n)
	}
}

// TestSubscriptionModeOffForwardsNothing pins step 2: with the mode off (the
// default), a valid header plus a login is a 403 subscription_off and never
// forwarded, from any peer; a wrong header is still the 401, so an
// unauthenticated caller learns nothing about the mode.
func TestSubscriptionModeOffForwardsNothing(t *testing.T) {
	up := newHeaderUpstream(t)
	h := New(subConfig(t, up.srv, false))
	good := hdrs(TokenHeader, tokenSentinel, "Authorization", "Bearer "+loginSentinel)
	wantGatewayError(t, "loopback", serveDirect(h, "/v1/messages", loopbackPeer, good, false), 403, errSubOff)
	wantGatewayError(t, "remote", serveDirect(h, "/v1/messages", "192.0.2.7:1", good, false), 403, errSubOff)
	wantGatewayError(t, "tls", serveDirect(h, "/v1/messages", "192.0.2.7:1", good, true), 403, errSubOff)
	wrong := hdrs(TokenHeader, "nope", "Authorization", "Bearer "+loginSentinel)
	wantGatewayError(t, "wrong header", serveDirect(h, "/v1/messages", loopbackPeer, wrong, false), 401, errUnauthorized)
	if n := up.calls.Load(); n != 0 {
		t.Errorf("upstream calls %d, want 0", n)
	}
}

// TestSubscriptionRefusedFromNonLoopbackOverHTTP pins the guard (decision 3):
// header mode from a non-loopback peer over plain http, from an unparseable
// peer address, or with any forwarding header that says the client is remote
// or came over plain http, is a 403 subscription_insecure that never reaches
// the upstream. Forwarding headers are only ever read to refuse: a spoofed
// X-Forwarded-For: 127.0.0.1 or X-Forwarded-Proto: https from a remote peer
// opens nothing. It matters because the login would otherwise cross a
// network in the clear.
func TestSubscriptionRefusedFromNonLoopbackOverHTTP(t *testing.T) {
	up := newHeaderUpstream(t)
	h := New(subConfig(t, up.srv, true))
	base := []string{TokenHeader, tokenSentinel, "Authorization", "Bearer " + loginSentinel}
	for _, tc := range []struct {
		name   string
		remote string
		tls    bool
		extra  []string
	}{
		{"v4 public", "192.0.2.7:1", false, nil},
		{"v4 private", "10.0.0.2:1", false, nil},
		{"v6", "[2001:db8::1]:1", false, nil},
		{"garbage", "garbage", false, nil},
		{"empty", "", false, nil},
		{"no port", "127.0.0.1", false, nil},
		{"unspecified", "0.0.0.0:1", false, nil},
		{"remote spoofs XFF loopback", "192.0.2.7:1", false, []string{"X-Forwarded-For", "127.0.0.1"}},
		{"remote spoofs XFP https", "192.0.2.7:1", false, []string{"X-Forwarded-Proto", "https"}},
		{"remote spoofs Forwarded", "192.0.2.7:1", false, []string{"Forwarded", "proto=https;for=127.0.0.1"}},
		{"loopback, XFF remote", loopbackPeer, false, []string{"X-Forwarded-For", "192.0.2.7"}},
		{"loopback, X-Real-IP remote", loopbackPeer, false, []string{"X-Real-IP", "192.0.2.7"}},
		{"loopback, Forwarded for remote", loopbackPeer, false, []string{"Forwarded", "for=192.0.2.7"}},
		{"loopback, Forwarded for unknown", loopbackPeer, false, []string{"Forwarded", "for=unknown"}},
		{"loopback, XFF garbage", loopbackPeer, false, []string{"X-Forwarded-For", "_hidden"}},
		{"loopback, XFF appended list", loopbackPeer, false, []string{"X-Forwarded-For", "127.0.0.1, 192.0.2.7"}},
		{"loopback, XFF second value", loopbackPeer, false, []string{"X-Forwarded-For", "127.0.0.1", "X-Forwarded-For", "192.0.2.7"}},
		{"loopback, XFP http beside Forwarded https", loopbackPeer, false, []string{
			"X-Forwarded-For", "192.0.2.7", "X-Forwarded-Proto", "http", "Forwarded", "proto=https"}},
		{"loopback, Forwarded mixed for, XFP http", loopbackPeer, false, []string{
			"Forwarded", "for=127.0.0.1, for=192.0.2.7;proto=https", "X-Forwarded-Proto", "http"}},
		{"loopback, XFP http alone", loopbackPeer, false, []string{"X-Forwarded-Proto", "http"}},
		{"loopback, XFP list with http", loopbackPeer, false, []string{"X-Forwarded-For", "192.0.2.7", "X-Forwarded-Proto", "https, http"}},
		{"loopback, XFP empty", loopbackPeer, false, []string{"X-Forwarded-For", "192.0.2.7", "X-Forwarded-Proto", ""}},
		{"loopback, Forwarded proto http", loopbackPeer, false, []string{"Forwarded", "proto=http"}},
		{"tls, XFP http", "192.0.2.7:1", true, []string{"X-Forwarded-Proto", "http"}},
		{"tls, Forwarded proto http", "192.0.2.7:1", true, []string{"Forwarded", "for=192.0.2.8;proto=http"}},
	} {
		hdr := hdrs(append(append([]string(nil), base...), tc.extra...)...)
		wantGatewayError(t, tc.name, serveDirect(h, "/v1/messages", tc.remote, hdr, tc.tls), 403, errSubInsecure)
	}
	if n := up.calls.Load(); n != 0 {
		t.Errorf("upstream calls %d, want 0", n)
	}
	// A loopback peer behind a same-host terminator that says https on every
	// indicator is accepted, remote client or not.
	hdr := hdrs(append(append([]string(nil), base...), "X-Forwarded-For", "192.0.2.7", "X-Forwarded-Proto", "https")...)
	if rec := serveDirect(h, "/v1/messages", loopbackPeer, hdr, false); rec.Code != 200 {
		t.Errorf("loopback, XFF remote, XFP https: status %d body %s, want 200", rec.Code, rec.Body.String())
	}
	if n := up.calls.Load(); n != 1 {
		t.Errorf("upstream calls %d, want 1", n)
	}
}

// TestSubscriptionAcceptedFromLoopbackOrTLS pins the allowed peers: every
// loopback spelling (127.0.0.0/8, ::1, a v4-mapped 127.x) and a direct TLS
// peer, whose own address is the real one, so a forwarded client address
// beside TLS is not read; a loopback peer whose forwarded list is all
// loopback is accepted too.
func TestSubscriptionAcceptedFromLoopbackOrTLS(t *testing.T) {
	up := newHeaderUpstream(t)
	h := New(subConfig(t, up.srv, true))
	base := []string{TokenHeader, tokenSentinel, "Authorization", "Bearer " + loginSentinel}
	for _, tc := range []struct {
		name   string
		remote string
		tls    bool
		extra  []string
	}{
		{"127.0.0.1", "127.0.0.1:1", false, nil},
		{"127.9.9.9", "127.9.9.9:1", false, nil},
		{"::1", "[::1]:1", false, nil},
		{"v4-mapped", "[::ffff:127.0.0.1]:1", false, nil},
		{"tls", "192.0.2.7:1", true, nil},
		{"tls, XFF remote", "192.0.2.7:1", true, []string{"X-Forwarded-For", "192.0.2.8"}},
		{"tls, XFP https", "192.0.2.7:1", true, []string{"X-Forwarded-Proto", "https"}},
		{"loopback, XFF all loopback", loopbackPeer, false, []string{"X-Forwarded-For", "127.0.0.1, 127.0.0.2"}},
		{"loopback, Forwarded v6 loopback", loopbackPeer, false, []string{"Forwarded", `for="[::1]:4711"`}},
	} {
		before := up.calls.Load()
		hdr := hdrs(append(append([]string(nil), base...), tc.extra...)...)
		rec := serveDirect(h, "/v1/messages", tc.remote, hdr, tc.tls)
		if rec.Code != 200 {
			t.Errorf("%s: status %d body %s, want 200", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if up.calls.Load() != before+1 {
			t.Errorf("%s: upstream not called", tc.name)
		}
		if got := up.last().Get("Authorization"); got != "Bearer "+loginSentinel {
			t.Errorf("%s: upstream Authorization %q", tc.name, got)
		}
	}
}

// TestLegacyRequestFromNonLoopbackUnaffected pins that the guard is the
// header branch's only: a request without the header from a remote peer is
// served with the token swap whether the mode is on or off, so every remote
// pi, codex and token-mode claude session keeps working.
func TestLegacyRequestFromNonLoopbackUnaffected(t *testing.T) {
	for _, on := range []bool{false, true} {
		up := newHeaderUpstream(t)
		rec := serveDirect(New(subConfig(t, up.srv, on)), "/v1/messages", "192.0.2.7:1",
			hdrs("Authorization", "Bearer "+tokenSentinel), false)
		if rec.Code != 200 {
			t.Errorf("mode %v: status %d body %s", on, rec.Code, rec.Body.String())
			continue
		}
		if got := up.last().Values("Authorization"); len(got) != 1 || got[0] != "Bearer "+testKey {
			t.Errorf("mode %v: upstream Authorization %q, want the host key", on, got)
		}
	}
}

// TestLegacyUpstreamHeadersUnchanged pins that a request without the header
// reaches LiteLLM with today's header map whether the mode is on or off:
// Authorization swapped for the host key, x-api-key dropped, a client
// x-litellm-api-key dropped too (#692: LiteLLM prefers it to Authorization),
// the forwarding headers removed by ReverseProxy, everything else untouched.
func TestLegacyUpstreamHeadersUnchanged(t *testing.T) {
	send := hdrs(
		"Authorization", "Bearer "+tokenSentinel,
		"x-api-key", tokenSentinel,
		"anthropic-beta", "oauth-2025-04-20",
		"anthropic-version", "2023-06-01",
		"content-type", "application/json",
		"x-custom", "kept",
		"x-litellm-api-key", "client",
		"api-key", "client",
		"X-Forwarded-For", "192.0.2.7",
	)
	want := hdrs(
		"Authorization", "Bearer "+testKey,
		"Anthropic-Beta", "oauth-2025-04-20",
		"Anthropic-Version", "2023-06-01",
		"Content-Type", "application/json",
		"X-Custom", "kept",
		"Accept-Encoding", "gzip", // added by http.Transport, as today
		"Content-Length", "2", // the "{}" body
	)
	var maps []http.Header
	for _, on := range []bool{false, true} {
		up := newHeaderUpstream(t)
		cfg := Config{Upstream: upstreamURL(t, up.srv), UpstreamKey: testKey, Token: tokenSentinel, AllowPaths: InferencePaths, SubscriptionPassthrough: on}
		rec := serveDirect(New(cfg), "/v1/messages", loopbackPeer, send, false)
		if rec.Code != 200 {
			t.Fatalf("mode %v: status %d", on, rec.Code)
		}
		maps = append(maps, up.last())
	}
	if !reflect.DeepEqual(maps[0], maps[1]) {
		t.Errorf("mode on changed the legacy upstream headers:\noff: %v\non:  %v", maps[0], maps[1])
	}
	if !reflect.DeepEqual(maps[0], want) {
		t.Errorf("legacy upstream headers %v, want %v", maps[0], want)
	}
}

// TestSubscriptionClientLiteLLMKeyReplaced pins that on the header branch a
// client-sent x-litellm-api-key never reaches LiteLLM: only the host key
// does, so a header-mode caller cannot pick its own LiteLLM identity.
func TestSubscriptionClientLiteLLMKeyReplaced(t *testing.T) {
	up := newHeaderUpstream(t)
	rec := serveDirect(New(subConfig(t, up.srv, true)), "/v1/messages", loopbackPeer, hdrs(
		TokenHeader, tokenSentinel, "Authorization", "Bearer "+loginSentinel,
		"x-litellm-api-key", "evil", "x-litellm-api-key", "evil2",
	), false)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := up.last().Values("x-litellm-api-key"); len(got) != 1 || got[0] != testKey {
		t.Errorf("upstream x-litellm-api-key %q, want only the host key", got)
	}
}

// TestSubscriptionPathCheckStillApplies pins that the header branch is still
// held to the inference allowlist: a valid header on /v1/files is the
// gateway's 404 and never reaches LiteLLM.
func TestSubscriptionPathCheckStillApplies(t *testing.T) {
	up := newHeaderUpstream(t)
	rec := serveDirect(New(subConfig(t, up.srv, true)), "/v1/files", loopbackPeer,
		hdrs(TokenHeader, tokenSentinel, "Authorization", "Bearer "+loginSentinel), false)
	wantGatewayError(t, "/v1/files", rec, 404, modelapi.GatewayErrorObject{
		Message: modelapi.MsgPathNotServed, Type: modelapi.TypeInvalidRequest, Code: modelapi.CodeNotFound,
	})
	if n := up.calls.Load(); n != 0 {
		t.Errorf("upstream calls %d, want 0", n)
	}
}

// TestSubscriptionSecretsNeverLogged pins that neither the client's login nor
// the wt token appears in the gateway's error log, in a response body or in a
// response header, across a success, every refusal, an unreachable upstream,
// an upstream that aborts mid-body and a client that cancels mid-stream. It
// matters because the gateway carries a user's claude.ai login, and a log
// line is the usual way a secret outlives a request.
func TestSubscriptionSecretsNeverLogged(t *testing.T) {
	var (
		buf   bytes.Buffer
		bufMu sync.Mutex
	)
	errLog := log.New(writerFunc(func(p []byte) (int, error) {
		bufMu.Lock()
		defer bufMu.Unlock()
		return buf.Write(p)
	}), "", 0)
	streamStarted := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/completions": // abort mid-body
			w.Header().Set("Content-Length", "1000")
			io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case "/v1/responses": // stream until the client goes
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: one\n\n")
			w.(http.Flusher).Flush()
			streamStarted <- struct{}{}
			<-r.Context().Done()
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer up.Close()
	cfg := subConfig(t, up, true)
	cfg.ErrorLog = errLog
	h := New(cfg)
	offCfg := cfg
	offCfg.SubscriptionPassthrough = false
	login := "Bearer " + loginSentinel
	good := hdrs(TokenHeader, tokenSentinel, "Authorization", login)

	var bodies []string
	var headers []http.Header
	record := func(what string, rec *httptest.ResponseRecorder, wantCode int) {
		t.Helper()
		if rec.Code != wantCode {
			t.Errorf("%s: status %d, want %d", what, rec.Code, wantCode)
		}
		bodies = append(bodies, rec.Body.String())
		headers = append(headers, rec.Header().Clone())
	}
	record("success", serveDirect(h, "/v1/messages", loopbackPeer, good, false), 200)
	record("401", serveDirect(h, "/v1/messages", loopbackPeer, hdrs(TokenHeader, "nope", "Authorization", login), false), 401)
	record("401 wt token in Authorization", serveDirect(h, "/v1/messages", loopbackPeer, hdrs(TokenHeader, tokenSentinel, "Authorization", "Bearer "+tokenSentinel), false), 401)
	record("403 off", serveDirect(New(offCfg), "/v1/messages", loopbackPeer, good, false), 403)
	record("403 insecure", serveDirect(h, "/v1/messages", "192.0.2.7:1", good, false), 403)
	record("403 no login", serveDirect(h, "/v1/messages", loopbackPeer, hdrs(TokenHeader, tokenSentinel, "Authorization", "Bearer sk-ant-api03-x"), false), 403)
	record("abort mid-body", serveDirect(h, "/v1/completions", loopbackPeer, good, false), 200)

	// A client that cancels mid-stream, through a real loopback listener.
	gw := httptest.NewServer(h)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/responses", strings.NewReader("{}"))
	req.Header = good.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	headers = append(headers, resp.Header.Clone())
	if resp.StatusCode != 200 {
		t.Errorf("stream: status %d, want 200", resp.StatusCode)
	} else {
		select {
		case <-streamStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("stream never reached the upstream")
		}
		first := make([]byte, 4)
		if _, err := io.ReadFull(resp.Body, first); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	resp.Body.Close()

	// The upstream down: a 503 the gateway writes.
	downURL := upstreamURL(t, up)
	gw.Close()
	up.Close()
	downCfg := cfg
	downCfg.Upstream = downURL
	record("503", serveDirect(New(downCfg), "/v1/messages", loopbackPeer, good, false), 503)

	bufMu.Lock()
	logged := buf.String()
	bufMu.Unlock()
	for _, s := range []string{loginSentinel, tokenSentinel, "SENTINEL"} {
		if strings.Contains(logged, s) {
			t.Errorf("error log holds %q: %s", s, logged)
		}
		for i, b := range bodies {
			if strings.Contains(b, s) {
				t.Errorf("response body %d holds %q: %s", i, s, b)
			}
		}
		for i, hd := range headers {
			for k, vs := range hd {
				for _, v := range vs {
					if strings.Contains(k, s) || strings.Contains(v, s) {
						t.Errorf("response header %d holds %q: %s: %s", i, s, k, v)
					}
				}
			}
		}
	}
	if logged == "" {
		t.Errorf("error log is empty: the abort case should have logged through Config.ErrorLog")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestSubscriptionStreams pins that the header branch streams like the token
// swap: a flushed chunk reaches the client while the upstream still holds the
// response open (a clone of TestGatewayStreamsWithoutBuffering in header mode).
func TestSubscriptionStreams(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "data: two\n\n")
	}))
	defer up.Close()
	defer close(release)
	gw := httptest.NewServer(New(subConfig(t, up, true)))
	t.Cleanup(gw.Close) // after the deferred release, as newGateway does
	resp := do(t, "POST", gw.URL+"/v1/messages", map[string]string{TokenHeader: tokenSentinel, "Authorization": "Bearer " + loginSentinel})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	buf := make([]byte, len("data: one\n\n"))
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, buf); done <- err }()
	select {
	case err := <-done:
		if err != nil || string(buf) != "data: one\n\n" {
			t.Fatalf("first chunk %q err %v", buf, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk not delivered while upstream still open")
	}
}

// litellmAuthHeaders are the headers LiteLLM 1.109.1 takes a key from
// (user_api_key_auth.get_api_key), highest precedence first: x-litellm-api-key
// supersedes Authorization, which is the bypass #692 closes; the rest rank
// below Authorization today but are the same kind of credential.
var litellmAuthHeaders = []string{"x-litellm-api-key", "api-key", "x-api-key", "x-goog-api-key", "ocp-apim-subscription-key"}

// TestTokenSwapDropsEveryClientLiteLLMAuthHeader pins #692: a caller holding
// the wt token cannot authenticate to LiteLLM with a key of its own choosing.
// Whatever LiteLLM auth header a client sends beside its token, upstream sees
// only the host key, as the bearer, and none of those headers.
func TestTokenSwapDropsEveryClientLiteLLMAuthHeader(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, via := range []string{"Authorization", "x-api-key"} {
			up := newHeaderUpstream(t)
			kv := []string{}
			if via == "Authorization" {
				kv = append(kv, "Authorization", "Bearer "+tokenSentinel)
			} else {
				kv = append(kv, "x-api-key", tokenSentinel)
			}
			for _, h := range litellmAuthHeaders {
				if h == "x-api-key" && via == "x-api-key" {
					continue // that one is the token itself
				}
				kv = append(kv, h, "evil-"+h, h, "evil2-"+h)
			}
			cfg := subConfig(t, up.srv, mode)
			rec := serveDirect(New(cfg), "/v1/messages", loopbackPeer, hdrs(kv...), false)
			if rec.Code != 200 {
				t.Fatalf("subscription=%v via %s: status %d", mode, via, rec.Code)
			}
			got := up.last()
			if v := got.Values("Authorization"); len(v) != 1 || v[0] != "Bearer "+testKey {
				t.Errorf("subscription=%v via %s: upstream Authorization %q, want only the host key", mode, via, v)
			}
			for _, h := range litellmAuthHeaders {
				if v := got.Values(h); len(v) != 0 {
					t.Errorf("subscription=%v via %s: upstream still carries %s %q", mode, via, h, v)
				}
			}
		}
	}
}

// TestHeaderModeDropsTheOtherLiteLLMAuthHeaders pins the same on the header
// branch: only x-litellm-api-key, set to the host key, authenticates there.
func TestHeaderModeDropsTheOtherLiteLLMAuthHeaders(t *testing.T) {
	up := newHeaderUpstream(t)
	kv := []string{TokenHeader, tokenSentinel, "Authorization", "Bearer " + loginSentinel}
	for _, h := range litellmAuthHeaders {
		kv = append(kv, h, "evil-"+h)
	}
	rec := serveDirect(New(subConfig(t, up.srv, true)), "/v1/messages", loopbackPeer, hdrs(kv...), false)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	got := up.last()
	if v := got.Values("x-litellm-api-key"); len(v) != 1 || v[0] != testKey {
		t.Errorf("upstream x-litellm-api-key %q, want only the host key", v)
	}
	for _, h := range litellmAuthHeaders[1:] {
		if v := got.Values(h); len(v) != 0 {
			t.Errorf("upstream still carries %s %q", h, v)
		}
	}
}
