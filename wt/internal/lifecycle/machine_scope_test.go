package lifecycle

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// machineSpec is a synthetic machine-scope family's process spec, shaped like
// rec's: one instance on the whole machine, `<bin> status` says whether it
// is ready and which model it serves, `<bin> stop` stops it whatever port it
// listens on.
var machineSpec = provider.ProcessSpec{
	Binary:     "recx",
	Subcommand: "serve",
	PortFlag:   "--port",
	Spawn:      []string{"serve", "--model", "{model}", "--port", "{port}"},
	Stop:       []string{"stop"},
	Ready:      readySpec.Ready,
}

// errSpawnStub is what the stubbed spawn returns: a start that reaches the
// spawn ends there, with no process.
var errSpawnStub = errors.New("spawn stub")

// machineHarness records, in order, every command a start runs and every
// spawn it makes: "status", "stop", "spawn".
type machineHarness struct {
	mu      sync.Mutex
	calls   []string
	stopped bool
	// stopIgnored makes the stop command succeed without stopping anything:
	// status keeps reporting the instance ready.
	stopIgnored bool
}

func (h *machineHarness) record(c string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, c)
}

func (h *machineHarness) seq() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

// stubFamily gives family the process spec spec and the family facts facts
// through the lifecycle seams, maps the provider id family to itself, and
// resolves its address to 127.0.0.1:port; all restored at cleanup.
func stubFamily(t *testing.T, family string, spec provider.ProcessSpec, facts provider.FamilySpec, port int) {
	t.Helper()
	oldSpec, oldFacts, oldOrigin, oldFamily := processSpecFor, familySpecFor, familyOriginPort, familyOf
	processSpecFor = func(f string) (provider.ProcessSpec, bool) {
		if f == family {
			return spec, true
		}
		return oldSpec(f)
	}
	familySpecFor = func(f string) (provider.FamilySpec, bool) {
		if f == family {
			return facts, true
		}
		return oldFacts(f)
	}
	familyOriginPort = func(cfg *config.Config, f string) (string, int, error) {
		if f == family {
			return "http://127.0.0.1:" + strconv.Itoa(port), port, nil
		}
		return oldOrigin(cfg, f)
	}
	familyOf = func(id string) string {
		if id == family {
			return family
		}
		return oldFamily(id)
	}
	t.Cleanup(func() {
		processSpecFor, familySpecFor, familyOriginPort, familyOf = oldSpec, oldFacts, oldOrigin, oldFamily
	})
}

// closedPort is a port nothing listens on, so the pre-spawn port check finds
// it free without a server.
func closedPort(t *testing.T) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(freeAddr(t))
	n, _ := strconv.Atoi(p)
	return n
}

// machineEnv is an env for the synthetic machine-scope family recx: status
// answers serving (what the instance serves; "" once stopped or when nothing
// runs), stop marks it stopped, and spawn is recorded and fails.
func machineEnv(t *testing.T, serving string) (*env, *machineHarness) {
	t.Helper()
	stubFamily(t, "recx", machineSpec, provider.FamilySpec{Name: "recx", Tenancy: provider.Exclusive, Scope: provider.ScopeMachine, Process: &machineSpec}, closedPort(t))
	h := &machineHarness{}
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	e.backends["recx"] = processServer{"recx"}
	dir := t.TempDir()
	e.serverProc["recx"] = pidProcess{name: "recx", pidfile: filepath.Join(dir, "recx.pid"), logfile: filepath.Join(dir, "recx.log")}
	e.lookPath = func(string) (string, error) { return "/bin/recx", nil }
	e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		h.record(cmd)
		h.mu.Lock()
		defer h.mu.Unlock()
		switch cmd {
		case "status":
			if serving == "" || h.stopped {
				return []byte(`{"ready":false}`), errors.New("exit status 1")
			}
			return []byte(`{"ready":true,"model":"` + serving + `","endpoint":"http://127.0.0.1:11500/v1"}`), nil
		case "stop":
			h.stopped = !h.stopIgnored
			return nil, nil
		}
		t.Errorf("unexpected command %s %s", name, cmd)
		return nil, errors.New("unexpected")
	}
	e.spawn = func(p pidProcess, bin string, args []string) (*spawned, error) {
		h.record("spawn")
		return nil, errSpawnStub
	}
	return e, h
}

