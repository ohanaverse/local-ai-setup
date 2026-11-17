package modelserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const testToken = "tok-123"

// newTestServer builds a Server over a fake lifecycle engine behind httptest.
func newTestServer(t *testing.T, f *fakeLife, upstream *url.URL) (*Server, *httptest.Server) {
	t.Helper()
	return newTestServerDeps(t, f.deps(serverCfg()), upstream)
}

// newTestServerDeps is newTestServer over Deps the test adjusted.
func newTestServerDeps(t *testing.T, d Deps, upstream *url.URL) (*Server, *httptest.Server) {
	t.Helper()
	s, err := NewServer(Options{
		Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour,
		Upstream: upstream, UpstreamKey: "upstream-key",
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// call sends one control request with the test token and returns the status
// and body.
func call(t *testing.T, ts *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	return callAuth(t, ts, method, path, body, "Bearer "+testToken)
}

func callAuth(t *testing.T, ts *httptest.Server, method, path, body, auth string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func wantAPIError(t *testing.T, status int, body []byte, wantStatus int, code string) modelapi.APIError {
	t.Helper()
	var e modelapi.APIError
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("body %q is not an APIError: %v", body, err)
	}
	if status != wantStatus || e.Error.Code != code {
		t.Fatalf("got %d code %q (%s), want %d %q", status, e.Error.Code, e.Error.Message, wantStatus, code)
	}
	if !slices.Contains(modelapi.ErrorCodes, e.Error.Code) {
		t.Fatalf("code %q is not an exported modelapi constant", e.Error.Code)
	}
	return e
}

// TestControlRequiresToken pins that a missing or wrong bearer token is a 401
// APIError on every control route and the handler never runs (the inventory
// is never probed), while the right token is served.
func TestControlRequiresToken(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	for _, auth := range []string{"", "Bearer wrong", "Basic " + testToken, testToken} {
		st, b := callAuth(t, ts, "GET", "/control/v1/catalog", "", auth)
		wantAPIError(t, st, b, http.StatusUnauthorized, modelapi.CodeUnauthorized)
	}
	st, b := callAuth(t, ts, "POST", "/control/v1/start", `{"model":"omlx/a"}`, "Bearer wrong")
	wantAPIError(t, st, b, http.StatusUnauthorized, modelapi.CodeUnauthorized)
	if got := f.callList(); len(got) != 0 {
		t.Errorf("calls = %q, want none behind a 401", got)
	}
	if st, _ := call(t, ts, "GET", "/control/v1/ping", ""); st != http.StatusOK {
		t.Errorf("authorised ping = %d, want 200", st)
	}
}

// TestPingReportsAPIMajor pins that ping returns the wire API major version
// and schema, which a client uses to refuse an incompatible server.
func TestPingReportsAPIMajor(t *testing.T) {
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, nil)
	st, b := call(t, ts, "GET", "/control/v1/ping", "")
	var p modelapi.Ping
	if err := json.Unmarshal(b, &p); err != nil || st != 200 {
		t.Fatalf("ping = %d %s (%v)", st, b, err)
	}
	if p.APIMajor != modelapi.APIMajor || p.Schema != modelapi.SchemaVersion {
		t.Errorf("ping = %+v", p)
	}
}

// TestCatalogEndpointShowsSessionCounts pins that the catalog is built fresh
// per call from the live session registry: registering a session is visible
// as sessions:1 on its model.
func TestCatalogEndpointShowsSessionCounts(t *testing.T) {
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap("omlx/a")}, nil)
	sessions := func() int {
		st, b := call(t, ts, "GET", "/control/v1/catalog", "")
		var c modelapi.Catalog
		if err := json.Unmarshal(b, &c); err != nil || st != 200 {
			t.Fatalf("catalog = %d %s (%v)", st, b, err)
		}
		for _, r := range c.Rows {
			if r.ID == "omlx/a" {
				return r.Sessions
			}
		}
		t.Fatal("omlx/a missing from the catalog")
		return -1
	}
	if n := sessions(); n != 0 {
		t.Fatalf("sessions before register = %d", n)
	}
	if st, b := call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["omlx/a"]}`); st != 204 {
		t.Fatalf("register = %d %s", st, b)
	}
	if n := sessions(); n != 1 {
		t.Errorf("sessions after register = %d, want 1", n)
	}
}

// TestResolveUnknownCarriesMissingReason pins that resolve returns the row for
// a known id, and for an unknown one a 404 unknown_model whose text is
// localmodels.MissingReason when the snapshot can explain it (a model not on
// disk) and a generic message otherwise.
func TestResolveUnknownCarriesMissingReason(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	f.snap.Entries[1].Artifact = "" // omlx/b: registered, known to be absent
	_, ts := newTestServer(t, f, nil)
	st, b := call(t, ts, "GET", "/control/v1/resolve?model=omlx%2Fa", "")
	var row modelapi.Row
	if err := json.Unmarshal(b, &row); err != nil || st != 200 || row.ID != "omlx/a" {
		t.Fatalf("resolve = %d %s", st, b)
	}
	st, b = call(t, ts, "GET", "/control/v1/resolve?model=omlx%2Fb", "")
	e := wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownModel)
	if !strings.Contains(e.Error.Message, "not on disk") {
		t.Errorf("error = %q, want the MissingReason text", e.Error.Message)
	}
	st, b = call(t, ts, "GET", "/control/v1/resolve?model=nope%2Fnone", "")
	e = wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownModel)
	if !strings.Contains(e.Error.Message, "nope/none") {
		t.Errorf("error = %q, want it to name the id", e.Error.Message)
	}
}

// TestStartReturnsJobRefAndJobCompletes pins that start is a 202 JobRef and
// the job, polled at jobs/{id}, reaches succeeded; an unknown job is a 404
// and cancelling it is a 404 too.
func TestStartReturnsJobRefAndJobCompletes(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	st, b := call(t, ts, "POST", "/control/v1/start", `{"model":"omlx/a"}`)
	var ref modelapi.JobRef
	if err := json.Unmarshal(b, &ref); err != nil || st != http.StatusAccepted || ref.ID == "" {
		t.Fatalf("start = %d %s", st, b)
	}
	var js modelapi.JobStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, b = call(t, ts, "GET", "/control/v1/jobs/"+ref.ID, "")
		if err := json.Unmarshal(b, &js); err != nil {
			t.Fatal(err)
		}
		if js.State == modelapi.JobSucceeded || js.State == modelapi.JobFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if js.State != modelapi.JobSucceeded || js.Result == nil || !js.Result.OK {
		t.Fatalf("job = %+v, want succeeded", js)
	}
	st, b = call(t, ts, "GET", "/control/v1/jobs/nope", "")
	wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownJob)
	st, b = call(t, ts, "POST", "/control/v1/jobs/nope/cancel", "")
	wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownJob)
}

// TestStopReturnsJobRef pins that stop is a 202 JobRef whose job runs the
// stop (an in-use model with no confirmation fails with in_use).
func TestStopReturnsJobRef(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	_, ts := newTestServer(t, f, nil)
	call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["omlx/a"]}`)
	st, b := call(t, ts, "POST", "/control/v1/stop", `{"model":"omlx/a"}`)
	var ref modelapi.JobRef
	if err := json.Unmarshal(b, &ref); err != nil || st != http.StatusAccepted {
		t.Fatalf("stop = %d %s", st, b)
	}
	var js modelapi.JobStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, b = call(t, ts, "GET", "/control/v1/jobs/"+ref.ID, "")
		_ = json.Unmarshal(b, &js)
		if js.State == modelapi.JobFailed || js.State == modelapi.JobSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if js.Result == nil || js.Result.Error == nil || js.Result.Error.Code != modelapi.CodeInUse {
		t.Fatalf("job = %+v, want an in_use failure", js)
	}
}

