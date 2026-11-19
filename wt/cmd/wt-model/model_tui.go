package main

import (
	"errors"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/spf13/cobra"
)

// modelCmd is `wt-model model`: bare, it opens the Models screen on its
// Models tab, which needs a terminal.
func modelCmd(h *host) *cobra.Command {
	return &cobra.Command{
		Use:          "model",
		Short:        "Manage the model registry (registry.toml)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdinTTY() {
				return errors.New("wt-model model needs a terminal to open the Models tab; without one use `wt model list`, `wt model add`, `wt model edit` or `wt model rm`")
			}
			return runModelEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), h, modeleditor.TabModels)
		},
	}
}

// runModelEditor opens the Models screen on the host's config with this
// package's seams, then runs the one route sync when the screen wrote the
// registry — even when the screen ended with an error, since the write
// already happened. It returns the screen's error.
func runModelEditor(out, errOut io.Writer, h *host, start modeleditor.Tab) error {
	theme, err := loadTheme()
	if err != nil {
		return err
	}
	cfg, _ := h.hostConfig()
	res, err := modeleditorRun(theme, cfg, h.hostConfigErr(), modeleditor.Options{
		StartTab: start,
		Models:   modeleditor.ModelsDeps{Probe: probeInventory, SeedEnv: seedEnv, Capabilities: ollamaCaps},
	})
	if res.RegistryChanged {
		// What `wt model add|edit|rm` run after their write: the sync's own
		// lines, then its warning on stderr. It never fails.
		_ = litellm.SyncAndWarn(out, errOut, nil, syncRoutesAfterWrite)
	}
	return err
}
