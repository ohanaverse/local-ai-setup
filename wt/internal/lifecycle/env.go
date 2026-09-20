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

// TEMPORARY stub, replaced by the real type in Task 5.
type pidProcess struct{ name, pidfile, logfile string }
