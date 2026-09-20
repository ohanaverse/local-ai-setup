# wt: local model lifecycle engine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Go engine that starts a local model (ollama warm-load, omlx start+warm, mtplx spawn+wait+warm), reports what a start would replace, and never replaces silently — with no UI and no writes to modelman state.

**Architecture:** New `wt/internal/lifecycle` package: an `env` of injectable seams (HTTP clients, exec, inventory, timeouts, pidfile paths), HTTP polling primitives, a `backend` interface with one implementation per provider (ollama, omlx, mtplx), and a generic orchestrator (`Start`/`Occupant`/`Stop`). Live `localmodels.Inventory` supplies both "already running?" and "who is the occupant?". The engine is not wired into any caller yet (sub-projects 4b/4c consume it).

**Tech Stack:** Go 1.26 (module root `wt/`), stdlib `net/http`, `os/exec`, `syscall`, `httptest`, `testing`.

**Spec:** `docs/superpowers/specs/2026-09-20-wt-lifecycle-engine-design.md`

## Global Constraints

- Wt writes nothing to modelman-owned state (`modelman.toml` flags, LiteLLM config) and never restarts the LiteLLM proxy.
- `Start` never replaces silently: with an occupant and `Options.AllowReplace == false` it returns `*OccupiedError` before touching anything. If the target is already running per live Inventory, `Start` is a no-op.
- Occupancy: never any for ollama (multi-tenant); `omlx` and `omlx-6bit` are one domain; mtplx = any running mtplx model other than the target. Unsupported providers (e.g. `mlx_lm_server`, retired `llamacpp`) get `*UnsupportedError`.
- Ports/origins come from `localmodels.FamilyOrigin(cfg, family)` (registry `auth.base_url`, else 11434 / 8000 / 8003).
- Ollama: daemon must answer `GET <origin>/api/tags`, else `*DaemonDownError`; then a 1-token chat request to `<origin>/v1/chat/completions` loads the model. Wt does NOT kickstart the ollama daemon.
- omlx: if `<origin>/v1/models` does not answer, run `omlx start` (binary via `exec.LookPath`, else `*BinaryMissingError`) and wait for it; then warm with the model name's LAST PATH SEGMENT (`path.Base`) — omlx serves directory basenames. Stop = `omlx stop` then wait for the port to close.
- mtplx: argv exactly `serve --model X --port N --host 127.0.0.1 --model-id X`; spawned in its own session (`Setsid`); pidfile `/tmp/local-ai-setup-mtplx.pid` and log `/tmp/local-ai-setup-mtplx.log` (the modelman paths). If the port still answers after the pre-bind wait, return `*PortBusyError` (never kill an unknown process). On failure or cancel, tear the spawned process down (SIGTERM, then SIGKILL after 5s) and remove the pidfile. Replacing another mtplx occupant = `mtplx stop --port N --grace-seconds 10`, then confirm the port closed.
- Timeouts (ported from modelman): warmup 600s (mtplx warmup 120s), model-load wait 300s, post-stop port-close wait 6s, port-up wait 90s, pre-bind wait 10s. Progress stages: `stopping-occupant`, `starting`, `waiting-for-model`, `warming`, reported in that order.
- Port semantics: any HTTP response (even an error status) means "answering"; only a connection-level failure means "closed"; a read timeout counts as still answering when checking for closure, and as NOT up when checking for openness.
- Cancellation via `context.Context`: every poll loop checks `ctx` and returns its error promptly.
- Every `Test*` needs a top-level `//` comment stating what it tests and why it matters (repo rule, `wt/CLAUDE.md`).
- Run Go commands from `wt/`. Run `go vet ./...` and `gofmt -l .` before each commit (wt-ci gates on gofmt).
- Commit messages during execution end with `- completes plan item #N` and a `Co-Authored-By:` trailer naming the model that authored the commit.
- Execute in an isolated worktree (superpowers:using-git-worktrees), never with `main` checked out in a linked worktree. Do not push or open a PR without asking.

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/localmodels/inventory.go` | Modify: `Entry.ModelName`, exported `Family` |
| `wt/internal/lifecycle/env.go` | Create: `env` seams + `defaultEnv` |
| `wt/internal/lifecycle/probe.go` | Create: probing/polling primitives (`probe`, `waitPortOpen`, `portClosedWithin`, `waitForModel`, `warmup`) |
| `wt/internal/lifecycle/lifecycle.go` | Create: `Target`, `Stage`, `Options`, errors, `backend` interface, `Start`/`Occupant`/`Stop` |
| `wt/internal/lifecycle/ollama.go`, `omlx.go` | Create: those backends |
| `wt/internal/lifecycle/pidproc.go`, `mtplx.go` | Create: pid-tracked process + mtplx backend |
| `*_test.go` in the same package | Tests (mtplx tests use a helper-process pattern via `TestMain`) |
| `wt/CLAUDE.md` | Modify: docs |

---

### Task 1: `localmodels` — `Entry.ModelName` and exported `Family`

**Files:**
- Modify: `wt/internal/localmodels/inventory.go`
- Test: `wt/internal/localmodels/inventory_test.go`

**Interfaces:**
- Consumes: existing `inventory`, `familyOf`, `Entry`.
- Produces: `Entry.ModelName string` (provider-side name: registered → the registry `ModelName`; discovered → the artifact name); `func Family(providerID string) string` (exported `familyOf`).

- [ ] **Step 1: Write the failing tests** (append to `inventory_test.go`; reuse its helpers `localProvider`, `ollamaServer`, `modelsServer`, `mkdirs`, `byModelID`, `testClient`)

```go
// TestInventoryEntriesCarryProviderSideModelName verifies every entry exposes
// the provider-side name (registered: the registry model_name; discovered: the
// artifact name). The lifecycle engine matches a start target against running
// entries by this name, and an entry with only a registry id cannot be compared.
func TestInventoryEntriesCarryProviderSideModelName(t *testing.T) {
	srv := ollamaServer(t, []string{"gemma4:9b", "other:1b"}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/gemma", ProviderID: "ollama", ModelName: "gemma4:9b"}},
	}
	snap := inventory(cfg, testClient)
	reg, _ := byModelID(snap, "ollama/gemma")
	if reg.ModelName != "gemma4:9b" {
		t.Errorf("registered ModelName = %q, want gemma4:9b", reg.ModelName)
	}
	disc, _ := byModelID(snap, config.DiscoveredModelID("ollama", "other:1b"))
	if disc.ModelName != "other:1b" {
		t.Errorf("discovered ModelName = %q, want other:1b", disc.ModelName)
	}
}

