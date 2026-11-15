package modelserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const testToken = "tok-123"

// newTestServer builds a Server over a fake lifecycle engine behind httptest.
func newTestServer(t *testing.T, f *fakeLife, upstream *url.URL) (*Server, *httptest.Server) {
	t.Helper()
	s, err := NewServer(Options{
		Listen: "127.0.0.1:0", Token: testToken, IdleGrace: time.Hour,
		Upstream: upstream, UpstreamKey: "upstream-key",
	}, f.deps(serverCfg()))
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
	if status != wantStatus || e.Code != code {
		t.Fatalf("got %d code %q (%s), want %d %q", status, e.Code, e.Error, wantStatus, code)
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
		wantAPIError(t, st, b, http.StatusUnauthorized, "unauthorized")
	}
	st, b := callAuth(t, ts, "POST", "/control/v1/start", `{"model":"omlx/a"}`, "Bearer wrong")
	wantAPIError(t, st, b, http.StatusUnauthorized, "unauthorized")
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
// catalog.MissingReason when the snapshot can explain it (a model not on
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
	if !strings.Contains(e.Error, "not on disk") {
		t.Errorf("error = %q, want the MissingReason text", e.Error)
	}
	st, b = call(t, ts, "GET", "/control/v1/resolve?model=nope%2Fnone", "")
	e = wantAPIError(t, st, b, http.StatusNotFound, modelapi.CodeUnknownModel)
	if !strings.Contains(e.Error, "nope/none") {
		t.Errorf("error = %q, want it to name the id", e.Error)
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
	wantAPIError(t, st, b, http.StatusNotFound, "unknown_job")
	st, b = call(t, ts, "POST", "/control/v1/jobs/nope/cancel", "")
	wantAPIError(t, st, b, http.StatusNotFound, "unknown_job")
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
	wantAPIError(t, st, b, http.StatusGone, "unknown_session")
	call(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["omlx/a"]}`)
	if st, b := call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", `{"models":["omlx/b"]}`); st != 204 {
		t.Fatalf("heartbeat = %d %s, want 204", st, b)
	}
	call(t, ts, "DELETE", "/control/v1/sessions/s1", "")
	st, b = call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", `{"models":[]}`)
	wantAPIError(t, st, b, http.StatusGone, "unknown_session")
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
	wantAPIError(t, st, b, http.StatusBadRequest, "bad_request")
	st, b = call(t, ts, "POST", "/control/v1/sessions", `{"id":`)
	wantAPIError(t, st, b, http.StatusBadRequest, "bad_request")
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

// TestNewServerRequiresToken pins that an empty token is refused on every
// listen address, loopback or not, with an error naming --token-file, and that
// a non-empty token is accepted.
func TestNewServerRequiresToken(t *testing.T) {
	d := (&fakeLife{}).deps(serverCfg())
	for _, listen := range []string{"127.0.0.1:0", "[::1]:0", "localhost:0", "0.0.0.0:8080", ":8080", "192.168.1.5:8080"} {
		_, err := NewServer(Options{Listen: listen}, d)
		if err == nil || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "token is required") {
			t.Errorf("Listen %q: err = %v, want a required-token error naming --token-file", listen, err)
		}
		if _, err := NewServer(Options{Listen: listen, Token: "t"}, d); err != nil {
			t.Errorf("Listen %q with a token: %v", listen, err)
		}
	}
}

// TestGatewayIsMountedAndAuthenticated pins that /v1/ is the gateway: it
// refuses a caller without the token, and with it forwards upstream under the
// upstream key, never the client's token.
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
}

// TestServeAnnouncesAndShutsDownCleanly pins that Serve announces the bound
// address (so a :0 listener is discoverable), answers on it, runs the
// auto-stop loop on the shortened period, and returns nil once ctx is done.
func TestServeAnnouncesAndShutsDownCleanly(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	s, err := NewServer(Options{Listen: "127.0.0.1:0", Token: testToken, IdleGrace: 0},
		f.deps(serverCfg()), func(s *Server) { s.autoStopEvery = 10 * time.Millisecond })
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
