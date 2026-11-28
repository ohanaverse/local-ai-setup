package modelgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const (
	testToken = "client-token"
	testKey   = "sk-upstream"
)

func newGateway(t *testing.T, up *httptest.Server) *httptest.Server {
	t.Helper()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(New(Config{Upstream: u, UpstreamKey: testKey, Token: testToken, AllowPaths: InferencePaths}))
	t.Cleanup(gw.Close)
	return gw
}

func do(t *testing.T, method, u string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestGatewayRejectsMissingOrWrongToken pins that an unauthenticated or
// wrongly authenticated caller gets 401 and never reaches the upstream.
func TestGatewayRejectsMissingOrWrongToken(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	gw := newGateway(t, up)
	for name, hdr := range map[string]map[string]string{
		"none":         nil,
		"wrong bearer": {"Authorization": "Bearer nope"},
		"wrong key":    {"x-api-key": "nope"},
		"upstream key": {"Authorization": "Bearer " + testKey},
		"no scheme":    {"Authorization": testToken},
	} {
		if got := do(t, "POST", gw.URL+"/v1/messages", hdr).StatusCode; got != 401 {
			t.Errorf("%s: status %d, want 401", name, got)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("upstream called %d times, want 0", calls.Load())
	}
}

// TestGatewaySwapsCredentials pins that the upstream sees only the host's
// LiteLLM key as a bearer, whichever header the client authenticated with.
func TestGatewaySwapsCredentials(t *testing.T) {
	var auth, xkey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, xkey = r.Header.Get("Authorization"), r.Header.Get("x-api-key")
	}))
	defer up.Close()
	gw := newGateway(t, up)
	for _, hdr := range []map[string]string{
		{"Authorization": "Bearer " + testToken},
		{"x-api-key": testToken},
		{"Authorization": "Bearer " + testToken, "x-api-key": testToken},
	} {
		auth, xkey = "", "unset"
		if got := do(t, "POST", gw.URL+"/v1/messages", hdr).StatusCode; got != 200 {
			t.Fatalf("status %d", got)
		}
		if auth != "Bearer "+testKey || xkey != "" {
			t.Errorf("upstream saw Authorization %q x-api-key %q", auth, xkey)
		}
	}
}

// TestGatewayForwardsSessionHeadersUnchanged pins that the per-conversation
// ids and the User-Agent an agent sends reach LiteLLM untouched. LiteLLM's
// session affinity reads these headers to keep one conversation on one
// deployment, and the gateway has only a credential deny list
// (upstreamAuthHeaders), so nothing else is stripped; this keeps a later
// header filter from silently breaking affinity.
func TestGatewayForwardsSessionHeadersUnchanged(t *testing.T) {
	sent := map[string]string{
		"x-claude-code-session-id": "claude-session-1",
		"x-litellm-session-id":     "litellm-session-2",
		"x-session-id":             "plain-session-3",
		"session-id":               "codex-session-4",
		"thread-id":                "codex-thread-5",
		"User-Agent":               "agent-test/1.2.3",
	}
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer up.Close()
	gw := newGateway(t, up)
	hdr := map[string]string{"Authorization": "Bearer " + testToken}
	for k, v := range sent {
		hdr[k] = v
	}
	if status := do(t, "POST", gw.URL+"/v1/messages", hdr).StatusCode; status != 200 {
		t.Fatalf("status %d", status)
	}
	for k, want := range sent {
		if have := got.Get(k); have != want {
			t.Errorf("upstream saw %s %q, want %q", k, have, want)
		}
	}
	if auth := got.Get("Authorization"); auth != "Bearer "+testKey {
		t.Errorf("upstream saw Authorization %q, want the LiteLLM key", auth)
	}
}

// TestGatewayAcceptsXAPIKey pins that Anthropic-style clients, which send
// x-api-key, are accepted.
func TestGatewayAcceptsXAPIKey(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	gw := newGateway(t, up)
	if got := do(t, "POST", gw.URL+"/v1/messages", map[string]string{"x-api-key": testToken}).StatusCode; got != 200 {
		t.Errorf("status %d, want 200", got)
	}
}

