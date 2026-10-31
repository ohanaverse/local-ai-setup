package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// readySpec is a command-json process spec shaped like rec's: readiness is
// `<bin> status` printing a JSON object whose "ready" is true.
var readySpec = provider.ProcessSpec{
	Binary: "recx",
	Ready: provider.Ready{
		Kind:      provider.ReadyCommandJSON,
		Command:   []string{"status"},
		Path:      "ready",
		Equals:    "true",
		ModelPath: "model",
	},
}

// stubReadySpec makes processSpecFor answer readySpec for the recx family.
func stubReadySpec(t *testing.T) {
	t.Helper()
	old := processSpecFor
	processSpecFor = func(family string) (provider.ProcessSpec, bool) {
		if family == "recx" {
			return readySpec, true
		}
		return old(family)
	}
	t.Cleanup(func() { processSpecFor = old })
}

// countingProc reports exited from the n-th call on (n == 0: never).
type countingProc struct {
	calls  int32
	exitAt int32
}

func (p *countingProc) exited() (bool, error) {
	n := atomic.AddInt32(&p.calls, 1)
	if p.exitAt > 0 && n >= p.exitAt {
		return true, errors.New("exit status 1")
	}
	return false, nil
}

// TestReadyNowCommandJSON verifies command-json readiness reads only the
// configured JSON value: true (or the text "true") is ready and names the
// model, while false, a failed command, non-JSON, a missing path or a
// command that outlives its timeout are all "not ready" with no error. A
// readiness check that panics or reports ready on garbage output would hand
// an agent a server that is still loading.
func TestReadyNowCommandJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		out   string
		err   error
		block bool
		want  readyState
	}{
		"ready":         {out: `{"ready":true,"model":"flint"}`, want: readyState{Ready: true, Model: "flint"}},
		"not ready":     {out: `{"ready":false}`},
		"text true":     {out: `{"ready":"true"}`, want: readyState{Ready: true}},
		"exit error":    {out: "", err: errors.New("exit status 1")},
		"error w/ json": {out: `{"ready":true}`, err: errors.New("exit status 1")},
		"not json":      {out: "not json"},
		"empty object":  {out: `{}`},
		"array":         {out: `[true]`},
		"object value":  {out: `{"ready":{"x":1}}`},
		"timeout":       {block: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := testEnv()
			e.readyCmdTimeout = 20 * time.Millisecond
			var gotName string
			var gotArgs []string
			e.runOut = func(ctx context.Context, n string, args ...string) ([]byte, error) {
				gotName, gotArgs = n, args
				if _, ok := ctx.Deadline(); !ok {
					t.Error("readiness command ran without a deadline")
				}
				if tc.block {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []byte(tc.out), tc.err
			}
			got, err := e.readyNow(context.Background(), readySpec, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "")
			if err != nil {
				t.Fatalf("readyNow err = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("readyNow = %+v, want %+v", got, tc.want)
			}
			if gotName != "/bin/recx" || strings.Join(gotArgs, " ") != "status" {
				t.Errorf("ran %q %q, want /bin/recx [status]", gotName, gotArgs)
			}
		})
	}
}

// TestReadyNowModelsListIgnoresUnloadedEntries verifies models-list readiness
// does not count a listed-but-unloaded entry as ready (sushi lists every
// pack), while a loaded entry, or one with no flag, is ready and names the
// listed id. Reading an unloaded pack as ready would hand an agent a server
// that cannot answer yet.
func TestReadyNowModelsListIgnoresUnloadedEntries(t *testing.T) {
	spec := provider.ProcessSpec{Binary: "listx", Ready: provider.Ready{Kind: provider.ReadyModelsList}}
	for name, tc := range map[string]struct {
		body string
		want readyState
	}{
		"unloaded": {`{"data":[{"id":"org/Qwen","loaded":false}]}`, readyState{}},
		"loaded":   {`{"data":[{"id":"org/Qwen","loaded":true}]}`, readyState{Ready: true, Model: "org/Qwen"}},
		"absent":   {`{"data":[{"id":"org/Qwen"}]}`, readyState{Ready: true, Model: "org/Qwen"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
			defer srv.Close()
			got, err := testEnv().readyNow(context.Background(), spec, "", srv.URL, "Qwen", "")
			if err != nil || got != tc.want {
				t.Errorf("readyNow = %+v, %v; want %+v, nil", got, err, tc.want)
			}
		})
	}
}

