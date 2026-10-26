// Package lifecycle is the start/stop/warmup lifecycle of local models
// (ollama, omlx, mtplx). It never replaces a running model without being told
// to (Options.AllowReplace).
package lifecycle

import (
	"context"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"syscall"
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
	// runOut is run with stdout alone: a readiness command's answer is JSON
	// on stdout, and a warning or update hint a tool prints on stderr ahead of
	// it must not make the answer unreadable (ready.go, #458).
	runOut    func(ctx context.Context, name string, args ...string) ([]byte, error)
	runEnv    func(ctx context.Context, extra []string, name string, args ...string) ([]byte, error)
	inventory func(*config.Config) localmodels.Snapshot
	backends  map[string]backend // keyed by provider family

	// onOccupantStopped fires the moment a replace has stopped the running
	// occupant, before the new model is started — and, on a Pool, once per
	// model a load evicted (reconcilePool). Production drops the
	// occupant's LiteLLM route there: if the new model then fails to load,
	// Start returns the error and no route hook runs, so the stopped occupant
	// would otherwise keep its model_list row until the next `wt litellm
	// sync` (which only repairs providers that refuse connections). Test envs leave it nil so the injectable cores stay
	// hook-free. It reports whether it wrote config.yaml without restarting
	// the proxy (a restart is owed): Start settles that with ONE bounce after
	// the start, success or failure, instead of one per hook.
	onOccupantStopped func(ctx context.Context, cfg *config.Config, occ localmodels.Entry) bool
	restartOwed       bool // set by start() when the hook reported an owed restart

	// serverProc is the pidfile + log of each process family's spawned
	// server (provider.ProcessFamilies), keyed by family.
	serverProc map[string]pidProcess
	// spawn starts a process family's server (pidProcess.spawn); a test
	// swaps it so a start that reaches the spawn starts no process.
	spawn func(p pidProcess, bin string, args []string) (*spawned, error)

	// The process table and the signal, for the one stop that has no port to
	// go through: a process server that is still loading (server_loading.go).
	describe func(pid int) (info procInfo, found bool, err error)
	signal   func(pid int, sig syscall.Signal) error

	pollInterval   time.Duration
	warmupTimeout  time.Duration // ollama/omlx warmup budget
	loadTimeout    time.Duration // wait for a spawned server to list the model
	stopTimeout    time.Duration // wait for a port to close after a stop
	stopGrace      time.Duration // SIGTERM-to-SIGKILL wait for a loading process server (`mtplx stop`'s --grace-seconds)
	portUpTimeout  time.Duration // wait for a daemon to answer after `start`
	prebindTimeout time.Duration // wait for a port to free before spawning
	// readyCmdTimeout bounds one run of a command-json readiness check
	// (ready.go); a run that outlives it counts as not ready.
	readyCmdTimeout time.Duration

	// ollamaUnloadTimeout bounds the wait for a model to leave /api/ps after
	// an `ollama stop` that exited 0. ollama accepts a stop of a model with a
	// request in flight and unloads it once the request ends: 6 to 20 seconds
	// after an agent's last request, 55 seconds behind a 1,500-word generation
	// queued behind another request; the bound is that with margin. The wait
	// re-probes every pollInterval, ollamaUnloadTimeout/pollInterval times at
	// most. A batch of stops shares one such bound (StopBatch).
	ollamaUnloadTimeout time.Duration
	// stopNoticeAfter is how long a stop waits for an unload before it says
	// so (StopNotice): a line that sits open longer than this looks hung.
	stopNoticeAfter time.Duration
}

func defaultEnv() *env {
	return &env{
		probeClient:       &http.Client{Timeout: 5 * time.Second},
		chatClient:        &http.Client{},
		lookPath:          exec.LookPath,
		run:               runCommand,
		runOut:            readyStdout,
		runEnv:            runCommandEnv,
		inventory:         localmodels.Inventory,
		backends:          maps.Clone(backendsByFamily),
		onOccupantStopped: routeAfterOccupantStopped,
		serverProc:        serverProcesses(),
		spawn:             pidProcess.spawn,
		describe:          describeProc,
		signal:            signalProc,
		pollInterval:      time.Second,
		warmupTimeout:     600 * time.Second,
		loadTimeout:       300 * time.Second,
		stopTimeout:       6 * time.Second,
		stopGrace:         10 * time.Second,
		portUpTimeout:     90 * time.Second,
		prebindTimeout:    10 * time.Second,
		readyCmdTimeout:   5 * time.Second,

		ollamaUnloadTimeout: 3 * time.Minute,
		stopNoticeAfter:     2 * time.Second,
	}
}

// commandWaitDelay bounds how long a command's Wait outlives its context
// once the context ends: without it Wait blocks until every writer to the
// output pipe closes, so a command that forked a child holding the pipe (a
// launcher) would hang its caller after the deadline killed it.
const commandWaitDelay = time.Second

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandWaitDelay
	return cmd.CombinedOutput()
}

// runCommandStdout is runCommand returning stdout alone. Stderr is not part of
// the returned bytes; exec's Output still keeps a capped copy in a failed
// command's *exec.ExitError.Stderr, which the readiness check does not read.
func runCommandStdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandWaitDelay
	return cmd.Output()
}

// runCommandEnv is runCommand with extra vars layered over the inherited
// environment (setting cmd.Env replaces it wholesale, so os.Environ must be
// restated). ollamaBackend uses it to pin `ollama stop` to the registry
// origin via OLLAMA_HOST.
func runCommandEnv(ctx context.Context, extra []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extra...)
	cmd.WaitDelay = commandWaitDelay
	return cmd.CombinedOutput()
}
