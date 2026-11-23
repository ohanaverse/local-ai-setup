package modelclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

const tok = "test-token"

func writeJSONBody(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSONBody(w, status, modelapi.APIError{Error: modelapi.ErrorObject{Message: msg, Code: code}})
}

// fixture is a test model server: a mux with a good ping already routed.
type fixture struct {
	*httptest.Server
	mux   *http.ServeMux
	pings atomic.Int32
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{mux: http.NewServeMux()}
	f.mux.HandleFunc("GET /control/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		f.pings.Add(1)
		writeJSONBody(w, 200, modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model"})
	})
	f.Server = httptest.NewServer(f.mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fixture) client(opts ...Option) *Client {
	return New(Endpoint{URL: f.URL, Token: tok}, opts...)
}

func addrOf(f *fixture) string { return strings.TrimPrefix(f.URL, "http://") }

func ctx() context.Context { return context.Background() }

// pingServer serves exactly one canned /ping body at 200.
func pingServer(t *testing.T, body string) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return New(Endpoint{URL: s.URL, Token: tok})
}

// TestPingRefusesDifferentMajorAndSaysWhichSide pins that a version skew names
// the side to upgrade, in both directions, with the exact wording.
func TestPingRefusesDifferentMajorAndSaysWhichSide(t *testing.T) {
	for _, tc := range []struct {
		major int
		want  string
	}{
		{modelapi.APIMajor + 1, "speaks API major 2, this wt speaks 1 — upgrade wt"},
		{modelapi.APIMajor - 1, "speaks API major 0, this wt speaks 1 — upgrade wt-model"},
	} {
		c := pingServer(t, `{"api_major":`+strconv.Itoa(tc.major)+`,"schema":2,"server":"wt-model"}`)
		_, err := c.Ping(ctx())
		var se *SkewError
		if !errors.As(err, &se) {
			t.Fatalf("major %d: err = %v, want *SkewError", tc.major, err)
		}
		if !strings.HasSuffix(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "model server at 127.0.0.1:") {
			t.Errorf("major %d: text = %q, want suffix %q", tc.major, err.Error(), tc.want)
		}
	}
}

// TestPingRefusesAnotherServer pins that a different program on the port, or
// one with no ping route, is *NotModelServerError, not a decode accident.
func TestPingRefusesAnotherServer(t *testing.T) {
	c := pingServer(t, `{"api_major":1,"schema":1,"server":"other"}`)
	_, err := c.Ping(ctx())
	var ne *NotModelServerError
	if !errors.As(err, &ne) || !strings.Contains(err.Error(), "is not a wt-model server") || !strings.HasSuffix(err.Error(), "check url under [model-server] in config.toml") {
		t.Fatalf("server other: %v", err)
	}
	s := httptest.NewServer(http.NotFoundHandler())
	defer s.Close()
	_, err = New(Endpoint{URL: s.URL, Token: tok}).Ping(ctx())
	if !errors.As(err, &ne) {
		t.Fatalf("404: err = %v, want *NotModelServerError", err)
	}
}

// TestCatalogSchemaMismatchIsRefused pins that a catalog schema this client
// does not read is refused with the exact text either way: a newer server
// needs a newer wt, an older one is a wt-model that was not restarted.
func TestCatalogSchemaMismatchIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema int
		want   string
	}{
		{"newer", modelapi.SchemaVersion + 1, "sends catalog schema 3, this wt reads up to 2 — upgrade wt"},
		{"older", modelapi.SchemaVersion - 1, "speaks catalog schema 1, this wt needs 2 — restart wt-model (it is older than this wt)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.mux.HandleFunc("GET /control/v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONBody(w, 200, modelapi.Catalog{Schema: tc.schema})
			})
			_, err := f.client().Catalog(ctx())
			var se *SchemaError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want *SchemaError", err)
			}
			if want := "model server at " + addrOf(f) + " " + tc.want; err.Error() != want {
				t.Errorf("text = %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestPingOlderSchemaIsRefused pins that a server whose Ping reports an older
// schema (a wt-model not restarted after wt was updated) is refused at the
// first call, and that no retry can heal it.
func TestPingOlderSchemaIsRefused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /control/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion - 1, Server: "wt-model"})
	})
	s := httptest.NewServer(mux)
	defer s.Close()
	_, err := New(Endpoint{URL: s.URL, Token: tok}).Ping(ctx())
	var se *SchemaError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "restart wt-model") {
		t.Fatalf("err = %v, want *SchemaError naming a restart", err)
	}
	if !IsPermanent(err) {
		t.Error("a schema mismatch is permanent: no retry heals it")
	}
}

