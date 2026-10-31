package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// sushiSpawn is the sushi serve argv template, as the plan fixes it.
var sushiSpawn = []string{"serve", "--port", "{port}", "--host", "127.0.0.1", "--ctx-size", "131072", "--max-resident-models", "1", "--no-update-check", "--max-tokens", "32768"}

// sushiEnv builds an env for the synthetic process family sushix whose server
// is this test binary run as helperSushi: it lists the one pack pack, answers
// chat with chatStatus, and records each chat POST in the returned chat-log
// path (absent until the first POST). The spec is sushi's (http-get /health
// readiness, no stop command, sushi's spawn argv) with warmupSeconds as its
// budget; 0 means the fixture's 2 s. describe is stubbed so a start writes
// its .wt record without reading the real process table. It returns the env,
// the cfg, the port, the argv file and the chat log.
func sushiEnv(t *testing.T, pack string, chatStatus, warmupSeconds int) (*env, *config.Config, int, string, string) {
	t.Helper()
	if warmupSeconds == 0 {
		warmupSeconds = 2
	}
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	chatLog := filepath.Join(dir, "chat.log")
	t.Setenv("LIFECYCLE_HELPER", "sushi")
	t.Setenv("LIFECYCLE_ARGV_FILE", argvFile)
	t.Setenv("LIFECYCLE_SUSHI_PACK", pack)
	t.Setenv("LIFECYCLE_SUSHI_LOG", chatLog)
	t.Setenv("LIFECYCLE_HELPER_CHAT_STATUS", strconv.Itoa(chatStatus))
	_, portStr, _ := net.SplitHostPort(freeAddr(t))
	port, _ := strconv.Atoi(portStr)
	spec := provider.ProcessSpec{
		Binary: "sushix", Subcommand: "serve", PortFlag: "--port",
		Spawn:         sushiSpawn,
		Ready:         provider.Ready{Kind: provider.ReadyHTTP, Path: "/health"},
		WarmupSeconds: warmupSeconds,
	}
	stubFamily(t, "sushix", spec, provider.FamilySpec{Name: "sushix", Tenancy: provider.Exclusive, Scope: provider.ScopePort, Process: &spec}, port)
	e := testEnv()
	e.serverProc["sushix"] = pidProcess{name: "sushix", pidfile: filepath.Join(dir, "sushix.pid"), logfile: filepath.Join(dir, "sushix.log")}
	e.lookPath = func(string) (string, error) { return os.Args[0], nil }
	e.loadTimeout = 3 * time.Second
	e.describe = func(pid int) (procInfo, bool, error) {
		return procInfo{uid: os.Getuid(), pgid: pid, start: "100.5"}, true, nil
	}
	t.Cleanup(func() { // kill a helper the test left running
		if b, err := os.ReadFile(e.serverProc["sushix"].pidfile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	})
	return e, provCfg("sushix", "http://127.0.0.1:"+portStr+"/v1"), port, argvFile, chatLog
}

// helperPid is the pid the helper wrote on the first line of argvFile.
func helperPid(t *testing.T, argvFile string) int {
	t.Helper()
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("argv file: %v", err)
	}
	pid, _ := strconv.Atoi(strings.SplitN(string(b), "\n", 2)[0])
	if pid == 0 {
		t.Fatalf("argv file %q names no pid", b)
	}
	return pid
}

// filesAtWarming returns a report func that records, when the start reaches
// StageWarming, whether family's pidfile and start record existed then; the
// cleanup assertions after a failure compare against it so "gone" is never
// vacuous.
func filesAtWarming(e *env, family string, pidfile, record *bool) func(Stage) {
	return func(s Stage) {
		if s == StageWarming {
			*pidfile, *record = filesLeftFor(e, family)
		}
	}
}

// assertCleanedUp fails unless the helper is gone and the pidfile and start
// record, which existed at warming, are removed.
func assertCleanedUp(t *testing.T, e *env, argvFile string, hadPidfile, hadRecord bool) {
	t.Helper()
	if !hadPidfile || !hadRecord {
		t.Fatalf("at warming pidfile=%v record=%v, want both present (else the cleanup check proves nothing)", hadPidfile, hadRecord)
	}
	if pid := helperPid(t, argvFile); !pidGone(pid) {
		t.Errorf("spawned helper %d still alive after the failed start", pid)
	}
	if pidfile, record := filesLeftFor(e, "sushix"); pidfile || record {
		t.Errorf("after the failed start pidfile=%v record=%v, want both removed", pidfile, record)
	}
}

