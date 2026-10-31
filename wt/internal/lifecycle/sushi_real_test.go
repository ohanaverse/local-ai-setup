package lifecycle

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// Conformance of the REAL sushi provider row (provider.ProcessFor("sushi"),
// not the synthetic sushix): the row is run through the engine with every
// external effect stubbed. A real sushi may be installed on the machine
// running these tests, so nothing here may run it, dial its port 12345 or
// touch /tmp/local-ai-setup-sushi.pid: the binary lookPath finds is this test
// binary run as helperSushi, the port is the registry's (a free one), the
// pidfile is in t.TempDir, and the process table and signal are procTable's.

// sushiPack is the registry's model_name for the real pack.
const sushiPack = "beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw"

// realSushiEnv is an env for the real sushi row whose server is this test
// binary run as helperSushi, listing sushiPack as loaded unless loaded is
// false. The registry (provCfg) puts the server on a free port, never 12345;
// the pidfile, log, argv file and chat log are under t.TempDir. lookPath
// answers only for "sushi" and hands back this test binary; spawn refuses any
// other binary and enters the spawned helper in the fake process table with a
// sushi argv; a signal (fake) is passed on only to that helper; running a
// command fails the test. It returns the table, the env, the cfg, the port,
// the argv file and the chat log.
func realSushiEnv(t *testing.T, loaded bool) (*procTable, *env, *config.Config, int, string, string) {
	t.Helper()
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	chatLog := filepath.Join(dir, "chat.log")
	t.Setenv("LIFECYCLE_HELPER", "sushi")
	t.Setenv("LIFECYCLE_ARGV_FILE", argvFile)
	t.Setenv("LIFECYCLE_SUSHI_PACK", sushiPack)
	t.Setenv("LIFECYCLE_SUSHI_LOG", chatLog)
	t.Setenv("LIFECYCLE_SUSHI_LOADED", strconv.FormatBool(loaded))
	t.Setenv("LIFECYCLE_HELPER_CHAT_STATUS", "200")
	_, portStr, _ := net.SplitHostPort(freeAddr(t))
	port, _ := strconv.Atoi(portStr)
	cfg := provCfg("sushi", "http://127.0.0.1:"+portStr+"/v1")
	if _, _, got, err := serverStartEndpoint(cfg, "sushi"); err != nil || got != port || got == 12345 {
		t.Fatalf("precondition: sushi start port = %d (%v), want the registry's %d, never 12345", got, err, port)
	}

	pt := &procTable{procs: map[int]procInfo{}}
	e := pt.env(t)
	e.serverProc["sushi"] = pidProcess{name: "sushi", pidfile: filepath.Join(dir, "sushi.pid"), logfile: filepath.Join(dir, "sushi.log")}
	e.loadTimeout = 3 * time.Second
	e.stopTimeout = 3 * time.Second
	e.lookPath = func(name string) (string, error) {
		if name != "sushi" {
			t.Errorf("lookPath(%q), want sushi", name)
		}
		return os.Args[0], nil
	}
	never := func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("ran %s %v; no command may run for sushi", name, args)
		return nil, nil
	}
	e.run, e.runOut = never, never
	e.runEnv = func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		return never(context.Background(), name, args...)
	}
	helper := 0
	e.spawn = func(p pidProcess, bin string, args []string) (*spawned, error) {
		if bin != os.Args[0] {
			t.Fatalf("spawn(%q), want the test binary %q: the real sushi must never run", bin, os.Args[0])
		}
		sp, err := p.spawn(bin, args)
		if err == nil {
			helper = sp.cmd.Process.Pid
			pt.procs[helper] = mine(helper, "100.5", append([]string{"/opt/homebrew/bin/sushi"}, args...))
		}
		return sp, err
	}
	pt.onSignal = func(pt *procTable, pid int, sig syscall.Signal) {
		if helper == 0 || (pid != helper && pid != -helper) {
			t.Errorf("signal %d to %d, want only the spawned helper %d", sig, pid, helper)
			return
		}
		_ = syscall.Kill(helper, sig)
		if pidGone(helper) {
			delete(pt.procs, helper)
		}
	}
	t.Cleanup(func() { // kill a helper the test left running
		if helper != 0 {
			_ = syscall.Kill(helper, syscall.SIGTERM)
		}
	})
	return pt, e, cfg, port, argvFile, chatLog
}