// TestGatewayStreamsWithoutBuffering pins that a flushed chunk reaches the
// client while the upstream is still holding the response open.
func TestGatewayStreamsWithoutBuffering(t *testing.T) {
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
	gw := newGateway(t, up)
	resp := do(t, "POST", gw.URL+"/v1/messages", map[string]string{"x-api-key": testToken})
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

// TestGatewayPreservesPaths pins that both API styles reach the upstream with
// path, query and body intact.
func TestGatewayPreservesPaths(t *testing.T) {
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
	}))
	defer up.Close()
	gw := newGateway(t, up)
	hdr := map[string]string{"x-api-key": testToken}
	do(t, "POST", gw.URL+"/v1/messages?beta=true", hdr)
	do(t, "POST", gw.URL+"/v1/chat/completions", hdr)
	want := []string{"POST /v1/messages?beta=true {}", "POST /v1/chat/completions {}"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("upstream saw %q, want %q", got, want)
	}
	if code := do(t, "GET", gw.URL+"/other", hdr).StatusCode; code != 404 {
		t.Errorf("non-/v1 path status %d, want 404", code)
	}
}

// TestGatewayRestartingIs503 pins that an unreachable upstream (LiteLLM
// restarting) becomes a retryable 503 with the documented JSON body.
func TestGatewayRestartingIs503(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	gw := newGateway(t, up)
	up.Close()
	resp := do(t, "POST", gw.URL+"/v1/messages", map[string]string{"x-api-key": testToken})
	if resp.StatusCode != 503 {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "5" {
		t.Errorf("Retry-After %q, want 5", ra)
	}
	var e modelapi.GatewayError
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	want := modelapi.GatewayErrorObject{Message: modelapi.MsgProxyUnavailable, Type: modelapi.TypeAPI, Code: modelapi.CodeProxyUnavailable}
	if e.Error != want {
		t.Errorf("body %+v, want %+v", e, want)
	}
}

// TestGatewayIgnoresClientDisconnectCleanly pins that a client abandoning a
// stream cancels the upstream request and leaves the gateway serving.
func TestGatewayIgnoresClientDisconnectCleanly(t *testing.T) {
	cancelled := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			return
		}
		io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer up.Close()
	gw := newGateway(t, up)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", gw.URL+"/v1/messages", nil)
	req.Header.Set("x-api-key", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request context was not cancelled")
	}
	if got := do(t, "GET", gw.URL+"/v1/models", map[string]string{"x-api-key": testToken}).StatusCode; got != 200 {
		t.Errorf("gateway after disconnect: status %d", got)
	}
}

// TestGateway401IsOpenAIStyle pins that the gateway's own 401 is the
// OpenAI-style error object with the shared wording and never carries the
// upstream key.
func TestGateway401IsOpenAIStyle(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	gw := newGateway(t, up)
	resp := do(t, "POST", gw.URL+"/v1/messages", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), testKey) {
		t.Errorf("body leaks the upstream key: %s", b)
	}
	var e modelapi.GatewayError
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	want := modelapi.GatewayErrorObject{Message: modelapi.MsgUnauthorized, Type: modelapi.TypeAuthentication, Code: modelapi.CodeUnauthorized}
	if e.Error != want {
		t.Errorf("body %+v, want %+v", e, want)
	}
}

// recorder is a fake upstream that records the request URI of every call.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.seen = append(rec.seen, r.URL.RequestURI())
}

func (rec *recorder) list() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.seen)
}

// wantNotServed checks a gateway 404 for a path off the allowlist: the
// OpenAI-style body agents parse, with the shared wording.
func wantNotServed(t *testing.T, resp *http.Response, what string) {
	t.Helper()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("%s: status %d, want 404", what, resp.StatusCode)
		return
	}
	var e modelapi.GatewayError
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	want := modelapi.GatewayErrorObject{Message: modelapi.MsgPathNotServed, Type: modelapi.TypeInvalidRequest, Code: modelapi.CodeNotFound}
	if e.Error != want {
		t.Errorf("%s: body %+v, want %+v", what, e.Error, want)
	}
}

