package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/guard"
	"github.com/ohanaverse/local-ai-setup/wt/internal/initseed"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/wt
var version = "0.1.0"

// tuiRun is the entry point for the interactive TUI. It is a package-level
// variable so tests can stub it out (the real tui.Run requires a TTY).
var tuiRun = tui.Run

// needsModelPicker reports whether the CLI must route to the interactive
// model picker. True when:
//   - the user did not pin an agent (no -A), OR
//   - the user pinned an agent that is not a command AND no model is pinned
//     (the launch path would have to pick a model itself).
//
// A pinned command (e.g. shell) returns false regardless of model pin
// because commands have no model layer. A pinned agent + pinned model
// returns false because every launch input is resolved.
//
// This predicate is the single source of truth for picker routing across
// --cwd, -W, and the outside-repo path; if you change it, retest all three.
func needsModelPicker(agent, pinned string) bool {
	return agent == "" || (pinned == "" && !agents.IsCommand(agent))
}

// resolveModelForLaunch wraps resolveModel with a "resolved" boolean so
// callers can short-circuit on a single resolvable model (auto-launch)
// without conflating "model is empty" with "error". A resolved return value
// (true, model, eligible, nil) means launchFiltered would have a unique model
// to use. A non-resolved return (false, zero, _, _) means the caller should
// fall through to the picker. err is non-nil only when resolveModel itself
// failed; the auto-launch path treats any error as "not resolved".
//
// The eligible list is returned so the caller can hand it to launchFiltered
// without recomputing it (the auto-launch path would otherwise call
// EligibleModels twice: once here and once inside launchFiltered).
func resolveModelForLaunch(agent string, cfg *config.Config, tags, family, pinned string) (bool, config.Model, []config.Model, error) {
	m, eligible, err := resolveModel(agent, cfg, tags, family, pinned)
	if err != nil {
		return false, config.Model{}, eligible, err
	}
	if m.ID == "" {
		return false, config.Model{}, eligible, nil
	}
	return true, m, eligible, nil
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
}

// runLaunchPath installs the guard when inside a git repo and either
// auto-launches or routes to the picker/TUI. launchPath is the resolved
// worktree path for -W, the repo root for --cwd, "." for outside-a-repo
// passthrough, and "" when the worktree picker should be shown. root is the
// repo root that owns launchPath ("" when not inside a git repo, or when the
// worktree picker will resolve it); it gates the guard install. Callers pass
// the root they already resolved so no git subprocess is spawned twice.
//
// Callers must install the guard themselves when launchPath == "" (the TUI
// branch), because the repo root is not known until the user selects a
// worktree inside the TUI.
func runLaunchPath(
	cmd *cobra.Command,
	a *app,
	agent, pinned, tags, family string,
	args []string,
	launchPath, root string,
) error {
	// Install the guard once when inside any git repo.
	if root != "" {
		maybeInstallGuard()
	}

	// launchPath == "" means the worktree picker should be shown; the launch
	// directory is not known until the user picks inside the TUI, so never
	// short-circuit to launchFiltered here — a pinned agent or command must
	// still pick a worktree.
	if launchPath == "" {
		return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
	}

	pinnedSupplied := cmd.Flags().Changed("model")

	if needsModelPicker(agent, pinned) {
		if resolved, _, eligible, err := resolveModelForLaunch(agent, a.cfg, tags, family, pinned); err == nil && resolved {
			return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args, eligible)
		}
		if !stdinTTY() {
			return pickerNeedsTTYError(agent)
		}
		return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
	}

	return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args, nil)
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
				d := agents.ByName(agent)
				if d == nil {
					return fmt.Errorf("unknown agent: %s", agent)
				}
				r, ok := d.(agents.Resumer)
				if !ok {
					fmt.Printf("%s: no resume support\n", agent)
					return nil
				}
				s, err := r.LatestSession(root)
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

			// Launch paths require a valid config. The `wt config` subcommand
			// bypasses this so it can repair a broken config.toml. Command
			// agents (shell, etc.) have no model layer, so a missing modelman
			// registry is tolerated for them — only real agents need the
			// registry, and they still fail closed with the migrate hint.
			if a.cfgErr != nil && !(agent != "" && agents.IsCommand(agent) && errors.Is(a.cfgErr, config.ErrRegistryMissing)) {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}

			// Fast-fail on unknown agent names. Without this, a typo'd -A surfaces
			// as "pass -M <model> to launch without it" — a hint aimed at the picker
			// path, not the actual problem. This mirrors the pre-PR launchFiltered
			// fast-fail: the agent picker is only useful for known agents.
			if agent != "" && agents.ByName(agent) == nil {
				return fmt.Errorf("unknown agent %q (known: %s)", agent, strings.Join(agents.Names(), ", "))
			}

			// -W <name>: use/create a worktree, then launch (no picker).
			if name := mustGetString(cmd, "worktree"); name != "" {
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				path, err := worktree.EnsureForName(root, name)
				if err != nil {
					return err
				}
				return runLaunchPath(cmd, a, agent, pinned, tags, family, args, path, root)
			}

			// --cwd: launch in the current repo root.
			if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				return runLaunchPath(cmd, a, agent, pinned, tags, family, args, root, root)
			}

			// Outside a git repo: pure passthrough to the agent. With no agent
			// given, show the picker with worktree pre-selected as "." (no git
			// enumeration needed).
			if !worktree.IsRepo(".") {
				return runLaunchPath(cmd, a, agent, pinned, tags, family, args, ".", "")
			}

			// Interactive TUI: worktree picker first, then the agent/command
			// picker (skipped only when -A pins an agent or command).
			if agent == "" && !stdinTTY() {
				return errPickerNeedsTTY
			}
			maybeInstallGuard()
			return runLaunchPath(cmd, a, agent, pinned, tags, family, args, "", "")
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
