package main

import (
	"context"
	"errors"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/spf13/cobra"
)

// modelCmd is `wt-model model`: bare, it opens the Models screen on its
// Models tab, which needs a terminal (the Aliases tab beside it edits the
// aliases `wt-model alias` does); init, list, add, edit and rm work without
// one. rec is the model server's runtime record, which the Aliases tab reads
// client sessions through.
func modelCmd(h *host, rec servestate.Store) *cobra.Command {
	c := &cobra.Command{
		Use:          "model",
		Short:        "Manage the model registry (registry.toml)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !h.stdinTTY() {
				return errors.New("wt-model model needs a terminal to open the Models tab; without one use `wt-model model list`, `wt-model model add`, `wt-model model edit` or `wt-model model rm`")
			}
			return runModelEditor(cmd.OutOrStdout(), cmd.ErrOrStderr(), h, rec, modeleditor.TabModels)
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
func runModelEditor(out, errOut io.Writer, h *host, rec servestate.Store, start modeleditor.Tab) error {
	theme, err := loadTheme()
	if err != nil {
		return err
	}
	cfg, _ := h.hostConfig()
	res, err := h.modeleditorRun(theme, cfg, h.hostConfigErr(), modeleditor.Options{
		StartTab: start,
		Models:   modeleditor.ModelsDeps{Probe: h.probeInventory, SeedEnv: h.seedEnv, Capabilities: h.ollamaCaps},
		Aliases:  h.aliasDeps(rec),
		Setup:    modeleditor.SetupDepsFromEnv(h.setupEnv()),
	})
	if res.RegistryChanged {
		// What `wt-model model add|edit|rm` run after their write: the sync's own
		// lines, then its warning on stderr. It never fails.
		_ = litellm.SyncAndWarn(out, errOut, nil, h.syncRoutesAfterWrite)
	}
	return err
}

// aliasDeps is the Aliases tab's machine, read through the same seams the
// `wt-model alias` commands use, so the tab and the commands cannot disagree:
// the routes through routedAliases (ids only, never a row's params), the
// sessions through clientSessions over the server's runtime record rec (an
// unread server comes back as an error, which the tab's remove guard refuses
// on, as `alias rm` does without --force-in-use), the windows' index through
// tiersIndex. Each is read when called, so a test's stub is the one used.
func (h *host) aliasDeps(rec servestate.Store) modeleditor.AliasDeps {
	return modeleditor.AliasDeps{
		Routed: func(ctx context.Context, cfg *config.Config) (map[string]bool, string) {
			return h.routedAliases(ctx, cfg)
		},
		Sessions: func(ctx context.Context) (map[string]int, error) {
			var unread error
			sessions := h.clientSessions(ctx, io.Discard, rec, &unread)()
			return sessions, unread
		},
		Index: func() tiers.Index { return h.tiersIndex() },
	}
}