// TestDefaultTransportIgnoresEnvProxy pins that the bearer token never goes
// to an HTTP(S)_PROXY from the environment.
func TestDefaultTransportIgnoresEnvProxy(t *testing.T) {
	tr, ok := New(Endpoint{URL: "http://127.0.0.1:1", Token: tok}).hc.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("transport proxy = %v (ok %v), want nil", tr, ok)
	}
}

// TestGatewayStatusReturnsTheStatusAsIs pins the one-shot probe: a 503 is a
// status, not an error, and nothing retries.
func TestGatewayStatusReturnsTheStatusAsIs(t *testing.T) {
	var n int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) { n++; w.WriteHeader(503) })
	s := httptest.NewServer(mux)
	defer s.Close()
	st, err := New(Endpoint{URL: s.URL, Token: tok}).GatewayStatus(ctx())
	if err != nil || st != 503 || n != 1 {
		t.Fatalf("status %d, err %v, polls %d; want 503, nil, 1", st, err, n)
	}
}

// TestUnreachableNamesTheAddressTried pins that a dead server is reported with
// the address wt tried and the command that starts one.
func TestUnreachableNamesTheAddressTried(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = New(Endpoint{URL: "http://" + addr, Token: tok}).Catalog(ctx())
	var ue *UnreachableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnreachableError", err)
	}
	if !strings.Contains(err.Error(), "model server at "+addr+" is not reachable (") || !strings.Contains(err.Error(), "wt-model serve --listen "+addr+" --token-file PATH") {
		t.Errorf("text = %q", err.Error())
	}
}

// TestUnauthorizedIsTyped pins that a 401 becomes *UnauthorizedError whose
// text points at the token file rather than echoing the server's body.
func TestUnauthorizedIsTyped(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, 401, modelapi.CodeUnauthorized, modelapi.MsgUnauthorized)
	}))
	defer s.Close()
	_, err := New(Endpoint{URL: s.URL, Token: tok}).Ping(ctx())
	var ue *UnauthorizedError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnauthorizedError", err)
	}
	want := "model server at " + strings.TrimPrefix(s.URL, "http://") + " refused the token (" + modelapi.MsgUnauthorized +
		") — check token_file under [model-server] in config.toml and that the server was started with the same token"
	if err.Error() != want {
		t.Errorf("text = %q\nwant %q", err.Error(), want)
	}
}

// TestAnswerPastTheReadLimitIsNamed pins the read limit: a server whose answer
// runs past 32 MiB is refused as *ResponseTooLargeError naming the size, not
// silently truncated into a decode error that would misread it as not a
// wt-model server.
func TestAnswerPastTheReadLimitIsNamed(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponse+1))
	}))
	defer s.Close()
	_, err := New(Endpoint{URL: s.URL, Token: tok}).Catalog(ctx())
	var te *ResponseTooLargeError
	if !errors.As(err, &te) || te.Limit != maxResponse {
		t.Fatalf("err = %v, want *ResponseTooLargeError with the 32 MiB limit", err)
	}
	if !strings.Contains(err.Error(), "larger than 32 MiB") {
		t.Errorf("text = %q", err.Error())
	}
}