// TestReadyNowReadsTheEndpoint verifies a ready answer carries the instance's
// endpoint from the spec's EndpointPath, and "" when the JSON lacks it or the
// spec names no path. A start compares that endpoint with the registry's
// address before it adopts an instance; a wrong or invented one would adopt a
// server the routes do not point at.
func TestReadyNowReadsTheEndpoint(t *testing.T) {
	withPath := readySpec
	withPath.Ready.EndpointPath = "endpoint"
	full := `{"ready":true,"model":"flint","endpoint":"http://127.0.0.1:9/v1"}`
	for name, tc := range map[string]struct {
		spec provider.ProcessSpec
		out  string
		want readyState
	}{
		"endpoint":       {spec: withPath, out: full, want: readyState{Ready: true, Model: "flint", Endpoint: "http://127.0.0.1:9/v1"}},
		"absent in json": {spec: withPath, out: `{"ready":true,"model":"flint"}`, want: readyState{Ready: true, Model: "flint"}},
		"no path":        {spec: readySpec, out: full, want: readyState{Ready: true, Model: "flint"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := testEnv()
			e.runOut = func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.out), nil }
			got, err := e.readyNow(context.Background(), tc.spec, "/bin/recx", "", "flint", "")
			if err != nil || got != tc.want {
				t.Errorf("readyNow = %+v, %v; want %+v, nil", got, err, tc.want)
			}
		})
	}
}

// TestWaitReadyKeepsWaitingWhilePortClosed verifies a server still loading
// (its readiness command says not ready, its process alive) is waited for,
// not failed: rec keeps its port closed for ~10 s while loading, and treating
// that as failure would kill every rec start.
func TestWaitReadyKeepsWaitingWhilePortClosed(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	var runs int32
	e.runOut = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		if atomic.AddInt32(&runs, 1) < 3 {
			return []byte("rec: no managed rec instance is reachable"), errors.New("exit status 1")
		}
		return []byte(`{"ready":true,"model":"flint"}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", time.Second)
	if err != nil {
		t.Fatalf("waitReady = %v, want nil", err)
	}
	if runs != 3 {
		t.Errorf("readiness command ran %d times, want 3", runs)
	}
}

// TestWaitReadyFailsFastWhenProcessDies verifies the wait ends the moment
// the spawned process has exited, with the "exited during model load" error,
// instead of polling out the whole load budget for a server that is gone.
func TestWaitReadyFailsFastWhenProcessDies(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	var runs int32
	e.runOut = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		atomic.AddInt32(&runs, 1)
		return []byte(`{"ready":false}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{exitAt: 2}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited during model load") {
		t.Fatalf("waitReady = %v, want an exited-during-load error", err)
	}
	if runs >= 10 {
		t.Errorf("readiness command ran %d times after the process died, want fewer than 10", runs)
	}
}