// recxTarget is a start of model on the synthetic machine-scope family.
func recxTarget(model string) Target { return Target{ProviderID: "recx", ModelName: model} }

// TestMachineScopeAdoptsAnInstanceServingTheRequestedModel verifies a start
// of a machine-scope family whose one instance already serves the requested
// model (found by the readiness command, on whatever port) adopts it: nil,
// nothing stopped, nothing spawned, no pidfile. Spawning a second instance
// would fail (one per machine), and stopping it would throw away a model
// that is already what the user asked for.
func TestMachineScopeAdoptsAnInstanceServingTheRequestedModel(t *testing.T) {
	e, h := machineEnv(t, "flint")
	if err := start(context.Background(), e, &config.Config{}, recxTarget("flint"), Options{}); err != nil {
		t.Fatalf("start = %v, want nil (adopted)", err)
	}
	if got := h.seq(); !slices.Equal(got, []string{"status"}) {
		t.Errorf("calls = %v, want only the readiness command", got)
	}
	if _, err := os.Stat(e.serverProc["recx"].pidfile); err == nil {
		t.Error("adopting wrote a pidfile for a process wt did not start")
	}
}

// TestMachineScopeAsksBeforeStoppingAnotherModel verifies a machine-scope
// instance serving another model is an occupant: without AllowReplace the
// start returns an *OccupiedError naming that model and touches nothing. The
// user's rule is to ask first and never stop a foreign instance silently.
func TestMachineScopeAsksBeforeStoppingAnotherModel(t *testing.T) {
	e, h := machineEnv(t, "other")
	err := start(context.Background(), e, &config.Config{}, recxTarget("flint"), Options{Others: OthersAnswer{Decided: true}})
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("start = %v, want *OccupiedError", err)
	}
	want := localmodels.Entry{ProviderID: "recx", ModelID: config.DiscoveredModelID("recx", "other"), ModelName: "other", Running: true}
	if len(occ.Occupants) != 1 || occ.Occupants[0] != want {
		t.Errorf("occupants = %+v, want [%+v]", occ.Occupants, want)
	}
	if got := h.seq(); !slices.Equal(got, []string{"status"}) {
		t.Errorf("calls = %v, want only the readiness command (nothing stopped or spawned)", got)
	}
}