// TestFamilyExported verifies the exported Family maps provider ids to probe
// families, with omlx-6bit sharing omlx's family and unknown ids returning "" —
// the lifecycle engine uses it to decide which backend and occupancy domain a
// target belongs to.
func TestFamilyExported(t *testing.T) {
	cases := map[string]string{"ollama": "ollama", "omlx": "omlx", "omlx-6bit": "omlx", "mtplx": "mtplx", "mlx_lm_server": "mlx_lm_server", "llamacpp": "", "": ""}
	for id, want := range cases {
		if got := Family(id); got != want {
			t.Errorf("Family(%q) = %q, want %q", id, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/localmodels -run 'ProviderSideModelName|FamilyExported' -v` — Expected: FAIL to compile (`ModelName`, `Family` undefined).

- [ ] **Step 3: Implement**

In `Entry` add (after `ModelID`):
```go
	ModelName  string // provider-side name: the registry model_name when registered, else the artifact name
```
Set it where entries are built in `inventory()`: the registered entry `e := Entry{ProviderID: m.ProviderID, ModelID: m.ID, ModelName: m.ModelName, Registered: true}`; the discovered entry gets `ModelName: a`. Add:
```go
// Family maps a registry provider id to its probe family ("omlx-6bit" shares
// "omlx"); "" when wt has no probe for it.
func Family(providerID string) string { return familyOf(providerID) }
```

- [ ] **Step 4: Run to verify pass** — `go build ./... && go vet ./... && go test -count=1 ./...` — Expected: PASS (existing tests that construct `Entry` values by field are unaffected).

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/localmodels
git commit -m "feat(localmodels): Entry.ModelName and exported Family - completes plan item #1"
```

---

### Task 2: `lifecycle` env and polling primitives

**Files:**
- Create: `wt/internal/lifecycle/env.go`, `wt/internal/lifecycle/probe.go`, `wt/internal/lifecycle/probe_test.go`

**Interfaces:**
- Consumes: `localmodels.FetchModelIDs`, `localmodels.NameMatches`.
- Produces:
  - `type env struct` (fields below) and `func defaultEnv() *env`
  - `type procWatch interface { exited() (done bool, err error) }`
  - `func (e *env) probe(ctx context.Context, url string, timeout time.Duration) (responded, timedOut bool)`
  - `func (e *env) sleep(ctx context.Context, d time.Duration) error`
  - `func (e *env) waitPortOpen(ctx context.Context, url string, timeout time.Duration) (bool, error)`
  - `func (e *env) portClosedWithin(ctx context.Context, url string, timeout time.Duration) (bool, error)`
  - `func (e *env) waitForModel(ctx context.Context, modelsURL, model string, proc procWatch, timeout time.Duration) error`
  - `func (e *env) warmup(ctx context.Context, chatURL, model, healthURL string, timeout time.Duration) error`

- [ ] **Step 1: Write the failing tests** (`probe_test.go`)

```go
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testEnv is an env with tiny poll intervals and timeouts so polling tests run
// in milliseconds.
func testEnv() *env {
	e := defaultEnv()
	e.pollInterval = 5 * time.Millisecond
	e.warmupTimeout = 300 * time.Millisecond
	e.loadTimeout = 300 * time.Millisecond
	e.stopTimeout = 200 * time.Millisecond
	e.portUpTimeout = 300 * time.Millisecond
	e.prebindTimeout = 200 * time.Millisecond
	return e
}

// TestProbeClassifiesResponses verifies probe's three outcomes: any HTTP
// response (even 404) is "responded"; a stalled listener is "timedOut"; a
// closed port is neither. The distinction matters: "still holds the port" must
// count a stalled listener but not a refused connection.
func TestProbeClassifiesResponses(t *testing.T) {
	e := testEnv()
	ctx := context.Background()

	notFound := httptest.NewServer(http.NotFoundHandler())
	if responded, timedOut := e.probe(ctx, notFound.URL, time.Second); !responded || timedOut {
		t.Errorf("404 server: responded=%v timedOut=%v, want true,false", responded, timedOut)
	}
	notFound.Close()
	if responded, timedOut := e.probe(ctx, notFound.URL, time.Second); responded || timedOut {
		t.Errorf("closed server: responded=%v timedOut=%v, want false,false", responded, timedOut)
	}

	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer stall.Close()
	if responded, timedOut := e.probe(ctx, stall.URL, 30*time.Millisecond); responded || !timedOut {
		t.Errorf("stalled server: responded=%v timedOut=%v, want false,true", responded, timedOut)
	}
}

// TestWaitPortOpenAndClosed verifies the two waits: waitPortOpen reports true
// for an answering server and false (no error) after its timeout for a dead
// one; portClosedWithin is the mirror image, and a stalled listener does NOT
// count as closed.
func TestWaitPortOpenAndClosed(t *testing.T) {
	e := testEnv()
	ctx := context.Background()
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()

	if ok, err := e.waitPortOpen(ctx, up.URL, 100*time.Millisecond); !ok || err != nil {
		t.Errorf("waitPortOpen(up) = %v, %v", ok, err)
	}
	if closed, err := e.portClosedWithin(ctx, up.URL, 60*time.Millisecond); closed || err != nil {
		t.Errorf("portClosedWithin(up) = %v, %v; want false, nil", closed, err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if ok, err := e.waitPortOpen(ctx, dead.URL, 60*time.Millisecond); ok || err != nil {
		t.Errorf("waitPortOpen(dead) = %v, %v; want false, nil", ok, err)
	}
	if closed, err := e.portClosedWithin(ctx, dead.URL, 100*time.Millisecond); !closed || err != nil {
		t.Errorf("portClosedWithin(dead) = %v, %v; want true, nil", closed, err)
	}
}

// TestWaitsAbortOnCancel verifies a cancelled context ends both waits promptly
// with the context's error — a user pressing cancel during a long start must
// not wait out the 300s model-load budget.
func TestWaitsAbortOnCancel(t *testing.T) {
	e := testEnv()
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := e.portClosedWithin(ctx, up.URL, 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("portClosedWithin err = %v, want context.Canceled", err)
	}
	if _, err := e.waitPortOpen(ctx, "http://127.0.0.1:1/", 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("waitPortOpen err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("waits took %v after cancel, want prompt return", time.Since(start))
	}
}

type fakeProc struct {
	done bool
	err  error
}

func (f *fakeProc) exited() (bool, error) { return f.done, f.err }

// TestWaitForModel verifies waitForModel returns once /v1/models lists the
// model (lenient prefix match), times out with a clear error when it never
// does, and fails fast with the process's exit error when the serve process
// dies during load instead of waiting out the whole budget.
func TestWaitForModel(t *testing.T) {
	e := testEnv()
	ctx := context.Background()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"org/Qwen3.8"}]}`))
	}))
	defer srv.Close()
	if err := e.waitForModel(ctx, srv.URL, "Qwen3.8", nil, time.Second); err != nil {
		t.Fatalf("waitForModel: %v", err)
	}

	never := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) }))
	defer never.Close()
	if err := e.waitForModel(ctx, never.URL, "x", nil, 60*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("never-listed err = %v, want timed out", err)
	}

	dead := &fakeProc{done: true, err: errors.New("exit status 3")}
	start := time.Now()
	if err := e.waitForModel(ctx, never.URL, "x", dead, 5*time.Second); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("dead-proc err = %v, want exited", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("dead process should fail fast, took %v", time.Since(start))
	}
}

// TestWarmupSendsOneTokenChatAndWaitsForCompletion verifies warmup polls the
// health URL, POSTs a 1-token chat completion naming the model, and only
// succeeds when the reply carries the chat.completion marker (an early error
// body is retried). A model is "loaded" only when a real completion came back.
func TestWarmupSendsOneTokenChatAndWaitsForCompletion(t *testing.T) {
	e := testEnv()
	var posts int32
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &lastBody)
		if atomic.AddInt32(&posts, 1) < 2 {
			http.Error(w, "loading", 503)
			return
		}
		_, _ = w.Write([]byte(`{"object": "chat.completion","choices":[]}`))
	}))
	defer srv.Close()

	if err := e.warmup(context.Background(), srv.URL+"/v1/chat/completions", "m1", srv.URL+"/health", time.Second); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	if lastBody["model"] != "m1" || lastBody["max_tokens"] != float64(1) || lastBody["stream"] != false {
		t.Errorf("request body = %v, want model m1, max_tokens 1, stream false", lastBody)
	}
	if atomic.LoadInt32(&posts) != 2 {
		t.Errorf("posts = %d, want 2 (first attempt errored)", posts)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"error":"no"}`)) }))
	defer bad.Close()
	if err := e.warmup(context.Background(), bad.URL, "m1", bad.URL, 80*time.Millisecond); err == nil || !strings.Contains(err.Error(), "warm") {
		t.Errorf("never-completes err = %v, want warm-up failure", err)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/lifecycle -v` — Expected: FAIL to compile (package empty).

- [ ] **Step 3: Implement**

