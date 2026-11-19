package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// seams are the hooks of wt-model's admin commands, carried per call on the
// host (rootCmd builds one) so parallel tests cannot leak stub state into
// each other. newHost fills them with the real functions; a test builds its
// host with testHost, whose seams reach nothing on the developer's machine.
type seams struct {
	// probeInventory is one inventory round of the local providers
	// (localmodels.Inventory).
	probeInventory func(*config.Config) localmodels.Snapshot
	// seedEnv describes the machine a registry write seeds provider rows
	// for: the configured agents, the providers they list, and what is on
	// PATH.
	seedEnv func() config.SeedEnv
	// ollamaCaps is the best-effort capability lookup an add of an ollama
	// model makes (modeladmin.OllamaCapabilities).
	ollamaCaps func(*config.Config, string) (map[string]any, error)
	// syncRoutesAfterWrite runs the one LiteLLM route sync that follows a
	// registry write and returns a warning for the user, or "" when the
	// routes are in step.
	syncRoutesAfterWrite func(out, errOut io.Writer) string
	// openTTY opens the controlling terminal a question is asked on.
	openTTY func() (*os.File, error)
	// confirmRemove asks before `wt-model model rm` removes rows: a y/N
	// prompt on the controlling terminal (realConfirmRemove), so piped input
	// can never authorise a removal.
	confirmRemove func(question string) (bool, error)
	// stdinTTY reports whether stdin is a terminal.
	stdinTTY func() bool
	// stdoutWidth is the terminal's width in columns, or 0 when stdout is
	// not a terminal (cli.StdoutWidth): what `wt-model model list` fits its
	// table to.
	stdoutWidth func() int
	// modeleditorRun opens the Models screen; a seam because it needs a
	// terminal.
	modeleditorRun func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error)

	// The `wt-model cloud-sync` seams (cloudsync*.go). testHost makes the
	// fetch, the ollama CLI and the confirmation fail closed, so no test
	// reaches the network, a terminal or the developer's ollama.

	// cloudFetch GETs one public page: OpenRouter's model list,
	// ollama.com/pricing, an ollama.com/library tags page.
	cloudFetch func(ctx context.Context, url string) ([]byte, error)
	// confirmCloudSync asks the one question that covers both printed plans
	// (h.promptCloudSync).
	confirmCloudSync func(question string) (bool, error)
	// cloudSyncNow is the clock the stamps and the saved-page name read.
	cloudSyncNow func() time.Time
	// ollamaCLI runs the ollama command against the daemon at origin and
	// returns what it printed (h.realOllamaCLI).
	ollamaCLI func(ctx context.Context, origin string, args ...string) (stdout, stderr string, err error)
	// ollamaWaitDelay bounds what a time limit leaves open. The limit kills
	// the command wt-model started; if that was a wrapper script, the process
	// it started in turn lives on holding the output pipes, and without this
	// Run would wait for it to exit, however long that takes.
	ollamaWaitDelay time.Duration
}

// newHost is the host of a real run: every seam is the real function.
func newHost() *host {
	h := &host{seams: seams{
		probeInventory:  localmodels.Inventory,
		seedEnv:         config.DefaultSeedEnv,
		ollamaCaps:      modeladmin.OllamaCapabilities,
		openTTY:         func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) },
		stdinTTY:        cli.IsStdinTTY,
		stdoutWidth:     cli.StdoutWidth,
		cloudFetch:      realCloudFetch,
		cloudSyncNow:    time.Now,
		ollamaWaitDelay: defaultOllamaWaitDelay,
		modeleditorRun: func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error) {
			return modeleditor.Run(theme, cfg, cfgErr, o)
		},
	}}
	h.syncRoutesAfterWrite = h.realSyncRoutesAfterWrite
	h.confirmRemove = h.realConfirmRemove
	h.confirmCloudSync = h.promptCloudSync
	h.ollamaCLI = h.realOllamaCLI
	return h
}

// realConfirmRemove asks question on the terminal h.openTTY opens (read per
// call, so a test that takes the terminal away reaches the real refusal):
// cli.ErrNoTerminal when there is none.
func (h *host) realConfirmRemove(question string) (bool, error) {
	return cli.AskOnTTY(h.openTTY, question, cli.ErrNoTerminal)
}

// realSyncRoutesAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt litellm sync` does
// (litellm.SyncAfterWrite, with the host's probeInventory seam).
func (h *host) realSyncRoutesAfterWrite(out, errOut io.Writer) string {
	return litellm.SyncAfterWrite(out, errOut, h.probeInventory)
}