// TestHeartbeatUnknownSessionIs410 pins Review Focus 1: a heartbeat for a
// session the registry does not know (never registered, ended or lapsed) is
// 410 Gone with code unknown_session, so the client re-registers instead of
// beating forever; a registered session's heartbeat is 204.
func TestHeartbeatUnknownSessionIs410(t *testing.T) {
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, nil)
	st, b := call(t, ts, "POST", "/control/v1/sessions/ghost/heartbeat", `{"models":[]}`)
	wantAPIError(t, st, b, http.StatusGone, modelapi.CodeUnknownSession)
	call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["omlx/a"]}`)
	if st, b := call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", `{"models":["omlx/b"]}`); st != 204 {
		t.Fatalf("heartbeat = %d %s, want 204", st, b)
	}
	call(t, ts, "DELETE", "/control/v1/sessions/s1", "")
	st, b = call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", `{"models":[]}`)
	wantAPIError(t, st, b, http.StatusGone, modelapi.CodeUnknownSession)
}

// TestEndSessionIsIdempotent pins that DELETE of a session is 204 whether or
// not it exists, so a client can always clean up.
func TestEndSessionIsIdempotent(t *testing.T) {
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, nil)
	call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":[]}`)
	for i := 0; i < 2; i++ {
		if st, b := call(t, ts, "DELETE", "/control/v1/sessions/s1", ""); st != 204 {
			t.Fatalf("delete #%d = %d %s, want 204", i, st, b)
		}
	}
}