// TestEveryCallSendsTheBearerToken pins that no method forgets the token,
// including the gateway readiness poll.
func TestEveryCallSendsTheBearerToken(t *testing.T) {
	var mu sync.Mutex
	var bad []string
	seen := map[string]bool{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = true
		if r.Header.Get("Authorization") != "Bearer "+tok {
			bad = append(bad, r.Method+" "+r.URL.Path)
		}
		mu.Unlock()
		switch {
		case r.URL.Path == "/control/v1/ping":
			writeJSONBody(w, 200, modelapi.Ping{APIMajor: 1, Schema: modelapi.SchemaVersion, Server: "wt-model"})
		case r.URL.Path == "/control/v1/start" || r.URL.Path == "/control/v1/stop":
			writeJSONBody(w, 202, modelapi.JobRef{ID: "j"})
		case r.URL.Path == "/control/v1/jobs":
			writeJSONBody(w, 200, modelapi.JobList{Jobs: []modelapi.JobStatus{}})
		case strings.HasPrefix(r.URL.Path, "/control/v1/jobs/") && strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/control/v1/jobs/"):
			writeJSONBody(w, 200, modelapi.JobStatus{ID: "j"})
		case r.URL.Path == "/control/v1/resolve":
			writeJSONBody(w, 200, modelapi.Row{ID: "m"})
		case r.URL.Path == "/control/v1/catalog":
			writeJSONBody(w, 200, modelapi.Catalog{Schema: modelapi.SchemaVersion})
		case r.URL.Path == "/control/v1/candidates":
			writeJSONBody(w, 200, modelapi.CandidateList{})
		case r.URL.Path == "/control/v1/spend":
			writeJSONBody(w, 200, modelapi.SpendReport{})
		case r.URL.Path == "/v1/models":
			writeJSONBody(w, 200, map[string]any{"data": []any{}})
		default:
			w.WriteHeader(204)
		}
	}))
	defer s.Close()
	c := New(Endpoint{URL: s.URL, Token: tok})
	now := time.Now()
	calls := []func() error{
		func() error { _, e := c.Ping(ctx()); return e },
		func() error { _, e := c.Catalog(ctx()); return e },
		func() error { _, e := c.Resolve(ctx(), "m"); return e },
		func() error { _, e := c.Candidates(ctx(), CandidateQuery{}); return e },
		func() error { _, e := c.Spend(ctx(), now, now); return e },
		func() error { _, e := c.SubmitStart(ctx(), modelapi.StartRequest{Model: "m"}); return e },
		func() error { _, e := c.SubmitStop(ctx(), modelapi.StopRequest{Model: "m"}); return e },
		func() error { _, e := c.Job(ctx(), "j"); return e },
		func() error { _, e := c.ActiveJobs(ctx(), "m"); return e },
		func() error { return c.CancelJob(ctx(), "j") },
		func() error { return c.Register(ctx(), "s", []string{"m"}) },
		func() error { _, e := c.Heartbeat(ctx(), "s", []string{"m"}); return e },
		func() error { return c.End(ctx(), "s") },
		func() error { return c.WaitGatewayReady(ctx()) },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bad) > 0 {
		t.Errorf("requests without the bearer token: %v", bad)
	}
	for _, want := range []string{"GET /v1/models", "DELETE /control/v1/sessions/s", "POST /control/v1/sessions/s/heartbeat"} {
		if !seen[want] {
			t.Errorf("never saw %s; saw %v", want, seen)
		}
	}
}

// TestResolveUnknownCarriesServerMessage pins that a resolve 404 is
// *UnknownModelError with the server's own explanation, and that the model id
// travels as a query value (never in the path, which the gateway-style
// routers 404 on an escaped slash).
func TestResolveUnknownCarriesServerMessage(t *testing.T) {
	f := newFixture(t)
	var rawPath, query string
	f.mux.HandleFunc("GET /control/v1/resolve", func(w http.ResponseWriter, r *http.Request) {
		rawPath, query = r.URL.RawPath, r.URL.Query().Get("model")
		apiErr(w, 404, modelapi.CodeUnknownModel, `unknown model "omlx/zzz"`)
	})
	_, err := f.client().Resolve(ctx(), "omlx/zzz")
	var um *UnknownModelError
	if !errors.As(err, &um) || um.Message != `unknown model "omlx/zzz"` || err.Error() != um.Message {
		t.Fatalf("err = %v", err)
	}
	if rawPath != "" || query != "omlx/zzz" {
		t.Errorf("rawPath %q query %q", rawPath, query)
	}
	if !IsCode(errors.Join(errors.New("x"), &modelapi.ResponseError{Code: "c"}), "c") || IsCode(nil, "c") {
		t.Error("IsCode wrong")
	}
}

// TestHeartbeatGoneIsNotAnError pins that 410 means "register again", which a
// caller needs as a value, not as a failure.
func TestHeartbeatGoneIsNotAnError(t *testing.T) {
	f := newFixture(t)
	status := 410
	f.mux.HandleFunc("POST /control/v1/sessions/s1/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		if status == 204 {
			w.WriteHeader(204)
			return
		}
		apiErr(w, status, modelapi.CodeUnknownSession, "gone")
	})
	known, err := f.client().Heartbeat(ctx(), "s1", []string{"m"})
	if known || err != nil {
		t.Fatalf("410: known=%v err=%v", known, err)
	}
	status = 204
	if known, err = f.client().Heartbeat(ctx(), "s1", nil); !known || err != nil {
		t.Fatalf("204: known=%v err=%v", known, err)
	}
}

// TestUnknownResponseFieldsAreIgnored pins the minor-version rule: a server
// may add fields to any response and an old client must not choke.
func TestUnknownResponseFieldsAreIgnored(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("GET /control/v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"schema":2,"generated":"g","providers":{},"rows":[{"id":"a","future":1}],"extra":{"x":1}}`))
	})
	f.mux.HandleFunc("GET /control/v1/jobs/j1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"j1","state":"running","new_field":[1,2]}`))
	})
	cat, err := f.client().Catalog(ctx())
	if err != nil || len(cat.Rows) != 1 || cat.Rows[0].ID != "a" {
		t.Fatalf("catalog = %+v, %v", cat, err)
	}
	if js, err := f.client().Job(ctx(), "j1"); err != nil || js.State != "running" {
		t.Fatalf("job = %+v, %v", js, err)
	}
}

