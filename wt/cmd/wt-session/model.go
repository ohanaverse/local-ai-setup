package main

import (
	"github.com/spf13/cobra"
)

// modelCmd is the `wt model` group: bare, it prints where the Models screen
// moved (`wt-model model`, movedError); list (model_list_server.go: --server
// is the catalog view, without it the pointer to `wt-model model list`) is
// its one live subcommand. init, add, edit and rm moved to `wt-model model`:
// their names here are movedCommands' pointer stubs (moved.go).
func modelCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "model",
		Short: "Manage the model registry (registry.toml)",
		Long: "The model registry is managed with `wt-model model` on the host.\n\n" +
			"With no subcommand, prints where the Models screen moved: `wt-model model`.\n" +
			"`wt model list --server` lists the model server's catalog.",
		Args: cobra.NoArgs,
		// A moved command is not a usage mistake.
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			return movedError("model", "model")
		},
	}
	c.AddCommand(modelListCmd(a))
	return c
}