// TestGatewayServesOnlyInferencePaths pins the allowlist: every
// InferencePaths entry, and a path under the /v1/responses/ and /v1/models/
// prefixes, reaches the upstream; LiteLLM's file, batch and model-info routes
// (and the batch API under /v1/messages/, which is no prefix) are a 404 that
// never reaches it, the token is still checked first.
func TestGatewayServesOnlyInferencePaths(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(rec)
	defer up.Close()
	gw := newGateway(t, up)
	hdr := map[string]string{"x-api-key": testToken}
	var allowed []string
	for _, p := range InferencePaths {
		if !strings.HasSuffix(p, "/") { // a prefix entry is covered below
			allowed = append(allowed, p)
		}
	}
	allowed = append(allowed, "/v1/responses/abc", "/v1/models/x", "/v1/models/omlx/qwen3.8-35b")
	for _, p := range allowed {
		if got := do(t, "POST", gw.URL+p, hdr).StatusCode; got != 200 {
			t.Errorf("%s: status %d, want 200", p, got)
		}
	}
	if got := rec.list(); !slices.Equal(got, allowed) {
		t.Errorf("upstream saw %q, want %q", got, allowed)
	}
	for _, p := range []string{"/v1/files", "/v1/batches", "/v1/model/info", "/v1/messages/batches", "/v1/modelsx"} {
		wantNotServed(t, do(t, "POST", gw.URL+p, hdr), p)
	}
	if got := do(t, "POST", gw.URL+"/v1/files", map[string]string{"x-api-key": "nope"}).StatusCode; got != 401 {
		t.Errorf("wrong token on /v1/files: status %d, want 401 before the path check", got)
	}
	if got := rec.list(); len(got) != len(allowed) {
		t.Errorf("upstream saw %q, want no call beyond the allowed paths", got)
	}
}

// TestGatewayAllowlistMatchesPathNotQuery pins that the allowlist reads the
// path only: Claude Code's /v1/messages?beta=true and
// /v1/messages/count_tokens?beta=true reach the upstream with the query
// intact, and a query naming an allowed path does not open another one.
func TestGatewayAllowlistMatchesPathNotQuery(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(rec)
	defer up.Close()
	gw := newGateway(t, up)
	hdr := map[string]string{"x-api-key": testToken}
	want := []string{"/v1/messages?beta=true", "/v1/messages/count_tokens?beta=true"}
	for _, p := range want {
		if got := do(t, "POST", gw.URL+p, hdr).StatusCode; got != 200 {
			t.Errorf("%s: status %d, want 200", p, got)
		}
	}
	wantNotServed(t, do(t, "POST", gw.URL+"/v1/files?p=/v1/messages", hdr), "/v1/files?p=/v1/messages")
	if got := rec.list(); !slices.Equal(got, want) {
		t.Errorf("upstream saw %q, want %q", got, want)
	}
}

// TestGatewayAllowlistRejectsTraversal pins that no spelling of a path
// reaches a route off the allowlist: dot segments, a doubled slash and
// percent-encoded dots are never answered 2xx and never reach the upstream,
// both through the whole handler (whose mux redirects an unclean path) and
// through the allowlist alone (which refuses an unclean or non-canonically
// encoded path itself, so it does not lean on the mux).
func TestGatewayAllowlistRejectsTraversal(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(rec)
	defer up.Close()
	gw := newGateway(t, up)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	paths := []string{"/v1/models/../files", "//v1/files", "/v1/messages/../files", "/v1/%2e%2e/files", "/v1/models/%2e%2e/files", "/v1/models/a%2Fb"}
	for _, p := range paths {
		for _, client := range []*http.Client{noFollow, http.DefaultClient} {
			req, err := http.NewRequest("POST", gw.URL+p, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("x-api-key", testToken)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode < 300 && resp.StatusCode >= 200 {
				t.Errorf("%s: status %d, want no 2xx", p, resp.StatusCode)
			}
		}
		var calls atomic.Int32
		h := allowlisted(InferencePaths, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		if w.Code != http.StatusNotFound || calls.Load() != 0 {
			t.Errorf("%s through the allowlist alone: status %d, %d calls; want 404 and none", p, w.Code, calls.Load())
		}
	}
	if got := rec.list(); len(got) != 0 {
		t.Errorf("upstream saw %q, want nothing", got)
	}
}

// TestGatewayEmptyAllowPathsDeniesAll pins that the gateway fails closed: a
// Config with no AllowPaths (nil or empty) serves no path at all, so a
// caller that forgets to pass InferencePaths shuts the gateway rather than
// opening LiteLLM's file, batch and model-info routes.
func TestGatewayEmptyAllowPathsDeniesAll(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(rec)
	defer up.Close()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, allow := range [][]string{nil, {}} {
		gw := httptest.NewServer(New(Config{Upstream: u, UpstreamKey: testKey, Token: testToken, AllowPaths: allow}))
		for _, p := range []string{"/v1/messages", "/v1/models", "/v1/files"} {
			wantNotServed(t, do(t, "POST", gw.URL+p, map[string]string{"x-api-key": testToken}), p)
		}
		gw.Close()
	}
	if got := rec.list(); len(got) != 0 {
		t.Errorf("upstream saw %q, want nothing", got)
	}
}