// TestUnknownBodyFieldIsIgnored pins forward compatibility: a newer minor may
// add optional request fields, so an unknown field in a body is ignored (the
// request is served), while a body that is not JSON is still a 400 APIError
// that starts nothing.
func TestUnknownBodyFieldIsIgnored(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	if st, b := call(t, ts, "POST", "/control/v1/start", `{"model":"omlx/a","bogus":1}`); st != http.StatusAccepted {
		t.Fatalf("start with an unknown field = %d %s, want 202", st, b)
	}
	if st, b := call(t, ts, "POST", "/control/v1/sessions", `{"id":"s","models":[],"x":true}`); st != http.StatusNoContent {
		t.Fatalf("register with an unknown field = %d %s, want 204", st, b)
	}
	st, b := call(t, ts, "POST", "/control/v1/start", `not json`)
	wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	st, b = call(t, ts, "POST", "/control/v1/sessions", `{"id":`)
	wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
}

// TestHeartbeatAcceptsEmptyBody pins that a heartbeat with no body at all is
// the same as an empty JSON object: a bare keep-alive needs no payload.
func TestHeartbeatAcceptsEmptyBody(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	if st, b := call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["omlx/a"]}`); st != http.StatusNoContent {
		t.Fatalf("register = %d %s", st, b)
	}
	if st, b := call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", ""); st != http.StatusNoContent {
		t.Fatalf("heartbeat with an empty body = %d %s, want 204", st, b)
	}
}

// TestTrailingDataInBodyIsRefused pins that a body carrying a second JSON
// value after the first is a 400 APIError, applies nothing and submits no
// job: two requests concatenated into one body must not silently run only
// the first, while a body that merely ends in whitespace still decodes as
// one value.
func TestTrailingDataInBodyIsRefused(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	st, b := call(t, ts, "POST", "/control/v1/start", `{"model":"omlx/a"}{"model":"omlx/b"}`)
	e := wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	if !strings.Contains(e.Error.Message, "trailing data") {
		t.Errorf("error = %q, want it to name the trailing data", e.Error)
	}
	st, b = call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["omlx/a"]}{"id":"s2","models":[]}`)
	wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	if got := f.callList(); len(got) != 0 {
		t.Errorf("life calls = %q, want none: nothing in the refused bodies was applied", got)
	}
	if st, b := call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", ""); st != http.StatusGone {
		t.Errorf("heartbeat of s1 = %d %s, want 410: the register was not applied either", st, b)
	}
	if st, b := call(t, ts, "POST", "/control/v1/start", "{\"model\":\"omlx/b\"}\n"); st != http.StatusAccepted {
		t.Fatalf("start with a trailing newline = %d %s, want 202 (trailing whitespace is not data)", st, b)
	}
}

