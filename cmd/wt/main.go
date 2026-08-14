package main

import (
	"fmt"
	"os"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/wt
var version = "0.1.0"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wt",
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
		Use:   "models",
		Short: "Browse and manage the model registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}

			fmt.Println("Providers:")
			for _, p := range cfg.Providers {
				fmt.Printf("  %-15s %-10s auth=%-8s base_url=%s\n",
					p.ID, p.Location, p.Auth.Type, p.Auth.BaseURL)
			}

			fmt.Println("\nModels:")
			for _, m := range cfg.ModelsWithTag("code") {
				loc, _ := cfg.ResolveLocation(m)
				fmt.Printf("  %-35s family=%-12s provider=%-12s location=%-6s tags=%v\n",
					m.ID, m.Family, m.ProviderID, loc, m.Tags)
			}

			fmt.Println("\nAgents:")
			for _, a := range cfg.Agents {
				fmt.Printf("  %-10s providers=%v default=%s\n",
					a.Name, a.SupportedProviders, a.DefaultProvider)
			}
			return nil
		},
	}
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List installed agents and set defaults",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("(agents not yet implemented - lesson 6)")
		},
	}
}