// TestSushiRowDrivesTheEngine verifies the real sushi row runs end to end
// through the generic process engine: the spawned argv is exactly the row's
// Spawn literal (no model flag), its warmup budget is 400 s, readiness is an
// http GET, and with no stop command the stop is a SIGTERM to the verified
// group that leaves no pidfile or start record. Nothing is run on the stop:
// running the bare sushi binary would start a server, not stop one.
func TestSushiRowDrivesTheEngine(t *testing.T) {
	spec, ok := provider.ProcessFor("sushi")
	if !ok {
		t.Fatal("no process spec for sushi")
	}
	if spec.WarmupSeconds != 400 || spec.Ready.Kind != provider.ReadyHTTP || len(spec.Stop) != 0 {
		t.Fatalf("spec warmup = %d, ready = %v, stop = %v; want 400, http, none", spec.WarmupSeconds, spec.Ready.Kind, spec.Stop)
	}
	pt, e, cfg, port, argvFile, _ := realSushiEnv(t, true)
	if err := (processServer{"sushi"}).start(context.Background(), e, cfg, Target{ProviderID: "sushi", ModelName: sushiPack}, func(Stage) {}); err != nil {
		t.Fatalf("start: %v", err)
	}
	b, _ := os.ReadFile(argvFile)
	lines := strings.SplitN(string(b), "\n", 2)
	literal := "serve --port " + strconv.Itoa(port) + " --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768"
	if len(lines) != 2 || lines[1] != literal || lines[1] != strings.Join(spec.SpawnArgs(sushiPack, port, nil), " ") {
		t.Errorf("argv = %q, want the Spawn literal %q", lines[1:], literal)
	}
	if pidfile, record := filesLeftFor(e, "sushi"); !pidfile || !record {
		t.Fatalf("precondition: after the start pidfile = %v, start record = %v, want both", pidfile, record)
	}
	pid := helperPid(t, argvFile)
	e.lookPath = func(string) (string, error) {
		t.Fatal("the stop looked up the sushi binary")
		return "", nil
	}
	if err := (processServer{"sushi"}).stop(context.Background(), e, cfg); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if want := []string{"-" + strconv.Itoa(pid) + ":15"}; !slices.Equal(pt.signals, want) {
		t.Errorf("signals = %v, want %v", pt.signals, want)
	}
	if !pidGone(pid) {
		t.Errorf("helper %d still alive after the stop", pid)
	}
	if pidfile, record := filesLeftFor(e, "sushi"); pidfile || record {
		t.Errorf("after the stop pidfile = %v, start record = %v, want both removed", pidfile, record)
	}
}

// TestSushiStartFailsWhenTheListNeverShowsLoaded verifies a sushi that
// answers the warmup chat but never lists the pack as loaded fails the start:
// wt must not report a model it cannot see loaded as started. The failure
// comes after the warmup (the chat was sent), carries the log tail like a
// warmup failure, and leaves no server, pidfile or start record behind.
func TestSushiStartFailsWhenTheListNeverShowsLoaded(t *testing.T) {
	pt, e, cfg, _, argvFile, chatLog := realSushiEnv(t, false)
	var hadPidfile, hadRecord bool
	err := (processServer{"sushi"}).start(context.Background(), e, cfg, Target{ProviderID: "sushi", ModelName: sushiPack}, filesAtWarming(e, "sushi", &hadPidfile, &hadRecord))
	if err == nil || !strings.Contains(err.Error(), "does not list") || !strings.Contains(err.Error(), "log tail") {
		t.Fatalf("start = %v, want a 'does not list' failure with the log tail", err)
	}
	if !hadPidfile || !hadRecord {
		t.Fatalf("precondition: at warming pidfile = %v, start record = %v, want both", hadPidfile, hadRecord)
	}
	if b, _ := os.ReadFile(chatLog); !strings.Contains(string(b), "chat") {
		t.Errorf("chat log = %q, want the warmup sent before the loaded check", b)
	}
	if pid := helperPid(t, argvFile); !pidGone(pid) {
		t.Errorf("helper %d still alive after the failed start", pid)
	}
	if pidfile, record := filesLeftFor(e, "sushi"); pidfile || record {
		t.Errorf("after the failed start pidfile = %v, start record = %v, want both removed", pidfile, record)
	}
	if len(pt.signals) != 0 {
		t.Errorf("signals = %v, want none (the start kills its own child)", pt.signals)
	}
}

