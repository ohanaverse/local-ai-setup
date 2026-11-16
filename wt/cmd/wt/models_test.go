package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// fakeModelServer is the only network a cmd/wt test may reach: a wt-model on
// httptest (a loopback port the OS picks) that serves ping, catalog, resolve,
// jobs and sessions, and records every request it gets.
type fakeModelServer struct {
	*httptest.Server
	mu       sync.Mutex
	Token    string
	Rows     []modelapi.Row
	APIMajor int
	Schema   int
	// Status, when not 0, answers every control call with it (401 for a wrong
	// token); Reqs is "METHOD path?query" per request, in order.
	Status int
	Reqs   []string

	// Starts records every POST start body, in order. Outcome, when set,
	// is the final state of the job the n-th start (from 0) creates; nil
	// succeeds. A job reads running (with Step) on its first poll and its
	// outcome on the next; Hang keeps every job running for ever.
	Starts  []modelapi.StartRequest
	Outcome func(n int, req modelapi.StartRequest) *modelapi.Problem
	Step    string
	Hang    bool
	// OnPoll runs (unlocked) before each job poll is answered.
	OnPoll func()
	// Registers records the model set of every POST sessions; RegisterStatus
	// and BeatStatus, when not 0, fail those calls with that status.
	Registers      [][]string
	RegisterStatus int
	BeatStatus     int
	// Stops records every POST stop body, in order. StopOutcome, when set, is
	// the final problem of the n-th stop's job (nil succeeds); StopStep is the
	// step a succeeded stop job reads (a still-finishing stop sets it).
	Stops       []modelapi.StopRequest
	StopOutcome func(n int, req modelapi.StopRequest) *modelapi.Problem
	StopStep    string
	// ActiveStart, when set, is the id of a start job already running for
	// every model: GET jobs lists it and POST start answers its id (the
	// server's one-job-per-model submit) instead of creating a job.
	ActiveStart string
	// Fixed serves these job statuses verbatim on GET jobs/{id}. A cancel of
	// one makes later polls read it running for CancelRunning polls, then
	// cancelled.
	Fixed         map[string]modelapi.JobStatus
	CancelRunning int
	// FailPollAfterCancel makes a cancelled fixed job's polls answer a 500.
	FailPollAfterCancel bool
	Cancels             []string
	// Log, when set, gets each request line appended as it arrives (a child
	// process's fake reports to its parent through it).
	Log string

	// Generated, Providers and Pricing fill the catalog document's own
	// members. Spend is the report GET spend answers; SpendCode, when set,
	// answers it with a 503 carrying that code instead. ClockSkew is how far
	// this server's clock runs from the real one: every response's Date
	// header says so, which is how a client learns the server's time.
	Generated string
	Providers map[string]string
	Pricing   *modelapi.Pricing
	Spend     modelapi.SpendReport
	SpendCode string
	ClockSkew time.Duration

	polls     map[string]int
	jobs      map[string]*modelapi.Problem
	kinds     map[string]string
	cancelled map[string]int
}

// reqs is a copy of Reqs, taken under the lock.
func (f *fakeModelServer) reqs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Reqs...)
}