// TestSushiStartHappyPath verifies a sushi-shaped start end to end: stages
// starting -> waiting-for-model -> warming, the exact serve argv the plan
// fixes (no model flag: sushi loads by the request's id), and a pidfile that
// names the spawned server, so a later stop can identify it.
func TestSushiStartHappyPath(t *testing.T) {
	e, cfg, port, argvFile, _ := sushiEnv(t, "Org/Pack", 200, 0)
	var stages []Stage
	err := processServer{"sushix"}.start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, func(s Stage) { stages = append(stages, s) })
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !reflect.DeepEqual(stages, []Stage{StageStarting, StageWaiting, StageWarming}) {
		t.Errorf("stages = %v", stages)
	}
	b, _ := os.ReadFile(argvFile)
	lines := strings.SplitN(string(b), "\n", 2)
	want := fmt.Sprintf("serve --port %d --host 127.0.0.1 --ctx-size 131072 --max-resident-models 1 --no-update-check --max-tokens 32768", port)
	if len(lines) != 2 || lines[1] != want {
		t.Errorf("argv = %q, want %q", lines[1:], want)
	}
	pidB, err := os.ReadFile(e.serverProc["sushix"].pidfile)
	if err != nil || strings.TrimSpace(string(pidB)) != strings.TrimSpace(lines[0]) {
		t.Errorf("pidfile = %q (%v), want the helper pid %s", pidB, err, lines[0])
	}
}

// TestSushiStartRejectsAnUnlistedPackBeforeWarmup verifies a model_name the
// server does not list ends the start before any chat request: sushi answers
// an unknown id with a misleading "503 No default model configured", so the
// warmup would only retry that for its whole budget. The error names what the
// server does list, and the server wt spawned is stopped and forgotten.
func TestSushiStartRejectsAnUnlistedPackBeforeWarmup(t *testing.T) {
	e, cfg, _, argvFile, chatLog := sushiEnv(t, "Org/X", 503, 0)
	var hadPidfile, hadRecord bool
	err := processServer{"sushix"}.start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Y"}, filesAtWarming(e, "sushix", &hadPidfile, &hadRecord))
	var pnl *PackNotListedError
	if !errors.As(err, &pnl) {
		t.Fatalf("err = %v, want *PackNotListedError", err)
	}
	if !reflect.DeepEqual(pnl.Listed, []string{"Org/X"}) || pnl.Model != "Org/Y" {
		t.Errorf("PackNotListedError = %+v, want Model Org/Y listing [Org/X]", pnl)
	}
	if !strings.Contains(err.Error(), "log tail") {
		t.Errorf("err = %v, want the server's log tail", err)
	}
	if _, serr := os.Stat(chatLog); !os.IsNotExist(serr) {
		t.Errorf("a chat request was made (chat log stat err = %v), want none before the pack check", serr)
	}
	assertCleanedUp(t, e, argvFile, hadPidfile, hadRecord)
}

// TestSushiWarmupFailureKillsOnlyItsOwnProcessAndCleans verifies a warmup
// that fails (sushi answering 503 for its whole budget) reports the server's
// answer and log tail, kills the server wt spawned and removes its pidfile
// and start record, and signals nothing else: an unrelated process of the
// same user is still running afterwards.
func TestSushiWarmupFailureKillsOnlyItsOwnProcessAndCleans(t *testing.T) {
	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	otherDone := make(chan struct{})
	go func() { _ = other.Wait(); close(otherDone) }()
	t.Cleanup(func() { _ = other.Process.Kill(); <-otherDone })

	e, cfg, _, argvFile, _ := sushiEnv(t, "Org/Pack", 503, 1)
	var hadPidfile, hadRecord bool
	err := processServer{"sushix"}.start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, filesAtWarming(e, "sushix", &hadPidfile, &hadRecord))
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "log tail") {
		t.Fatalf("err = %v, want the HTTP 503 warmup failure with a log tail", err)
	}
	assertCleanedUp(t, e, argvFile, hadPidfile, hadRecord)
	select {
	case <-otherDone:
		t.Error("an unrelated process was killed by the failed start")
	default:
	}
}

// TestSushiCancelledStartTearsDown verifies a start cancelled during the
// warmup (the user backed out of a ~2-minute cold load) kills the server wt
// spawned and removes its pidfile and start record, and returns the
// cancellation with no log tail: the server did not fail. The cancel is
// triggered by the helper's first chat POST appearing in its chat log, not
// by a fixed delay, and the warmup budget (60 s) outlasts any loaded
// runner, so the cancel always lands inside the warmup.
func TestSushiCancelledStartTearsDown(t *testing.T) {
	e, cfg, _, argvFile, chatLog := sushiEnv(t, "Org/Pack", 503, 60)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var hadPidfile, hadRecord bool
	atWarming := filesAtWarming(e, "sushix", &hadPidfile, &hadRecord)
	report := func(s Stage) {
		atWarming(s)
		if s == StageWarming {
			go func() {
				// Cancel once the first chat POST is logged; past the
				// deadline cancel anyway and let the chat-log check fail.
				defer cancel()
				for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
					if _, err := os.Stat(chatLog); err == nil {
						return
					}
				}
			}()
		}
	}
	err := processServer{"sushix"}.start(ctx, e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, report)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "log tail") {
		t.Errorf("err = %v, want no log tail on a cancellation", err)
	}
	if _, serr := os.Stat(chatLog); serr != nil {
		t.Errorf("chat log stat err = %v, want the cancel to land during the warmup", serr)
	}
	assertCleanedUp(t, e, argvFile, hadPidfile, hadRecord)
}

