package main

import (
	"errors"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/spf13/cobra"
)

// removedRotateCmd is the hidden stub `wt rotate` became when rotation was
// removed (2026-11-27): the model picker opens on the row the last launch picked
// (else the first row that is not blocked), so there is no "model after the
// last-launched" left to print.
// Without the stub the root, which accepts any positional arguments, would
// take `rotate code` as agent arguments and open the picker. It needs no
// model server; flag parsing is off and any arguments are accepted, so
// `wt rotate --json` reaches the same line, exit 1. The removedCmdAnnotation
// marks the Hidden as deliberate for TestNoWtCommandIsHidden.
func removedRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use: "rotate", Hidden: true, DisableFlagParsing: true, SilenceUsage: true,
		Args:        cobra.ArbitraryArgs,
		Annotations: map[string]string{removedCmdAnnotation: "2026-11-27 (rotation removed)"},
		RunE: func(*cobra.Command, []string) error {
			return exitcode.New(1, errors.New("rotate was removed: the model picker no longer rotates, it opens on the row you launched last, else the first row that is not blocked; nothing was changed"), false)
		},
	}
}
