package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/spf13/cobra"
)

func rotateCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:    "rotate <tag>",
		Short:  "Print the model after the last-launched in a tag group (debug)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := args[0]
			models := a.cfg.ModelsWithTag(tag)
			if len(models) == 0 {
				return fmt.Errorf("no models tagged %q", tag)
			}
			// rotate is a debug helper that walks the global model list filtered
			// by tag and prints the model after the last launched one.
			r := rotation.New()
			last, ok := r.Last()
			if !ok {
				fmt.Println(models[0].ID)
				return nil
			}
			next, ok := rotation.FirstAfter(models, config.Model{ID: last})
			if !ok {
				fmt.Println(models[0].ID)
				return nil
			}
			fmt.Println(next.ID)
			return nil
		},
	}
	return c
}