// TestCatalogDecodesTheFixture pins that the contract fixture, which is what
// the server side is tested against, decodes through every client method.
func TestCatalogDecodesTheFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/model-api.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	serve := func(path, key string) {
		f.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(doc[key])
		})
	}
	serve("/control/v1/catalog", "catalog")
	serve("/control/v1/jobs/job-0001", "job")
	serve("/control/v1/jobs", "jobs")
	serve("/control/v1/candidates", "candidates")
	serve("/control/v1/spend", "spend")
	f.mux.HandleFunc("GET /control/v1/resolve", func(w http.ResponseWriter, _ *http.Request) {
		var c modelapi.Catalog
		_ = json.Unmarshal(doc["catalog"], &c)
		writeJSONBody(w, 200, c.Rows[0])
	})
	c := f.client()
	cat, err := c.Catalog(ctx())
	if err != nil || len(cat.Rows) != 8 || cat.Pricing == nil || cat.Rows[1].Price == nil || !cat.Rows[1].Price.TimePriced || cat.Rows[0].Routed == nil {
		t.Fatalf("catalog: %+v %v", cat, err)
	}
	if a := cat.Rows[7]; a.Kind != modelapi.KindAlias || len(a.Targets) != 2 || a.Targets[1].ID != "omlx/qwen3.8-35b" || a.Targets[1].Order != 2 {
		t.Errorf("alias row: %+v", a)
	}
	js, err := c.Job(ctx(), "job-0001")
	if err != nil || js.Result == nil || js.Result.Error == nil || js.Result.Error.Code != "occupied" || len(js.Result.Error.Others) != 1 || js.Result.Error.Others[0].Figure != "4.2 GB loaded" {
		t.Fatalf("job: %+v %v", js, err)
	}
	if jobs, err := c.ActiveJobs(ctx(), ""); err != nil || len(jobs) != 2 {
		t.Fatalf("jobs: %+v %v", jobs, err)
	}
	if cl, err := c.Candidates(ctx(), CandidateQuery{}); err != nil || len(cl.Candidates) != 2 || cl.StaleSources[0] != "lmarena" {
		t.Fatalf("candidates: %+v %v", cl, err)
	}
	if sp, err := c.Spend(ctx(), time.Now(), time.Now()); err != nil || len(sp.Rows) != 1 || sp.Unattributed != 2 {
		t.Fatalf("spend: %+v %v", sp, err)
	}
	if row, err := c.Resolve(ctx(), "omlx/qwen3.8-35b"); err != nil || row.ID != "omlx/qwen3.8-35b" {
		t.Fatalf("resolve: %+v %v", row, err)
	}
}

// TestCandidateAndSpendQueries pins the query encoding: a "+" in an RFC 3339
// offset must reach the server as a plus, so it is sent as %2B, and the
// candidate filters use the names the server reads.
func TestCandidateAndSpendQueries(t *testing.T) {
	f := newFixture(t)
	var gotRaw []string
	var spendStart string
	f.mux.HandleFunc("GET /control/v1/candidates", func(w http.ResponseWriter, r *http.Request) {
		gotRaw = append(gotRaw, r.URL.RawQuery)
		writeJSONBody(w, 200, modelapi.CandidateList{})
	})
	f.mux.HandleFunc("GET /control/v1/spend", func(w http.ResponseWriter, r *http.Request) {
		gotRaw = append(gotRaw, r.URL.RawQuery)
		spendStart = r.URL.Query().Get("start")
		writeJSONBody(w, 200, modelapi.SpendReport{})
	})
	c := f.client()
	if _, err := c.Candidates(ctx(), CandidateQuery{OpenOnly: true, Providers: []string{"omlx", "ollama"}, Near: "omlx/a", PerProvider: 2}); err != nil {
		t.Fatal(err)
	}
	q := gotRaw[0]
	for _, want := range []string{"open_only=true", "provider=omlx%2Collama", "near=omlx%2Fa", "per_provider=2"} {
		if !strings.Contains(q, want) {
			t.Errorf("candidates query %q lacks %q", q, want)
		}
	}
	zone := time.FixedZone("p", 5*3600+1800)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, zone)
	if _, err := c.Spend(ctx(), start, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotRaw[1], "+") || spendStart != start.Format(time.RFC3339) {
		t.Errorf("spend raw %q start %q", gotRaw[1], spendStart)
	}
}

// TestRequestTimesOutOnAHungServer pins that a server that accepts and never
// answers cannot hang a control call: it ends as *UnreachableError naming the
// timeout within the request timeout.
func TestRequestTimesOutOnAHungServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	c := New(Endpoint{URL: "http://" + ln.Addr().String(), Token: tok}, WithRequestTimeout(150*time.Millisecond))
	start := time.Now()
	_, err = c.Catalog(ctx())
	var ue *UnreachableError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want *UnreachableError naming a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

