package modelserver

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
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const e2eUpstreamKey = "upstream-key"

// fakeUpstream is a fake LiteLLM: /v1/models lists the ids route() has been
// given, /v1/chat/completions streams a two-chunk reply, and every chat
// request's credential headers are kept for the test to inspect.
type fakeUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	listed   []string
	chatAuth [][]string // Authorization values per chat request
	chatKey  []string   // x-api-key per chat request
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		u.mu.Lock()
		ids := slices.Clone(u.listed)
		u.mu.Unlock()
		type m struct {
			ID string `json:"id"`
		}
		var body struct {
			Data []m `json:"data"`
		}
		for _, id := range ids {
			body.Data = append(body.Data, m{ID: id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.chatAuth = append(u.chatAuth, slices.Clone(r.Header.Values("Authorization")))
		u.chatKey = append(u.chatKey, r.Header.Get("x-api-key"))
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, chunk := range []string{`data: {"delta":"hel"}`, `data: {"delta":"lo"}`, `data: [DONE]`} {
			_, _ = io.WriteString(w, chunk+"\n\n")
			fl.Flush()
		}
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

func (u *fakeUpstream) route(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.listed = append(u.listed, id)
}

// e2eJSON decodes a control response and fails on an unexpected status.
func e2eJSON(t *testing.T, ts *httptest.Server, method, path, body string, wantStatus int, into any) {
	t.Helper()
	st, b := call(t, ts, method, path, body)
	if st != wantStatus {
		t.Fatalf("%s %s = %d %s, want %d", method, path, st, b, wantStatus)
	}
	if into != nil {
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, b, err)
		}
	}
}

// e2eWaitJob polls a job until done(status) holds, bounded to a few seconds.
func e2eWaitJob(t *testing.T, ts *httptest.Server, id string, done func(modelapi.JobStatus) bool) modelapi.JobStatus {
	t.Helper()
	var js modelapi.JobStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e2eJSON(t, ts, "GET", "/control/v1/jobs/"+id, "", 200, &js)
		if done(js) {
			return js
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never reached the wanted state; last %+v", id, js)
	return js
}

func e2eFinished(js modelapi.JobStatus) bool {
	return js.State == modelapi.JobSucceeded || js.State == modelapi.JobFailed || js.State == modelapi.JobCancelled
}

func e2eRow(t *testing.T, ts *httptest.Server, id string) modelapi.Row {
	t.Helper()
	var c modelapi.Catalog
	e2eJSON(t, ts, "GET", "/control/v1/catalog", "", 200, &c)
	return rowByID(t, c, id)
}

// e2eServer builds a Server whose lifecycle is fakeLife and whose Routed
// really asks the fake upstream (via the production routeWaiter), all on the
// shared fake clock.
func e2eServer(t *testing.T, clk *fakeClock, f *fakeLife, up *fakeUpstream) (*Server, *httptest.Server) {
	t.Helper()
	upURL, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	d := f.deps(serverCfg())
	d.Now = clk.now
	w := routeWaiter{client: up.Client(), upstream: upURL, key: e2eUpstreamKey, every: 2 * time.Millisecond, timeout: 5 * time.Second}
	d.Routed = w.wait
	s, err := NewServer(Options{
		Listen: "127.0.0.1:0", Token: testToken, IdleGrace: 10 * time.Minute,
		Upstream: upURL, UpstreamKey: e2eUpstreamKey,
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.jobs.Shutdown(ctx)
	})
	return s, ts
}

// TestEndToEndSessionLifecycle is the one place the whole server is
// exercised together over real HTTP: each unit passing does not show that the
// catalog, jobs, sessions, gateway and auto-stopper agree on the wire. A
// session registers, starts a model (the job ends only once the fake LiteLLM
// lists it), chats through the gateway (which must swap the client token for
// the upstream key), ends, and the idle model is auto-stopped after the
// grace; a later heartbeat is 410.
func TestEndToEndSessionLifecycle(t *testing.T) {
	clk := &fakeClock{t: testNow}
	up := newFakeUpstream(t)
	f := &fakeLife{snap: serverSnap()}
	s, ts := e2eServer(t, clk, f, up)
	const m = "omlx/a"

	// The engine's Start brings the model up locally; the gateway lists it
	// only when the test says so.
	inner := s.d.Start
	s.d.Start = func(ctx context.Context, cfg *config.Config, tg lifecycle.Target, o lifecycle.Options) error {
		err := inner(ctx, cfg, tg, o)
		f.mu.Lock()
		f.snap = serverSnap(tg.ModelID)
		f.mu.Unlock()
		return err
	}
	// (1) ping
	var ping modelapi.Ping
	e2eJSON(t, ts, "GET", "/control/v1/ping", "", 200, &ping)
	if ping.APIMajor != modelapi.APIMajor {
		t.Fatalf("ping = %+v", ping)
	}
	// (2) stopped, startable
	if r := e2eRow(t, ts, m); r.State != modelapi.StateStopped || r.Action != modelapi.ActionStart || r.Sessions != 0 {
		t.Fatalf("before start: %+v", r)
	}
	// (3) register
	e2eJSON(t, ts, "POST", "/control/v1/sessions", `{"id":"s1","models":["`+m+`"]}`, 204, nil)
	// (4) start; the job waits on the gateway until the upstream lists m
	var ref modelapi.JobRef
	e2eJSON(t, ts, "POST", "/control/v1/start", `{"model":"`+m+`"}`, http.StatusAccepted, &ref)
	js := e2eWaitJob(t, ts, ref.ID, func(js modelapi.JobStatus) bool { return js.Step == stepGateway || e2eFinished(js) })
	if js.State != modelapi.JobRunning {
		t.Fatalf("job finished before the gateway listed %s: %+v", m, js)
	}
	up.route(m)
	js = e2eWaitJob(t, ts, ref.ID, e2eFinished)
	if js.State != modelapi.JobSucceeded || js.Result == nil || !js.Result.OK {
		t.Fatalf("start job = %+v", js)
	}
	// (5) running with one session
	if r := e2eRow(t, ts, m); r.State != modelapi.StateRunning || r.Sessions != 1 {
		t.Fatalf("after start: %+v", r)
	}
	// (6) a streamed chat completion with the client token
	req, err := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(`{"model":"`+m+`","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("x-api-key", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("chat = %d %v", resp.StatusCode, err)
	}
	if got := string(body); !strings.Contains(got, `"hel"`) || !strings.Contains(got, `"lo"`) || !strings.Contains(got, "[DONE]") {
		t.Fatalf("stream body = %q", got)
	}
	up.mu.Lock()
	auth, xkey := slices.Clone(up.chatAuth), slices.Clone(up.chatKey)
	up.mu.Unlock()
	if len(auth) != 1 || len(auth[0]) != 1 || auth[0][0] != "Bearer "+e2eUpstreamKey || xkey[0] != "" {
		t.Fatalf("upstream saw Authorization %q, x-api-key %q; want only the upstream key", auth, xkey)
	}
	// (7) heartbeat
	e2eJSON(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", `{"models":["`+m+`"]}`, 204, nil)
	// (8) end the session
	e2eJSON(t, ts, "DELETE", "/control/v1/sessions/s1", "", 204, nil)
	// (9) within the grace nothing stops; past it the scan stops m
	s.stopper.Tick(context.Background())
	drain(t, s.jobs)
	if got := f.stopped(); len(got) != 0 {
		t.Fatalf("stopped within the grace: %v", got)
	}
	clk.advance(11 * time.Minute)
	s.stopper.Tick(context.Background())
	drain(t, s.jobs) // FIFO: the auto-stop job has finished
	got := f.stopped()
	if len(got) != 1 || got[0].ModelID != m {
		t.Fatalf("stopped = %v, want exactly %s", got, m)
	}
	var begins []string
	for _, c := range f.callList() {
		if strings.HasPrefix(c, "BeginStops") {
			begins = append(begins, c)
		}
	}
	if !slices.Equal(begins, []string{"BeginStops " + m}) {
		t.Fatalf("BeginStops calls = %q", begins)
	}
	// (10) the session is gone
	st, b := call(t, ts, "POST", "/control/v1/sessions/s1/heartbeat", `{"models":[]}`)
	wantAPIError(t, st, b, http.StatusGone, modelapi.CodeUnknownSession)
}

// TestTwoStartsRunInOrderAndSurviveDisconnect pins, over HTTP, that start
// jobs run one at a time in submission order and outlive the request that
// submitted them: the first client disconnects at once, the second job reads
// queued while the first runs, and both still end succeeded.
func TestTwoStartsRunInOrderAndSurviveDisconnect(t *testing.T) {
	clk := &fakeClock{t: testNow}
	up := newFakeUpstream(t)
	f := &fakeLife{snap: serverSnap()}
	s, ts := e2eServer(t, clk, f, up)
	// Both models are already listed by the gateway; only the engine's Start
	// of the first is held, on a gate the test opens.
	up.route("omlx/a")
	up.route("omlx/b")
	gate := make(chan struct{})
	inA := make(chan struct{})
	inner := s.d.Start
	s.d.Start = func(ctx context.Context, cfg *config.Config, tg lifecycle.Target, o lifecycle.Options) error {
		if tg.ModelID == "omlx/a" {
			close(inA)
			<-gate
		}
		return inner(ctx, cfg, tg, o)
	}

	post := func(ctx context.Context, model string) modelapi.JobRef {
		req, err := http.NewRequestWithContext(ctx, "POST", ts.URL+"/control/v1/start", strings.NewReader(`{"model":"`+model+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var ref modelapi.JobRef
		if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil || resp.StatusCode != http.StatusAccepted {
			t.Fatalf("start %s = %d %v", model, resp.StatusCode, err)
		}
		return ref
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	refA := post(ctxA, "omlx/a")
	cancelA() // the first client disconnects immediately
	<-inA     // its job is running, held in Start
	refB := post(context.Background(), "omlx/b")

	var jsA, jsB modelapi.JobStatus
	e2eJSON(t, ts, "GET", "/control/v1/jobs/"+refB.ID, "", 200, &jsB)
	if jsB.State != modelapi.JobQueued {
		t.Fatalf("second job = %q while the first runs, want queued", jsB.State)
	}
	e2eJSON(t, ts, "GET", "/control/v1/jobs/"+refA.ID, "", 200, &jsA)
	if jsA.State != modelapi.JobRunning {
		t.Fatalf("first job = %q, want running", jsA.State)
	}
	close(gate)
	for _, ref := range []modelapi.JobRef{refA, refB} {
		if js := e2eWaitJob(t, ts, ref.ID, e2eFinished); js.State != modelapi.JobSucceeded {
			t.Fatalf("job %s = %+v, want succeeded", ref.ID, js)
		}
	}
	var starts []string
	for _, c := range f.callList() {
		if strings.HasPrefix(c, "Start ") {
			starts = append(starts, c)
		}
	}
	if !slices.Equal(starts, []string{"Start omlx/a", "Start omlx/b"}) {
		t.Fatalf("start order = %q", starts)
	}
}
