package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// These tests drive the real rec rows of the provider table (no synthetic
// spec): `rec serve --port {port}`, `rec stop`, and `rec status` JSON with
// ready, model and endpoint. Nothing here runs rec or dials a port other
// than the test's own httptest server.

// recHarness is a fake rec: the registry's rec provider points at addr, where
// an httptest server that wants `Authorization: Bearer k` comes up only when
// the stubbed spawn runs (the port must be free for the pre-spawn check, as
// it is before a real `rec serve`). It records every command and spawn in
// order ("status", "stop", "spawn") with the spawn's argv.
type recHarness struct {
	mu        sync.Mutex
	calls     []string
	bins      []string
	spawnArgv []string
	// status is what `rec status` prints on its n-th run (1-based), and
	// whether it exits 0.
	status  func(n int) (string, bool)
	nStatus int
	// stopped is set by `rec stop`.
	stopped bool
	// serve: a spawn starts the httptest server on addr and a real, idle
	// child (sleep) stands in for `rec serve`; otherwise a spawn fails with
	// errSpawnStub and starts nothing.
	serve bool

	addr string
	port int
	// What the server saw: the Authorization header of each request by
	// path, and the model each chat request named.
	auth       map[string][]string
	chatModels []string
	signalled  []int
}

func (h *recHarness) seq() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

// url is the base URL the registry gives rec: the harness address.
func (h *recHarness) url() string { return "http://" + h.addr }

// recCfg is a registry whose rec provider lives at base and names secretRef.
func recCfg(base, secretRef string) *config.Config {
	return &config.Config{Providers: []config.Provider{{
		ID: "rec", Location: config.LocationLocal,
		Auth: config.AuthConfig{Type: "none", BaseURL: base + "/v1", SecretRef: secretRef},
	}}}
}

// handler is the fake rec's HTTP side: /v1/models and /v1/chat/completions
// answer 401 without `Bearer k`.
func (h *recHarness) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.auth[r.URL.Path] = append(h.auth[r.URL.Path], r.Header.Get("Authorization"))
		h.mu.Unlock()
		keyed := r.Header.Get("Authorization") == "Bearer k"
		switch r.URL.Path {
		case "/v1/models":
			if !keyed {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"flint"}]}`))
		case "/v1/chat/completions":
			if !keyed {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			model, _ := m["model"].(string)
			h.mu.Lock()
			h.chatModels = append(h.chatModels, model)
			h.mu.Unlock()
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
		default:
			http.NotFound(w, r)
		}
	})
}

// recEnv is an env for the real rec family against h: lookPath finds
// /x/rec, run answers `rec status` and `rec stop` from h, spawn is h's, the
// signal is recorded (and fails), and the pidfile is under t.TempDir.
func recEnv(t *testing.T, h *recHarness) *env {
	t.Helper()
	h.addr = freeAddr(t)
	_, p, _ := net.SplitHostPort(h.addr)
	h.port, _ = strconv.Atoi(p)
	h.auth = map[string][]string{}
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	dir := t.TempDir()
	e.serverProc["rec"] = pidProcess{name: "rec", pidfile: filepath.Join(dir, "rec.pid"), logfile: filepath.Join(dir, "rec.log")}
	e.lookPath = func(name string) (string, error) {
		if name != "rec" {
			t.Errorf("lookPath(%q), want rec", name)
		}
		return "/x/rec", nil
	}
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls = append(h.calls, cmd)
		h.bins = append(h.bins, name)
		switch cmd {
		case "status":
			h.nStatus++
			out, ok := h.status(h.nStatus)
			if h.stopped {
				out, ok = `{"ready":false}`, false
			}
			if !ok {
				return []byte(out), errors.New("exit status 1")
			}
			return []byte(out), nil
		case "stop":
			h.stopped = true
			return nil, nil
		}
		t.Errorf("unexpected command %s %s", name, cmd)
		return nil, errors.New("unexpected")
	}
	e.spawn = func(pp pidProcess, bin string, args []string) (*spawned, error) {
		h.mu.Lock()
		h.calls = append(h.calls, "spawn")
		h.bins = append(h.bins, bin)
		h.spawnArgv = slices.Clone(args)
		serve := h.serve
		h.mu.Unlock()
		if !serve {
			return nil, errSpawnStub
		}
		serveAt(t, h.addr, h.handler())
		sp, err := pp.spawn("/bin/sleep", []string{"30"})
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() {
			if done, _ := sp.exited(); !done {
				sp.kill()
			}
		})
		return sp, nil
	}
	e.signal = func(pid int, sig syscall.Signal) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.signalled = append(h.signalled, pid)
		return errors.New("signal not allowed in rec tests")
	}
	return e
}

// recTarget is a start of model on rec.
func recTarget(model string) Target { return Target{ProviderID: "rec", ModelName: model} }

// TestRecStartSpawnsWaitsWarmsWithKey verifies rec's whole start against the
// real table row: it spawns `rec serve --port <registry port>`, polls `rec
// status` until it says ready serving flint, then warms flint with the
// registry's key, reporting Starting, Waiting, Warming in order. rec refuses
// keyless requests, so a start that drops the key (or warms the wrong model,
// or spawns on another port than the routes point at) leaves a model nothing
// can use.
func TestRecStartSpawnsWaitsWarmsWithKey(t *testing.T) {
	h := &recHarness{serve: true}
	e := recEnv(t, h)
	h.status = func(n int) (string, bool) {
		if n <= 2 {
			return `{"ready":false}`, false
		}
		return `{"ready":true,"model":"flint","endpoint":"` + h.url() + `/v1"}`, true
	}
	e.loadTimeout = 3 * time.Second
	var stages []Stage
	err := start(context.Background(), e, recCfg(h.url(), "k"), recTarget("flint"), Options{
		Others:   OthersAnswer{Decided: true},
		Progress: func(s Stage) { stages = append(stages, s) },
	})
	if err != nil {
		t.Fatalf("start = %v, want nil", err)
	}
	if want := []string{"serve", "--port", strconv.Itoa(h.port)}; !slices.Equal(h.spawnArgv, want) {
		t.Errorf("spawn argv = %v, want %v", h.spawnArgv, want)
	}
	if got, want := h.seq(), []string{"status", "spawn", "status", "status"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v (not-ready twice, around the spawn, then ready)", got, want)
	}
	for _, b := range h.bins {
		if b != "/x/rec" {
			t.Errorf("ran %q, want the binary lookPath found (/x/rec)", b)
		}
	}
	if want := []Stage{StageStarting, StageWaiting, StageWarming}; !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.chatModels, []string{"flint"}) {
		t.Errorf("warmup chat models = %v, want [flint]", h.chatModels)
	}
	if got := h.auth["/v1/chat/completions"]; len(got) == 0 || slices.ContainsFunc(got, func(a string) bool { return a != "Bearer k" }) {
		t.Errorf("chat Authorization headers = %q, want every one Bearer k", got)
	}
}