// TestEndReturnsWithinBoundWhenServerIsDown pins that ending a session at exit
// never holds the exit longer than its 3 s bound, whether the server is gone
// or hung, and that it still tries when the caller's ctx is already cancelled.
func TestEndReturnsWithinBoundWhenServerIsDown(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	// closed listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	start := time.Now()
	err = New(Endpoint{URL: "http://" + addr, Token: tok}).End(cancelled, "s")
	var ue *UnreachableError
	if !errors.As(err, &ue) || time.Since(start) > 3500*time.Millisecond {
		t.Errorf("closed: err = %v after %v", err, time.Since(start))
	}

	// hung server: the handler never answers, but the request must arrive
	var hits atomic.Int32
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
		<-release
	}))
	defer hung.Close()
	defer close(release)
	start = time.Now()
	err = New(Endpoint{URL: hung.URL, Token: tok}).End(cancelled, "s")
	if !errors.As(err, &ue) || time.Since(start) > 3500*time.Millisecond {
		t.Errorf("hung: err = %v after %v", err, time.Since(start))
	}
	if !strings.Contains(err.Error(), "timed out after 3s") {
		t.Errorf("hung: %q does not name the 3s bound that expired", err.Error())
	}
	if hits.Load() != 1 {
		t.Errorf("the DELETE reached the server %d times, want 1", hits.Load())
	}
}

// TestWaitGatewayReadyHonoursRetryAfterAndCap pins the gateway poll: a 503
// sleeps its Retry-After, a recovery returns nil, and a gateway that never
// recovers ends as *GatewayDownError after 3 minutes of (fake) time.
func TestWaitGatewayReadyHonoursRetryAfterAndCap(t *testing.T) {
	f := newFixture(t)
	var n atomic.Int32
	f.mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) <= 2 {
			w.Header().Set("Retry-After", "1")
			apiErr(w, 503, modelapi.CodeProxyUnavailable, modelapi.MsgProxyUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	var slept []time.Duration
	c := f.client(WithSleep(func(d time.Duration) { slept = append(slept, d) }))
	if err := c.WaitGatewayReady(ctx()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != time.Second {
		t.Errorf("slept %v, want two 1s", slept)
	}

	never := newFixture(t)
	never.mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, 503, modelapi.CodeProxyUnavailable, modelapi.MsgProxyUnavailable)
	})
	var total time.Duration
	c = never.client(WithSleep(func(d time.Duration) { total += d }))
	err := c.WaitGatewayReady(ctx())
	var gd *GatewayDownError
	if !errors.As(err, &gd) {
		t.Fatalf("err = %v, want *GatewayDownError", err)
	}
	want := "the gateway at " + addrOf(never) + " still answers 503 after 3m — the model server may be restarting LiteLLM; try again shortly"
	if err.Error() != want {
		t.Errorf("text = %q", err.Error())
	}
	if total != 3*time.Minute {
		t.Errorf("slept %v in total, want 3m (default Retry-After 5s)", total)
	}
}

// TestNowFollowsTheServerDateHeader pins R16: lease and age arithmetic use the
// server's clock, so Now() carries the offset the latest response measured.
func TestNowFollowsTheServerDateHeader(t *testing.T) {
	f := newFixture(t)
	skew := 5 * time.Minute
	f.mux.HandleFunc("GET /control/v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", time.Now().Add(skew).UTC().Format(http.TimeFormat))
		writeJSONBody(w, 200, modelapi.Catalog{Schema: modelapi.SchemaVersion})
	})
	c := f.client()
	if d := time.Until(c.Now()); d > time.Second || d < -time.Second {
		t.Errorf("before any response Now() is %v from local", d)
	}
	if _, err := c.Catalog(ctx()); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(c.Now()); d < skew-3*time.Second || d > skew+3*time.Second {
		t.Errorf("Now() is %v ahead, want about %v", d, skew)
	}
}

