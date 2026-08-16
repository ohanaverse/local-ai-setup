package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/guard"
	"github.com/ohanaverse/agent-worktree/internal/initseed"
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
	a, err := newApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt: config error:", err)
		os.Exit(1)
	}

	var showVersion bool

	cmd := &cobra.Command{
		Use:   "wt",
		Short: "Launch AI coding agents in a chosen worktree, branch, and model",
		Example: "  wt                          # interactive TUI\n" +
			"  wt -w my-feature --agent claude  # create worktree and launch\n" +
			"  wt --cwd --agent codex           # launch in current repo root\n" +
			"  wt --init                        # seed agent instruction files",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read the raw --agent flag early so --init can seed agent-specific
			// pointer files when a wrapper like claude-wt forwards --agent claude.
			agentFlag := mustGetString(cmd, "agent")

			if check, _ := cmd.Flags().GetBool("check-guard"); check {
				status, err := checkGuardStatus()
				if err != nil {
					return err
				}
				switch status {
				case guard.Installed:
					fmt.Println("wt: main guard is installed in this repo.")
				default:
					fmt.Fprintln(os.Stderr, "wt: main guard is NOT installed in this repo.")
					os.Exit(1)
				}
				return nil
			}

			if noGuard, _ := cmd.Flags().GetBool("no-guard"); noGuard {
				if err := removeGuard(); err != nil {
					return err
				}
				fmt.Println("wt: main guard removed.")
				return nil
			}

			if initFlag, _ := cmd.Flags().GetBool("init"); initFlag {
				root, err := initseed.Root()
				if err != nil {
					return err
				}
				res, err := initseed.Seed(agentFlag, root)
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

			// Resolve the agent: --agent flag wins, else the config default.
			agent := agentFlag
			if agent == "" {
				agent = defaultAgent(a.cfg)
			}

			// -w <name>: use/create a worktree, then launch (no picker).
			if name := mustGetString(cmd, "worktree"); name != "" {
				root, err := worktree.RepoRoot()
				if err != nil {
					return err
				}
				maybeInstallGuard()
				path, err := worktree.EnsureForName(root, name)
				if err != nil {
					return err
				}
				return launch(agent, path, a.cfg, yolo(cmd))
			}

			// --cwd: launch in the current repo root.
			if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
				root, err := worktree.RepoRoot()
				if err != nil {
					return err
				}
				maybeInstallGuard()
				return launch(agent, root, a.cfg, yolo(cmd))
			}

			// Outside a git repo: pure passthrough to the agent.
			if !inGitRepo() {
				return launchDirect(agent, a.cfg, yolo(cmd))
			}

			// Interactive TUI.
			maybeInstallGuard()
			return tui.Run(yolo(cmd))
		},
	}

	// Flags shared by wt and its subcommands.
	cmd.PersistentFlags().Bool("yolo", false, "Skip permission prompts")
	cmd.PersistentFlags().StringP("worktree", "w", "", "Use/create worktree for branch")
	cmd.PersistentFlags().String("agent", "", "Agent to launch (claude, codex, copilot, pi, agy, opencode)")
	cmd.PersistentFlags().Bool("cwd", false, "Launch in the current repo root, no picker")
	cmd.PersistentFlags().BoolVar(&showVersion, "version", false, "Print version and exit")

	// Test-only flags.
	cmd.Flags().Bool("debug-worktrees", false, "List worktrees and branches (test helper)")
	cmd.Flags().String("debug-session", "", "Print newest session for an agent (claude|opencode) (test helper)")

	// Seed agent instruction files and exit (no agent binary required).
	cmd.Flags().Bool("init", false, "Seed agent instruction files and exit")

	// Guard management flags (legacy parity).
	cmd.Flags().Bool("check-guard", false, "Check if the main guard is installed and exit")
	cmd.Flags().Bool("no-guard", false, "Uninstall the main guard and exit")

	cmd.AddCommand(modelsCmd(a), agentsCmd(a), rotateCmd(a))
	return cmd
}
