package main

import (
	"errors"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/spf13/cobra"
)

// modelCmd is `wt-model model`: bare, it opens the Models screen on its
// Models tab, which needs a terminal; init, list, add, edit and rm work
// without one.
func modelCmd(h *host) *cobra.Command {
	c := &cobra.Command{
		Use:          "model",
		Short:        "Manage the model registry (registry.toml)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !h.stdinTTY() {
				return errors.New("wt-model model needs a terminal to open the Models tab; without one use `wt-model model list`, `wt-model model add`, `wt-model model edit` or `wt-model model rm`")
			}
			return runModelEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), h, modeleditor.TabModels)
		},
	}
	c.AddCommand(modelInitCmd(h), modelListCmd(h), modelAddCmd(h), modelEditCmd(h), modelRmCmd(h))
	return c
}

// runModelEditor opens the Models screen on tab start, on the host's config
// with this package's seams (the Setup tab over setupEnv), then runs the one
// route sync when the screen wrote the registry — even when the screen ended
// with an error, since the write already happened. It returns the screen's
// error.
func runModelEditor(out, errOut io.Writer, h *host, start modeleditor.Tab) error {
	theme, err := loadTheme()
	if err != nil {
		return err
	}
	cfg, _ := h.hostConfig()
	res, err := h.modeleditorRun(theme, cfg, h.hostConfigErr(), modeleditor.Options{
		StartTab: start,
		Models:   modeleditor.ModelsDeps{Probe: h.probeInventory, SeedEnv: h.seedEnv, Capabilities: h.ollamaCaps},
		Setup:    modeleditor.SetupDepsFromEnv(h.setupEnv()),
	})
	if res.RegistryChanged {
		// What `wt-model model add|edit|rm` run after their write: the sync's own
		// lines, then its warning on stderr. It never fails.
		_ = litellm.SyncAndWarn(out, errOut, nil, h.syncRoutesAfterWrite)
	}
	return err
}
