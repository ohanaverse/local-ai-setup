package modelgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	gw := httptest.NewServer(New(Config{Upstream: u, UpstreamKey: testKey, Token: testToken}))
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
		if r.URL.Path == "/v1/ok" {
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
	if got := do(t, "POST", gw.URL+"/v1/ok", map[string]string{"x-api-key": testToken}).StatusCode; got != 200 {
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