`env.go`:
```go
// Package lifecycle starts local models (ollama, omlx, mtplx): the Go port of
// modelman's start/stop/warmup lifecycle. It never writes modelman-owned state
// and never replaces a running model without being told to (Options.AllowReplace).
package lifecycle

import (
	"context"
	"net/http"
	"os/exec"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// env carries every seam the engine uses so tests can substitute HTTP clients,
// process execution, the live inventory, timeouts and pidfile paths.
type env struct {
	probeClient *http.Client // short requests (health, /v1/models)
	chatClient  *http.Client // warmup chat requests; bounded by ctx, not a client timeout
	lookPath    func(string) (string, error)
	run         func(ctx context.Context, name string, args ...string) ([]byte, error)
	inventory   func(*config.Config) localmodels.Snapshot
	backends    map[string]backend // keyed by provider family

	mtplxProc pidProcess // pidfile + log used for the spawned mtplx server

	pollInterval   time.Duration
	warmupTimeout  time.Duration // ollama/omlx warmup budget
	loadTimeout    time.Duration // wait for a spawned server to list the model
	stopTimeout    time.Duration // wait for a port to close after a stop
	portUpTimeout  time.Duration // wait for a daemon to answer after `start`
	prebindTimeout time.Duration // wait for a port to free before spawning
}

func defaultEnv() *env {
	return &env{
		probeClient:    &http.Client{Timeout: 5 * time.Second},
		chatClient:     &http.Client{},
		lookPath:       exec.LookPath,
		run:            runCommand,
		inventory:      localmodels.Inventory,
		backends:       map[string]backend{},
		mtplxProc:      pidProcess{name: "mtplx", pidfile: "/tmp/local-ai-setup-mtplx.pid", logfile: "/tmp/local-ai-setup-mtplx.log"},
		pollInterval:   time.Second,
		warmupTimeout:  600 * time.Second,
		loadTimeout:    300 * time.Second,
		stopTimeout:    6 * time.Second,
		portUpTimeout:  90 * time.Second,
		prebindTimeout: 10 * time.Second,
	}
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
```
(`backend` and `pidProcess` are defined in Tasks 3 and 5. To keep this task compiling on its own, define TEMPORARY minimal stubs at the bottom of `env.go`: `type backend interface{}` and `type pidProcess struct{ name, pidfile, logfile string }`, and REMOVE each stub in the task that defines the real type.)

`probe.go`:
```go
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// procWatch lets waitForModel notice that a serve process died mid-load.
type procWatch interface {
	exited() (done bool, err error)
}

var chatCompletionMarker = regexp.MustCompile(`"object"\s*:\s*"chat\.completion"`)

// probe does one GET. responded is true for ANY HTTP response (2xx or an error
// status: something is listening); timedOut is true when the listener accepted
// the connection and then stalled. A refused/reset connection is neither.
func (e *env) probe(ctx context.Context, url string, timeout time.Duration) (responded, timedOut bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, false
	}
	resp, err := e.probeClient.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			return false, true
		}
		return false, false
	}
	_ = resp.Body.Close()
	return true, false
}

func (e *env) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// waitPortOpen polls url until it answers (any response counts), returning
// false (no error) when timeout passes first; ctx cancellation returns its error.
func (e *env) waitPortOpen(ctx context.Context, url string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if responded, _ := e.probe(ctx, url, time.Second); responded {
			return true, nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return false, err
		}
	}
	return false, ctx.Err()
}

// portClosedWithin polls url until it stops answering. A response — success or
// error status — or a read timeout means something still holds the port; only
// a connection-level failure means it closed.
func (e *env) portClosedWithin(ctx context.Context, url string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		responded, timedOut := e.probe(ctx, url, time.Second)
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !responded && !timedOut {
			return true, nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return false, err
		}
	}
	return false, ctx.Err()
}

// waitForModel polls modelsURL until it lists model (lenient name match), the
// serve process dies (when proc is given), or timeout passes.
func (e *env) waitForModel(ctx context.Context, modelsURL, model string, proc procWatch, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if proc != nil {
			if done, err := proc.exited(); done {
				return fmt.Errorf("serve process exited during model load: %v", err)
			}
		}
		for _, id := range localmodels.FetchModelIDs(e.probeClient, modelsURL) {
			if localmodels.NameMatches(id, model) {
				return nil
			}
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("timed out waiting for %s to serve %s", modelsURL, model)
}

// warmup forces model into memory: poll healthURL for liveness, then POST a
// 1-token chat completion to chatURL and require the chat.completion marker.
func (e *env) warmup(ctx context.Context, chatURL, model, healthURL string, timeout time.Duration) error {
	payload, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens":  1,
		"temperature": 0,
		"stream":      false,
	})
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if responded, _ := e.probe(ctx, healthURL, 2*time.Second); !responded {
			if err := e.sleep(ctx, e.pollInterval); err != nil {
				return err
			}
			continue
		}
		if e.tryChat(ctx, chatURL, payload) {
			return nil
		}
		if err := e.sleep(ctx, e.pollInterval); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("failed to warm up model %s at %s", model, chatURL)
}

func (e *env) tryChat(ctx context.Context, chatURL string, payload []byte) bool {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return err == nil && chatCompletionMarker.Match(body)
}
```

- [ ] **Step 4: Run to verify pass** — `go test ./internal/lifecycle -count=3 -v 2>&1 | tail -15 && go vet ./... && go build ./...` — Expected: PASS, stable across 3 runs.

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/lifecycle
git commit -m "feat(lifecycle): env seams and HTTP polling primitives - completes plan item #2"
```

---

### Task 3: Orchestrator — types, errors, `Occupant`, `Start`, `Stop`

**Files:**
- Create: `wt/internal/lifecycle/lifecycle.go`, `wt/internal/lifecycle/lifecycle_test.go`
- Modify: `wt/internal/lifecycle/env.go` (delete the temporary `backend` stub)

**Interfaces:**
- Consumes: `env` (Task 2), `localmodels.Snapshot`/`Entry`/`Family`/`NameMatches`/`OllamaNameMatches` (Task 1).
- Produces:
  - `type Target struct{ ProviderID, ModelName string }`
  - `type Stage string` with `StageStoppingOccupant="stopping-occupant"`, `StageStarting="starting"`, `StageWaiting="waiting-for-model"`, `StageWarming="warming"`
  - `type Options struct{ AllowReplace bool; Progress func(Stage) }`
  - Errors: `*OccupiedError{Occupant localmodels.Entry}`, `*DaemonDownError{Provider, Origin string}`, `*BinaryMissingError{Binary string}`, `*PortBusyError{Port int}`, `*UnsupportedError{ProviderID string}` (each with an `Error()` message)
  - `type backend interface { singleModel() bool; stop(ctx context.Context, e *env, cfg *config.Config) error; start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error }`
  - `func Occupant(t Target, snap localmodels.Snapshot) (localmodels.Entry, bool)`
  - `func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error`
  - `func Stop(ctx context.Context, cfg *config.Config, providerID string) error`
  - unexported `start(ctx, e *env, cfg, t, opts)` (tests call it with a fake-backend env)

- [ ] **Step 1: Write the failing tests** (`lifecycle_test.go`)

```go
package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

type fakeBackend struct {
	single   bool
	calls    *[]string
	stopErr  error
	startErr error
}

func (f *fakeBackend) singleModel() bool { return f.single }
func (f *fakeBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	*f.calls = append(*f.calls, "stop")
	return f.stopErr
}
func (f *fakeBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	*f.calls = append(*f.calls, "start:"+t.ModelName)
	report(StageStarting)
	return f.startErr
}

func fakeEnv(snap localmodels.Snapshot, single bool, calls *[]string) *env {
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
	fb := &fakeBackend{single: single, calls: calls}
	e.backends = map[string]backend{"omlx": fb, "mtplx": fb, "ollama": &fakeBackend{single: false, calls: calls}}
	return e
}

func running(provider, id, name string) localmodels.Entry {
	return localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true}
}

// TestOccupantRules verifies who a start would replace: nobody for ollama
// (multi-tenant), a running model on the same single-model domain otherwise —
// with omlx and omlx-6bit sharing one domain — never the target itself and
// never a model that is not running. Getting this wrong either silently kills
// a model the user was using or skips a confirmation that was needed.
func TestOccupantRules(t *testing.T) {
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		running("ollama", "ollama/a", "a:1b"),
		running("omlx-6bit", "omlx-6bit/six", "org/Six-6bit"),
		{ProviderID: "omlx", ModelID: "omlx/idle", ModelName: "Idle-4bit"},
		running("mtplx", "mtplx/m1", "Org/M1"),
	}}

	if _, ok := Occupant(Target{"ollama", "b:2b"}, snap); ok {
		t.Error("ollama must never have an occupant")
	}
	if occ, ok := Occupant(Target{"omlx", "Qwen-4bit"}, snap); !ok || occ.ModelID != "omlx-6bit/six" {
		t.Errorf("omlx occupant = %+v ok=%v, want the running omlx-6bit model (one domain)", occ, ok)
	}
	if _, ok := Occupant(Target{"omlx", "Six-6bit"}, snap); ok {
		t.Error("the target itself (matching by lenient name) must not be its own occupant")
	}
	if occ, ok := Occupant(Target{"mtplx", "Org/M2"}, snap); !ok || occ.ModelID != "mtplx/m1" {
		t.Errorf("mtplx occupant = %+v ok=%v", occ, ok)
	}
	if _, ok := Occupant(Target{"mtplx", "Org/M1"}, snap); ok {
		t.Error("mtplx target already running must not be its own occupant")
	}
	if _, ok := Occupant(Target{"omlx", "Qwen-4bit"}, localmodels.Snapshot{Entries: []localmodels.Entry{{ProviderID: "omlx", ModelName: "Idle-4bit"}}}); ok {
		t.Error("a non-running entry is not an occupant")
	}
}