// TestNewServerRequiresToken pins that an empty token is refused on every
// listen address, loopback or not, with an error naming --token-file, and that
// a non-empty token is accepted.
func TestNewServerRequiresToken(t *testing.T) {
	d := (&fakeLife{}).deps(serverCfg())
	for _, listen := range []string{"127.0.0.1:0", "[::1]:0", "localhost:0", "0.0.0.0:8080", ":8080", "192.168.1.5:8080"} {
		_, err := NewServer(Options{Listen: listen}, d)
		if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "token is required") {
			t.Errorf("Listen %q: err = %v, want a required-token error naming --token-file", listen, err)
		} else if !strings.Contains(err.Error(), "umask 077") || strings.Contains(err.Error(), "tokenCreateCmd") {
			t.Errorf("Listen %q: err = %v, want the create command, not its Go name", listen, err)
		}
		if _, err := NewServer(Options{Listen: listen, Token: "t", IdleGrace: time.Minute}, d); err != nil {
			t.Errorf("Listen %q with a token: %v", listen, err)
		}
	}
}

// TestGatewayIsMountedAndAuthenticated pins that /v1/ is the gateway: it
// refuses a caller without the token, and with it forwards upstream under the
// upstream key, never the client's token. The server passes the inference
// allowlist: a model list is served, a LiteLLM file route is not.
func TestGatewayIsMountedAndAuthenticated(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, u)
	if st, _ := callAuth(t, ts, "GET", "/v1/models", "", ""); st != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", st)
	}
	st, b := call(t, ts, "GET", "/v1/models", "")
	if st != 200 || !bytes.Contains(b, []byte("data")) {
		t.Fatalf("gateway = %d %s", st, b)
	}
	if gotAuth != "Bearer upstream-key" {
		t.Errorf("upstream saw %q, want the upstream key", gotAuth)
	}
	gotAuth = ""
	if st, b := call(t, ts, "GET", "/v1/files", ""); st != http.StatusNotFound || gotAuth != "" {
		t.Errorf("/v1/files = %d %s (upstream saw %q), want a 404 that never reaches the upstream", st, b, gotAuth)
	}
}

// TestServeAnnouncesAndShutsDownCleanly pins that Serve announces the bound
// address (so a :0 listener is discoverable), answers on it, runs the
// auto-stop loop on the shortened period, and returns nil once ctx is done.
func TestServeAnnouncesAndShutsDownCleanly(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(serverCfg())
	d.Now = time.Now // a real clock, so a 1ns grace elapses between End and a scan
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Nanosecond},
		d, func(s *Server) { s.autoStopEvery = 10 * time.Millisecond })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.Register("s1", []string{"omlx/a"}); err != nil {
		t.Fatal(err)
	}
	s.sessions.End("s1") // the model is seen but unused: auto-stop owes it a stop
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, func(a string) { addrc <- a }) }()
	var addr string
	select {
	case addr = <-addrc:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never announced")
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/control/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ping on announced addr: %v %v", resp, err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for len(f.stopped()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(f.stopped()) == 0 {
		t.Error("auto-stop loop never stopped the idle model")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after ctx done")
	}
}

// TestCatalogSeesReloadedConfig pins that the catalog is built from the
// config returned at each request, not one captured at startup: a model added
// to the registry between two requests appears in the second answer.
func TestCatalogSeesReloadedConfig(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	cfg := serverCfg()
	d := f.deps(cfg)
	var current atomic.Pointer[config.Config]
	current.Store(cfg)
	d.Config = current.Load
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour}, d)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	ids := func() []string {
		_, body := call(t, ts, "GET", "/control/v1/catalog", "")
		var c modelapi.Catalog
		if err := json.Unmarshal(body, &c); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range c.Rows {
			out = append(out, r.ID)
		}
		return out
	}
	before := ids()
	next := *cfg
	next.Models = append(slices.Clone(cfg.Models), config.Model{
		ID: "openrouter/added", Family: "f", ProviderID: "openrouter", ModelName: "added",
	})
	current.Store(&next)
	after := ids()
	if slices.Contains(before, "openrouter/added") || !slices.Contains(after, "openrouter/added") {
		t.Fatalf("before = %v, after = %v; want openrouter/added only after the config changed", before, after)
	}
}