// TestSushiRequirePackListedIsExact verifies the pack check compares ids
// exactly: sushi loads only by its exact id and answers 503 for anything
// else, so the lenient suffix match used elsewhere would let a bare name pass
// against a listed org/name (and the reverse) only to fail in the warmup.
func TestSushiRequirePackListedIsExact(t *testing.T) {
	for name, tc := range map[string]struct{ listed, model string }{
		"bare name, org listed": {"beamster/Q", "Q"},
		"org name, bare listed": {"Q", "beamster/Q"},
	} {
		t.Run(name, func(t *testing.T) {
			e, cfg, _, _, chatLog := sushiEnv(t, tc.listed, 200, 0)
			err := processServer{"sushix"}.start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: tc.model}, func(Stage) {})
			var pnl *PackNotListedError
			if !errors.As(err, &pnl) {
				t.Fatalf("err = %v, want *PackNotListedError", err)
			}
			if _, serr := os.Stat(chatLog); !os.IsNotExist(serr) {
				t.Errorf("a chat request was made (chat log stat err = %v), want none", serr)
			}
		})
	}
}

// TestMtplxWarmupFailureHasNoLogTailAdded verifies the pack check's log tail
// is sushi's alone: an mtplx warmup failure reads as it always has, even with
// a non-empty log, so only the http-get readiness gating keeps it out. mtplx
// is given a 1 s warmup budget (a copy of its spec) to keep the test short.
func TestMtplxWarmupFailureHasNoLogTailAdded(t *testing.T) {
	e, cfg, port, _ := mtplxEnv(t, "serve", 0)
	t.Setenv("LIFECYCLE_HELPER_CHAT_STATUS", "503")
	t.Setenv("LIFECYCLE_HELPER_LOG_LINE", "mtplx helper ready")
	spec, _ := provider.ProcessFor("mtplx")
	spec.WarmupSeconds = 1
	facts, _ := provider.FamilyByName("mtplx")
	stubFamily(t, "mtplx", spec, facts, port)
	err := processServer{"mtplx"}.start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "Org/Model"}, func(Stage) {})
	if err == nil || !strings.Contains(err.Error(), "failed to warm up model") {
		t.Fatalf("err = %v, want a warmup failure", err)
	}
	if tail := e.serverProc["mtplx"].logTailLines(512); !strings.Contains(tail, "mtplx helper ready") {
		t.Fatalf("mtplx log tail = %q, want the helper's line (else the check below proves nothing)", tail)
	}
	if strings.Contains(err.Error(), "log tail") {
		t.Errorf("err = %v, want no log tail on an mtplx warmup failure", err)
	}
}

// TestSushiPackCheckCountsUnloadedPacks verifies the pack check reads the
// unfiltered list: a pack sushi lists with "loaded": false is one it can
// load, so it passes, even beside another pack that is loaded (the filtered
// served-models list would hold only that one and refuse the start).
func TestSushiPackCheckCountsUnloadedPacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"Org/Other","loaded":true},{"id":"Org/Pack","loaded":false}]}`))
	}))
	defer srv.Close()
	e := testEnv()
	if err := e.requirePackListed(context.Background(), "sushix", srv.URL+"/v1/models", "Org/Pack", ""); err != nil {
		t.Errorf("unloaded listed pack: err = %v, want nil", err)
	}
	var pnl *PackNotListedError
	if err := e.requirePackListed(context.Background(), "sushix", srv.URL+"/v1/models", "Org/Missing", ""); !errors.As(err, &pnl) || !reflect.DeepEqual(pnl.Listed, []string{"Org/Other", "Org/Pack"}) {
		t.Errorf("unlisted pack: err = %v, want *PackNotListedError listing both ids", err)
	}
}

// TestSushiPackCheckSkipsAnEmptyList verifies a server that lists nothing at
// all is not judged by the pack check: the start goes on to the warmup, and it
// is the later load check (confirmLoaded) that refuses a pack still not
// listed as loaded. Only a non-empty list without the id is refused before
// the warmup.
func TestSushiPackCheckSkipsAnEmptyList(t *testing.T) {
	e, cfg, _, _, chatLog := sushiEnv(t, "", 200, 0)
	err := (processServer{"sushix"}).start(context.Background(), e, cfg, Target{ProviderID: "sushix", ModelName: "Org/Pack"}, func(Stage) {})
	var pnl *PackNotListedError
	if errors.As(err, &pnl) {
		t.Fatalf("start = %v, want no *PackNotListedError for an empty list", err)
	}
	if _, serr := os.Stat(chatLog); serr != nil {
		t.Errorf("chat log stat err = %v, want the warmup to have run", serr)
	}
	if err == nil || !strings.Contains(err.Error(), "does not list Org/Pack as loaded") {
		t.Errorf("start = %v, want the load check to refuse the pack after the warmup", err)
	}
}