// TestStartAlreadyRunningIsNoOp verifies a target that live Inventory already
// reports running causes no backend calls and no progress — selecting a model
// that is up must not restart or re-warm it.
func TestStartAlreadyRunningIsNoOp(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/q", "Qwen-4bit")}}
	var stages []Stage
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{"omlx", "Qwen-4bit"}, Options{Progress: func(s Stage) { stages = append(stages, s) }})
	if err != nil || len(calls) != 0 || len(stages) != 0 {
		t.Errorf("err=%v calls=%v stages=%v, want no-op", err, calls, stages)
	}
}

// TestStartNeverReplacesSilently verifies that with a running occupant and
// AllowReplace false, Start returns *OccupiedError carrying that occupant and
// touches nothing — the caller (TUI/non-TUI) must confirm first.
func TestStartNeverReplacesSilently(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/old", "Old-4bit")}}
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{"omlx", "New-4bit"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) || occ.Occupant.ModelID != "omlx/old" {
		t.Fatalf("err = %v, want *OccupiedError for omlx/old", err)
	}
	if len(calls) != 0 {
		t.Errorf("backend touched despite refusal: %v", calls)
	}
}

// TestStartReplacesInOrderWhenAllowed verifies replacement stops the occupant
// BEFORE starting the target and reports stages in order (stopping-occupant,
// then the backend's own stages).
func TestStartReplacesInOrderWhenAllowed(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("mtplx", "mtplx/old", "Org/Old")}}
	var stages []Stage
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{"mtplx", "Org/New"},
		Options{AllowReplace: true, Progress: func(s Stage) { stages = append(stages, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"stop", "start:Org/New"}) {
		t.Errorf("calls = %v, want stop then start", calls)
	}
	if !reflect.DeepEqual(stages, []Stage{StageStoppingOccupant, StageStarting}) {
		t.Errorf("stages = %v", stages)
	}
}

// TestStartOllamaIgnoresOtherRunningModels verifies ollama starts alongside
// other running ollama models with no stop and no confirmation — it is
// multi-tenant and nothing is replaced.
func TestStartOllamaIgnoresOtherRunningModels(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("ollama", "ollama/a", "a:1b")}}
	if err := start(context.Background(), fakeEnv(snap, false, &calls), &config.Config{}, Target{"ollama", "b:2b"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b:2b"}) {
		t.Errorf("calls = %v", calls)
	}
}

// TestStartStopFailureDoesNotStart verifies a failed occupant stop aborts the
// start with an error naming both models — starting into a still-occupied port
// would fail confusingly or corrupt the running model.
func TestStartStopFailureDoesNotStart(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/old", "Old-4bit")}}
	e := fakeEnv(snap, true, &calls)
	e.backends["omlx"].(*fakeBackend).stopErr = errors.New("still listening")
	err := start(context.Background(), e, &config.Config{}, Target{"omlx", "New-4bit"}, Options{AllowReplace: true})
	if err == nil || len(calls) != 1 || calls[0] != "stop" {
		t.Errorf("err=%v calls=%v, want an error after only a stop call", err, calls)
	}
}

// TestStartUnsupportedProvider verifies providers without a backend
// (mlx_lm_server, retired llamacpp, unknown ids) fail with *UnsupportedError
// rather than silently doing nothing.
func TestStartUnsupportedProvider(t *testing.T) {
	var calls []string
	for _, id := range []string{"mlx_lm_server", "llamacpp", "nope"} {
		err := start(context.Background(), fakeEnv(localmodels.Snapshot{}, true, &calls), &config.Config{}, Target{id, "x"}, Options{})
		var u *UnsupportedError
		if !errors.As(err, &u) {
			t.Errorf("%s: err = %v, want *UnsupportedError", id, err)
		}
	}
}

// TestTypedErrorMessages verifies each typed error's message names what the
// user must do — these strings are what 4b/4c will show verbatim.
func TestTypedErrorMessages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}, "omlx/old"},
		{&DaemonDownError{Provider: "ollama", Origin: "http://localhost:11434"}, "http://localhost:11434"},
		{&BinaryMissingError{Binary: "mtplx"}, "mtplx"},
		{&PortBusyError{Port: 8003}, "8003"},
		{&UnsupportedError{ProviderID: "mlx_lm_server"}, "mlx_lm_server"},
	}
	for _, tc := range cases {
		if msg := tc.err.Error(); !containsFold(msg, tc.want) {
			t.Errorf("%T message %q must mention %q", tc.err, msg, tc.want)
		}
	}
}

func containsFold(s, sub string) bool { return len(sub) > 0 && (indexFold(s, sub) >= 0) }
func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
```
(The `containsFold` helpers above may be replaced by `strings.Contains(strings.ToLower(...))` — simplify when writing.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/lifecycle -run 'Occupant|StartAlready|NeverReplaces|ReplacesInOrder|OllamaIgnores|StopFailure|Unsupported|TypedError' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement** (`lifecycle.go`; delete the temporary `backend` stub from `env.go`)

```go
package lifecycle

import (
	"context"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Target names the model to start by its provider-side name — exactly
// config.Model.ModelName. For a discovered model that is the artifact name, so
// no registry entry is required.
type Target struct{ ProviderID, ModelName string }

// Stage is a progress milestone reported during Start.
type Stage string

const (
	StageStoppingOccupant Stage = "stopping-occupant"
	StageStarting         Stage = "starting"
	StageWaiting          Stage = "waiting-for-model"
	StageWarming          Stage = "warming"
)

// Options tunes Start. AllowReplace permits stopping a running occupant of a
// single-model provider; Progress (optional) receives stages in order.
type Options struct {
	AllowReplace bool
	Progress     func(Stage)
}

// OccupiedError means starting the target would replace a running model and
// AllowReplace was false. Nothing was touched.
type OccupiedError struct{ Occupant localmodels.Entry }

func (e *OccupiedError) Error() string {
	return fmt.Sprintf("starting this model would stop %s, which is running — confirm to replace it", e.Occupant.ModelID)
}

// DaemonDownError means the provider's daemon is not answering.
type DaemonDownError struct{ Provider, Origin string }

func (e *DaemonDownError) Error() string {
	return fmt.Sprintf("%s is not answering at %s — start the %s app/service first", e.Provider, e.Origin, e.Provider)
}

// BinaryMissingError means a required provider binary is not on PATH.
type BinaryMissingError struct{ Binary string }

func (e *BinaryMissingError) Error() string { return fmt.Sprintf("%s binary not found on PATH", e.Binary) }

// PortBusyError means the provider's port still answers when a fresh server
// needs it, and no known model explains it — wt never kills unknown processes.
type PortBusyError struct{ Port int }

func (e *PortBusyError) Error() string {
	return fmt.Sprintf("port %d is in use by another process — stop it before starting this model", e.Port)
}

// UnsupportedError means wt has no lifecycle backend for the provider.
type UnsupportedError struct{ ProviderID string }

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("wt cannot start models for provider %q", e.ProviderID)
}

// backend is one provider family's lifecycle.
type backend interface {
	// singleModel reports whether the provider serves one model at a time
	// (starting another replaces it).
	singleModel() bool
	// stop stops the provider's current occupant and waits for it to release its port.
	stop(ctx context.Context, e *env, cfg *config.Config) error
	// start runs everything from spawn through warmup for t, reporting stages.
	start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error
}

// sameModel reports whether two provider-side names denote the same model under
// the family's matching rule.
func sameModel(family, a, b string) bool {
	if family == "ollama" {
		return localmodels.OllamaNameMatches(a, b) || localmodels.OllamaNameMatches(b, a)
	}
	return localmodels.NameMatches(a, b)
}