// TestMachineScopeStopsForeignInstanceWhenAllowed verifies the confirmed
// replace: with AllowReplace the start stops the instance with the spec's
// stop command, waits for the readiness command to say not ready, and only
// then spawns. Spawning before the old instance is gone would collide with
// it; stopping it by signal would act on a process wt never identified.
func TestMachineScopeStopsForeignInstanceWhenAllowed(t *testing.T) {
	e, h := machineEnv(t, "other")
	err := start(context.Background(), e, &config.Config{}, recxTarget("flint"), Options{AllowReplace: true, Others: OthersAnswer{Decided: true}})
	if !errors.Is(err, errSpawnStub) {
		t.Fatalf("start = %v, want the stubbed spawn's error", err)
	}
	if got, want := h.seq(), []string{"status", "stop", "status", "spawn"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

// TestMachineScopeDoesNotSpawnWhileStatusStaysReady verifies a confirmed
// replace whose stop command leaves the instance reporting ready ends with
// an error naming the stop, and never spawns: the old instance holds the
// machine, so a second one would collide with it (or be refused) and the
// start would report a failure that hides the real one.
func TestMachineScopeDoesNotSpawnWhileStatusStaysReady(t *testing.T) {
	e, h := machineEnv(t, "other")
	h.stopIgnored = true
	e.stopTimeout = 30 * time.Millisecond
	err := start(context.Background(), e, &config.Config{}, recxTarget("flint"), Options{AllowReplace: true, Others: OthersAnswer{Decided: true}})
	if err == nil || errors.Is(err, errSpawnStub) || !strings.Contains(err.Error(), "still reports") {
		t.Fatalf("start = %v, want the stop's still-reports-ready error", err)
	}
	if got := h.seq(); slices.Contains(got, "spawn") || !slices.Contains(got, "stop") {
		t.Errorf("calls = %v, want a stop and no spawn", got)
	}
}

// TestPortScopeNeverRunsTheReadinessCommand verifies a port-scope family
// (mtplx) never runs a readiness command to look for a foreign instance,
// even with a command-json spec: its servers exclude only their own port, so
// the machine-wide check would cost a command per start and could name a
// server on another port an occupant.
func TestPortScopeNeverRunsTheReadinessCommand(t *testing.T) {
	spec, _ := provider.ProcessFor("mtplx")
	spec.Ready = readySpec.Ready
	facts, _ := provider.FamilyByName("mtplx")
	if facts.Scope != provider.ScopePort {
		t.Fatalf("mtplx scope = %v, want ScopePort", facts.Scope)
	}
	stubFamily(t, "mtplx", spec, facts, closedPort(t))
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	dir := t.TempDir()
	e.serverProc["mtplx"] = pidProcess{name: "mtplx", pidfile: filepath.Join(dir, "mtplx.pid"), logfile: filepath.Join(dir, "mtplx.log")}
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	runs := 0
	e.run = func(context.Context, string, ...string) ([]byte, error) {
		runs++
		return []byte(`{"ready":true,"model":"other"}`), nil
	}
	didSpawn := false
	e.spawn = func(pidProcess, string, []string) (*spawned, error) {
		didSpawn = true
		return nil, errSpawnStub
	}
	if _, running := e.foreignInstance(context.Background(), "mtplx", "/bin/mtplx", ""); running {
		t.Error("foreignInstance reported a running instance for a port-scope family")
	}
	err := start(context.Background(), e, loadCfg(), Target{ProviderID: "mtplx", ModelName: "org/m"}, Options{Others: OthersAnswer{Decided: true}})
	if !errors.Is(err, errSpawnStub) || !didSpawn {
		t.Fatalf("start = %v (spawned %v), want it to reach the stubbed spawn", err, didSpawn)
	}
	if runs != 0 {
		t.Errorf("a port-scope start ran %d commands, want 0", runs)
	}
}

// loadingRecx makes the pidfile of e's recx family name a live recx server
// (pid 4242) that a process-table lookup identifies as wt's own, as one wt
// spawned and is still loading. alive reports whether the process still exists.
func loadingRecx(t *testing.T, e *env, alive *bool) {
	t.Helper()
	if err := os.WriteFile(e.serverProc["recx"].pidfile, []byte("4242"), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := []string{"recx", "serve", "--model", "flint", "--port", strconv.Itoa(serverPort(&config.Config{}, "recx"))}
	e.describe = func(pid int) (procInfo, bool, error) {
		if pid != 4242 || !*alive {
			return procInfo{}, false, nil
		}
		return mine(4242, "100.5", argv), true, nil
	}
}

// TestMachineScopeRefusesToSpawnBesideALoadingInstance verifies a start whose
// readiness command says "not ready" while the pidfile names a live recx
// server wt spawned fails with *LoadingInstanceError and spawns nothing: a
// loading instance answers exactly like no instance, and a second one would
// collide with it.
func TestMachineScopeRefusesToSpawnBesideALoadingInstance(t *testing.T) {
	e, h := machineEnv(t, "")
	alive := true
	loadingRecx(t, e, &alive)
	err := start(context.Background(), e, &config.Config{}, recxTarget("other"), Options{Others: OthersAnswer{Decided: true}})
	var le *LoadingInstanceError
	if !errors.As(err, &le) || le.PID != 4242 {
		t.Fatalf("start = %v, want *LoadingInstanceError for pid 4242", err)
	}
	if got := h.seq(); slices.Contains(got, "spawn") || slices.Contains(got, "stop") {
		t.Errorf("calls = %v, want no spawn and no stop", got)
	}
}

// TestMachineScopeStopWaitsForALoadingProcessToExit verifies a stop whose
// readiness command already says "not ready" (the instance is still loading)
// is not done while the process wt named is alive: it fails when the process
// outlives the stop timeout, and succeeds once it is gone.
func TestMachineScopeStopWaitsForALoadingProcessToExit(t *testing.T) {
	e, _ := machineEnv(t, "")
	e.stopTimeout = 30 * time.Millisecond
	alive := true
	loadingRecx(t, e, &alive)
	b := processServer{"recx"}
	err := b.stop(context.Background(), e, &config.Config{})
	if err == nil || !strings.Contains(err.Error(), "pid 4242") {
		t.Fatalf("stop = %v, want an error naming the process still running", err)
	}
	alive = false
	if err := b.stop(context.Background(), e, &config.Config{}); err != nil {
		t.Errorf("stop after exit = %v, want nil", err)
	}
}