// TestWaitReadyRejectsAnInstanceServingAnotherModel verifies a readiness
// command that reports ready for a different model does not satisfy the
// wait: it times out naming the model the instance serves, while the same
// command naming the requested model still succeeds. A machine-scope
// family's readiness command answers for whatever instance holds the
// machine, so a foreign instance finishing its own load must not read as
// the server wt spawned being ready.
func TestWaitReadyRejectsAnInstanceServingAnotherModel(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	e.runOut = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		return []byte(`{"ready":true,"model":"other"}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "recx reports an instance serving other, not flint") {
		t.Fatalf("waitReady = %v, want a timeout naming the other model", err)
	}
	e.runOut = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		return []byte(`{"ready":true,"model":"flint"}`), nil
	}
	if err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", time.Second); err != nil {
		t.Errorf("waitReady for the matching model = %v, want nil", err)
	}
}

// TestWaitReadyTimeout verifies a server that never becomes ready ends the
// wait at the deadline with an error naming the family, and that a cancelled
// context ends it with the context's error: a start must not hang forever.
func TestWaitReadyTimeout(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	e.runOut = func(ctx context.Context, n string, args ...string) ([]byte, error) {
		return []byte(`{"ready":false}`), nil
	}
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "recx") {
		t.Fatalf("waitReady = %v, want a timeout naming recx", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.waitReady(ctx, "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("waitReady after cancel = %v, want context.Canceled", err)
	}
}

// TestWaitReadyTreatsAReadyAnswerWithNoModelAsNotReady verifies a ready
// answer that names no model, from a spec that reads one, does not end the
// wait: an instance mid-transition may serve something else, and warming the
// wrong model is worse than a timeout that says what was seen.
func TestWaitReadyTreatsAReadyAnswerWithNoModelAsNotReady(t *testing.T) {
	stubReadySpec(t)
	e := testEnv()
	e.runOut = func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"ready":true}`), nil }
	err := e.waitReady(context.Background(), "recx", &countingProc{}, "/bin/recx", "http://127.0.0.1:1/v1/models", "flint", "", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "names no model") {
		t.Fatalf("waitReady = %v, want a timeout saying the instance names no model", err)
	}
}

// TestReadyNowIgnoresStderrNoise verifies a readiness command that prints a
// warning or update hint on stderr ahead of its JSON still reads ready: the
// check parses stdout alone (#458). With the combined output the engine used
// to parse, that stderr text broke the JSON, so a live rec read as not ready:
// `start` spawned a second instance, `stop` reported a running one stopped,
// and a healthy start timed out. It runs a real script through the real
// runner, since only a real process has two streams.
func TestReadyNowIgnoresStderrNoise(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "recx")
	script := "#!/bin/sh\necho 'warning: update available' >&2\necho '{\"ready\":true,\"model\":\"flint\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := testEnv()
	e.runOut = runCommandStdout
	e.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Error("readiness read the combined-output runner")
		return nil, nil
	}
	got, err := e.readyNow(context.Background(), readySpec, bin, "", "flint", "")
	if want := (readyState{Ready: true, Model: "flint"}); err != nil || got != want {
		t.Errorf("readyNow = %+v, %v; want %+v, nil", got, err, want)
	}
}

// TestRunCommandStdoutDropsStderr verifies the stdout-only runner returns
// stdout alone on success and on a nonzero exit (the exit error still
// surfaces), because the readiness check relies on neither stream leaking
// into the other.
func TestRunCommandStdoutDropsStderr(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho out\necho err >&2\n[ \"$1\" = fail ] && exit 3\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runCommandStdout(context.Background(), bin)
	if err != nil || string(out) != "out\n" {
		t.Errorf("ok run = %q, %v; want %q, nil", out, err, "out\n")
	}
	out, err = runCommandStdout(context.Background(), bin, "fail")
	if err == nil || string(out) != "out\n" {
		t.Errorf("failing run = %q, %v; want %q and an error", out, err, "out\n")
	}
}

// httpSpec is an http-get process spec shaped like sushi's: ready as soon as
// GET /health answers 200, whether or not any model is loaded.
var httpSpec = provider.ProcessSpec{
	Binary: "httpx",
	Ready:  provider.Ready{Kind: provider.ReadyHTTP, Path: "/health"},
}

// stubHTTPSpec makes processSpecFor answer httpSpec for the httpx family.
func stubHTTPSpec(t *testing.T) {
	t.Helper()
	old := processSpecFor
	processSpecFor = func(family string) (provider.ProcessSpec, bool) {
		if family == "httpx" {
			return httpSpec, true
		}
		return old(family)
	}
	t.Cleanup(func() { processSpecFor = old })
}

// TestReadyNowHTTPGet verifies http-get readiness is exactly "GET <Path> on
// the models URL's host answers 200": ready even when /v1/models lists
// nothing, not ready on any other status or when only /v1/models is served,
// not ready with a nil error when the port is closed, and the models URL's own
// path and query never reach the request. A server that answers /health long
// before it loads a model (sushi) must read as ready, and a still-starting one
// must not abort the wait.
func TestReadyNowHTTPGet(t *testing.T) {
	var gotPath, gotQuery string
	health := http.StatusOK
	serveHealth := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" || !serveHealth {
			http.NotFound(w, r)
			return
		}
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.WriteHeader(health)
	}))
	defer srv.Close()
	e := testEnv()
	ask := func(url string) readyState {
		t.Helper()
		got, err := e.readyNow(context.Background(), httpSpec, "/bin/httpx", url, "m", "")
		if err != nil {
			t.Fatalf("readyNow err = %v, want nil", err)
		}
		return got
	}
	if got := ask(srv.URL + "/v1/models?x=1#frag"); !got.Ready {
		t.Errorf("/health 200: readyNow = %+v, want ready", got)
	}
	if gotPath != "/health" || gotQuery != "" {
		t.Errorf("requested %q?%q, want /health with no query", gotPath, gotQuery)
	}
	health = http.StatusServiceUnavailable
	if got := ask(srv.URL + "/v1/models"); got.Ready {
		t.Errorf("/health 503: readyNow = %+v, want not ready", got)
	}
	serveHealth = false
	if got := ask(srv.URL + "/v1/models"); got.Ready {
		t.Errorf("/health 404: readyNow = %+v, want not ready", got)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL + "/v1/models"
	closed.Close()
	if got := ask(url); got.Ready {
		t.Errorf("closed port: readyNow = %+v, want not ready", got)
	}
}