// isRunning reports whether live Inventory already shows the target serving.
func isRunning(snap localmodels.Snapshot, family string, t Target) bool {
	for _, en := range snap.Entries {
		if en.Running && localmodels.Family(en.ProviderID) == family && sameModel(family, en.ModelName, t.ModelName) {
			return true
		}
	}
	return false
}

// Occupant reports the running model that starting t would replace: none for
// ollama (multi-tenant) or unsupported providers; on omlx/omlx-6bit (one
// domain) and mtplx, any running model of the family other than t itself.
func Occupant(t Target, snap localmodels.Snapshot) (localmodels.Entry, bool) {
	family := localmodels.Family(t.ProviderID)
	if family != "omlx" && family != "mtplx" {
		return localmodels.Entry{}, false
	}
	for _, en := range snap.Entries {
		if !en.Running || localmodels.Family(en.ProviderID) != family {
			continue
		}
		if sameModel(family, en.ModelName, t.ModelName) {
			continue
		}
		return en, true
	}
	return localmodels.Entry{}, false
}

// Start starts t. It is a no-op when live Inventory shows t already running,
// returns *OccupiedError (touching nothing) when it would replace a running
// model and opts.AllowReplace is false, and otherwise stops the occupant (if
// any) and runs the provider's start sequence.
func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error {
	return start(ctx, defaultEnv(), cfg, t, opts)
}

func start(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error {
	family := localmodels.Family(t.ProviderID)
	b := e.backends[family]
	if b == nil {
		return &UnsupportedError{ProviderID: t.ProviderID}
	}
	report := func(s Stage) {
		if opts.Progress != nil {
			opts.Progress(s)
		}
	}
	snap := e.inventory(cfg)
	if isRunning(snap, family, t) {
		return nil
	}
	if b.singleModel() {
		if occ, ok := Occupant(t, snap); ok {
			if !opts.AllowReplace {
				return &OccupiedError{Occupant: occ}
			}
			report(StageStoppingOccupant)
			if err := b.stop(ctx, e, cfg); err != nil {
				return fmt.Errorf("stopping %s before starting %s: %w", occ.ModelID, t.ModelName, err)
			}
		}
	}
	return b.start(ctx, e, cfg, t, report)
}

// Stop stops the provider's running model (used for replacement; wt has no
// user-facing stop command). A no-op for multi-tenant ollama.
func Stop(ctx context.Context, cfg *config.Config, providerID string) error {
	e := defaultEnv()
	b := e.backends[localmodels.Family(providerID)]
	if b == nil {
		return &UnsupportedError{ProviderID: providerID}
	}
	return b.stop(ctx, e, cfg)
}
```

- [ ] **Step 4: Run to verify pass** — `go test ./internal/lifecycle -count=1 -v 2>&1 | tail -20 && go vet ./... && go build ./...` — Expected: PASS.

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/lifecycle
git commit -m "feat(lifecycle): orchestrator - Start/Occupant/Stop, typed errors - completes plan item #3"
```

---

### Task 4: ollama and omlx backends

**Files:**
- Create: `wt/internal/lifecycle/ollama.go`, `wt/internal/lifecycle/omlx.go`, `wt/internal/lifecycle/backends_test.go`
- Modify: `wt/internal/lifecycle/env.go` (`defaultEnv` registers the two backends)

**Interfaces:**
- Consumes: `env` primitives (Task 2), `backend` interface + errors (Task 3), `localmodels.FamilyOrigin`.
- Produces: `ollamaBackend`, `omlxBackend` implementing `backend`; `defaultEnv().backends["ollama"|"omlx"]` set.

- [ ] **Step 1: Write the failing tests** (`backends_test.go`)

```go
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func provCfg(id, baseURL string) *config.Config {
	return &config.Config{Providers: []config.Provider{{ID: id, Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}}}
}

// chatServer answers every path: GET -> {}, POST chat -> a chat.completion. It
// records POST bodies.
type chatServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newChatServer(t *testing.T) *chatServer {
	t.Helper()
	cs := &chatServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			cs.mu.Lock()
			cs.bodies = append(cs.bodies, m)
			cs.mu.Unlock()
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"models":[]}`))
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *chatServer) lastModel() string {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if len(cs.bodies) == 0 {
		return ""
	}
	s, _ := cs.bodies[len(cs.bodies)-1]["model"].(string)
	return s
}

// freeAddr reserves then releases a local port so a test can start a server on
// it later (modelling "daemon down, then `omlx start` brings it up").
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func serveAt(t *testing.T, addr string, h http.Handler) *http.Server {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func chatHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
}

// TestOllamaStartWarmsModel verifies an ollama start checks the daemon, then
// sends a 1-token chat naming the model (which loads it) and reports only the
// warming stage — there is no process to start.
func TestOllamaStartWarmsModel(t *testing.T) {
	srv := newChatServer(t)
	e := testEnv()
	var stages []Stage
	err := ollamaBackend{}.start(context.Background(), e, provCfg("ollama", srv.URL), Target{"ollama", "gemma4:9b"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if srv.lastModel() != "gemma4:9b" || !reflect.DeepEqual(stages, []Stage{StageWarming}) {
		t.Errorf("model=%q stages=%v", srv.lastModel(), stages)
	}
}

// TestOllamaDaemonDown verifies a dead daemon yields *DaemonDownError (with the
// origin) and no chat attempt — wt reports instead of kickstarting launchd.
func TestOllamaDaemonDown(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	err := ollamaBackend{}.start(context.Background(), testEnv(), provCfg("ollama", url), Target{"ollama", "m"}, func(Stage) {})
	var dd *DaemonDownError
	if !errors.As(err, &dd) || dd.Origin != url {
		t.Errorf("err = %v, want *DaemonDownError with origin %s", err, url)
	}
}

// TestOllamaIsMultiTenant verifies ollama declares itself not single-model and
// its stop is a no-op, so replacement logic never tries to stop the daemon.
func TestOllamaIsMultiTenant(t *testing.T) {
	if (ollamaBackend{}).singleModel() {
		t.Error("ollama must not be single-model")
	}
	if err := (ollamaBackend{}).stop(context.Background(), testEnv(), &config.Config{}); err != nil {
		t.Errorf("stop = %v, want nil", err)
	}
}

// TestOmlxAlreadyUpSkipsStartAndWarmsBasename verifies that with the daemon
// answering, no `omlx start` is run, and the warmup names the model's last path
// segment (omlx serves directory basenames, the registry stores HF repo ids).
func TestOmlxAlreadyUpSkipsStartAndWarmsBasename(t *testing.T) {
	srv := newChatServer(t)
	e := testEnv()
	var ran [][]string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, append([]string{name}, args...))
		return nil, nil
	}
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var stages []Stage
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", srv.URL), Target{"omlx", "mlx-community/Qwen3.8-27B-4bit"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("omlx start must not run when the daemon is up, ran %v", ran)
	}
	if srv.lastModel() != "Qwen3.8-27B-4bit" || !reflect.DeepEqual(stages, []Stage{StageWarming}) {
		t.Errorf("model=%q stages=%v", srv.lastModel(), stages)
	}
}

// TestOmlxStartsDaemonWhenDown verifies a down daemon triggers `omlx start`
// (stage starting), waits for the port, then warms — the port comes up when the
// fake `omlx start` starts a server on the configured address.
func TestOmlxStartsDaemonWhenDown(t *testing.T) {
	addr := freeAddr(t)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name)
		ran = append(ran, args...)
		serveAt(t, addr, chatHandler())
		return nil, nil
	}
	var stages []Stage
	err := omlxBackend{}.start(context.Background(), e, provCfg("omlx", "http://"+addr), Target{"omlx", "Qwen-4bit"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ran, []string{"/bin/omlx", "start"}) || !reflect.DeepEqual(stages, []Stage{StageStarting, StageWarming}) {
		t.Errorf("ran=%v stages=%v", ran, stages)
	}
}

// TestOmlxMissingBinaryAndNeverComesUp verifies the two failure shapes: no omlx
// binary -> *BinaryMissingError; `omlx start` that never brings the port up ->
// an error that includes the command's output so the user can see why.
func TestOmlxMissingBinaryAndNeverComesUp(t *testing.T) {
	url := "http://" + freeAddr(t)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	var bm *BinaryMissingError
	if err := (omlxBackend{}).start(context.Background(), e, provCfg("omlx", url), Target{"omlx", "m"}, func(Stage) {}); !errors.As(err, &bm) {
		t.Errorf("err = %v, want *BinaryMissingError", err)
	}

	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) { return []byte("license expired"), errors.New("exit 1") }
	err := (omlxBackend{}).start(context.Background(), e, provCfg("omlx", url), Target{"omlx", "m"}, func(Stage) {})
	if err == nil || !containsFold(err.Error(), "license expired") {
		t.Errorf("err = %v, want it to include the omlx start output", err)
	}
}

