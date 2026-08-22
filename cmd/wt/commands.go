package main

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/rotation"
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
			// Legacy CLI shape: rotate is scoped by tag only. We use an empty
			// agent/family so the state-file name matches legacy behavior
			// (rotation-<tag>.state via the back-compat read path).
			slot := rotation.Slot{Agent: "-", Tag: tag, Family: "-"}
			r := rotation.NewForSlot(slot, models, "")
			last, ok := r.LastLaunched()
			if !ok {
				// No prior launch; print the first model.
				fmt.Println(models[0].ID)
				return nil
			}
			next, _ := rotation.FirstAfter(models, last)
			fmt.Println(next.ID)
			return nil
		},
	}
	return c
}
