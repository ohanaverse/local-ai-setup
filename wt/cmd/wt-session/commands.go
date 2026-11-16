package main

import (
	"context"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/spf13/cobra"
)

func rotateCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "rotate <tag>",
		Short: "Print the model after the last-launched in a tag group (debug)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := args[0]
			// The model server's registered rows, in its Order. With no
			// model server configured it refuses: a session never reads the
			// registry (Task 18).
			models, err := rotateModelsRemote(cmd.Context(), a, tag)
			if err != nil {
				cmd.SilenceUsage = true
				return remoteExit(err)
			}
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

// rotateModelsRemote is the model list `wt rotate TAG` walks on a model
// server: the catalog's registered rows that carry tag, in the server's Order
// (a discovered running model is never rotated into). Its errors say that
// nothing was printed.
func rotateModelsRemote(ctx context.Context, a *app, tag string) ([]config.Model, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	const still = "nothing was printed"
	ms, err := a.openModels(ctx, still)
	if err != nil {
		return nil, err
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return nil, modelclient.Still(err, still)
	}
	var out []config.Model
	for _, m := range catalog.RegisteredOrdered(cat.Rows) {
		if m.HasTag(tag) {
			out = append(out, m)
		}
	}
	return out, nil
}