// TestOmlxStopWaitsForPortClose verifies stop runs `omlx stop` and returns nil
// once the port closes, and returns an error naming the origin when it does
// not — a replacement must not start while the old daemon still holds the port.
func TestOmlxStopWaitsForPortClose(t *testing.T) {
	addr := freeAddr(t)
	srv := serveAt(t, addr, chatHandler())
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/omlx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, args...)
		_ = srv.Close()
		return nil, nil
	}
	if err := (omlxBackend{}).stop(context.Background(), e, provCfg("omlx", "http://"+addr)); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"stop"}) {
		t.Errorf("ran = %v", ran)
	}

	addr2 := freeAddr(t)
	serveAt(t, addr2, chatHandler())
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	e.stopTimeout = 60 * time.Millisecond
	if err := (omlxBackend{}).stop(context.Background(), e, provCfg("omlx", "http://"+addr2)); err == nil || !containsFold(err.Error(), addr2) {
		t.Errorf("err = %v, want still-listening error naming %s", err, addr2)
	}
}

// TestDefaultEnvRegistersBackends verifies the production env wires the ollama
// and omlx backends (single-model: omlx yes, ollama no).
func TestDefaultEnvRegistersBackends(t *testing.T) {
	e := defaultEnv()
	if e.backends["ollama"] == nil || e.backends["omlx"] == nil {
		t.Fatalf("backends = %v", e.backends)
	}
	if e.backends["ollama"].singleModel() || !e.backends["omlx"].singleModel() {
		t.Error("ollama must be multi-tenant and omlx single-model")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/lifecycle -run 'Ollama|Omlx|DefaultEnv' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`ollama.go`:
```go
package lifecycle

import (
	"context"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// ollamaBackend loads a model into a running ollama daemon. There is no
// process to start (wt does not kickstart the daemon) and ollama is
// multi-tenant, so nothing is ever replaced.
type ollamaBackend struct{}

func (ollamaBackend) singleModel() bool { return false }

func (ollamaBackend) stop(ctx context.Context, e *env, cfg *config.Config) error { return nil }

func (ollamaBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "ollama")
	health := origin + "/api/tags"
	if responded, _ := e.probe(ctx, health, 2*time.Second); !responded {
		if err := ctx.Err(); err != nil {
			return err
		}
		return &DaemonDownError{Provider: "ollama", Origin: origin}
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, health, e.warmupTimeout)
}
```
`omlx.go`:
```go
package lifecycle

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// omlxBackend serves omlx and omlx-6bit, which are ONE physical daemon on one
// port; only one model is treated as the occupant.
type omlxBackend struct{}

func (omlxBackend) singleModel() bool { return true }

func (omlxBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	health := origin + "/v1/models"
	if responded, _ := e.probe(ctx, health, 2*time.Second); !responded {
		if err := ctx.Err(); err != nil {
			return err
		}
		bin, err := e.lookPath("omlx")
		if err != nil {
			return &BinaryMissingError{Binary: "omlx"}
		}
		report(StageStarting)
		out, runErr := e.run(ctx, bin, "start")
		up, werr := e.waitPortOpen(ctx, health, e.portUpTimeout)
		if werr != nil {
			return werr
		}
		if !up {
			return fmt.Errorf("omlx did not come up at %s (omlx start: %v: %s)", health, runErr, strings.TrimSpace(string(out)))
		}
	}
	report(StageWarming)
	// omlx serves directory basenames; registry names are HF repo ids.
	return e.warmup(ctx, origin+"/v1/chat/completions", path.Base(t.ModelName), health, e.warmupTimeout)
}

func (omlxBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	if bin, err := e.lookPath("omlx"); err == nil {
		_, _ = e.run(ctx, bin, "stop")
	}
	closed, err := e.portClosedWithin(ctx, origin+"/v1/models", e.stopTimeout)
	if err != nil {
		return err
	}
	if !closed {
		return fmt.Errorf("omlx still listening at %s", origin)
	}
	return nil
}
```
`env.go` `defaultEnv`: replace `backends: map[string]backend{}` with `backends: map[string]backend{"ollama": ollamaBackend{}, "omlx": omlxBackend{}}`.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/lifecycle -count=2 -v 2>&1 | tail -25 && go vet ./... && go build ./...` — Expected: PASS.

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/lifecycle
git commit -m "feat(lifecycle): ollama and omlx backends - completes plan item #4"
```

---

### Task 5: mtplx — pid-tracked process and backend

**Files:**
- Create: `wt/internal/lifecycle/pidproc.go`, `wt/internal/lifecycle/mtplx.go`, `wt/internal/lifecycle/mtplx_test.go`
- Modify: `wt/internal/lifecycle/env.go` (delete the temporary `pidProcess` stub; register `"mtplx": mtplxBackend{}`)

**Interfaces:**
- Consumes: `env` primitives, `procWatch`, `backend`, errors, `localmodels.FamilyOrigin`.
- Produces:
  - `type pidProcess struct{ name, pidfile, logfile string }`, `func (p pidProcess) spawn(bin string, args []string) (*spawned, error)`, `func (p pidProcess) logTail(max int) string`
  - `type spawned struct{...}` with `exited() (bool, error)` (satisfies `procWatch`) and `kill()` (SIGTERM, SIGKILL after 5s, removes the pidfile)
  - `mtplxBackend` implementing `backend`; `defaultEnv().backends["mtplx"]` set.

- [ ] **Step 1: Write the failing tests** (`mtplx_test.go`)

The tests use a **helper process**: the test binary re-invoked as a fake `mtplx`. `TestMain` intercepts when `LIFECYCLE_HELPER=mtplx` is set, records its argv (and pid) to `LIFECYCLE_ARGV_FILE`, and behaves per `LIFECYCLE_HELPER_MODE`: `serve` (default: after `LIFECYCLE_HELPER_DELAY_MS` listen on `--host:--port`, serve `/v1/models` listing `--model-id` and answer POST chat with `{"object":"chat.completion"}`, exit 0 on SIGTERM), `die` (print `boom` to stderr, exit 3 after 400ms), `die-now` (exit 1 immediately).

```go
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func TestMain(m *testing.M) {
	if os.Getenv("LIFECYCLE_HELPER") == "mtplx" {
		helperMtplx()
		return
	}
	os.Exit(m.Run())
}

// helperMtplx is the fake `mtplx serve` process (see file comment).
func helperMtplx() {
	args := os.Args[1:]
	opt := map[string]string{}
	for i := 1; i+1 < len(args); i += 2 { // args[0] == "serve"
		opt[strings.TrimPrefix(args[i], "--")] = args[i+1]
	}
	if f := os.Getenv("LIFECYCLE_ARGV_FILE"); f != "" {
		_ = os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())+"\n"+strings.Join(args, " ")), 0o644)
	}
	switch os.Getenv("LIFECYCLE_HELPER_MODE") {
	case "die-now":
		os.Exit(1)
	case "die":
		time.Sleep(400 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	}
	if ms, _ := strconv.Atoi(os.Getenv("LIFECYCLE_HELPER_DELAY_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": opt["model-id"]}}})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
	})
	l, err := net.Listen("tcp", opt["host"]+":"+opt["port"])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	go func() { _ = http.Serve(l, mux) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	os.Exit(0)
}

// mtplxEnv builds an env whose fake mtplx binary is this test binary, with the
// pidfile/log/argv files under t.TempDir, plus an mtplx cfg on a free port.
func mtplxEnv(t *testing.T, mode string, delayMS int) (*env, *config.Config, int, string) {
	t.Helper()
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	t.Setenv("LIFECYCLE_HELPER", "mtplx")
	t.Setenv("LIFECYCLE_ARGV_FILE", argvFile)
	t.Setenv("LIFECYCLE_HELPER_MODE", mode)
	t.Setenv("LIFECYCLE_HELPER_DELAY_MS", strconv.Itoa(delayMS))
	e := testEnv()
	e.mtplxProc = pidProcess{name: "mtplx", pidfile: filepath.Join(dir, "mtplx.pid"), logfile: filepath.Join(dir, "mtplx.log")}
	e.lookPath = func(string) (string, error) { return os.Args[0], nil }
	e.loadTimeout = 3 * time.Second
	addr := freeAddr(t)
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	t.Cleanup(func() { // kill a helper the test left running
		if b, err := os.ReadFile(e.mtplxProc.pidfile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	})
	return e, provCfg("mtplx", "http://"+addr+"/v1"), port, argvFile
}

func pidGone(pid int) bool {
	for i := 0; i < 40; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestMtplxStartSpawnsWaitsWarms verifies the full happy path: the exact serve
// argv (identical to modelman's), a pidfile and log at the configured paths,
// stages starting -> waiting-for-model -> warming, and a server that answers.
// Matching modelman's argv/pidfile keeps the two tools interoperable.
func TestMtplxStartSpawnsWaitsWarms(t *testing.T) {
	e, cfg, port, argvFile := mtplxEnv(t, "serve", 0)
	var stages []Stage
	err := mtplxBackend{}.start(context.Background(), e, cfg, Target{"mtplx", "Org/Model"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !reflect.DeepEqual(stages, []Stage{StageStarting, StageWaiting, StageWarming}) {
		t.Errorf("stages = %v", stages)
	}
	b, _ := os.ReadFile(argvFile)
	lines := strings.SplitN(string(b), "\n", 2)
	want := fmt.Sprintf("serve --model Org/Model --port %d --host 127.0.0.1 --model-id Org/Model", port)
	if len(lines) != 2 || lines[1] != want {
		t.Errorf("argv = %q, want %q", lines[1:], want)
	}
	pidB, err := os.ReadFile(e.mtplxProc.pidfile)
	if err != nil || strings.TrimSpace(string(pidB)) != strings.TrimSpace(lines[0]) {
		t.Errorf("pidfile = %q (%v), want the helper pid %s", pidB, err, lines[0])
	}
	if _, err := os.Stat(e.mtplxProc.logfile); err != nil {
		t.Errorf("logfile missing: %v", err)
	}
}

// TestMtplxCancelTearsDownSpawnedProcess verifies cancelling mid-load kills the
// process wt spawned and removes the pidfile — a half-started server must not
// keep holding GPU memory and port 8003 after the user backed out.
func TestMtplxCancelTearsDownSpawnedProcess(t *testing.T) {
	e, cfg, _, argvFile := mtplxEnv(t, "serve", 5000) // never listens within the test
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	err := mtplxBackend{}.start(ctx, e, cfg, Target{"mtplx", "Org/Model"}, func(Stage) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	b, _ := os.ReadFile(argvFile)
	pid, _ := strconv.Atoi(strings.SplitN(string(b), "\n", 2)[0])
	if pid == 0 || !pidGone(pid) {
		t.Errorf("spawned process %d still alive after cancel", pid)
	}
	if _, err := os.Stat(e.mtplxProc.pidfile); !os.IsNotExist(err) {
		t.Errorf("pidfile should be removed, stat err = %v", err)
	}
}

// TestMtplxProcessDiesDuringLoad verifies a server that dies while loading
// fails fast with an "exited" error rather than waiting out the load budget,
// and immediate death is reported at spawn time.
func TestMtplxProcessDiesDuringLoad(t *testing.T) {
	e, cfg, _, _ := mtplxEnv(t, "die", 0)
	start := time.Now()
	err := mtplxBackend{}.start(context.Background(), e, cfg, Target{"mtplx", "Org/Model"}, func(Stage) {})
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("die: err = %v, want an 'exited' error", err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("should fail fast, took %v", time.Since(start))
	}

	e2, cfg2, _, _ := mtplxEnv(t, "die-now", 0)
	err = mtplxBackend{}.start(context.Background(), e2, cfg2, Target{"mtplx", "Org/Model"}, func(Stage) {})
	if err == nil || !strings.Contains(err.Error(), "exited immediately") {
		t.Errorf("die-now: err = %v, want 'exited immediately'", err)
	}
}

// TestMtplxMissingBinaryAndPortBusy verifies: no mtplx on PATH ->
// *BinaryMissingError; a port that still answers before spawning ->
// *PortBusyError and nothing spawned (wt never kills unknown processes).
func TestMtplxMissingBinaryAndPortBusy(t *testing.T) {
	e, cfg, _, argvFile := mtplxEnv(t, "serve", 0)
	e.lookPath = func(string) (string, error) { return "", errors.New("nope") }
	var bm *BinaryMissingError
	if err := (mtplxBackend{}).start(context.Background(), e, cfg, Target{"mtplx", "m"}, func(Stage) {}); !errors.As(err, &bm) {
		t.Errorf("err = %v, want *BinaryMissingError", err)
	}

	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()
	e.lookPath = func(string) (string, error) { return os.Args[0], nil }
	err := (mtplxBackend{}).start(context.Background(), e, provCfg("mtplx", busy.URL), Target{"mtplx", "m"}, func(Stage) {})
	var pb *PortBusyError
	if !errors.As(err, &pb) {
		t.Errorf("err = %v, want *PortBusyError", err)
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Error("nothing should have been spawned when the port is busy")
	}
}

// TestMtplxStopRunsMtplxStopAndConfirmsPortClosed verifies replacement stop
// runs `mtplx stop --port N --grace-seconds 10` (modelman's command) and only
// succeeds once the port really closed; a stop that leaves the port open
// surfaces the command's own error text.
func TestMtplxStopRunsMtplxStopAndConfirmsPortClosed(t *testing.T) {
	addr := freeAddr(t)
	srv := serveAt(t, addr, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append([]string{name}, args...)
		_ = srv.Close()
		return nil, nil
	}
	cfg := provCfg("mtplx", "http://"+addr+"/v1")
	if err := (mtplxBackend{}).stop(context.Background(), e, cfg); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"/bin/mtplx", "stop", "--port", portStr, "--grace-seconds", "10"}) {
		t.Errorf("ran = %v", ran)
	}

	addr2 := freeAddr(t)
	serveAt(t, addr2, chatHandler())
	e.stopTimeout = 60 * time.Millisecond
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) { return []byte("no such server"), errors.New("exit 1") }
	if err := (mtplxBackend{}).stop(context.Background(), e, provCfg("mtplx", "http://"+addr2+"/v1")); err == nil || !strings.Contains(err.Error(), "no such server") {
		t.Errorf("err = %v, want the command's stderr text", err)
	}
}

// TestDefaultEnvRegistersMtplx verifies the production env wires mtplx as a
// single-model backend with modelman's pidfile and log paths.
func TestDefaultEnvRegistersMtplx(t *testing.T) {
	e := defaultEnv()
	if e.backends["mtplx"] == nil || !e.backends["mtplx"].singleModel() {
		t.Fatal("mtplx backend missing or not single-model")
	}
	if e.mtplxProc.pidfile != "/tmp/local-ai-setup-mtplx.pid" || e.mtplxProc.logfile != "/tmp/local-ai-setup-mtplx.log" {
		t.Errorf("mtplxProc = %+v, want modelman's paths", e.mtplxProc)
	}
}
```
(`TestMain` here must be the ONLY `TestMain` in the package. Task 2-4 tests run in the same binary; `LIFECYCLE_HELPER` is only set by `mtplxEnv` via `t.Setenv`, so they are unaffected.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/lifecycle -run Mtplx -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`pidproc.go`:
```go
package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// pidProcess is a pidfile-tracked background process: wt spawns it, records its
// pid where modelman also records it, and keeps the log.
type pidProcess struct{ name, pidfile, logfile string }

// spawned is a live child started by pidProcess.spawn.
type spawned struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	p       pidProcess
}

// spawn starts bin with args detached in its own session (it survives the
// terminal closing), stdout/stderr appended to the log, pid written to the
// pidfile. It returns an error (with a log tail) if the process exits within
// 200ms.
func (p pidProcess) spawn(bin string, args []string) (*spawned, error) {
	log, err := os.OpenFile(p.logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	_ = log.Close() // the child holds its own descriptor
	sp := &spawned{cmd: cmd, done: make(chan struct{}), p: p}
	go func() {
		sp.waitErr = cmd.Wait()
		close(sp.done)
	}()
	if err := os.WriteFile(p.pidfile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		sp.kill()
		return nil, err
	}
	select {
	case <-sp.done:
		msg := fmt.Sprintf("%s exited immediately (%v)", p.name, sp.waitErr)
		if tail := p.logTail(512); tail != "" {
			msg += "; log tail: " + tail
		}
		_ = os.Remove(p.pidfile)
		return nil, fmt.Errorf("%s", msg)
	case <-time.After(200 * time.Millisecond):
	}
	return sp, nil
}

// exited reports whether the process has exited (and how); it satisfies procWatch.
func (s *spawned) exited() (bool, error) {
	select {
	case <-s.done:
		return true, s.waitErr
	default:
		return false, nil
	}
}

// kill stops the process (SIGTERM, then SIGKILL after 5s) and removes the pidfile.
func (s *spawned) kill() {
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	_ = os.Remove(s.p.pidfile)
}

// logTail returns up to max trailing bytes of the log ("" when unreadable).
func (p pidProcess) logTail(max int) string {
	b, err := os.ReadFile(p.logfile)
	if err != nil {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b)
}
```
`mtplx.go`:
```go
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// mtplxBackend runs one `mtplx serve` process per model on one port, so
// starting another model replaces the current one.
type mtplxBackend struct{}

func (mtplxBackend) singleModel() bool { return true }

// mtplxEndpoint returns the origin, /v1/models URL and port for the family.
func mtplxEndpoint(cfg *config.Config) (origin, modelsURL string, port int) {
	origin, _ = localmodels.FamilyOrigin(cfg, "mtplx")
	port = 8003
	if u, err := url.Parse(origin); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			port = p
		}
	}
	return origin, origin + "/v1/models", port
}

func (mtplxBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) (err error) {
	bin, lerr := e.lookPath("mtplx")
	if lerr != nil {
		return &BinaryMissingError{Binary: "mtplx"}
	}
	origin, models, port := mtplxEndpoint(cfg)
	report(StageStarting)
	closed, cerr := e.portClosedWithin(ctx, models, e.prebindTimeout)
	if cerr != nil {
		return cerr
	}
	if !closed {
		return &PortBusyError{Port: port}
	}
	sp, serr := e.mtplxProc.spawn(bin, []string{
		"serve", "--model", t.ModelName, "--port", strconv.Itoa(port),
		"--host", "127.0.0.1", "--model-id", t.ModelName,
	})
	if serr != nil {
		return serr
	}
	defer func() {
		if err != nil {
			sp.kill() // never leave a half-started server holding the port
		}
	}()
	report(StageWaiting)
	if err = e.waitForModel(ctx, models, t.ModelName, sp, e.loadTimeout); err != nil {
		if tail := e.mtplxProc.logTail(512); tail != "" && !errors.Is(err, context.Canceled) {
			err = fmt.Errorf("%w; log tail: %s", err, strings.TrimSpace(tail))
		}
		return err
	}
	report(StageWarming)
	return e.warmup(ctx, origin+"/v1/chat/completions", t.ModelName, models, 120*time.Second)
}

func (mtplxBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	bin, lerr := e.lookPath("mtplx")
	if lerr != nil {
		return &BinaryMissingError{Binary: "mtplx"}
	}
	_, models, port := mtplxEndpoint(cfg)
	out, runErr := e.run(ctx, bin, "stop", "--port", strconv.Itoa(port), "--grace-seconds", "10")
	closed, cerr := e.portClosedWithin(ctx, models, e.stopTimeout)
	if cerr != nil {
		return cerr
	}
	if closed {
		return nil
	}
	if runErr != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = "mtplx stop failed"
		}
		return errors.New(msg)
	}
	return fmt.Errorf("mtplx still listening on port %d", port)
}
```
`env.go`: delete the temporary `pidProcess` stub; register `"mtplx": mtplxBackend{}` in `defaultEnv`. Note: `%w` with `err` reassign inside the deferred-kill function is fine because `err` is the named result.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/lifecycle -count=2 -v 2>&1 | tail -30 && go vet ./... && go build ./...` and `go test -race ./internal/lifecycle -count=1` — Expected: PASS, no data races. If a helper-process test is flaky under load, report it; do not loosen the timing bounds below the values given.

- [ ] **Step 5: Commit**
```bash
gofmt -l internal
git add wt/internal/lifecycle
git commit -m "feat(lifecycle): mtplx backend and pid-tracked process - completes plan item #5"
```

---

### Task 6: Documentation

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Edit `wt/CLAUDE.md`**
  - Package list: add `lifecycle`.
  - Package table: add row `internal/lifecycle/` — "Local model start engine (Go port of modelman's start/warmup): `Start(ctx, cfg, Target{ProviderID, ModelName}, Options{AllowReplace, Progress})`, `Occupant(target, snapshot)`, `Stop`. Backends: ollama (daemon must answer; 1-token chat loads the model; multi-tenant), omlx (`omlx start` when the daemon is down, warm by model basename, `omlx stop` to replace), mtplx (`mtplx serve …` spawned in its own session with modelman's pidfile/log paths; `mtplx stop --port N` to replace; torn down on failure/cancel). Live Inventory decides already-running and occupant; never replaces without `AllowReplace` (returns `*OccupiedError`); typed errors `DaemonDownError`/`BinaryMissingError`/`PortBusyError`/`UnsupportedError`. Writes nothing to modelman state; does not kickstart the ollama daemon. Not yet wired into a launch path (sub-projects 4b/4c consume it)."
  - `internal/localmodels/` row: mention `Entry.ModelName` and exported `Family`.
  - The "wt never shells out for discovery" note: keep it true for discovery, and add that `internal/lifecycle` (not discovery) does exec provider binaries (`omlx`, `mtplx`).
  - Test seams list: mention the lifecycle `env` (all seams in one struct, tests build it with `testEnv()`), and that the package's `TestMain` doubles as a fake `mtplx` helper process.

- [ ] **Step 2: Verify** — from the repo root `make check-links && git grep -n "internal/lifecycle" wt/CLAUDE.md` — Expected: links OK; matches present.

- [ ] **Step 3: Final verification** — from `wt/`: `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .` — Expected: all pass; `gofmt -l` prints nothing.

- [ ] **Step 4: Commit**
```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document the lifecycle engine - completes plan item #6"
```

---

## Self-Review

- **Spec coverage:** API (`Start`/`Occupant`/`Stop`, `Target`, `Options`, stages) -> Task 3; never-silent-replace + idempotent -> Task 3 tests; per-provider behavior (ollama daemon check + warm, omlx start/warm/stop with basename, mtplx argv/session/pidfile/wait/warm/teardown/`mtplx stop`) -> Tasks 4-5; ports/origins from `FamilyOrigin` -> Tasks 4-5; timeouts -> Task 2 `defaultEnv`; cancel + typed errors -> Tasks 2-5; modelman-compatible argv/pidfile/log -> Task 5 tests; docs -> Task 6. Out-of-scope items (UI, non-TUI/pin/smoke, mlx_lm_server, modelman flags, LiteLLM, user-facing stop, stop-on-exit) untouched.
- **Spec-text deviations to flag at review:** (a) `PortBusyError` is added (the spec listed three typed errors; a fourth covers "port answers but no known occupant" so wt never kills unknown processes) and `UnsupportedError` (providers without a backend); (b) the spec said cancelling an omlx/ollama start "just stops waiting" — the plan's `ctx` checks implement exactly that; (c) `Entry.ModelName` and exported `localmodels.Family` are new, small `localmodels` changes (Task 1) the occupant rules need.
- **Placeholders:** none. Two temporary stubs (`backend`, `pidProcess`) in `env.go` are explicitly created in Task 2 and removed in Tasks 3 and 5, so each commit builds.
- **Known risk:** the mtplx tests spawn the test binary as a helper process and use real 200ms/400ms timings; the plan forbids loosening the timing bounds and asks for `-race` and `-count=2` runs.
- **Type consistency:** `env` fields, `backend` methods, `Target`/`Stage`/`Options`, error types, `procWatch`, `pidProcess`/`spawned`, and helper names (`testEnv`, `provCfg`, `freeAddr`, `serveAt`, `chatHandler`, `containsFold`) are used identically across tasks.