// count is how many recorded requests start with prefix.
func (f *fakeModelServer) count(prefix string) int {
	n := 0
	for _, r := range f.reqs() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func newFakeModelServer(t *testing.T, rows ...modelapi.Row) *fakeModelServer {
	t.Helper()
	f := &fakeModelServer{Token: "tok", Rows: rows, APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion,
		polls: map[string]int{}, jobs: map[string]*modelapi.Problem{}, kinds: map[string]string{}, cancelled: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeModelServer) apiErr(w http.ResponseWriter, code int, c, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": c, "message": msg}})
}

func (f *fakeModelServer) serve(w http.ResponseWriter, r *http.Request) {
	line := r.Method + " " + r.URL.RequestURI()
	f.mu.Lock()
	f.Reqs = append(f.Reqs, line)
	if f.Log != "" {
		if lf, err := os.OpenFile(f.Log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
			fmt.Fprintln(lf, line)
			lf.Close()
		}
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.ClockSkew != 0 {
		w.Header().Set("Date", time.Now().Add(f.ClockSkew).UTC().Format(http.TimeFormat))
	}
	if r.Header.Get("Authorization") != "Bearer "+f.Token || f.Status == http.StatusUnauthorized {
		f.apiErr(w, 401, modelapi.CodeUnauthorized, modelapi.MsgUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/control/v1/ping":
		_ = json.NewEncoder(w).Encode(modelapi.Ping{APIMajor: f.APIMajor, Schema: f.Schema, Server: "wt-model"})
	case "/control/v1/catalog":
		_ = json.NewEncoder(w).Encode(modelapi.Catalog{Schema: f.Schema, Generated: f.Generated, Providers: f.Providers, Rows: f.Rows, Pricing: f.Pricing})
	case "/control/v1/spend":
		if f.SpendCode != "" {
			f.apiErr(w, http.StatusServiceUnavailable, f.SpendCode, "the LiteLLM database is not reachable")
			return
		}
		rep := f.Spend
		if rep.Rows == nil {
			rep.Rows = []modelapi.SpendRow{}
		}
		_ = json.NewEncoder(w).Encode(rep)
	case "/control/v1/resolve":
		id := r.URL.Query().Get("model")
		for _, row := range f.Rows {
			if row.ID == id {
				_ = json.NewEncoder(w).Encode(row)
				return
			}
		}
		f.apiErr(w, 404, modelapi.CodeUnknownModel, fmt.Sprintf("unknown model %q on the server", id))
	case "/control/v1/jobs":
		jobs := []modelapi.JobStatus{}
		if f.ActiveStart != "" {
			jobs = append(jobs, modelapi.JobStatus{ID: f.ActiveStart, Kind: "start", Model: r.URL.Query().Get("model"), State: modelapi.JobRunning})
		}
		_ = json.NewEncoder(w).Encode(modelapi.JobList{Jobs: jobs})
	case "/control/v1/stop":
		var req modelapi.StopRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		n := len(f.Stops)
		f.Stops = append(f.Stops, req)
		var p *modelapi.Problem
		if f.StopOutcome != nil {
			p = f.StopOutcome(n, req)
		}
		id := fmt.Sprintf("stop-%d", n)
		f.jobs[id], f.kinds[id] = p, "stop"
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(modelapi.JobRef{ID: id})
	case "/control/v1/start":
		var req modelapi.StartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		n := len(f.Starts)
		f.Starts = append(f.Starts, req)
		if f.ActiveStart != "" {
			if _, ok := f.jobs[f.ActiveStart]; !ok {
				f.jobs[f.ActiveStart] = nil
			}
			f.mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(modelapi.JobRef{ID: f.ActiveStart})
			return
		}
		var p *modelapi.Problem
		if f.Outcome != nil {
			p = f.Outcome(n, req)
		}
		id := fmt.Sprintf("job-%d", n)
		f.jobs[id] = p
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(modelapi.JobRef{ID: id})
	case "/control/v1/sessions":
		var reg modelapi.SessionRegister
		_ = json.NewDecoder(r.Body).Decode(&reg)
		f.mu.Lock()
		f.Registers = append(f.Registers, reg.Models)
		status := f.RegisterStatus
		f.mu.Unlock()
		if status != 0 {
			f.apiErr(w, status, "failed", "register failed")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	default:
		switch {
		case strings.HasPrefix(r.URL.Path, "/control/v1/jobs/") && strings.HasSuffix(r.URL.Path, "/cancel"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/control/v1/jobs/"), "/cancel")
			f.mu.Lock()
			_, fixed := f.Fixed[id]
			_, made := f.jobs[id]
			f.Cancels = append(f.Cancels, id)
			if fixed {
				f.cancelled[id] = 0
			}
			f.mu.Unlock()
			if !fixed && !made {
				f.apiErr(w, 404, modelapi.CodeUnknownJob, "no such job")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/control/v1/jobs/"):
			f.poll(w, strings.TrimPrefix(r.URL.Path, "/control/v1/jobs/"))
		case strings.HasSuffix(r.URL.Path, "/heartbeat") && f.BeatStatus != 0:
			f.apiErr(w, f.BeatStatus, "failed", "heartbeat failed")
		case strings.HasPrefix(r.URL.Path, "/control/v1/sessions/"):
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			f.apiErr(w, 404, modelapi.CodeNotFound, modelapi.MsgNotFound)
		}
	}
}

// poll answers GET jobs/{id}: running with Step on the first poll (and on
// every poll while Hang), then the job's outcome.
func (f *fakeModelServer) poll(w http.ResponseWriter, id string) {
	if f.OnPoll != nil {
		f.OnPoll()
	}
	f.mu.Lock()
	if fx, ok := f.Fixed[id]; ok {
		if n, was := f.cancelled[id]; was {
			if f.FailPollAfterCancel {
				f.mu.Unlock()
				f.apiErr(w, 500, modelapi.CodeFailed, "poll failed")
				return
			}
			f.cancelled[id] = n + 1
			if n < f.CancelRunning {
				fx.State, fx.Step, fx.Result = modelapi.JobRunning, "cancelling", nil
			} else {
				fx.State, fx.Step = modelapi.JobCancelled, "cancelled"
				fx.Result = &modelapi.JobResult{Error: &modelapi.Problem{Code: modelapi.CodeCancelled, Message: "cancelled"}}
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(fx)
		return
	}
	p, ok := f.jobs[id]
	f.polls[id]++
	first := f.polls[id] == 1
	hang, step, kind, stopStep := f.Hang, f.Step, f.kinds[id], f.StopStep
	f.mu.Unlock()
	if kind == "" {
		kind = "start"
	}
	if !ok {
		f.apiErr(w, 404, modelapi.CodeUnknownJob, "unknown job")
		return
	}
	js := modelapi.JobStatus{ID: id, Kind: kind, State: modelapi.JobRunning, Step: step}
	switch {
	case first || hang:
	case p == nil:
		js.State, js.Result = modelapi.JobSucceeded, &modelapi.JobResult{OK: true}
		if kind == "stop" {
			js.Step = stopStep
		}
	default:
		js.State, js.Result = modelapi.JobFailed, &modelapi.JobResult{Error: p}
	}
	_ = json.NewEncoder(w).Encode(js)
}

// wireRow is a registered wire row of the given id and action, in order n.
func wireRow(id, action string, n int) modelapi.Row {
	return modelapi.Row{
		ID: id, GatewayID: id, ProviderID: "openrouter", ModelName: id, Family: "f",
		Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning,
		Action: action, Order: n,
	}
}

// pointAtFake writes a config.toml in the isolated config home whose
// [model-server] names f, with its token in a 0600 file, and returns a fresh app.
func pointAtFake(t *testing.T, f *fakeModelServer, extra string) *app {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(home, "token")
	if err := os.WriteFile(tok, []byte(f.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("default_tag = \"code\"\n\n[model-server]\nurl = %q\ntoken_file = %q\n%s", f.URL, tok, extra)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &app{cfg: &config.Config{DefaultTag: "code"}}
}

// TestEndpointConfiguredFromConfigOrEnv proves endpointConfigured is true for
// a [model-server] url or token_file in config.toml and for each WT_MODEL_*
// variable, and false when none is set.
func TestEndpointConfiguredFromConfigOrEnv(t *testing.T) {
	cfgHome := func(t *testing.T, body string) {
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", home)
		_ = os.MkdirAll(filepath.Join(home, "agent-wt"), 0o755)
		if err := os.WriteFile(filepath.Join(home, "agent-wt", "config.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, toml, env, val string
		want                 bool
	}{
		{"nothing", "default_tag = \"code\"\n", "", "", false},
		{"url", "[model-server]\nurl = \"http://127.0.0.1:1\"\n", "", "", true},
		{"token_file", "[model-server]\ntoken_file = \"~/t\"\n", "", "", true},
		{"env url", "", "WT_MODEL_URL", "http://127.0.0.1:1", true},
		{"env token file", "", "WT_MODEL_TOKEN_FILE", "/x", true},
		{"env token", "", "WT_MODEL_TOKEN", "t", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome(t, tc.toml)
			if tc.env != "" {
				t.Setenv(tc.env, tc.val)
			}
			if got := (&app{}).endpointConfigured(); got != tc.want {
				t.Errorf("endpointConfigured = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSessionCfgNeedsNoRegistry proves sessionCfg loads with no registry.toml
// in the config home, synthesizes the driver agents, and caches its answer.
func TestSessionCfgNeedsNoRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "agent-wt"), 0o755)
	if err := os.WriteFile(filepath.Join(home, "agent-wt", "config.toml"), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &app{}
	cfg, err := a.sessionCfg()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 0 {
		t.Errorf("models = %d, want none", len(cfg.Models))
	}
	if _, err := cfg.AgentByName("claude"); err != nil {
		t.Errorf("claude not synthesized: %v", err)
	}
	if again, _ := a.sessionCfg(); again != cfg {
		t.Error("sessionCfg is not cached")
	}
}

// TestAppCfgIsStillTheRegistryUntilTheFlip proves that, with an endpoint
// configured, newApp still loads the registry into a.cfg and that sessionCfg
// does not replace it: Task 10 changes no host command.
func TestAppCfgIsStillTheRegistryUntilTheFlip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "agent-wt")
	_ = os.MkdirAll(dir, 0o755)
	t.Setenv("WT_MODEL_URL", "http://127.0.0.1:1")
	reg := filepath.Join(t.TempDir(), "registry.toml")
	t.Setenv("WT_REGISTRY", reg)
	if err := os.WriteFile(reg, []byte(minimalRegistryTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	before := a.cfg
	if len(a.cfg.Models) == 0 {
		t.Fatal("a.cfg.Models is empty: the registry is no longer loaded")
	}
	if _, err := a.sessionCfg(); err != nil {
		t.Fatal(err)
	}
	if a.cfg != before || len(a.cfg.Models) == 0 {
		t.Error("sessionCfg replaced a.cfg")
	}
}

const minimalRegistryTOML = `
[[providers]]
id = "openrouter"
type = "openrouter"
name = "OpenRouter"
location = "cloud"

[[models]]
id = "openrouter/x/y"
provider_id = "openrouter"
model_name = "x/y"
location = "cloud"
family = "f"
tags = ["code"]
`

// TestOpenModelsWrapsEveryFailureWithStill proves every way openModels can
// fail (nothing configured, unreachable, a wrong token, API skew, a token in
// config.toml) says what did not happen, ending the
// text with the caller's clause, and keeps the typed cause for errors.As.
func TestOpenModelsWrapsEveryFailureWithStill(t *testing.T) {
	const still = "nothing was started"
	ends := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.HasSuffix(err.Error(), "; "+still) {
			t.Fatalf("err = %v, want text ending %q", err, still)
		}
	}
	t.Run("not configured", func(t *testing.T) {
		pointAtFake(t, newFakeModelServer(t), "")
		home := os.Getenv("XDG_CONFIG_HOME")
		_ = os.WriteFile(filepath.Join(home, "agent-wt", "config.toml"), []byte("default_tag = \"code\"\n"), 0o644)
		_, err := (&app{}).openModels(context.Background(), still)
		ends(t, err)
		var nc *modelclient.NotConfiguredError
		if !errors.As(err, &nc) {
			t.Errorf("err = %T, want *NotConfiguredError", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		f := newFakeModelServer(t)
		a := pointAtFake(t, f, "")
		f.Close()
		_, err := a.openModels(context.Background(), still)
		ends(t, err)
		var ue *modelclient.UnreachableError
		if !errors.As(err, &ue) {
			t.Errorf("err = %T, want *UnreachableError", err)
		}
	})
	t.Run("wrong token", func(t *testing.T) {
		f := newFakeModelServer(t)
		a := pointAtFake(t, f, "")
		f.Token = "other"
		_, err := a.openModels(context.Background(), still)
		ends(t, err)
		var ue *modelclient.UnauthorizedError
		if !errors.As(err, &ue) {
			t.Errorf("err = %T, want *UnauthorizedError", err)
		}
	})
	t.Run("skew", func(t *testing.T) {
		f := newFakeModelServer(t)
		f.APIMajor = modelapi.APIMajor + 1
		a := pointAtFake(t, f, "")
		_, err := a.openModels(context.Background(), still)
		ends(t, err)
		var se *modelclient.SkewError
		if !errors.As(err, &se) {
			t.Errorf("err = %T, want *SkewError", err)
		}
	})
	t.Run("inline token", func(t *testing.T) {
		f := newFakeModelServer(t)
		a := pointAtFake(t, f, "token = \"secret\"\n")
		_, err := a.openModels(context.Background(), still)
		ends(t, err)
		if !strings.Contains(err.Error(), "token_file") || strings.Contains(err.Error(), "secret") {
			t.Errorf("err = %v, want the problem text without the value", err)
		}
	})
}

// TestOpenModelsCachesTheHandle proves a successful openModels pings once and
// a second call returns the same handle without another request.
func TestOpenModelsCachesTheHandle(t *testing.T) {
	f := newFakeModelServer(t)
	a := pointAtFake(t, f, "")
	m1, err := a.openModels(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	n := len(f.Reqs)
	m2, err := a.openModels(context.Background(), "x")
	if err != nil || m1 != m2 || len(f.Reqs) != n {
		t.Errorf("second call: same=%v reqs %d -> %d err=%v", m1 == m2, n, len(f.Reqs), err)
	}
	if m1.ep.URL != f.URL {
		t.Errorf("endpoint url = %q, want %q", m1.ep.URL, f.URL)
	}
}

// TestRemoteExitTable pins the one exit-code mapping: a declined question is
// 3, a started thing that did not finish is 2, everything else is 1, wrapped
// errors map through errors.As, and the error text is not changed.
func TestRemoteExitTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"declined", &modelclient.DeclinedError{What: "started"}, 3},
		{"job", &modelclient.JobError{Problem: modelapi.Problem{Code: "failed", Message: "boom"}}, 2},
		{"lost server", &modelclient.LostServerError{JobID: "j", Addr: "a"}, 2},
		{"job lost", &modelclient.JobLostError{JobID: "j"}, 2},
		{"detached", &modelclient.DetachedError{JobID: "j"}, 2},
		{"wrapped declined", fmt.Errorf("ctx: %w", &modelclient.DeclinedError{What: "stopped"}), 3},
		{"still-wrapped job", modelclient.Still(&modelclient.JobError{}, "x"), 2},
		{"not configured", &modelclient.NotConfiguredError{MissingURL: true}, 1},
		{"unreachable", &modelclient.UnreachableError{Addr: "a", Reason: "r"}, 1},
		{"unauthorized", &modelclient.UnauthorizedError{Addr: "a"}, 1},
		{"skew", &modelclient.SkewError{Addr: "a"}, 1},
		{"unknown model", &modelclient.UnknownModelError{Message: "m"}, 1},
		{"plain", errors.New("x"), 1},
		{"schema", &modelclient.SchemaError{Addr: "a", Server: 2, Client: 1}, 1},
		{"not a model server", &modelclient.NotModelServerError{Addr: "a", Why: "w"}, 1},
		{"gateway down", &modelclient.GatewayDownError{Addr: "a"}, 1},
		{"token file", &tokenfile.Error{Path: "/p", Kind: tokenfile.KindLoose, Detail: "0644"}, 1},
		{"session token error", modelclient.SessionTokenError(&tokenfile.Error{Path: "/p", Kind: tokenfile.KindLoose, Detail: "0644"}), 1},
		{"still-wrapped not configured", modelclient.Still(&modelclient.NotConfiguredError{MissingURL: true, MissingToken: true}, "x"), 1},
		{"canceled", context.Canceled, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := remoteExit(tc.err)
			if exitcode.Of(got) != tc.want {
				t.Errorf("code = %d, want %d", exitcode.Of(got), tc.want)
			}
			if got.Error() != tc.err.Error() {
				t.Errorf("text changed: %q vs %q", got.Error(), tc.err.Error())
			}
			if exitcode.IsQuiet(got) {
				t.Error("quiet: the error would never be printed")
			}
		})
	}
	if remoteExit(nil) != nil {
		t.Error("remoteExit(nil) != nil")
	}
	var c int
	var once error = exitcode.New(2, errors.New("x"), false)
	if c = exitcode.Of(remoteExit(once)); c != 2 {
		t.Errorf("an already coded error became %d", c)
	}
}

// TestAgentDriverNamesSkipsCommands proves command agents (no model layer) are
// not synthesized as agents.
func TestAgentDriverNamesSkipsCommands(t *testing.T) {
	names := agentDriverNames()
	if len(names) == 0 {
		t.Fatal("no driver names")
	}
	for _, n := range names {
		if agents.IsCommand(n) {
			t.Errorf("%q is a command agent", n)
		}
	}
}

// loopbackOnlyModelClient is TestMain's newModelClient: a client whose
// transport refuses to dial any address that is not loopback.
func loopbackOnlyModelClient(ep modelclient.Endpoint) *modelclient.Client {
	d := &net.Dialer{}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			return nil, fmt.Errorf("cmd/wt tests dial loopback only, not %q", addr)
		}
		return d.DialContext(ctx, network, addr)
	}}
	return modelclient.New(ep, modelclient.WithHTTPClient(&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
}

// TestModelClientSeamRefusesNonLoopback pins TestMain's guard: the client a
// test gets cannot dial an address off this machine.
func TestModelClientSeamRefusesNonLoopback(t *testing.T) {
	c := newModelClient(modelclient.Endpoint{URL: "http://192.0.2.1:9", Token: "t"})
	_, err := c.Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "loopback only") {
		t.Errorf("err = %v, want the loopback refusal", err)
	}
}

// TestEndpointConfiguredInlineTokenOnlyAndLoadFailure proves a [model-server]
// holding only an inline token counts as configured (so openModels can report
// the problem), that a config.toml that fails to load counts as configured
// (the error surfaces from openModels instead of a quiet legacy path), and
// that openModels then reports each.
func TestEndpointConfiguredInlineTokenOnlyAndLoadFailure(t *testing.T) {
	write := func(t *testing.T, body string) {
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", home)
		_ = os.MkdirAll(filepath.Join(home, "agent-wt"), 0o755)
		if err := os.WriteFile(filepath.Join(home, "agent-wt", "config.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(t, "[model-server]\ntoken = \"secret\"\n")
	a := &app{}
	if !a.endpointConfigured() {
		t.Error("inline token only: endpointConfigured = false")
	}
	if _, err := a.openModels(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "token_file") {
		t.Errorf("openModels err = %v, want the inline-token problem", err)
	}
	write(t, "this is [not toml\n")
	b := &app{}
	if !b.endpointConfigured() {
		t.Error("unloadable config: endpointConfigured = false")
	}
	if _, err := b.openModels(context.Background(), "x"); err == nil {
		t.Error("unloadable config: openModels err = nil")
	}
}