// TestSushiConfirmLoadedIsExact verifies the loaded check compares ids
// exactly against the loaded-only list: sushi serves only the exact id, so a
// bare name must not pass against a loaded org/name (localmodels.NameMatches
// would), nor an exact id the server lists as not loaded. A refused key is a
// *KeyRefusedError naming the key, not a "not loaded" verdict.
func TestSushiConfirmLoadedIsExact(t *testing.T) {
	_, addr := serveFree(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"beamster/Q","loaded":true},{"id":"beamster/R","loaded":false}]}`))
	}))
	url := "http://" + addr + "/v1/models"
	e := testEnv()
	for model, wantErr := range map[string]bool{"beamster/Q": false, "Q": true, "beamster/R": true} {
		err := e.confirmLoaded(context.Background(), url, model, "")
		if wantErr != (err != nil) {
			t.Errorf("%s: err = %v, want error = %v", model, err, wantErr)
		}
		if wantErr && (err == nil || !strings.Contains(err.Error(), "does not list "+model+" as loaded")) {
			t.Errorf("%s: err = %v, want 'does not list %s as loaded'", model, err, model)
		}
	}
	_, refusing := serveFree(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	var kr *KeyRefusedError
	if err := e.confirmLoaded(context.Background(), "http://"+refusing+"/v1/models", "beamster/Q", "k"); !errors.As(err, &kr) || !kr.KeySent {
		t.Errorf("refused key: err = %v, want *KeyRefusedError with the key sent", err)
	}
}

// TestSushiPidfileEnvIsIsolated verifies TestMain's isolation covers the
// sushi row: WT_SUSHI_PIDFILE points into a directory that does not exist, so
// a test that reaches the production stop path reads no real sushi pidfile.
func TestSushiPidfileEnvIsIsolated(t *testing.T) {
	if got := os.Getenv("WT_SUSHI_PIDFILE"); !strings.Contains(got, "wt-test-no-sushi-") {
		t.Errorf("WT_SUSHI_PIDFILE = %q, want the isolated wt-test-no-sushi- path", got)
	}
}

// TestSushiNeverTouchesTheRealPidfile verifies the production env built in
// this test binary does not name the real /tmp/local-ai-setup-sushi.pid, the
// handle a developer's own `wt stop` and llmbench find a running sushi by.
func TestSushiNeverTouchesTheRealPidfile(t *testing.T) {
	p, ok := defaultEnv().serverProc["sushi"]
	if !ok || p.pidfile == "" {
		t.Fatal("precondition: defaultEnv has no sushi pidfile")
	}
	if p.pidfile == "/tmp/local-ai-setup-sushi.pid" {
		t.Errorf("sushi pidfile = %q, want the isolated path", p.pidfile)
	}
}

// TestWiredLimitGateCoversTheSushiRow verifies the wired-limit preflight is on
// for the real sushi row and, end to end, refuses a start whose pack cannot
// be wired before anything else happens: the other provider's model is not
// stopped and nothing is spawned. A start that is bound to fail minutes into
// the load must not first take the user's other models down.
func TestWiredLimitGateCoversTheSushiRow(t *testing.T) {
	if !wiredLimitFamilies["sushi"] {
		t.Fatal(`wiredLimitFamilies["sushi"] = false, want true`)
	}
	models := t.TempDir()
	sparseFile(t, filepath.Join(models, sushiPack, "model-00001-of-00002.safetensors"), 30000<<20)
	sparseFile(t, filepath.Join(models, sushiPack, "model-00002-of-00002.safetensors"), 17213<<20)
	cfg := provCfg("sushi", "http://127.0.0.1:"+strconv.Itoa(closedPort(t))+"/v1")
	cfg.Providers[0].ModelDir = models
	oldDir := modelDirFor
	modelDirFor = func(cfg *config.Config, family string) (string, error) {
		if family != "sushi" {
			t.Fatalf("modelDirFor(%q), want sushi", family)
		}
		dir, err := localmodels.ModelDir(cfg, family)
		if err != nil || dir != models {
			t.Fatalf("precondition: registry model_dir = %q (%v), want %q", dir, err, models)
		}
		return dir, err
	}
	t.Cleanup(func() { modelDirFor = oldDir })

	var calls []string
	e := testEnv()
	e.lookPath = func(name string) (string, error) {
		if name != "sushi" {
			t.Errorf("lookPath(%q), want sushi", name)
		}
		return os.Args[0], nil
	}
	e.wiredLimit = func() (int64, bool, error) { return 49152, true, nil }
	e.spawn = func(pidProcess, string, []string) (*spawned, error) {
		t.Fatal("spawned a server for a start the wired limit refuses")
		return nil, nil
	}
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("ollama", "ollama/x", "x:1b")}}
	e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
	e.backends = map[string]backend{
		"ollama": &fakeBackend{ten: Shared, calls: &calls, family: "ollama"},
		"sushi":  processServer{"sushi"},
	}
	stopped := recordStopped(e)
	err := start(context.Background(), e, cfg, Target{ProviderID: "sushi", ModelName: sushiPack},
		Options{Others: OthersAnswer{Decided: true, Stop: []string{"ollama/x"}}})
	var wl *WiredLimitError
	if !errors.As(err, &wl) || !reflect.DeepEqual(*wl, WiredLimitError{Pack: sushiPack, LimitMB: 49152, NeedMB: 51934}) {
		t.Fatalf("start = %v, want *WiredLimitError{49152, 51934}", err)
	}
	if len(calls) != 0 || len(*stopped) != 0 {
		t.Errorf("other backend calls = %v, route hook = %v; want nothing stopped", calls, *stopped)
	}
}
