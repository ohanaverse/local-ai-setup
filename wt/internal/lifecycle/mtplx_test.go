package lifecycle

import (
	"bytes"
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

func TestMain(m *testing.M) {
	if os.Getenv("LIFECYCLE_HELPER") == "mtplx" {
		helperMtplx()
		return
	}
	// No test may reach the real config.yaml or proxy through the public wrappers.
	applyRoutes = func(*config.Config, []string, []string, litellm.Options) (litellm.Result, error) {
		return litellm.Result{}, errors.New("applyRoutes not stubbed in this test")
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
	srv, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	e := testEnv()
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append([]string{name}, args...)
		srv.Close()
		return nil, nil
	}
	cfg := provCfg("mtplx", "http://"+addr+"/v1")
	if err := (mtplxBackend{}).stop(context.Background(), e, cfg); err != nil {
		logPortHolder(t, addr)
		t.Fatalf("stop: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"/bin/mtplx", "stop", "--port", portStr, "--grace-seconds", "10"}) {
		t.Errorf("ran = %v", ran)
	}

	_, addr2 := serveFree(t, chatHandler())
	e.stopTimeout = 60 * time.Millisecond
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("no such server"), errors.New("exit 1")
	}
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

// TestMtplxEndpointPortMatchesModelsURL verifies the port mtplx is spawned with
// and the URL it is polled at come from one resolution. When they diverge the
// spawn succeeds and the wait then times out for the full load budget before
// killing the server it just started. The registry host is one the fallback
// cannot produce, so this passes only through the portless resolution path: if
// that path (and defaultPortFor) went away, the fallback's localhost origin
// would show up here instead of the registry's host and fail the test.
func TestMtplxEndpointPortMatchesModelsURL(t *testing.T) {
	cfg := provCfg("mtplx", "http://127.0.0.1") // registry value with no port
	origin, modelsURL, port := mtplxEndpoint(cfg)
	if origin != "http://127.0.0.1:8003" {
		t.Errorf("origin = %q, want http://127.0.0.1:8003", origin)
	}
	if modelsURL != "http://127.0.0.1:8003/v1/models" {
		t.Errorf("modelsURL = %q, want http://127.0.0.1:8003/v1/models", modelsURL)
	}
	if port != 8003 {
		t.Errorf("port = %d, want 8003", port)
	}
}

// TestLogTailReadsOnlyTheTail verifies logTail returns up to the last max bytes
// of a log, and nothing when the file is missing. The mtplx log is append-only
// and shared with modelman, so it grows across every start; a failed start must
// not depend on the whole file fitting in memory to show a 512-byte tail. The
// boundary cases below pin the behaviour at the edges: max == size must return
// all N bytes (at equality the `>`-vs-`>=` choice is behaviourally identical, so
// this pins the boundary result rather than discriminating that operator), max
// == 0 must return nothing rather than the whole file, and a zero-byte log must
// come back empty without panicking. These pin the boundary *result*, not the
// clamp: when the file is longer than max the seek already lands max bytes from
// the end, so the clamp body runs only if the log grew between the stat and the
// read.
func TestLogTailReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	p := pidProcess{
		name:    "mtplx",
		pidfile: filepath.Join(dir, "mtplx.pid"),
		logfile: filepath.Join(dir, "mtplx.log"),
	}
	content := append(bytes.Repeat([]byte("A"), 4096), []byte("TAIL")...)
	if err := os.WriteFile(p.logfile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.logTail(4); got != "TAIL" {
		t.Errorf("logTail(4) = %q, want %q", got, "TAIL")
	}
	if got := p.logTail(1 << 20); got != string(content) {
		t.Errorf("logTail larger than the file = %d bytes, want all %d", len(got), len(content))
	}
	if got := p.logTail(len(content)); got != string(content) {
		t.Errorf("logTail(exactly the file size) = %d bytes, want all %d", len(got), len(content))
	}
	if got := p.logTail(0); got != "" {
		t.Errorf("logTail(0) = %q, want empty (never the whole file)", got)
	}
	emptyFile := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := (pidProcess{logfile: emptyFile}).logTail(8); got != "" {
		t.Errorf("logTail on an empty file = %q, want empty", got)
	}
	missing := pidProcess{logfile: filepath.Join(dir, "nope.log")}
	if got := missing.logTail(8); got != "" {
		t.Errorf("logTail on a missing file = %q, want empty", got)
	}
}

// TestLogTailClampsWhatArrivedAfterTheSizeWasSampled pins the clamp: the one
// guarantee that exists to make logTail's "at most max bytes" promise true even
// when more bytes arrive between sampling the size and reading. It matters
// because a failed start reads this tail to explain itself, and the log is
// shared with modelman and appended by a live subprocess — so "more arrived
// after the stat" is reachable exactly when the tail is read.
//
// A regular file cannot reach the clamp: when the file is longer than max the
// seek already leaves exactly max bytes, so len(b) > max is false (which is why
// the boundary cases above cannot pin it). A FIFO can — it reports size 0, so
// the seek guard is skipped and the whole payload comes back. That is the
// append race made deterministic rather than simulated: without the clamp these
// assertions return all 4100 bytes, with it the last max. The writer closes, so
// ReadAll sees EOF — no sleeps and no timing dependence.
//
// Note what this does NOT pin: the bounded read. A logTail that read the whole
// file and then clamped would pass, since the clamp is the behaviour under
// test; only the returned value's guarantee is asserted here.
func TestLogTailClampsWhatArrivedAfterTheSizeWasSampled(t *testing.T) {
	dir := t.TempDir()
	content := append(bytes.Repeat([]byte("A"), 4096), []byte("TAIL")...)

	// fifoTail makes one logTail call over a fresh FIFO carrying content, and
	// returns what logTail gave back plus any error from the writing end. Each
	// call needs its own FIFO: logTail's open blocks until a writer opens, and
	// that writer must close to give the read a clean EOF. The writer's error
	// travels on a channel so the test never reads a variable the goroutine
	// writes (-race).
	n := 0
	fifoTail := func(max int) (string, error) {
		t.Helper()
		n++ // unique per call: two calls must never share a path
		fifo := filepath.Join(dir, fmt.Sprintf("log-%d-%d.fifo", max, n))
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			// A name collision is a bug in this fixture, not an environment
			// limit. Skipping on it would hide the only pin on the clamp, and
			// hide it green: CI runs `go test ./...` non-verbose, where a SKIP
			// prints nothing.
			if errors.Is(err, syscall.EEXIST) {
				t.Fatalf("FIFO %s already exists: fixture names must be unique per call", fifo)
			}
			t.Skipf("cannot create a FIFO in this environment: %v", err)
		}
		werr := make(chan error, 1)
		go func() {
			// Blocks until logTail opens the read end, which is what lets this
			// writer run at all: if logTail's open were to fail, this goroutine
			// would stay blocked and the receive below would never return, so
			// the error check after the read is unreachable for that case.
			w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
			if err == nil {
				_, err = w.Write(content)
				_ = w.Close()
			}
			werr <- err
		}()
		// Operands evaluate left to right: read first, then collect the error.
		return pidProcess{name: "mtplx", logfile: fifo}.logTail(max), <-werr
	}

	// max < the payload: the clamp must keep the last max bytes of what it read.
	got, werr := fifoTail(4)
	if werr != nil {
		t.Fatalf("writing the FIFO failed: %v", werr)
	}
	if got != "TAIL" {
		t.Errorf("logTail(4) over a FIFO = %q, want %q: the clamp must keep the last max bytes of what was read", got, "TAIL")
	}

	// max == 0: the clamp must hand back nothing, not the 4100 bytes it read.
	// An off-by-one in the clamp's index fails here too.
	got, werr = fifoTail(0)
	if werr != nil {
		t.Fatalf("writing the FIFO failed: %v", werr)
	}
	if got != "" {
		t.Errorf("logTail(0) over a FIFO = %d bytes, want empty: the clamp must never hand back everything it read", len(got))
	}
}