// TestReadyNowHTTPGetSendsTheKey verifies the http-get probe sends
// Authorization: Bearer <key> only when the registry names a key, and reads a
// 401 to a keyed probe as a *KeyRefusedError like the models-list case. A key
// leaked to (or missing from) the health path would break authenticated
// servers or silently probe them as anonymous.
func TestReadyNowHTTPGetSendsTheKey(t *testing.T) {
	var auth string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	e := testEnv()
	for key, want := range map[string]string{"k": "Bearer k", "": ""} {
		auth = "unset"
		if _, err := e.readyNow(context.Background(), httpSpec, "/bin/httpx", srv.URL+"/v1/models", "m", key); err != nil {
			t.Fatalf("key %q: readyNow err = %v", key, err)
		}
		if auth != want {
			t.Errorf("key %q: Authorization = %q, want %q", key, auth, want)
		}
	}
	status = http.StatusUnauthorized
	_, err := e.readyNow(context.Background(), httpSpec, "/bin/httpx", srv.URL+"/v1/models", "m", "k")
	var kre *KeyRefusedError
	if !errors.As(err, &kre) {
		t.Errorf("401 with a key: err = %v, want *KeyRefusedError", err)
	}
	if got, err := e.readyNow(context.Background(), httpSpec, "/bin/httpx", srv.URL+"/v1/models", "m", ""); err != nil || got.Ready {
		t.Errorf("401 without a key: got %+v, %v; want not ready, nil", got, err)
	}
}

// TestWaitReadyHTTPGetPollsUntil200 verifies waitReady polls an http-get
// family through 503s and returns nil at the first 200, and ends at once with
// the "exited during model load" error when the process dies while /health
// still answers 503. Without it a crashed server would be polled for the
// whole warmup budget.
func TestWaitReadyHTTPGetPollsUntil200(t *testing.T) {
	stubHTTPSpec(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	e := testEnv()
	err := e.waitReady(context.Background(), "httpx", &countingProc{}, "/bin/httpx", srv.URL+"/v1/models", "m", "", time.Second)
	if err != nil {
		t.Fatalf("waitReady = %v, want nil", err)
	}
	if hits != 3 {
		t.Errorf("/health hit %d times, want 3", hits)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	err = e.waitReady(context.Background(), "httpx", &countingProc{exitAt: 2}, "/bin/httpx", down.URL+"/v1/models", "m", "", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "serve process exited during model load") {
		t.Errorf("waitReady = %v, want an exited-during-load error", err)
	}
}