// TestRecStartFailsFastWithoutTheBinary verifies a start of rec with no rec on
// PATH is a *BinaryMissingError naming rec, with nothing run or spawned: the
// user is told what to install instead of waiting on a spawn that cannot
// happen.
func TestRecStartFailsFastWithoutTheBinary(t *testing.T) {
	h := &recHarness{status: func(int) (string, bool) { return `{"ready":false}`, false }}
	e := recEnv(t, h)
	e.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	err := start(context.Background(), e, recCfg(h.url(), "k"), recTarget("flint"), Options{Others: OthersAnswer{Decided: true}})
	var bm *BinaryMissingError
	if !errors.As(err, &bm) || bm.Binary != "rec" {
		t.Fatalf("start = %v, want *BinaryMissingError{Binary: rec}", err)
	}
	if got := h.seq(); len(got) != 0 {
		t.Errorf("calls = %v, want none (no status, no spawn)", got)
	}
}

// TestRecStartFailsFastWhenTheKeyCannotBeRead verifies a rec start whose
// registry secret_ref cannot be resolved ends at once with a
// *localmodels.KeyUnreadableError, before any spawn and well under a second:
// rec refuses every keyless probe, so starting it anyway would only time out
// on readiness without ever naming the key.
func TestRecStartFailsFastWhenTheKeyCannotBeRead(t *testing.T) {
	h := &recHarness{serve: true, status: func(int) (string, bool) { return `{"ready":false}`, false }}
	e := recEnv(t, h)
	begin := time.Now()
	err := start(context.Background(), e, recCfg(h.url(), "exec:/nonexistent/k"), recTarget("flint"), Options{Others: OthersAnswer{Decided: true}})
	elapsed := time.Since(begin)
	var ku *localmodels.KeyUnreadableError
	if !errors.As(err, &ku) || ku.Family != "rec" {
		t.Fatalf("start = %v, want *localmodels.KeyUnreadableError for rec", err)
	}
	if slices.Contains(h.seq(), "spawn") {
		t.Errorf("calls = %v, want no spawn", h.seq())
	}
	if elapsed >= time.Second {
		t.Errorf("start took %v, want under 1s", elapsed)
	}
}