// TestReloadingConfigKeepsLastGood pins the production Deps.Config getter: it
// returns each fresh reload, and when a reload fails (a registry mid-write)
// it keeps answering with the last good config instead of failing.
func TestReloadingConfigKeepsLastGood(t *testing.T) {
	initial, second := serverCfg(), serverCfg()
	var fail bool
	next := second
	get := reloadingConfig(initial, func() (*config.Config, error) {
		if fail {
			return nil, errors.New("registry mid-write")
		}
		return next, nil
	})
	if got := get(); got != second {
		t.Fatal("did not return the reloaded config")
	}
	fail = true
	if got := get(); got != second {
		t.Fatal("a failed reload must keep the last good config")
	}
	fresh := serverCfg()
	fail, next = false, fresh
	if got := get(); got != fresh {
		t.Fatal("did not recover after the reload succeeded again")
	}
	// Before any good reload the initial config is the last good one.
	get = reloadingConfig(initial, func() (*config.Config, error) { return nil, errors.New("boom") })
	if got := get(); got != initial {
		t.Fatal("a failing first reload must return the initial config")
	}
}

// TestStartStopRejectEmptyModel pins that a start or stop naming no model is a
// 400 bad_request and submits no job.
func TestStartStopRejectEmptyModel(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	for _, path := range []string{"/control/v1/start", "/control/v1/stop"} {
		for _, body := range []string{``, `{}`, `{"model":""}`} {
			st, b := call(t, ts, "POST", path, body)
			wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
		}
	}
	if got := f.callList(); len(got) != 0 {
		t.Errorf("calls = %q, want none", got)
	}
}

// TestNewServerRejectsNonPositiveIdleGrace pins that a zero or negative idle
// grace is refused: it would auto-stop a model the moment it went idle.
func TestNewServerRejectsNonPositiveIdleGrace(t *testing.T) {
	d := (&fakeLife{}).deps(serverCfg())
	for _, g := range []time.Duration{0, -time.Second} {
		if _, err := NewServer(Options{Listen: "127.0.0.1:0", Token: "t", IdleGrace: g}, d); err == nil {
			t.Errorf("IdleGrace %s accepted, want an error", g)
		}
	}
}

// TestUnroutedControlPathsAnswerTheErrorObject pins that an unknown path is a
// 404 not_found and a wrong method a 405 method_not_allowed with an Allow
// header, both as the API's error object, and that the token check still runs
// first (an unauthenticated caller gets 401, not a route hint).
func TestUnroutedControlPathsAnswerTheErrorObject(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	st, b := call(t, ts, "GET", "/control/v1/nope", "")
	wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeNotFound)

	req, err := http.NewRequest("DELETE", ts.URL+"/control/v1/catalog", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ = io.ReadAll(resp.Body)
	wantAPIError(t, resp.StatusCode, b, http.StatusMethodNotAllowed, modelapi.CodeMethodNotAllowed)
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow = %q, want it to list GET", allow)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	st, b = callAuth(t, ts, "GET", "/control/v1/nope", "", "")
	e := wantAPIError(t, st, b, http.StatusUnauthorized, modelapi.CodeUnauthorized)
	if e.Error.Message != modelapi.MsgUnauthorized {
		t.Errorf("401 message = %q", e.Error.Message)
	}
	st, b = callAuth(t, ts, "DELETE", "/control/v1/catalog", "", "")
	wantAPIError(t, st, b, http.StatusUnauthorized, modelapi.CodeUnauthorized)
}

