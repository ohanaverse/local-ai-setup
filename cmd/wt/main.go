package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags:
//   go build -ldflags "-X main.version=1.2.3" ./cmd/wt
var version = "0.1.0"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "wt",
		Short: "Launch AI coding agents in a chosen worktree, branch, and model",
	}

	// Flags shared by wt and its subcommands.
	cmd.PersistentFlags().Bool(
		"yolo",
		false,
		"Skip permission prompts",
	)
	cmd.PersistentFlags().StringP(
		"worktree",
		"w",
		"",
		"Use/create worktree for branch",
	)
	var showVersion bool
	cmd.PersistentFlags().BoolVar(
		&showVersion,
		"version",
		false,
		"Print version and exit",
	)

	// With no subcommand, wt launches the interactive TUI.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if showVersion {
			fmt.Println("wt", version)
			return nil
		}
		fmt.Println("(TUI not yet implemented - coming in lesson 12)")
		return nil
	}

	cmd.AddCommand(modelsCmd(), agentsCmd())
	return cmd
}

func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "models",
		Short: "Browse and manage the model registry",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("(models registry not yet implemented - lesson 2)")
		},
	}
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "agents",
		Short: "List installed agents and set defaults",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("(agents not yet implemented - lesson 6)")
		},
	}
}