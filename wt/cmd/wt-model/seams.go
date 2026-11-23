package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/hoststop"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// seams are the hooks of wt-model's admin commands, carried per call on the
// host (rootCmd builds one) so parallel tests cannot leak stub state into
// each other. newHost fills them with the real functions; a test builds its
// host with testHost, whose seams reach nothing on the developer's machine.
type seams struct {
	// probeInventory is one inventory round of the local providers
	// (localmodels.Inventory).
	probeInventory func(*config.Config) localmodels.Snapshot
	// adminFor is the LiteLLM model-API client a route write sends local
	// routes through (litellm.Options.Admin, #598): adminFromPair over the
	// proxy pair cfg's load resolved, nil when either half is empty. testHost
	// makes it nil, so no test reaches a proxy unless it hands in a fake.
	adminFor func(*config.Config) *litellm.AdminClient
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
	// syncRoutesAfterAliasRemoval is syncRoutesAfterWrite after `wt-model
	// alias rm` (litellm.SyncAfterAliasRemoval): a database half that cannot
	// run says the removed aliases' rows still route (decision 4).
	syncRoutesAfterAliasRemoval func(out, errOut io.Writer) string
	// openTTY opens the controlling terminal a question is asked on.
	openTTY func() (*os.File, error)
	// confirmRemove asks before `wt-model model rm` or `wt-model alias rm`
	// removes rows, and before `wt-model alias set` makes an alias's window
	// smaller: a y/N prompt on the controlling terminal (realConfirmRemove),
	// so piped input can never authorise it.
	confirmRemove func(question string) (bool, error)
	// tiersIndex is the tiers file as the alias commands read a cloud
	// model's window from it (tiers.Load; windows.Model). testHost reads no
	// file.
	tiersIndex func() tiers.Index
	// clientSessions is the running model server's client sessions per
	// model or alias id, read through serve's runtime record in rec
	// (hostSessionReader: nil with no record). `wt-model alias list|set|rm`
	// read it; testHost knows none.
	clientSessions func(ctx context.Context, w io.Writer, rec servestate.Store) func() map[string]int
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

	// The `wt-model stop <provider>|--all` seams (stop.go). testHost probes
	// nothing, stops nothing and declines every question, so no test reaches
	// a live server or stops a real model.

	// stopState is one inventory round and one read of the live client
	// sessions: the stop candidates, and per provider family what its probe
	// said and how many client sessions use it
	// (hoststop.ReadStopStateSessions over stopSessions).
	stopState func(*config.Config) hoststop.StopState
	// stopSessions is the running model server's client sessions per model
	// id, read through serve's runtime record (nil: none known). The stop
	// command sets it (hostSessionReader); the default knows none.
	stopSessions func() map[string]int
	// stopCtx is the stop command's own context (cmd.Context(), which main
	// cancels on the first Ctrl+C): one cancelled by the time the sessions
	// and the inventory are read, or the question is answered, ends the
	// command with nothing stopped (stopCancelled). nil: never cancelled.
	stopCtx context.Context
	// stopEntries is the model-by-model stop loop (hoststop.StopEntries).
	stopEntries func(io.Writer, *config.Config, []localmodels.Entry) error
	// confirmStop asks before stopping a model client sessions use: the y/N
	// prompt on the controlling terminal that `wt-model model rm` asks with
	// too (realConfirmRemove), so piped input can never authorise it.
	confirmStop func(question string) (bool, error)
	// stopProvider stops a provider's server as a whole (`wt-model stop
	// omlx`; lifecycle.Stop).
	stopProvider func(ctx context.Context, cfg *config.Config, providerID string) error
	// stopLoading ends a provider's server that has not opened its port yet
	// (an mtplx still reading its weights), by the pid the stop state
	// verified (lifecycle.StopLoading).
	stopLoading func(ctx context.Context, cfg *config.Config, providerID string, pid int) error
	// waitPendingRoutes waits for the LiteLLM proxy restart a route write
	// started (lifecycle.WaitPendingRoutes): `wt-model stop --all` waits on
	// it between halts, so two restarts never run at once. Tests stub it to
	// observe that ordering without a proxy.
	waitPendingRoutes func()
	// startSignalCtx is signal.NotifyContext over SIGINT and SIGTERM: a test
	// needs a cancellable context without installing a real signal handler.
	startSignalCtx func() (context.Context, context.CancelFunc)
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
		tiersIndex:      tiers.Load,
		clientSessions:  hostSessionReader,
		ollamaWaitDelay: defaultOllamaWaitDelay,
		modeleditorRun: func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error) {
			return modeleditor.Run(theme, cfg, cfgErr, o)
		},
	}}
	h.adminFor = func(cfg *config.Config) *litellm.AdminClient {
		return adminFromPair(cfg.LitellmBaseURL(), cfg.LitellmAPIKey())
	}
	h.syncRoutesAfterWrite = h.realSyncRoutesAfterWrite
	h.syncRoutesAfterAliasRemoval = h.realSyncRoutesAfterAliasRemoval
	h.confirmRemove = h.realConfirmRemove
	h.confirmCloudSync = h.promptCloudSync
	h.ollamaCLI = h.realOllamaCLI
	h.stopSessions = func() map[string]int { return nil }
	h.stopState = func(cfg *config.Config) hoststop.StopState {
		return hoststop.ReadStopStateSessions(cfg, h.stopSessions())
	}
	h.stopEntries = hoststop.StopEntries
	h.confirmStop = h.realConfirmRemove
	h.stopProvider = lifecycle.Stop
	h.stopLoading = lifecycle.StopLoading
	h.waitPendingRoutes = lifecycle.WaitPendingRoutes
	h.startSignalCtx = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
	return h
}

// realConfirmRemove asks question on the terminal h.openTTY opens (read per
// call, so a test that takes the terminal away reaches the real refusal):
// cli.ErrNoTerminal when there is none. It is the one y/N prompt of
// `wt-model model rm` and `wt-model stop` (confirmRemove, confirmStop).
func (h *host) realConfirmRemove(question string) (bool, error) {
	return cli.AskOnTTY(h.openTTY, question, cli.ErrNoTerminal)
}

// realSyncRoutesAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt-model litellm sync` does
// (litellm.SyncAfterWrite, with the host's probeInventory and adminFor
// seams).
func (h *host) realSyncRoutesAfterWrite(out, errOut io.Writer) string {
	return litellm.SyncAfterWrite(out, errOut, h.probeInventory, func(c *config.Config) *litellm.AdminClient { return h.adminFor(c) })
}

// realSyncRoutesAfterAliasRemoval is realSyncRoutesAfterWrite after a write
// that removed aliases (litellm.SyncAfterAliasRemoval).
func (h *host) realSyncRoutesAfterAliasRemoval(out, errOut io.Writer) string {
	return litellm.SyncAfterAliasRemoval(out, errOut, h.probeInventory, func(c *config.Config) *litellm.AdminClient { return h.adminFor(c) })
}
