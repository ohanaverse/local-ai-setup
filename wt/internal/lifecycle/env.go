// Package lifecycle starts local models (ollama, omlx, mtplx): the Go port of
// modelman's start/stop/warmup lifecycle. It never writes modelman-owned state
// and never replaces a running model without being told to (Options.AllowReplace).
package lifecycle

import (
	"context"
	"maps"
	"net/http"
	"os"
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
	runEnv      func(ctx context.Context, extra []string, name string, args ...string) ([]byte, error)
	inventory   func(*config.Config) localmodels.Snapshot
	backends    map[string]backend // keyed by provider family

	// onOccupantStopped fires the moment a replace has stopped the running
	// occupant, before the new model is started. Production drops the
	// occupant's LiteLLM route there: if the new model then fails to load,
	// Start returns the error and no route hook runs, so the stopped occupant
	// would otherwise keep its model_list row until the next `wt litellm
	// sync` (which only repairs providers that refuse connections). Test envs leave it nil so the injectable cores stay
	// hook-free. It reports whether it wrote config.yaml without restarting
	// the proxy (a restart is owed): Start settles that with ONE bounce after
	// the start, success or failure, instead of one per hook.
	onOccupantStopped func(ctx context.Context, cfg *config.Config, occ localmodels.Entry) bool
	restartOwed       bool // set by start() when the hook reported an owed restart

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
		probeClient:       &http.Client{Timeout: 5 * time.Second},
		chatClient:        &http.Client{},
		lookPath:          exec.LookPath,
		run:               runCommand,
		runEnv:            runCommandEnv,
		inventory:         localmodels.Inventory,
		backends:          maps.Clone(backendsByFamily),
		onOccupantStopped: routeAfterOccupantStopped,
		mtplxProc:         pidProcess{name: "mtplx", pidfile: "/tmp/local-ai-setup-mtplx.pid", logfile: "/tmp/local-ai-setup-mtplx.log"},
		pollInterval:      time.Second,
		warmupTimeout:     600 * time.Second,
		loadTimeout:       300 * time.Second,
		stopTimeout:       6 * time.Second,
		portUpTimeout:     90 * time.Second,
		prebindTimeout:    10 * time.Second,
	}
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// runCommandEnv is runCommand with extra vars layered over the inherited
// environment (setting cmd.Env replaces it wholesale, so os.Environ must be
// restated). ollamaBackend uses it to pin `ollama stop` to the registry
// origin via OLLAMA_HOST.
func runCommandEnv(ctx context.Context, extra []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), extra...)
	return cmd.CombinedOutput()
}
