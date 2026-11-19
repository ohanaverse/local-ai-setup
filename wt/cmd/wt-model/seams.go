package main

import (
	"io"

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
	// stdinTTY reports whether stdin is a terminal.
	stdinTTY func() bool
	// modeleditorRun opens the Models screen; a seam because it needs a
	// terminal.
	modeleditorRun func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error)
}

// newHost is the host of a real run: every seam is the real function.
func newHost() *host {
	h := &host{seams: seams{
		probeInventory: localmodels.Inventory,
		seedEnv:        config.DefaultSeedEnv,
		ollamaCaps:     modeladmin.OllamaCapabilities,
		stdinTTY:       cli.IsStdinTTY,
		modeleditorRun: func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error) {
			return modeleditor.Run(theme, cfg, cfgErr, o)
		},
	}}
	h.syncRoutesAfterWrite = h.realSyncRoutesAfterWrite
	return h
}

// realSyncRoutesAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt litellm sync` does
// (litellm.SyncAfterWrite, with the host's probeInventory seam).
func (h *host) realSyncRoutesAfterWrite(out, errOut io.Writer) string {
	return litellm.SyncAfterWrite(out, errOut, h.probeInventory)
}
