package main

import (
	"fmt"
	"os"
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
		fmt.Fprintln(os.Stderr, "wt: error:", err)
		os.Exit(1)
	}

	var showVersion bool
	// Legacy short flag rejection: `-w` was removed in favor of `-W`.
	// Registered below as a hidden string flag; checked in RunE.
	var legacyShortW string

	cmd := &cobra.Command{
		Use:   "wt",
		Short: "Launch AI coding agents in a chosen worktree, branch, and model",
		Example: "  wt                          # interactive TUI\n" +
			"  wt -W my-feature -A claude   # create worktree and launch\n" +
			"  wt --cwd --agent codex       # launch in current repo root\n" +
			"  wt --init                    # seed agent instruction files",
		// ArbitraryArgs overrides cobra's default legacyArgs validator, which
		// rejects any leading positional arg that isn't a registered
		// subcommand name (models/agents/rotate). Without this, passthrough
		// commands given without `--` (e.g. `shell-wt ls -la`) fail with
		// "unknown command \"ls\" for \"wt\"" even though Find() would never
		// have routed them to a subcommand anyway.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read the raw --agent flag early so --init can seed agent-specific
			// pointer files when a wrapper like claude-wt forwards --agent claude.
			agentFlag := mustGetString(cmd, "agent")

			// Legacy short flag rejection: `-w` was removed in favor of `-W`.
			if legacyShortW != "" {
				return fmt.Errorf("-w is removed; use -W or --worktree")
			}

			// Reject removed subcommands. `cobra.ArbitraryArgs` (set below)
			// is load-bearing for shell-wt passthrough (`shell-wt ls -la` →
			// `wt --agent shell ls -la`), so it swallows the first positional
			// without complaining. Without this guard, `wt models -A claude`
			// silently creates a worktree named "models" and launches claude
			// there — a footgun for users with stale muscle-memory invocations.
			// Keep this list in sync with removed subcommand names.
			if len(args) > 0 {
				switch args[0] {
				case "models":
					return fmt.Errorf("wt models is removed; use `wt config` to view models")
				case "agents":
					return fmt.Errorf("wt agents is removed; use `wt config` to view agents")
				}
			}

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
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				s, err := session.LatestForAgent(agent, root)
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
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				groups, err := worktree.Enumerate(root, root)
				if err != nil {
					return err
				}
				// Enumerate now returns three ordered groups; iterate
				// groups and entries the same way the picker does.
				for _, g := range groups {
					for _, e := range g.Entries {
						fmt.Printf("%-9s %-30s %s\n", e.Type, e.Branch, e.Path)
					}
				}
				return nil
			}

			// The agent comes only from --agent or the picker; there is no
			// implicit default. Every launch branch below routes through the
			// agent/command picker when no agent was provided.
			agent := agentFlag

			// Read the new filter flags (-M/-T/-F). They are plumbed through
			// to launchFiltered even when empty so that the legacy
			// defaultModel-based path is replaced uniformly. The TUI fallback
			// below forwards them to tui.Run for the model picker.
			tags := mustGetString(cmd, "tags")
			family := mustGetString(cmd, "family")
			pinned := mustGetString(cmd, "model")
			// cmd.Flags().Changed("model") is true only when -M was passed
			// (regardless of value). Used to surface a stderr note when -M
			// is paired with a command agent.
			pinnedSupplied := cmd.Flags().Changed("model")

			// Launch paths require a valid config. The `wt config` subcommand
			// bypasses this so it can repair a broken config.toml.
			if a.cfgErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}

			// -W <name>: use/create a worktree, then launch (no picker).
			if name := mustGetString(cmd, "worktree"); name != "" {
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				maybeInstallGuard()
				path, err := worktree.EnsureForName(root, name)
				if err != nil {
					return err
				}
				if agent == "" {
					// No agent provided: show the agent/command picker with the
					// worktree already resolved (skips the worktree picker).
					if !isStdinTTY() {
						return errPickerNeedsTTY
					}
					return tui.Run(yolo(cmd), "", tags, family, args, a.theme, path)
				}
				return launchFiltered(agent, path, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
			}

			// --cwd: launch in the current repo root.
			if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				maybeInstallGuard()
				if agent == "" {
					// No agent provided: show the picker with the current repo
					// root pre-selected (skips the worktree picker).
					if !isStdinTTY() {
						return errPickerNeedsTTY
					}
					return tui.Run(yolo(cmd), "", tags, family, args, a.theme, root)
				}
				return launchFiltered(agent, root, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
			}

			// Outside a git repo: pure passthrough to the agent. With no agent
			// given, show the picker with worktree pre-selected as "." (no git
			// enumeration needed).
			if !inGitRepo() {
				if agent == "" {
					if !isStdinTTY() {
						return errPickerNeedsTTY
					}
					return tui.Run(yolo(cmd), "", tags, family, args, a.theme, ".")
				}
				return launchFiltered(agent, ".", a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
			}

			// Interactive TUI: worktree picker first, then the agent/command
			// picker (skipped only when -A pins an agent or command).
			if agent == "" && !isStdinTTY() {
				return errPickerNeedsTTY
			}
			maybeInstallGuard()
			return tui.Run(yolo(cmd), agent, tags, family, args, a.theme, "")
		},
	}

	// Flags shared by wt and its subcommands.
	cmd.PersistentFlags().Bool("yolo", false, "Skip permission prompts")
	cmd.PersistentFlags().StringP("worktree", "W", "", "Use/create worktree for branch")
	cmd.PersistentFlags().StringP("agent", "A", "", "Agent or command to launch (claude, codex, copilot, pi, agy, opencode, shell)")
	cmd.PersistentFlags().StringP("model", "M", "", "Pin the model as <provider>/<name>")
	cmd.PersistentFlags().StringP("tags", "T", "", "Comma-delimited tags to filter models (OR within flag)")
	cmd.PersistentFlags().StringP("family", "F", "", "Comma-delimited model families to filter models (OR within flag)")
	cmd.PersistentFlags().Bool("cwd", false, "Launch in the current repo root, no picker")
	cmd.PersistentFlags().BoolVar(&showVersion, "version", false, "Print version and exit")

	// Legacy short flag rejection: `-w` was removed in favor of `-W`.
	// Register it as a hidden string flag so pflag parses the old arity
	// (`-w name` or `-wname`), then error out with the migration message in
	// RunE.
	cmd.Flags().StringVarP(&legacyShortW, "legacy-w", "w", "", "Deprecated; use -W")
	cmd.Flags().MarkHidden("legacy-w")

	// Test-only flags.
	cmd.Flags().Bool("debug-worktrees", false, "List worktrees and branches (test helper)")
	cmd.Flags().String("debug-session", "", "Print newest session for an agent (claude|opencode) (test helper)")

	// Seed agent instruction files and exit (no agent binary required).
	cmd.Flags().Bool("init", false, "Seed agent instruction files and exit")

	// Guard management flags (legacy parity).
	cmd.Flags().Bool("check-guard", false, "Check if the main guard is installed and exit")
	cmd.Flags().Bool("no-guard", false, "Uninstall the main guard and exit")

	cmd.AddCommand(rotateCmd(a), configCmd(a))
	return cmd
}
