package main

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// The package-var seams of wt-model's admin commands (the documented
// migrating exception to per-call seams): each is swapped by a test and
// stubbed for the whole package in TestMain, so no test reaches the
// developer's providers, PATH, ollama, terminal or config.yaml.

// probeInventory is one inventory round of the local providers
// (localmodels.Inventory).
var probeInventory = localmodels.Inventory

// seedEnv describes the machine a registry write seeds provider rows for:
// the configured agents, the providers they list, and what is on PATH.
var seedEnv = config.DefaultSeedEnv

// ollamaCaps is the best-effort capability lookup an add of an ollama model
// makes (modeladmin.OllamaCapabilities).
var ollamaCaps = modeladmin.OllamaCapabilities

// realSyncRoutesAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt litellm sync` does
// (litellm.SyncAfterWrite, with the probeInventory seam).
func realSyncRoutesAfterWrite(out, errOut io.Writer) string {
	return litellm.SyncAfterWrite(out, errOut, probeInventory)
}

// syncRoutesAfterWrite runs the one LiteLLM route sync that follows a
// registry write and returns a warning for the user, or "" when the routes
// are in step. A seam: unstubbed, a test that writes the registry would go on
// to probe the developer's providers and rewrite their config.yaml.
var syncRoutesAfterWrite = realSyncRoutesAfterWrite

// stdinTTY reports whether stdin is a terminal.
var stdinTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

// modeleditorRun opens the Models screen; a seam because it needs a terminal.
var modeleditorRun = func(theme themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error) {
	return modeleditor.Run(theme, cfg, cfgErr, o)
}