// TestPingIsCachedPerClient pins that the version handshake runs once, however
// many calls follow.
func TestPingIsCachedPerClient(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("GET /control/v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, modelapi.Catalog{Schema: modelapi.SchemaVersion})
	})
	c := f.client()
	for range 3 {
		if _, err := c.Catalog(ctx()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Ping(ctx()); err != nil {
		t.Fatal(err)
	}
	if f.pings.Load() != 1 {
		t.Errorf("pinged %d times, want 1", f.pings.Load())
	}
}

const hostile = "evil\nwt: forged line\x1b]0;title\x07‮"

func isOneLine(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// TestCleanRowCleansAliasStrings pins that an alias row's own strings and
// every target's strings come back as one printable line each (M11): the
// server is a remote party, and a target id or state is printed by the
// picker and `wt model list --server` like any row id.
func TestCleanRowCleansAliasStrings(t *testing.T) {
	r := modelapi.Row{ID: "alias/x", Kind: hostile, Targets: []modelapi.AliasTarget{
		{ID: hostile, ProviderID: hostile, Location: hostile, State: hostile},
	}}
	cleanRow(&r)
	tg := r.Targets[0]
	for _, s := range []string{r.Kind, tg.ID, tg.ProviderID, tg.Location, tg.State} {
		if !isOneLine(s) {
			t.Errorf("not cleaned: %q", s)
		}
	}
}

// TestServerTextCannotForgeLines is M11: the server is a remote party, so
// newline, ESC and bidi runes in anything it sends (a job step, a problem
// message, a resident label, a block reason, an unknown-model message, an
// error body) come back as one printable line from every decode path.
func TestServerTextCannotForgeLines(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("GET /control/v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, modelapi.Catalog{Schema: modelapi.SchemaVersion, Generated: hostile, Providers: map[string]string{hostile: hostile},
			Rows: []modelapi.Row{{ID: hostile, BlockReason: hostile, Family: hostile, Tags: []string{hostile}}}})
	})
	job := modelapi.JobStatus{ID: "j", Step: hostile, Result: &modelapi.JobResult{Error: &modelapi.Problem{
		Code: "others", Message: hostile, Occupants: []string{hostile}, OthersUnknown: []string{hostile}, Sessions: []string{hostile},
		Others: []modelapi.Resident{{ID: hostile, Label: hostile, Figure: hostile}},
	}}}
	f.mux.HandleFunc("GET /control/v1/jobs/j", func(w http.ResponseWriter, _ *http.Request) { writeJSONBody(w, 200, job) })
	f.mux.HandleFunc("GET /control/v1/jobs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, modelapi.JobList{Jobs: []modelapi.JobStatus{job}})
	})
	f.mux.HandleFunc("GET /control/v1/resolve", func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, 404, modelapi.CodeUnknownModel, hostile)
	})
	f.mux.HandleFunc("GET /control/v1/spend", func(w http.ResponseWriter, _ *http.Request) {
		apiErr(w, 503, modelapi.CodeSpendUnavailable, hostile)
	})
	f.mux.HandleFunc("GET /control/v1/candidates", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, modelapi.CandidateList{StaleSources: []string{hostile}, Candidates: []modelapi.Candidate{{ID: hostile, Provider: hostile, Flags: []string{hostile}}}})
	})
	c := f.client()
	var all []string
	cat, err := c.Catalog(ctx())
	if err != nil {
		t.Fatal(err)
	}
	r := cat.Rows[0]
	all = append(all, cat.Generated, r.ID, r.BlockReason, r.Family, r.Tags[0])
	for k, v := range cat.Providers {
		all = append(all, k, v)
	}
	collect := func(js modelapi.JobStatus) {
		p := js.Result.Error
		all = append(all, js.Step, p.Message, p.Occupants[0], p.OthersUnknown[0], p.Sessions[0], p.Others[0].ID, p.Others[0].Label, p.Others[0].Figure)
	}
	js, err := c.Job(ctx(), "j")
	if err != nil {
		t.Fatal(err)
	}
	collect(js)
	jobs, err := c.ActiveJobs(ctx(), "")
	if err != nil {
		t.Fatal(err)
	}
	collect(jobs[0])
	cl, err := c.Candidates(ctx(), CandidateQuery{})
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, cl.StaleSources[0], cl.Candidates[0].ID, cl.Candidates[0].Provider, cl.Candidates[0].Flags[0])
	_, err = c.Resolve(ctx(), "x")
	var um *UnknownModelError
	if !errors.As(err, &um) {
		t.Fatal(err)
	}
	all = append(all, um.Message, err.Error())
	_, err = c.Spend(ctx(), time.Now(), time.Now())
	if err == nil {
		t.Fatal("spend: no error")
	}
	all = append(all, err.Error())
	for i, s := range all {
		if !isOneLine(s) {
			t.Errorf("field %d = %q is not one printable line", i, s)
		}
	}
}