// TestWriteErrorCodesAreConstants pins that every writeError call in the
// non-test sources passes a modelapi.Code* constant, never a literal, so a
// client can switch on every code the server writes.
func TestWriteErrorCodesAreConstants(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) < 3 {
				return true
			}
			if id, ok := c.Fun.(*ast.Ident); !ok || id.Name != "writeError" {
				return true
			}
			calls++
			sel, ok := c.Args[2].(*ast.SelectorExpr)
			if !ok {
				return true // the generic writeError(w, status, code, msg) helper body passes parameters
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "modelapi" || !strings.HasPrefix(sel.Sel.Name, "Code") {
				t.Errorf("%s: writeError code is not a modelapi.Code* constant", f)
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && len(c.Args) >= 3 {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "writeError" {
					if _, lit := c.Args[2].(*ast.BasicLit); lit {
						t.Errorf("%s: writeError called with a string literal code", f)
					}
				}
			}
			return true
		})
	}
	if calls == 0 {
		t.Fatal("found no writeError calls; the scan is broken")
	}
}

// TestServerAnswersMatchErrorSamples pins that what the server answers for a
// 401 and an unknown model is the fixture's error_unauthorized and
// error_unknown_model shape (the same message for the 401, the same code and a
// non-empty message for the unknown model).
func TestServerAnswersMatchErrorSamples(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Unauthorized modelapi.APIError `json:"error_unauthorized"`
		UnknownModel modelapi.APIError `json:"error_unknown_model"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	f := &fakeLife{snap: serverSnap()}
	_, ts := newTestServer(t, f, nil)
	st, b := callAuth(t, ts, "GET", "/control/v1/catalog", "", "")
	got := wantAPIError(t, st, b, http.StatusUnauthorized, doc.Unauthorized.Error.Code)
	if got != doc.Unauthorized {
		t.Errorf("401 body %+v, fixture %+v", got, doc.Unauthorized)
	}
	st, b = call(t, ts, "GET", "/control/v1/resolve?model=nope/none", "")
	got = wantAPIError(t, st, b, http.StatusNotFound, doc.UnknownModel.Error.Code)
	if got.Error.Message == "" {
		t.Error("unknown model answered an empty message")
	}
}

// TestBadJSONBodyDoesNotEchoTheDecoder pins that a body that is not JSON gets
// one short fixed message, never the decoder's raw error text (J-rules, M6).
func TestBadJSONBodyDoesNotEchoTheDecoder(t *testing.T) {
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, nil)
	st, b := call(t, ts, "POST", "/control/v1/start", `{"model": oops`)
	e := wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	if e.Error.Message != "the request body is not valid JSON" {
		t.Errorf("message = %q, want the fixed wording", e.Error.Message)
	}
}

// TestSessionMessagesSayClientSession pins N7 on the API's own text.
func TestSessionMessagesSayClientSession(t *testing.T) {
	_, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, nil)
	st, b := call(t, ts, "POST", "/control/v1/sessions/none/heartbeat", "")
	e := wantAPIError(t, st, b, http.StatusGone, modelapi.CodeUnknownSession)
	if !strings.Contains(e.Error.Message, "client session") {
		t.Errorf("message = %q, want it to say client session", e.Error.Message)
	}
	st, b = call(t, ts, "POST", "/control/v1/sessions", `{"id":""}`)
	e = wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	if e.Error.Message != "the client session id is empty" {
		t.Errorf("message = %q, want it to stand alone, with no package label", e.Error.Message)
	}
}

// TestServeListenFailureNamesAddressAndFlag pins that a listen failure keeps
// the reason, names the address and ends on the way out (--listen).
func TestServeListenFailureNamesAddressAndFlag(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for _, tc := range []struct{ addr, reason string }{
		{busy.Addr().String(), "address already in use"},
		{"not-an-address", "missing port in address"},
	} {
		s, err := NewServer(Options{Listen: tc.addr, Token: testToken, IdleGrace: time.Hour}, (&fakeLife{}).deps(serverCfg()))
		if err != nil {
			t.Fatal(err)
		}
		err = s.Serve(t.Context(), nil)
		if err == nil {
			t.Fatalf("%s: Serve returned nil", tc.addr)
		}
		want := "cannot listen on " + tc.addr + ": " + tc.reason + " (pick another with --listen HOST:PORT)"
		if err.Error() != want {
			t.Errorf("%s: err = %q, want %q", tc.addr, err, want)
		}
	}
}

// pollJob polls GET jobs/{id} until the job finishes, and returns it.
func pollJob(t *testing.T, ts *httptest.Server, id string) modelapi.JobStatus {
	t.Helper()
	var js modelapi.JobStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, b := call(t, ts, "GET", "/control/v1/jobs/"+id, "")
		if st != http.StatusOK {
			t.Fatalf("job %s = %d %s", id, st, b)
		}
		if err := json.Unmarshal(b, &js); err != nil {
			t.Fatal(err)
		}
		if js.State != modelapi.JobQueued && js.State != modelapi.JobRunning {
			return js
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish: %+v", id, js)
	return js
}

// postStart posts a start and returns the job id; safe to call from any
// goroutine (it reports instead of failing the test).
func postStart(ts *httptest.Server, body string) (string, error) {
	req, err := http.NewRequest("POST", ts.URL+"/control/v1/start", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var ref modelapi.JobRef
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(b, &ref) != nil || ref.ID == "" {
		return "", fmt.Errorf("start = %d %s", resp.StatusCode, b)
	}
	return ref.ID, nil
}

// heldStart makes d.Start wait until the returned release is called (safe to
// call twice; the test's cleanup calls it too), signalling entered first, so
// a test can act while a start job is running.
func heldStart(t *testing.T, d *Deps) (entered <-chan struct{}, release func()) {
	t.Helper()
	in := make(chan struct{}, 1)
	gate := make(chan struct{})
	release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	start := d.Start
	d.Start = func(ctx context.Context, cfg *config.Config, tg lifecycle.Target, o lifecycle.Options) error {
		select {
		case in <- struct{}{}:
		default:
		}
		<-gate
		return start(ctx, cfg, tg, o)
	}
	return in, release
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// TestJobsListEndpoint pins GET /control/v1/jobs: the queued and running jobs
// of ?model=ID (every model without it), as a JobList whose jobs array is
// always present, [] when none is active.
func TestJobsListEndpoint(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	d := f.deps(serverCfg())
	entered, release := heldStart(t, &d)
	_, ts := newTestServerDeps(t, d, nil)
	id, err := postStart(ts, `{"model":"omlx/a"}`)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	list := func(path string) (modelapi.JobList, []byte) {
		t.Helper()
		st, b := call(t, ts, "GET", path, "")
		var l modelapi.JobList
		if err := json.Unmarshal(b, &l); err != nil || st != http.StatusOK {
			t.Fatalf("%s = %d %s (%v)", path, st, b, err)
		}
		return l, b
	}
	l, _ := list("/control/v1/jobs?model=omlx%2Fa")
	if len(l.Jobs) != 1 || l.Jobs[0].ID != id || l.Jobs[0].Kind != "start" || l.Jobs[0].Model != "omlx/a" || l.Jobs[0].State != modelapi.JobRunning {
		t.Errorf("jobs of omlx/a = %+v, want the running start %s", l.Jobs, id)
	}
	if l, _ := list("/control/v1/jobs"); len(l.Jobs) != 1 || l.Jobs[0].ID != id {
		t.Errorf("all jobs = %+v, want the one start", l.Jobs)
	}
	if _, raw := list("/control/v1/jobs?model=omlx%2Fb"); !bytes.Contains(raw, []byte(`"jobs":[]`)) {
		t.Errorf("jobs of an idle model = %s, want \"jobs\":[]", raw)
	}
	release()
	pollJob(t, ts, id)
	if l, _ := list("/control/v1/jobs?model=omlx%2Fa"); len(l.Jobs) != 0 {
		t.Errorf("jobs after the start finished = %+v, want none", l.Jobs)
	}
}

// TestConcurrentStartSameModelReturnsOneJob pins that two client sessions
// that start the same model at once share one job: otherwise the second
// queues a duplicate start that runs behind the first one's route restart.
// Eight concurrent POST start for one model get one job id and the engine's
// Start runs once (run it under -race).
func TestConcurrentStartSameModelReturnsOneJob(t *testing.T) {
	f := &fakeLife{snap: serverSnap()}
	d := f.deps(serverCfg())
	_, release := heldStart(t, &d)
	_, ts := newTestServerDeps(t, d, nil)
	const n = 8
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { ids[i], errs[i] = postStart(ts, `{"model":"omlx/a"}`) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	release()
	slices.Sort(ids)
	if distinct := slices.Compact(slices.Clone(ids)); len(distinct) != 1 {
		t.Fatalf("job ids = %q, want one shared job", distinct)
	}
	if js := pollJob(t, ts, ids[0]); js.State != modelapi.JobSucceeded {
		t.Fatalf("job = %+v, want succeeded", js)
	}
	if got := countCalls(f.callList(), "Start omlx/a"); got != 1 {
		t.Errorf("Start ran %d times, want once (calls %q)", got, f.callList())
	}
}

// TestSecondStartJoinsAndInheritsConsent pins what a joining start means: the
// second client gets the first one's job, and that job keeps the first
// request's answers. A posts start with allow_replace, B posts the same start
// without it while A's job runs: B gets A's job id, the engine runs once with
// AllowReplace, and the occupant A agreed to stop is stopped (the fake engine
// refuses as occupied without AllowReplace, as the real one does).
func TestSecondStartJoinsAndInheritsConsent(t *testing.T) {
	f := &fakeLife{snap: serverSnap("mtplx/y")}
	d := f.deps(serverCfg())
	d.Start = func(_ context.Context, _ *config.Config, tg lifecycle.Target, o lifecycle.Options) error {
		f.record("Start " + tg.ModelID)
		if !o.AllowReplace {
			return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{entry("mtplx", "y", true)}}
		}
		f.record("Stop mtplx/y")
		return nil
	}
	entered, release := heldStart(t, &d)
	_, ts := newTestServerDeps(t, d, nil)
	a, err := postStart(ts, `{"model":"mtplx/x","allow_replace":true}`)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	b, err := postStart(ts, `{"model":"mtplx/x"}`)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if b != a {
		t.Fatalf("B's job = %s, want A's %s", b, a)
	}
	if js := pollJob(t, ts, a); js.State != modelapi.JobSucceeded {
		t.Fatalf("job = %+v, want succeeded under A's allow_replace", js)
	}
	calls := f.callList()
	if countCalls(calls, "Start mtplx/x") != 1 || !slices.Contains(calls, "Stop mtplx/y") {
		t.Errorf("calls = %q, want one Start that stopped the occupant", calls)
	}
}

// TestFinishedJobsAreKeptUpToTheCap pins the job retention a client sees:
// the 101st job's finish drops the first, which then answers 404 unknown_job
// to a poll and to a cancel, while the 2nd through the 101st stay readable.
func TestFinishedJobsAreKeptUpToTheCap(t *testing.T) {
	s, ts := newTestServer(t, &fakeLife{snap: serverSnap()}, nil)
	okRun := func(context.Context, func(string)) modelapi.JobResult { return modelapi.JobResult{OK: true} }
	var ids []string
	for i := range 101 {
		ids = append(ids, s.jobs.Submit("start", fmt.Sprintf("omlx/m%d", i), okRun))
	}
	pollJob(t, ts, ids[100])
	st, b := call(t, ts, "GET", "/control/v1/jobs/"+ids[0], "")
	wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownJob)
	st, b = call(t, ts, "POST", "/control/v1/jobs/"+ids[0]+"/cancel", "")
	wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownJob)
	for _, i := range []int{1, 99, 100} {
		if st, b := call(t, ts, "GET", "/control/v1/jobs/"+ids[i], ""); st != http.StatusOK {
			t.Errorf("job %d = %d %s, want 200", i+1, st, b)
		}
	}
}
