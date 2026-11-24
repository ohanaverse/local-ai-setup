package main

import (
	"errors"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/spf13/cobra"
)

// setupCmd is `wt-model setup`: it opens the Models screen on its Setup tab
// (this machine's LaunchAgent, token file and LiteLLM key), which needs a
// terminal. It runs the same runModelEditor as `wt-model model`, so a Models
// edit made after switching tabs still gets its one route sync.
func setupCmd(h *host, rec servestate.Store) *cobra.Command {
	return &cobra.Command{
		Use:          "setup",
		Short:        "Set up wt-model on this machine (LaunchAgent, token file, LiteLLM key)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !h.stdinTTY() {
				return errors.New("wt-model setup needs a terminal to open the Setup tab")
			}
			return runModelEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), h, rec, modeleditor.TabSetup)
		},
	}
}

// setupEnv is the Setup tab's machine: modelsetup's real one, with the
// routes check asking what a sync would write now through the host's
// probeInventory seam (read at call time, so a test's stub is the one used),
// the database's routes included through the adminFor seam.
func (h *host) setupEnv() modelsetup.Env {
	env := modelsetup.DefaultEnv()
	env.DesiredRoutes = litellm.DesiredRouteIDsFor(func(c *config.Config) localmodels.Snapshot {
		return h.probeInventory(c)
	}, func(c *config.Config) *litellm.AdminClient { return h.adminFor(c) })
	return env
}