// TestWaitGatewayReadyClampsHostileRetryAfter pins that a Retry-After the
// server controls can neither overflow the sleep (a huge or negative number)
// nor outlast the 3-minute cap (a day): each ends as *GatewayDownError with at
// most 3 minutes slept.
func TestWaitGatewayReadyClampsHostileRetryAfter(t *testing.T) {
	for _, ra := range []string{"99999999999", "86400", "-5", "0", "abc", "9223372036854775807", ""} {
		f := newFixture(t)
		f.mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
			if ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			apiErr(w, 503, modelapi.CodeProxyUnavailable, modelapi.MsgProxyUnavailable)
		})
		var total time.Duration
		var n int
		c := f.client(WithSleep(func(d time.Duration) {
			n++
			if d < time.Second || n > 1000 {
				t.Fatalf("Retry-After %q: sleep %v (call %d) is outside [1s, cap]", ra, d, n)
			}
			total += d
		}))
		err := c.WaitGatewayReady(ctx())
		var gd *GatewayDownError
		if !errors.As(err, &gd) || total > 3*time.Minute {
			t.Errorf("Retry-After %q: err = %v, slept %v", ra, err, total)
		}
	}
}

// TestWaitGatewayReadyStopsOnCancel pins that the default sleep honours ctx: a
// cancel during a long Retry-After wait returns promptly with the ctx error.
func TestWaitGatewayReadyStopsOnCancel(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		apiErr(w, 503, modelapi.CodeProxyUnavailable, modelapi.MsgProxyUnavailable)
	})
	cctx, cancel := context.WithCancel(ctx())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := f.client().WaitGatewayReady(cctx)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

// TestWaitGatewayReadyCountsPollTime pins that the 3-minute cap is on elapsed
// time, polls included: with a slow gateway the fake sleeps stop short of 3m.
func TestWaitGatewayReadyCountsPollTime(t *testing.T) {
	f := newFixture(t)
	f.mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(60 * time.Millisecond)
		w.Header().Set("Retry-After", "1")
		apiErr(w, 503, modelapi.CodeProxyUnavailable, modelapi.MsgProxyUnavailable)
	})
	var total time.Duration
	c := f.client(WithSleep(func(d time.Duration) { total += d }), withGatewayCap(time.Second))
	err := c.WaitGatewayReady(ctx())
	var gd *GatewayDownError
	if !errors.As(err, &gd) || total != time.Second {
		t.Fatalf("err = %v, slept %v; want GatewayDownError after one 1s sleep", err, total)
	}
}

// TestRedirectIsNeverFollowed pins that a 3xx cannot replay the bearer token
// and body to another host or port: the second server never sees the request
// and the caller gets *NotModelServerError.
func TestRedirectIsNeverFollowed(t *testing.T) {
	var hit atomic.Int32
	var gotAuth atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		gotAuth.Store(r.Header.Get("Authorization"))
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	c := New(Endpoint{URL: first.URL, Token: tok})
	var ne *NotModelServerError
	if _, err := c.Ping(ctx()); !errors.As(err, &ne) {
		t.Fatalf("ping err = %v, want *NotModelServerError", err)
	}
	if err := c.End(ctx(), "s"); err == nil {
		t.Error("End followed a redirect as success")
	}
	if hit.Load() != 0 {
		t.Errorf("the redirect target was reached %d times, Authorization %q", hit.Load(), gotAuth.Load())
	}
}

// TestHeartbeat410IsGoneWhateverTheBody pins that HTTP 410 alone means the
// session lapsed, even when the body is not an API error.
func TestHeartbeat410IsGoneWhateverTheBody(t *testing.T) {
	for _, body := range []string{"<html>gone</html>", `{"error":{"message":"m","code":"something_else"}}`, ""} {
		f := newFixture(t)
		f.mux.HandleFunc("POST /control/v1/sessions/s/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(410)
			_, _ = w.Write([]byte(body))
		})
		if known, err := f.client().Heartbeat(ctx(), "s", nil); known || err != nil {
			t.Errorf("body %q: known=%v err=%v", body, known, err)
		}
	}
}

// TestBadErrorBodiesStayOneLine pins that an error body that is not JSON, or
// carries no or an unknown code, still becomes a one-line error.
func TestBadErrorBodiesStayOneLine(t *testing.T) {
	for _, body := range []string{"<html>\nboom\x1b[2J</html>", `{"error":{"message":"` + `a\nb","code":""}}`, `{"error":{"message":"a\nb","code":"x\ny"}}`, `[1]`} {
		f := newFixture(t)
		f.mux.HandleFunc("GET /control/v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(body))
		})
		_, err := f.client().Catalog(ctx())
		if err == nil || !isOneLine(err.Error()) {
			t.Errorf("body %q: err = %q", body, err)
		}
		var re *modelapi.ResponseError
		if errors.As(err, &re) && !isOneLine(re.Code) && re.Code != "" {
			t.Errorf("body %q: code %q", body, re.Code)
		}
	}
}