// TestRecStopRunsRecStop verifies stopping rec runs `rec stop` and is done
// once `rec status` says not ready, sending no signal: rec is one instance
// per machine that wt may not have started, so it is stopped through its own
// command on whatever port, never by a pid. A port-scope stop would skip the
// status check and call a still-running instance stopped.
func TestRecStopRunsRecStop(t *testing.T) {
	h := &recHarness{}
	e := recEnv(t, h)
	h.status = func(int) (string, bool) {
		return `{"ready":true,"model":"flint","endpoint":"` + h.url() + `/v1"}`, true
	}
	if err := stop(context.Background(), e, recCfg(h.url(), "k"), "rec"); err != nil {
		t.Fatalf("stop = %v, want nil", err)
	}
	if got, want := h.seq(), []string{"stop", "status"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if len(h.signalled) != 0 {
		t.Errorf("signalled %v, want no signal", h.signalled)
	}
}

// TestRecStartWhileAnotherRecServesOtherModelAsksFirst verifies a start of
// flint while `rec status` reports an instance serving another model is an
// *OccupiedError that touches nothing, and with AllowReplace stops it with
// `rec stop`, waits for status to say not ready, and only then spawns on the
// registry's port. The user's rule: never stop a foreign instance silently,
// and never spawn beside one.
func TestRecStartWhileAnotherRecServesOtherModelAsksFirst(t *testing.T) {
	other := func(h *recHarness) func(int) (string, bool) {
		return func(int) (string, bool) {
			return `{"ready":true,"model":"other","endpoint":"` + h.url() + `/v1"}`, true
		}
	}
	h := &recHarness{}
	e := recEnv(t, h)
	h.status = other(h)
	err := start(context.Background(), e, recCfg(h.url(), "k"), recTarget("flint"), Options{Others: OthersAnswer{Decided: true}})
	var occ *OccupiedError
	if !errors.As(err, &occ) || len(occ.Occupants) != 1 || occ.Occupants[0].ModelName != "other" {
		t.Fatalf("start = %v, want *OccupiedError naming other", err)
	}
	if got := h.seq(); !slices.Equal(got, []string{"status"}) {
		t.Errorf("calls = %v, want only the readiness command", got)
	}

	h = &recHarness{}
	e = recEnv(t, h)
	h.status = other(h)
	err = start(context.Background(), e, recCfg(h.url(), "k"), recTarget("flint"), Options{AllowReplace: true, Others: OthersAnswer{Decided: true}})
	if !errors.Is(err, errSpawnStub) {
		t.Fatalf("start with AllowReplace = %v, want the stubbed spawn's error", err)
	}
	if got, want := h.seq(), []string{"status", "stop", "status", "spawn"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if want := []string{"serve", "--port", strconv.Itoa(h.port)}; !slices.Equal(h.spawnArgv, want) {
		t.Errorf("spawn argv = %v, want %v", h.spawnArgv, want)
	}
	if len(h.signalled) != 0 {
		t.Errorf("signalled %v, want no signal", h.signalled)
	}
}

// TestRecProcessIdentity verifies which process a rec pidfile may name: the
// `rec serve --port <port>` wt spawned (argv[0] a full path to rec) reads as
// a loading rec server, while rec's own supervisor child (`rec _supervise
// ... serve flint --port <port>`) and a command that only mentions the words
// (`vim rec serve --port <port>`) are strays wt leaves alone. Only the spawned
// pid is recorded; identifying any other process in rec's tree as the server
// would let a stop signal it.
func TestRecProcessIdentity(t *testing.T) {
	h := &recHarness{status: func(int) (string, bool) { return `{"ready":false}`, false }}
	e := recEnv(t, h)
	cfg := recCfg(h.url(), "k")
	port := strconv.Itoa(h.port)
	if err := os.WriteFile(e.serverProc["rec"].pidfile, []byte("4242"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		argv   []string
		server bool
	}{
		"spawned rec serve": {argv: []string{"/Users/x/.local/bin/rec", "serve", "--port", port}, server: true},
		"supervisor":        {argv: []string{"rec", "_supervise", "/x/rec-mlx", "serve", "flint", "--port", port}},
		"vim":               {argv: []string{"vim", "rec", "serve", "--port", port}},
	} {
		t.Run(name, func(t *testing.T) {
			e.describe = func(pid int) (procInfo, bool, error) {
				if pid != 4242 {
					return procInfo{}, false, nil
				}
				return mine(4242, "100.5", tc.argv), true, nil
			}
			_, l := e.loadingServer(cfg, "rec")
			if tc.server {
				if l != (Loading{PID: 4242}) {
					t.Errorf("loading = %+v, want Loading{PID: 4242}", l)
				}
				return
			}
			if l.PID != 0 || l.Stray == "" {
				t.Errorf("loading = %+v, want a stray (no PID)", l)
			}
		})
	}
}
