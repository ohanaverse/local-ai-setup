package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func TestMain(m *testing.M) {
	if os.Getenv("LIFECYCLE_HELPER") == "mtplx" {
		helperMtplx()
		return
	}
	// No test may read the real mtplx pidfile or signal a process it did not
	// start: the production env's process seams fail and its pidfile is
	// nowhere (TestProcessSeamsFailClosedHere).
	IsolateProcessesForTest()
	// No test may reach the real config.yaml or proxy through the public wrappers.
	applyRoutes = func(*config.Config, litellm.Change, litellm.Options) (litellm.Result, error) {
		return litellm.Result{}, errors.New("applyRoutes not stubbed in this test")
	}
	probeProxy = func(context.Context, string, time.Duration) bool { return false }
	// No test may dial a family's default port to see whether it serves.
	unregisteredAnswering = func(*config.Config, *http.Client, localmodels.Snapshot, string) []string { return nil }
	os.Exit(m.Run())
}

// helperMtplx is the fake `mtplx serve` process.
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
	addr := opt["host"] + ":" + opt["port"]
	// A test of the port wt serves mtplx on by default must not bind it: the
	// helper then listens here, and the test's transport stands it on the
	// port it was told (servedAt).
	if a := os.Getenv("LIFECYCLE_HELPER_LISTEN"); a != "" {
		addr = a
	}
	l, err := net.Listen("tcp", addr)
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
	e.serverProc["mtplx"] = pidProcess{name: "mtplx", pidfile: filepath.Join(dir, "mtplx.pid"), logfile: filepath.Join(dir, "mtplx.log")}
	e.lookPath = func(string) (string, error) { return os.Args[0], nil }
	e.loadTimeout = 3 * time.Second
	addr := freeAddr(t)
	_, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	t.Cleanup(func() { // kill a helper the test left running
		if b, err := os.ReadFile(e.serverProc["mtplx"].pidfile); err == nil {
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
// argv, a pidfile and log at the configured paths, stages starting ->
// waiting-for-model -> warming, and a server that answers. llmbench's mtplx
// backend serves with the same argv and tracks the same pidfile, so either
// tool can stop or find the server the other started.
func TestMtplxStartSpawnsWaitsWarms(t *testing.T) {
	e, cfg, port, argvFile := mtplxEnv(t, "serve", 0)
	var stages []Stage
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(s Stage) { stages = append(stages, s) })
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
	pidB, err := os.ReadFile(e.serverProc["mtplx"].pidfile)
	if err != nil || strings.TrimSpace(string(pidB)) != strings.TrimSpace(lines[0]) {
		t.Errorf("pidfile = %q (%v), want the helper pid %s", pidB, err, lines[0])
	}
	if _, err := os.Stat(e.serverProc["mtplx"].logfile); err != nil {
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
	err := processServer{"mtplx"}.start(ctx, e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	b, _ := os.ReadFile(argvFile)
	pid, _ := strconv.Atoi(strings.SplitN(string(b), "\n", 2)[0])
	if pid == 0 || !pidGone(pid) {
		t.Errorf("spawned process %d still alive after cancel", pid)
	}
	if _, err := os.Stat(e.serverProc["mtplx"].pidfile); !os.IsNotExist(err) {
		t.Errorf("pidfile should be removed, stat err = %v", err)
	}
}

// TestMtplxProcessDiesDuringLoad verifies a server that dies while loading
// fails fast with an "exited" error rather than waiting out the load budget,
// and immediate death is reported at spawn time.
func TestMtplxProcessDiesDuringLoad(t *testing.T) {
	e, cfg, _, _ := mtplxEnv(t, "die", 0)
	start := time.Now()
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Errorf("die: err = %v, want an 'exited' error", err)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("should fail fast, took %v", time.Since(start))
	}

	e2, cfg2, _, _ := mtplxEnv(t, "die-now", 0)
	err = processServer{"mtplx"}.start(context.Background(), e2, cfg2, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
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
	if err := (processServer{"mtplx"}).start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "m"}, func(Stage) {}); !errors.As(err, &bm) {
		t.Errorf("err = %v, want *BinaryMissingError", err)
	}

	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()
	e.lookPath = func(string) (string, error) { return os.Args[0], nil }
	err := (processServer{"mtplx"}).start(context.Background(), e, provCfg("mtplx", busy.URL), Target{ProviderID: "mtplx", ModelName: "m"}, func(Stage) {})
	var pb *PortBusyError
	if !errors.As(err, &pb) {
		t.Errorf("err = %v, want *PortBusyError", err)
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Error("nothing should have been spawned when the port is busy")
	}
}

// TestMtplxStopRunsMtplxStopAndConfirmsPortClosed verifies replacement stop
// runs `mtplx stop --port N --grace-seconds 10` (the command llmbench's mtplx
// backend runs too) and only succeeds once the port really closed; a stop
// that leaves the port open surfaces the command's own error text.
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
	if err := (processServer{"mtplx"}).stop(context.Background(), e, cfg); err != nil {
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
	if err := (processServer{"mtplx"}).stop(context.Background(), e, provCfg("mtplx", "http://"+addr2+"/v1")); err == nil || !strings.Contains(err.Error(), "no such server") {
		t.Errorf("err = %v, want the command's stderr text", err)
	}
}

// TestDefaultEnvRegistersMtplx verifies the production env wires mtplx as a
// single-model backend with the pidfile and log paths llmbench's mtplx
// backend uses (MTPLX_PIDFILE and MTPLX_LOG in its backends/mtplx.py). With
// two paths, a server one tool started would be invisible to the other.
func TestDefaultEnvRegistersMtplx(t *testing.T) {
	t.Setenv(mtplxPidfileEnv, "") // TestMain points it away from the real files
	e := defaultEnv()
	if e.backends["mtplx"] == nil || e.backends["mtplx"].tenancy() != Exclusive {
		t.Fatal("mtplx backend missing or not single-model")
	}
	if e.serverProc["mtplx"].pidfile != "/tmp/local-ai-setup-mtplx.pid" || e.serverProc["mtplx"].logfile != "/tmp/local-ai-setup-mtplx.log" {
		t.Errorf("serverProc[\"mtplx\"] = %+v, want the paths llmbench's mtplx backend uses", e.serverProc["mtplx"])
	}
	if got := e.serverProc["mtplx"].startfile(); got != "/tmp/local-ai-setup-mtplx.pid.wt" {
		t.Errorf("start record = %q, want it beside the pidfile, which stays a bare pid for llmbench", got)
	}
	// WT_MTPLX_PIDFILE moves all three, so a run against scratch state never
	// touches the real ones.
	t.Setenv(mtplxPidfileEnv, "/scratch/m.pid")
	if p := defaultEnv().serverProc["mtplx"]; p.pidfile != "/scratch/m.pid" || p.logfile != "/scratch/m.log" || p.startfile() != "/scratch/m.pid.wt" {
		t.Errorf("serverProc[\"mtplx\"] under %s = %+v, want the pidfile it names and the log beside it", mtplxPidfileEnv, p)
	}
}

// TestMtplxEndpointPortMatchesModelsURL verifies the port mtplx is spawned with
// and the URL it is polled at come from one resolution. When they diverge the
// spawn succeeds and the wait then times out for the full load budget before
// killing the server it just started. The registry host is one the fallback
// cannot produce, so this passes only through the registry's own url: if a
// url with no port stopped resolving, the fallback's localhost origin would
// show up here instead of the registry's host and fail the test.
func TestMtplxEndpointPortMatchesModelsURL(t *testing.T) {
	cfg := provCfg("mtplx", "http://127.0.0.1") // registry value with no port
	origin, modelsURL, port := serverEndpoint(cfg, "mtplx")
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
// and shared with llmbench, so it grows across every start; a failed start must
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

// TestLogTailLinesBeginsAtALineStart verifies the tail shown to a user starts
// at the beginning of a line (#344): a cut at a byte count landed mid-word,
// so a failed start's message began "stained MTP runtime". The partial first
// line is dropped; a cut that already falls on a line start loses nothing; a
// log that fits is whole; and a tail with no newline at all — one long line —
// is still returned, since dropping it would show nothing. The same holds for
// a tail whose only newline is its last byte: a last line longer than max, or
// a run of "\r" progress redraws before the server's last words. And for a
// long last line with only blank lines after it: those are all a failed start
// would be left to show, and it trims them to an empty "log tail:".
func TestLogTailLinesBeginsAtALineStart(t *testing.T) {
	dir := t.TempDir()
	p := pidProcess{name: "mtplx", logfile: filepath.Join(dir, "mtplx.log")}
	for name, tc := range map[string]struct {
		log  string
		max  int
		want string
	}{
		"cut mid-line":            {"could not load the sustained MTP runtime\nfatal: out of memory\n", 40, "fatal: out of memory\n"},
		"cut on a line start":     {"first\nsecond\nthird\n", 13, "second\nthird\n"},
		"cut just after the line": {"first\nsecond\nthird\n", 12, "third\n"},
		"log fits":                {"first\nsecond\n", 512, "first\nsecond\n"},
		"log is exactly max":      {"first\nsecond\n", 13, "first\nsecond\n"},
		"one long line":           {strings.Repeat("x", 100), 10, strings.Repeat("x", 10)},
		"long last line":          {"first\n" + strings.Repeat("x", 100) + "\n", 10, strings.Repeat("x", 9) + "\n"},
		"long line, then a blank": {"first\n" + strings.Repeat("x", 100) + "\n\n", 10, strings.Repeat("x", 8) + "\n\n"},
		"redraws, then one line":  {"first\n" + strings.Repeat("10%\r", 20) + "boom\n", 10, "\r10%\rboom\n"},
		"empty log":               {"", 512, ""},
		"nothing asked for":       {"first\n", 0, ""},
	} {
		if err := os.WriteFile(p.logfile, []byte(tc.log), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := p.logTailLines(tc.max); got != tc.want {
			t.Errorf("%s: logTailLines(%d) = %q, want %q", name, tc.max, got, tc.want)
		}
	}
	if got := (pidProcess{logfile: filepath.Join(dir, "missing.log")}).logTailLines(512); got != "" {
		t.Errorf("unreadable log: logTailLines = %q, want empty", got)
	}
}

// TestLogTailLinesReadsOnlyTheTail verifies the line-start tail keeps
// logTail's bound: the log is append-only and shared with llmbench, so it
// grows without limit, and a failed start must read one byte more than it
// shows, not the file.
func TestLogTailLinesReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	p := pidProcess{name: "mtplx", logfile: filepath.Join(dir, "mtplx.log")}
	content := append(bytes.Repeat([]byte("A"), 1<<20), []byte("\nlast line\n")...)
	if err := os.WriteFile(p.logfile, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.logTailLines(512); got != "last line\n" {
		t.Errorf("logTailLines(512) = %d bytes %q, want the last line alone", len(got), got)
	}
}

// TestMtplxFailedStartShowsWholeLogLines verifies the failed start itself
// uses the line-start tail: with more than 512 bytes of log before the
// server's last words, the error carries those lines and no fragment of the
// line the cut fell in.
func TestMtplxFailedStartShowsWholeLogLines(t *testing.T) {
	e, cfg, _, _ := mtplxEnv(t, "die", 0)
	if err := os.WriteFile(e.serverProc["mtplx"].logfile, []byte(strings.Repeat("x", 600)+" sustained MTP runtime\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
	if err == nil || !strings.HasSuffix(err.Error(), "; log tail: boom") {
		t.Errorf("err = %v, want the log tail to be the server's last line alone", err)
	}
}

// TestSpawnExitedImmediatelyShowsWholeLogLines is the same for a process that
// dies inside spawn's own 200ms watch: its "exited immediately" message
// carries the line-start tail too.
func TestSpawnExitedImmediatelyShowsWholeLogLines(t *testing.T) {
	e, cfg, _, _ := mtplxEnv(t, "die-now", 0)
	if err := os.WriteFile(e.serverProc["mtplx"].logfile, []byte(strings.Repeat("x", 600)+" sustained MTP runtime\nfatal: out of memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
	if err == nil || !strings.Contains(err.Error(), "exited immediately") {
		t.Fatalf("err = %v, want 'exited immediately'", err)
	}
	_, tail, _ := strings.Cut(err.Error(), "; log tail: ")
	if !strings.HasPrefix(tail, "fatal: out of memory\n") {
		t.Errorf("log tail = %q, want it to begin at a line start", tail)
	}
}

// TestLogTailClampsWhatArrivedAfterTheSizeWasSampled pins the clamp: the one
// guarantee that exists to make logTail's "at most max bytes" promise true even
// when more bytes arrive between sampling the size and reading. It matters
// because a failed start reads this tail to explain itself, and the log is
// shared with llmbench and appended by a live subprocess — so "more arrived
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

// TestMtplxStopNeverReportsAServerItLeftUp pins what `wt stop mtplx` and `wt
// stop --all` rely on when they halt an mtplx whose probe they could not
// read (#308): the stop is an error whenever the port still answers
// afterwards — `mtplx stop` exited 0 (a stale pidfile, or a server it does
// not own), or the mtplx binary is missing — so the command cannot print
// "done" and exit 0 over a server that is still up.
func TestMtplxStopNeverReportsAServerItLeftUp(t *testing.T) {
	_, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	cfg := provCfg("mtplx", "http://"+addr+"/v1")
	e := testEnv()
	e.stopTimeout = 60 * time.Millisecond
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	e.run = func(context.Context, string, ...string) ([]byte, error) { return []byte("no server to stop"), nil }
	err := (processServer{"mtplx"}).stop(context.Background(), e, cfg)
	if err == nil || !strings.Contains(err.Error(), "mtplx still listening on port "+portStr) {
		t.Errorf("exit 0, port open: err = %v, want still-listening", err)
	}

	e.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	var missing *BinaryMissingError
	if err := (processServer{"mtplx"}).stop(context.Background(), e, cfg); !errors.As(err, &missing) {
		t.Errorf("no binary: err = %v, want *BinaryMissingError", err)
	}
}

// TestProcessServerMtplxArgvPinned pins the exact argv the spec-driven
// backend hands mtplx: `serve --model M --port 8003 --host 127.0.0.1
// --model-id M` to spawn and `stop --port 8003 --grace-seconds 10` to stop.
// llmbench runs and finds the server with the same argv, and wt's own
// identification of a loading server matches on it (serverArgvMatches), so a
// template drift would make either tool blind to the other's server. The
// registry names no port, so the port is wt's default; the test stands the
// helper elsewhere and never binds 8003 (servedAt).
func TestProcessServerMtplxArgvPinned(t *testing.T) {
	e, _, _, argvFile := mtplxEnv(t, "serve", 0)
	listen := freeAddr(t)
	t.Setenv("LIFECYCLE_HELPER_LISTEN", listen)
	network := &servedAt{to: map[string]string{mtplxServeHost: listen}}
	network.on(e)
	cfg := provCfg("mtplx", "http://127.0.0.1/v1")
	b := processServer{"mtplx"}
	if err := b.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "M"}, func(Stage) {}); err != nil {
		t.Fatalf("start: %v", err)
	}
	raw, _ := os.ReadFile(argvFile)
	lines := strings.SplitN(string(raw), "\n", 2)
	if want := "serve --model M --port 8003 --host 127.0.0.1 --model-id M"; len(lines) != 2 || lines[1] != want {
		t.Errorf("spawn argv = %q, want %q", lines[1:], want)
	}
	pid, _ := strconv.Atoi(lines[0])
	var ran []string
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ran = append([]string{name}, args...)
		_ = syscall.Kill(pid, syscall.SIGTERM)
		pidGone(pid)
		return nil, nil
	}
	if err := b.stop(context.Background(), e, cfg); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if want := []string{os.Args[0], "stop", "--port", "8003", "--grace-seconds", "10"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("stop argv = %v, want %v", ran, want)
	}
}

// TestExpandArgs verifies a spec's argv template has {model} and {port}
// replaced in every element, wherever they appear, and everything else left
// as written: the template is the only place a family's command line is
// spelled, so the expansion must not add, drop or reorder arguments, nor
// change the template it was given.
func TestExpandArgs(t *testing.T) {
	tmpl := []string{"serve", "--model", "{model}", "--port", "{port}", "--id={model}@{port}", "plain"}
	got := expandArgs(tmpl, "Org/M", 8123)
	want := []string{"serve", "--model", "Org/M", "--port", "8123", "--id=Org/M@8123", "plain"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandArgs = %v, want %v", got, want)
	}
	if tmpl[2] != "{model}" {
		t.Errorf("expandArgs changed its template: %v", tmpl)
	}
	if got := expandArgs([]string{"stop", "--port", "{port}"}, "", 8003); !reflect.DeepEqual(got, []string{"stop", "--port", "8003"}) {
		t.Errorf("stop expansion = %v", got)
	}
}

// TestPortlessAddressErrorKeepsMtplxText verifies the generic error a start
// returns for a registry base_url with no port keeps, for mtplx, the exact
// sentence the mtplx-only address error printed before PortlessAddressError
// replaced it: users and docs know it, and the
// refactor must not change what wt says.
func TestPortlessAddressErrorKeepsMtplxText(t *testing.T) {
	err := &PortlessAddressError{Provider: "mtplx", Origin: "http://mybox"}
	want := "the registry's mtplx base_url (http://mybox) names no port, and wt serves mtplx on 127.0.0.1 — name the port mtplx listens on in the base_url, or use http://127.0.0.1"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestProcessServerIsComparable verifies the process-server backend is a
// comparable value holding only its family, so the production registry can
// still be compared with maps.Equal — the guard that keeps defaultEnv's
// backends and the families wt can start one set — and mtplx is built from
// the provider table as processServer{"mtplx"}.
func TestProcessServerIsComparable(t *testing.T) {
	if !maps.Equal(defaultEnv().backends, map[string]backend{"ollama": ollamaBackend{}, "omlx": omlxBackend{}, "mtplx": processServer{"mtplx"}, "rec": processServer{"rec"}}) {
		t.Errorf("defaultEnv().backends = %v, want ollama, omlx, processServer{mtplx} and processServer{rec}", defaultEnv().backends)
	}
}

// TestStartFailsFastWhenTheKeyCannotBeRead verifies a start whose provider
// names an auth.secret_ref wt cannot resolve stops at once with the key error
// (*KeyUnreadableError naming the family), before the port check and before
// anything is spawned or a pidfile written. Without it the start sent no key,
// polled a key-on-every-route server's 401s for the whole load budget and
// ended in a "timed out" message that never named the key.
func TestStartFailsFastWhenTheKeyCannotBeRead(t *testing.T) {
	e, cfg, _, argvFile := mtplxEnv(t, "serve", 0)
	cfg.Providers[0].Auth = config.AuthConfig{Type: "api_key", BaseURL: cfg.Providers[0].Auth.BaseURL, SecretRef: "exec:/nonexistent/key-cmd"}
	var stages []Stage
	start := time.Now()
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(s Stage) { stages = append(stages, s) })
	var ku *localmodels.KeyUnreadableError
	if !errors.As(err, &ku) || ku.Family != "mtplx" {
		t.Fatalf("err = %v, want *localmodels.KeyUnreadableError{Family: mtplx}", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("an unreadable key should fail at once, took %v", time.Since(start))
	}
	if _, err := os.Stat(argvFile); !os.IsNotExist(err) {
		t.Errorf("a process was spawned (argv file stat err = %v)", err)
	}
	if _, err := os.Stat(e.serverProc["mtplx"].pidfile); !os.IsNotExist(err) {
		t.Errorf("a pidfile was written (stat err = %v)", err)
	}
	if len(stages) != 0 {
		t.Errorf("stages reported = %v, want none", stages)
	}
}

// serveArgsTarget is a start of the registered mtplx model Org/Model.
func serveArgsTarget() Target {
	return Target{ProviderID: "mtplx", ModelID: "mtplx/Org--Model", ModelName: "Org/Model"}
}

func argvOf(t *testing.T, argvFile string) string {
	t.Helper()
	raw, _ := os.ReadFile(argvFile)
	lines := strings.SplitN(string(raw), "\n", 2)
	if len(lines) != 2 {
		t.Fatalf("nothing was spawned: argv file %q", raw)
	}
	return lines[1]
}

// TestMtplxStartAppendsTheRowsServeArgs pins #432: a registered model's
// serve_args follow wt's own arguments, in order, whichever way the row is
// found (by the target's id, or by provider and model name when the target
// carries no id), and a model with no row, or none in it, spawns exactly the
// argv the pinned test names. wt's arguments stay first so --model, --port and
// --model-id are read as they always were.
func TestMtplxStartAppendsTheRowsServeArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		target Target
		models []config.Model
		suffix string
	}{
		"by id":         {serveArgsTarget(), []config.Model{{ID: "mtplx/Org--Model", ProviderID: "mtplx", ModelName: "Org/Model", ServeArgs: config.ServeArgs{Args: []string{"--profile", "turbo"}}}}, " --profile turbo"},
		"by name":       {Target{ProviderID: "mtplx", ModelName: "Org/Model"}, []config.Model{{ID: "mtplx/Org--Model", ProviderID: "mtplx", ModelName: "Org/Model", ServeArgs: config.ServeArgs{Args: []string{"--verbose"}}}}, " --verbose"},
		"order kept":    {serveArgsTarget(), []config.Model{{ID: "mtplx/Org--Model", ProviderID: "mtplx", ModelName: "Org/Model", ServeArgs: config.ServeArgs{Args: []string{"--b", "2", "--a"}}}}, " --b 2 --a"},
		"row has none":  {serveArgsTarget(), []config.Model{{ID: "mtplx/Org--Model", ProviderID: "mtplx", ModelName: "Org/Model"}}, ""},
		"no row at all": {serveArgsTarget(), nil, ""},
		"another model": {serveArgsTarget(), []config.Model{{ID: "mtplx/Other", ProviderID: "mtplx", ModelName: "Other", ServeArgs: config.ServeArgs{Args: []string{"--profile", "turbo"}}}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e, cfg, port, argvFile := mtplxEnv(t, "serve", 0)
			cfg.Models = tc.models
			if err := (processServer{"mtplx"}).start(context.Background(), e, cfg, tc.target, func(Stage) {}); err != nil {
				t.Fatalf("start: %v", err)
			}
			want := fmt.Sprintf("serve --model Org/Model --port %d --host 127.0.0.1 --model-id Org/Model", port) + tc.suffix
			if got := argvOf(t, argvFile); got != want {
				t.Errorf("argv = %q, want %q", got, want)
			}
		})
	}
}

// TestMtplxStartRefusesUnusableServeArgsBeforeSpawning verifies a row whose
// serve_args cannot be used stops the start with *ServeArgsError naming the
// problem, before the port is checked and before anything is spawned: a flag
// wt sets itself (alone or as --flag=value), an empty argument, a value that
// was not an array of strings (read as absent by the loader, and still
// refused here, so a hand edit is not silently ignored), and a provider that
// takes none.
func TestMtplxStartRefusesUnusableServeArgsBeforeSpawning(t *testing.T) {
	row := func(provider string, sa config.ServeArgs) []config.Model {
		return []config.Model{{ID: "mtplx/Org--Model", ProviderID: provider, ModelName: "Org/Model", ServeArgs: sa}}
	}
	for name, tc := range map[string]struct {
		models []config.Model
		want   string
	}{
		"port":                {row("mtplx", config.ServeArgs{Args: []string{"--port", "9"}}), "names --port, which wt sets itself"},
		"model id with =":     {row("mtplx", config.ServeArgs{Args: []string{"--model-id=x"}}), "names --model-id"},
		"host":                {row("mtplx", config.ServeArgs{Args: []string{"--host", "0.0.0.0"}}), "names --host"},
		"model":               {row("mtplx", config.ServeArgs{Args: []string{"--model", "x"}}), "names --model"},
		"empty":               {row("mtplx", config.ServeArgs{Args: []string{""}}), "empty argument"},
		"not an array":        {row("mtplx", config.ServeArgs{Malformed: true}), "not an array of strings"},
		"provider takes none": {row("rec", config.ServeArgs{Args: []string{"--x"}}), "not used by provider rec"},
	} {
		t.Run(name, func(t *testing.T) {
			e, cfg, _, argvFile := mtplxEnv(t, "serve", 0)
			cfg.Models = tc.models
			err := (processServer{"mtplx"}).start(context.Background(), e, cfg, serveArgsTarget(), func(Stage) {})
			var sae *ServeArgsError
			if !errors.As(err, &sae) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "mtplx/Org--Model") {
				t.Fatalf("err = %v, want *ServeArgsError naming the model and %q", err, tc.want)
			}
			if _, statErr := os.Stat(argvFile); statErr == nil {
				t.Error("a refused start spawned a process")
			}
		})
	}
}