// fillStrings sets every string in v (fields, slice elements, map keys and
// values, through pointers) to hostile text, growing empty slices, maps and
// nil pointers to one element so nothing escapes the walk.
func fillStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(hostile)
	case reflect.Ptr:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillStrings(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillStrings(v.Field(i))
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillStrings(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillStrings(k)
		fillStrings(e)
		v.SetMapIndex(k, e)
	}
}

// walkStrings reports every string under v that is not one printable line.
func walkStrings(t *testing.T, path string, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		if !isOneLine(v.String()) {
			t.Errorf("%s = %q is not one printable line", path, v.String())
		}
	case reflect.Ptr:
		if !v.IsNil() {
			walkStrings(t, path, v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkStrings(t, path+"."+v.Type().Field(i).Name, v.Field(i))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkStrings(t, path+"[]", v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkStrings(t, path+"{key}", k)
			walkStrings(t, path+"{val}", v.MapIndex(k))
		}
	}
}

// TestEveryDecodedStringIsCleaned fills every string of every response type
// with hostile text on the server side and walks what each client method
// returns, so a string field added later and not cleaned fails here. Ping is
// covered through its refusal text (its Server must equal wt-model to pass).
func TestEveryDecodedStringIsCleaned(t *testing.T) {
	filled := func(v any) any {
		fillStrings(reflect.ValueOf(v).Elem())
		return v
	}
	cat, row, job := filled(&modelapi.Catalog{}).(*modelapi.Catalog), filled(&modelapi.Row{}).(*modelapi.Row), filled(&modelapi.JobStatus{}).(*modelapi.JobStatus)
	cl, sp := filled(&modelapi.CandidateList{}).(*modelapi.CandidateList), filled(&modelapi.SpendReport{}).(*modelapi.SpendReport)
	cat.Schema = modelapi.SchemaVersion
	f := newFixture(t)
	serve := func(path string, v any) {
		f.mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) { writeJSONBody(w, 200, v) })
	}
	serve("/control/v1/catalog", cat)
	serve("/control/v1/resolve", row)
	serve("/control/v1/jobs/j", job)
	serve("/control/v1/jobs", modelapi.JobList{Jobs: []modelapi.JobStatus{*job}})
	serve("/control/v1/candidates", cl)
	serve("/control/v1/spend", sp)
	c := f.client()
	gotCat, err1 := c.Catalog(ctx())
	gotRow, err2 := c.Resolve(ctx(), "x")
	gotJob, err3 := c.Job(ctx(), "j")
	gotJobs, err4 := c.ActiveJobs(ctx(), "")
	gotCl, err5 := c.Candidates(ctx(), CandidateQuery{})
	gotSp, err6 := c.Spend(ctx(), time.Now(), time.Now())
	for i, err := range []error{err1, err2, err3, err4, err5, err6} {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	walkStrings(t, "Catalog", reflect.ValueOf(gotCat))
	walkStrings(t, "Row", reflect.ValueOf(gotRow))
	walkStrings(t, "Job", reflect.ValueOf(gotJob))
	walkStrings(t, "Jobs", reflect.ValueOf(gotJobs))
	walkStrings(t, "Candidates", reflect.ValueOf(gotCl))
	walkStrings(t, "Spend", reflect.ValueOf(gotSp))

	other := pingServer(t, `{"api_major":1,"schema":1,"server":"`+`a\nb\u001b[2J"}`)
	if _, err := other.Ping(ctx()); err == nil || !isOneLine(err.Error()) {
		t.Errorf("ping refusal = %q", err)
	}
}

// TestClientWithBadCAFileNeverDials proves a CA file that went bad after
// ResolveEndpoint checked it (removed, or replaced by bytes holding no
// certificate) fails every request of New's client with the CA file error:
// the ping and a gateway call alike, with no connection made (a counting
// listener accepts none, so no bearer token is written) and never a fall back
// to the system pool or the default client.
func TestClientWithBadCAFileNeverDials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var conns atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	dir := t.TempDir()
	garbage := dir + "/garbage.pem"
	if err := os.WriteFile(garbage, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, ca := range map[string]string{"removed": dir + "/gone.pem", "no certificate": garbage} {
		c := New(Endpoint{URL: "https://" + ln.Addr().String(), Token: tok, CAFile: ca})
		_, perr := c.Ping(context.Background())
		_, gerr := c.GatewayStatus(context.Background())
		for call, err := range map[string]error{"ping": perr, "gateway": gerr} {
			var cfe *modelping.CAFileError
			if !errors.As(err, &cfe) || cfe.Path != ca {
				t.Errorf("%s, %s: err = %v, want the CA file error naming %s", name, call, err, ca)
			}
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := conns.Load(); n != 0 {
		t.Errorf("the listener saw %d connections, want none", n)
	}
}
