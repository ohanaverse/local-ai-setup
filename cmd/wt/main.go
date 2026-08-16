package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/guard"
	"github.com/ohanaverse/agent-worktree/internal/initseed"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/ohanaverse/agent-worktree/internal/tui"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
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

	// Test-only flag: print the next model in a tag rotation and exit.
	cmd.Flags().String(
		"rotate-tag",
		"",
		"Print next model in the given tag group (test helper)",
	)
	// Test-only flag: enumerate worktrees and branches.
	cmd.Flags().Bool(
		"debug-worktrees",
		false,
		"List worktrees and branches (test helper)",
	)
	// Test-only flag: print the newest resumable session for an agent.
	cmd.Flags().String(
		"debug-session",
		"",
		"Print newest session for an agent (claude|opencode) (test helper)",
	)

	// Seed agent instruction files and exit (no agent binary required).
	cmd.Flags().Bool(
		"init",
		false,
		"Seed agent instruction files and exit",
	)

	// With no subcommand, wt launches the interactive TUI.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if initFlag, _ := cmd.Flags().GetBool("init"); initFlag {
			root, err := initseed.Root()
			if err != nil {
				return err
			}
			res, err := initseed.Seed("", root)
			if err != nil {
				return err
			}
			if len(res.Created) == 0 {
				fmt.Println("wt: instruction files already exist.")
			} else {
				fmt.Printf("wt: seeded: %s\n", strings.Join(res.Created, ", "))
			}
			// Also auto-install the guard, like a normal launch would.
			if _, err := guard.Install(); err != nil {
				return err
			}
			return nil
		}

		if showVersion {
			fmt.Println("wt", version)
			return nil
		}
		if tag, _ := cmd.Flags().GetString("rotate-tag"); tag != "" {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			r := rotation.ForTag(cfg, tag)
			m, ok := r.Next("")
			if !ok {
				return fmt.Errorf("no models tagged %q", tag)
			}
			fmt.Println(m.ID)
			return nil
		}
		if w, _ := cmd.Flags().GetString("worktree"); w != "" {
			root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
			if err != nil {
				return fmt.Errorf("not in a git repo: %w", err)
			}
			path, err := worktree.EnsureForName(strings.TrimSpace(string(root)), w)
			if err != nil {
				return err
			}
			fmt.Println("worktree at:", path)
			return nil
		}
		if agent, _ := cmd.Flags().GetString("debug-session"); agent != "" {
			root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
			if err != nil {
				return fmt.Errorf("not in a git repo: %w", err)
			}
			cwdRoot := strings.TrimSpace(string(root))
			s, err := session.LatestForAgent(agent, cwdRoot)
			if err != nil {
				return err
			}
			if s == nil {
				fmt.Println("(no sessions)")
				return nil
			}
			fmt.Printf("resume %s (last %s)\n", s.ID, session.RelativeTime(s.MTime))
			return nil
		}
		if debug, _ := cmd.Flags().GetBool("debug-worktrees"); debug {
			root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
			if err != nil {
				return fmt.Errorf("not in a git repo: %w", err)
			}
			cwdRoot := strings.TrimSpace(string(root))
			entries, err := worktree.Enumerate(cwdRoot, cwdRoot)
			if err != nil {
				return err
			}
			for _, e := range entries {
				fmt.Printf("%-9s %-30s %s\n", e.Type, e.Branch, e.Path)
			}
			return nil
		}
		return tui.Run()
}

	cmd.AddCommand(modelsCmd(), agentsCmd())
	return cmd
}

// borderStyle is the shared table border colour.
var borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

func renderTable(headers []string, rows [][]string) string {
	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.NormalBorder()).
		BorderStyle(borderStyle).
		BorderRow(true)
	return t.Render()
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

			// Providers table
			provRows := make([][]string, 0, len(cfg.Providers))
			for _, p := range cfg.Providers {
				provRows = append(provRows, []string{
					p.ID,
					string(p.Location),
					p.Auth.Type,
					p.Auth.BaseURL,
				})
			}
			fmt.Println("Providers:")
			fmt.Println(renderTable(
				[]string{"ID", "LOCATION", "AUTH", "BASE_URL"},
				provRows,
			))

			// Models table — sort by provider, then ID
			models := make([]config.Model, len(cfg.Models))
			copy(models, cfg.Models)
			sort.Slice(models, func(i, j int) bool {
				if models[i].ProviderID != models[j].ProviderID {
					return models[i].ProviderID < models[j].ProviderID
				}
				return models[i].ID < models[j].ID
			})

			modelRows := make([][]string, 0, len(models))
			for _, m := range models {
				loc, _ := cfg.ResolveLocation(m)
				modelRows = append(modelRows, []string{
					m.ID,
					m.Family,
					m.ProviderID,
					string(loc),
					strings.Join(m.Tags, ", "),
				})
			}
			fmt.Println("Models:")
			fmt.Println(renderTable(
				[]string{"ID", "FAMILY", "PROVIDER", "LOCATION", "TAGS"},
				modelRows,
			))

			// Agents table
			agentRows := make([][]string, 0, len(cfg.Agents))
			for _, a := range cfg.Agents {
				agentRows = append(agentRows, []string{
					a.Name,
					strings.Join(a.SupportedProviders, ", "),
					a.DefaultProvider,
				})
			}
			fmt.Println("Agents:")
			fmt.Println(renderTable(
				[]string{"NAME", "PROVIDERS", "DEFAULT"},
				agentRows,
			))

			return nil
		},
	}
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List installed agents and set defaults",
		Run: func(cmd *cobra.Command, args []string) {
			names := agents.Names()
			sort.Strings(names)
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				d := agents.ByName(n)
				installed := "no"
				if agents.Installed(n) {
					installed = "yes"
				}
				rows = append(rows, []string{n, installed, d.YoloFlag()})
			}
			fmt.Println("Agents:")
			fmt.Println(renderTable(
				[]string{"NAME", "INSTALLED", "YOLO_FLAG"},
				rows,
			))
		},
	}
}
