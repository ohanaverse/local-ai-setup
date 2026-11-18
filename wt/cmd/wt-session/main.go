package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/guard"
	"github.com/ohanaverse/local-ai-setup/wt/internal/initseed"
	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/wt-session
var version = "0.1.0"

// tuiRun is the entry point for the interactive TUI. It is a package-level
// variable so tests can stub it (see TestWorktreeWithAgentWithoutModelShowsModelPicker)
// instead of opening a real /dev/tty.
var tuiRun = tui.Run

// installed is a test seam wrapping agents.Installed for the pinned-agent
// binary check in RunE (issue #147): a pinned agent whose binary is missing
// must fail before any worktree work, and tests stub this so that check does
// not depend on the host's installed binaries. cmd/wt-session's TestMain defaults it
// to true so existing tests are unaffected unless they opt into stubbing it.
var installed = agents.Installed

func main() {
	err := rootCmd().Execute()
	// Never exit while an async LiteLLM proxy restart a route write kicked off
	// (internal/lifecycle/routes.go) is still in flight: goroutines do not
	// survive main returning, so the restart would be killed mid-flight and
	// the proxy would never pick up the route change. Only host commands
	// write routes (`wt stop <provider>`, `litellm sync`, …); a session writes
	// none, so for it this returns at once, and the session's other exits
	// (runAgentCmd's exit-code propagation, wt smoke's failure exit) need no
	// wait of their own.
	lifecycle.WaitPendingRoutes()
	if err != nil {
		os.Exit(reportExit(err, os.Stderr))
	}
}

// runLaunchPath installs the guard when inside a git repo and either
// auto-launches or routes to the picker/TUI. launchPath is the resolved
// worktree path for -W, the current directory for --cwd, "." for outside-a-repo
// passthrough, and "" when the worktree picker should be shown. root is the
// repo root that owns launchPath ("" when not inside a git repo — --cwd
// included — or when the worktree picker will resolve it); it gates the guard
// install and nothing else, which is why a launch that has a directory needs
// no repository. Callers pass the root they already resolved so no git
// subprocess is spawned twice.
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
	// Every launch goes through the model server (Task 18): a launch that has
	// its directory and its agent runs launchFilteredRemote (no registry, no
	// local probe), which refuses a
	// model-driven agent before anything starts when no server is
	// configured, and launches a command agent (shell) without one. The
	// picker (no directory or no agent yet) reads its table from the server
	// too.
	//
	// Several launchable models with no -M on a terminal are the picker's
	// to decide (errRemoteNeedsPicker): the launch falls through to runTUI
	// below with the directory and agent it has.
	//
	// What is wrong with slot-profiles.toml is said once, before any picker
	// opens or any launch starts, and only for a launch that could use a
	// profile: no agent chosen yet, or an agent whose driver has slots. A
	// client session says it without the registry's "profile/" holder,
	// which it never reads.
	var slotNotes []string
	// A native launch (native mode, #615) never uses a slot
	// profile, so it says nothing about the file either.
	if _, ok := agents.SlotsFor(agent); (agent == "" || ok) && !wouldLaunchNative(a, agent, pinned, cmd.Flags().Changed("model"), tags, family) {
		slotNotes = slotWarningsRemote(a)
	}
	printSlotNotes := func() {
		for _, line := range slotNotes {
			fmt.Fprintln(os.Stderr, line)
		}
	}
	pickOnServer := false
	if launchPath != "" && agent != "" {
		if root != "" {
			maybeInstallGuard()
		}
		printSlotNotes()
		err := func() error {
			holdLaunch(agent, launchPath)
			defer releaseRootClaim()
			return launchFilteredRemote(context.Background(), a, agent, launchPath, yolo(cmd), tags, family, pinned, cmd.Flags().Changed("model"), args, string(othersMode), allowReplace, a.profileState())
		}()
		if !errors.Is(err, errRemoteNeedsPicker) {
			return err
		}
		pickOnServer = true
		root = "" // the guard is installed
	}
	// A directory and no agent opens the agent/command picker, which needs a
	// terminal (P5: the refusal names -A).
	if launchPath != "" && agent == "" && !stdinTTY() {
		printSlotNotes()
		return pickerNeedsTTYError(agent)
	}

	// The picker's model table comes from the model server, so with none
	// configured a picker that may need a model refuses here, before the
	// guard, the registry read below or the TUI: exactly the launch's
	// refusal. A pinned command agent's picker only picks a worktree, and
	// opens without a server.
	var models tui.ModelClient
	scfg, serr := a.sessionCfg()
	ms, merr := a.buildModels(launchStill)
	switch {
	case merr == nil:
		models = tui.NewModelClient(ms.client, ms.ep)
	case agents.IsCommand(agent):
		// A command needs no model; nothing below asks for one.
	case nativeFallback(merr, pinned, cmd.Flags().Changed("model"), tags, family):
		// Native mode and no model asked for (#615): the picker
		// opens with no client and launches a chosen agent natively.
	default:
		return remoteExit(merr) // buildModels' errors carry the still clause
	}
	if serr != nil {
		return remoteExit(modelclient.Still(serr, launchStill))
	}

	// applyProfile closes over a's already-loaded profiles.toml state
	// (newApp() loaded and validated it once) so the TUI launch path
	// applies local-model launch profiles exactly like the non-TUI path
	// does, via the same applyProfileForLaunch — internal/tui cannot call
	// it directly (import-cycle/package-boundary reasons documented on
	// tui.ProfileApplier), so it is handed in as a closure instead.
	// A slot-profile launch never reaches applyProfile. The TUI is handed
	// the loaded slot profiles and applySlotProfile, the fail-closed applier
	// the non-TUI path calls directly, as tui.SlotProfiles. The profile
	// layer reads the session config, as launchFilteredRemote's runLaunch
	// does.
	applyProfile := func(cmd *exec.Cmd, agentName string, m config.Model) (func() error, error) {
		return applyProfileForLaunch(cmd, agentName, m, scfg, a.profileState())
	}

	// removeFuncs gives every picker load's row actions the same removal
	// engine `wt cleanup`'s list takes (cleanupRemoveFuncs, tui's
	// RemoveFuncsBuilder — internal/tui cannot build it without the registry,
	// which is this package's, so it is handed in as a closure like
	// applyProfile), from that load's own snapshot and inputs in `wt
	// cleanup`'s shape: the registry's protected paths (R10: read through
	// hostConfig, best-effort; the picker's gather reads no registry, so the
	// engine's re-check is what refuses a row holding an artifact) and PR
	// facts re-read by every re-check and refresh (the load's SkipPRs belongs
	// to its PR fill, not the engine). The command's default settings
	// otherwise (no --min-age on this path); a registry that did not load
	// turns removal off exactly as `wt cleanup`'s list is turned off.
	protPaths, protErr := protectedPaths(a)
	removeFuncs := func(snap cleanup.Snapshot, in cleanup.GatherInputs) tui.RemoveFuncs {
		in.ProtectedPaths = protPaths
		in.SkipPRs = false
		return cleanupRemoveFuncs(a, snap, in, 0, protErr)
	}

	// The slot notes (above) are printed here unless the launch already
	// printed them before falling through to the picker.
	if !pickOnServer {
		printSlotNotes()
	}

	// Install the guard once when inside any git repo.
	if root != "" {
		maybeInstallGuard()
	}

	// The TUI asks its one launch question at the commit point
	// (internal/tui's launchGate) — except for a preset path, which RunE's
	// confirmLaunch above has already asked about, so a root launch is asked
	// at most once. The TUI's alternate screen covers what was just printed
	// for as long as a picker is up, so a launch that went through the TUI
	// says it again once the TUI has returned and the terminal is the
	// user's.
	err := tuiRun(yolo(cmd), allowReplace, string(othersMode), agent, pinned, tags, family, args, a.theme, launchPath, models, scfg, applyProfile, tui.SlotProfiles{Checks: a.slotChecks, Apply: applySlotProfile}, removeFuncs)
	printSlotNotes()
	// tui.Run returns the agent's run error after its post-exit flow; a
	// non-zero exit becomes wt's own status, as on the non-TUI path, and
	// like runAgentCmd's os.Exit it prints nothing: no cobra "Error:" line
	// or usage block, no main "wt:" line (#412). Any other error is main's
	// one "wt:" line: cobra's "Error:" copy and usage block would only
	// repeat it.
	err = agentExitStatus(err)
	if err != nil {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
	}
	return err
}

// refuseCwdValue is #392's guard for the first word before any `--` of a
// --cwd launch: a word that names an existing directory was meant as
// --cwd's value, which the flag does not take, and is refused. A command
// agent's first word is the program to run, and a bare name that resolves
// on PATH runs that program whatever the current directory holds
// (`shell-wt --cwd wt model list` beside a `wt/` directory), so it is
// passed on. The current directory itself gets its own advice: there is
// nothing to cd into.
func refuseCwdValue(agent, word string) error {
	fi, err := os.Stat(word)
	if err != nil || !fi.IsDir() {
		return nil
	}
	if agents.IsCommand(agent) && !strings.ContainsRune(word, os.PathSeparator) {
		if _, err := exec.LookPath(word); err == nil {
			return nil
		}
	}
	if here, err := os.Stat("."); err == nil && os.SameFile(fi, here) {
		return fmt.Errorf("--cwd takes no value: %q is a directory, the current one; drop it, or pass it to the agent after --", word)
	}
	return fmt.Errorf("--cwd takes no value: %q is a directory; cd into it and run --cwd there, or pass it to the agent after --", word)
}

func rootCmd() *cobra.Command { return rootCmdWith(hooks{}) }

// rootCmdWith is rootCmd over an app whose hooks are h: production passes
// the zero value (rootCmd), a test the replacements it needs.
func rootCmdWith(h hooks) *cobra.Command {
	a, err := newApp()
	if err != nil {
		return failedRoot(err)
	}
	a.hooks = h

	var showVersion bool
	// Legacy short flag rejection: `-w` was removed in favor of `-W`.
	// Registered below as a hidden string flag; checked in RunE.
	var legacyShortW string

	cmd := &cobra.Command{
		Use:   "wt",
		Short: "Launch AI coding agents in a chosen worktree, branch, and model",
		Example: "  wt                          # interactive TUI\n" +
			"  wt -W my-feature -A claude   # use the worktree; a new name is created and said so\n" +
			"  wt --cwd --agent codex       # launch in the current directory\n" +
			"  wt -A claude -- --verbose    # pass arguments to the agent (a relative path is\n" +
			"                               # resolved from the directory the agent starts in)\n" +
			"  wt --init                    # seed agent instruction files\n" +
			"  wt start [model]             # start a local model (picker when omitted)\n" +
			"  wt stop [model|provider]     # stop a local model or provider (picker when omitted)\n" +
			"  wt smoke [model]             # smoke-test every agent that supports a model\n" +
			"  wt cloud-sync --dry-run      # plan a refresh of cloud prices and the ollama catalog",
		// ArbitraryArgs overrides cobra's default legacyArgs validator, which
		// rejects any leading positional arg that isn't a registered
		// subcommand name (config, start, rotate, …). Without this, passthrough
		// commands given without `--` (e.g. `shell-wt npm test`) fail with
		// "unknown command \"npm\" for \"wt\"" even though Find() would never
		// have routed them to a subcommand anyway.
		Args: cobra.ArbitraryArgs,
		// cobra's own "Error:" line is silenced for the whole tree: main
		// prints every error once, as "wt: <message>" (reportExit). Usage is
		// silenced per command (silenceUsageOnError), not here: a root-level
		// SilenceUsage would silence every subcommand's usage too.
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Flags and arguments have parsed: nothing below is a usage
			// mistake that cobra's usage block would help with.
			cmd.SilenceUsage = true
			// Read the raw --agent flag early so --init can seed agent-specific
			// pointer files when a wrapper like claude-wt forwards --agent claude.
			agentFlag := mustGetString(cmd, "agent")

			// Legacy short flag rejection: `-w` was removed in favor of `-W`.
			if legacyShortW != "" {
				return fmt.Errorf("-w is removed; use -W or --worktree")
			}

			// Reject removed subcommands. `cobra.ArbitraryArgs` (set above)
			// is load-bearing for shell-wt passthrough (`shell-wt npm test` →
			// `wt --agent shell npm test`), so it swallows the first positional
			// without complaining. Without this guard, `wt models -A claude`
			// hands the word "models" to claude as its first argument — a
			// footgun for users with stale muscle-memory invocations.
			// Keep this list in sync with removed subcommand names.
			//
			// Only a word typed before any `--` is refused. ArgsLenAtDash is
			// the number of positionals before the `--`: 0 means the first
			// positional came after it, where the user has said outright
			// that it is a passthrough command (`shell-wt -- models`), and
			// it is passed through whatever its name. -1 (no `--`) and 1 or
			// more (`wt models -- x`) leave the word where a subcommand
			// would stand.
			firstBeforeDash := len(args) > 0 && cmd.ArgsLenAtDash() != 0
			if firstBeforeDash {
				switch args[0] {
				case "models":
					return fmt.Errorf("wt models is removed; use `wt config` to view models")
				case "agents":
					return fmt.Errorf("wt agents is removed; use `wt config` to view agents")
				}
			}

			// --cwd takes no value (#392). `--cwd <dir>` leaves <dir> as
			// the first positional, which ArbitraryArgs passes on: claude
			// read it as the prompt, ran in the shell's directory, and wt
			// exited 0. A first word before any `--` that names an existing
			// directory is that mistake, so it is refused before anything
			// starts. Only the first word: `shell-wt --cwd ls proj` names a
			// directory as the command's own argument. After `--` the user
			// has said the word is the agent's, and it is passed on.
			cwd, _ := cmd.Flags().GetBool("cwd")
			if cwd && firstBeforeDash {
				if err := refuseCwdValue(agentFlag, args[0]); err != nil {
					return err
				}
			}

			// --root, --cwd and -W each say where to launch: two of them
			// contradict each other.
			rootFlag, _ := cmd.Flags().GetBool("root")
			places := 0
			for _, set := range []bool{rootFlag, cwd, mustGetString(cmd, "worktree") != ""} {
				if set {
					places++
				}
			}
			if places > 1 {
				return fmt.Errorf("--root, --cwd and -W are mutually exclusive: each says where to launch")
			}
			newFlag, _ := cmd.Flags().GetBool("new")
			if newFlag && mustGetString(cmd, "worktree") == "" {
				return fmt.Errorf("--new is only valid with -W <name>")
			}

			if check, _ := cmd.Flags().GetBool("check-guard"); check {
				status, err := checkGuardStatus()
				if err != nil {
					return err
				}
				if status != guard.Installed {
					return exitcode.New(1, errors.New("main guard is NOT installed in this repo."), false)
				}
				fmt.Println("wt: main guard is installed in this repo.")
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
				// Also auto-install the guard, like a normal launch would,
				// and announce a first-time install the same way so the hook
				// never appears silently. A hook already present prints
				// nothing, and an install failure returns as before.
				installed, err := guard.Install()
				if err != nil {
					return err
				}
				if installed {
					announceGuardInstall()
				}
				return nil
			}

			if showVersion {
				fmt.Println("wt", version)
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

			// Read the filter flags (-M/-T/-F). They are plumbed through to
			// launchFilteredRemote even when empty. The TUI fallback below forwards
			// them to tui.Run for the model picker.
			tags := mustGetString(cmd, "tags")
			family := mustGetString(cmd, "family")
			pinned := mustGetString(cmd, "model")
			// --replace is a process-wide launch mode: the launch's start
			// reads it, and the TUI receives it as an argument.
			allowReplace, _ = cmd.Flags().GetBool("replace")

			// Launch paths require a config.toml that loads. The `wt config`
			// subcommand bypasses this so it can repair a broken
			// config.toml. A launch reads no registry (Task 18): its models
			// come from the model server, so a missing or broken registry
			// is no launch's error.
			if _, err := a.sessionCfg(); err != nil {
				return configError(err)
			}

			// Fast-fail on unknown agent names. Without this, a typo'd -A surfaces
			// as "pass -M <model> to launch without it" — a hint aimed at the picker
			// path, not the actual problem. The agent picker is only useful for
			// known agents.
			if agent != "" && agents.ByName(agent) == nil {
				return fmt.Errorf("unknown agent %q (known: %s)", agent, strings.Join(agents.Names(), ", "))
			}

			// A pinned model-driven agent whose binary is missing can never
			// launch, configured or not; fail before any worktree work rather
			// than deep inside BuildLaunchCmdGateway after a worktree may already
			// have been created. Commands (e.g. shell) have no binary
			// requirement of their own — ListEntries/IssueFor already treat
			// them as always-installed.
			if agent != "" && !agents.IsCommand(agent) && !installed(agent) {
				return fmt.Errorf("agent %q is not installed — install the %s binary", agent, agent)
			}

			// A launch that needs a model needs the model server: refuse
			// before the launch question, a worktree or branch, the guard or
			// any other git work, rather than after (the same refusal
			// launchFilteredRemote and the picker give, decided without a
			// network call). A model-driven agent always needs one, unless the
			// mode is native and no model is asked for (-M, -T, -F): then it
			// launches natively (nativeFallback, #615); an unset mode refuses
			// here with ModeUnsetError's pointer at wt config; with no
			// agent the picker does (without a terminal the picker's own
			// refusal, below, comes first, as before; for `-W <new>` that
			// is only after plan.Ensure has created the worktree and
			// branch, which this check does not change). A command agent
			// needs none.
			if (agent != "" && !agents.IsCommand(agent)) || (agent == "" && stdinTTY()) {
				if _, err := a.buildModels(launchStill); err != nil && !nativeFallback(err, pinned, cmd.Flags().Changed("model"), tags, family) {
					return remoteExit(err) // buildModels' errors carry the still clause
				}
			}

			// --root: the primary checkout, found from anywhere. The flag is
			// the consent for the root warning alone (RootChosen) — a root
			// another live session holds still asks.
			if rootFlag {
				if _, err := worktree.RepoRoot(); err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				root, err := worktree.PrimaryOf(".")
				if err != nil {
					return fmt.Errorf("no primary checkout: %w", err)
				}
				t, terr := launchwarn.ForDir(root)
				t.RootChosen = true
				if err := confirmLaunch(t, terr, os.Stdin, osStderr, stdinTTY()); err != nil {
					return err
				}
				return runLaunchPath(cmd, a, agent, pinned, tags, family, args, root, root)
			}

			// -W <name>: use a worktree, then launch (no picker). The one
			// launch question runs before anything exists: a name the plan
			// will create (a new branch, a tracked remote, a worktree for an
			// existing branch) is announced as a creation; a name that is
			// already a worktree — the root's own branch included — is judged
			// as the directory it resolves to, root warning and all. A no
			// creates nothing. --new is a silent no-op, kept for old scripts.
			if name := mustGetString(cmd, "worktree"); name != "" {
				root, err := worktree.RepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repo: %w", err)
				}
				plan, err := worktree.Resolve(root, name)
				if err != nil {
					return err
				}
				// The target is read before the plan runs, so a creation is
				// described while it is still only planned.
				var t launchwarn.Target
				var terr error
				if _, serr := os.Stat(plan.Path()); plan.Creates() || plan.Tracks() != "" || serr != nil {
					t, terr = launchwarn.ForNew(root, plan)
				} else {
					t, terr = launchwarn.ForDir(plan.Path())
				}
				if err := confirmLaunch(t, terr, os.Stdin, osStderr, stdinTTY()); err != nil {
					return err
				}
				path, err := plan.Ensure()
				if err != nil {
					return err
				}
				return runLaunchPath(cmd, a, agent, pinned, tags, family, args, path, root)
			}

			// --cwd: launch in the current directory — the one launch that
			// does not move to a checkout's root, so a relative path after
			// `--` means the same thing to the agent as it did to the user.
			// The agent starts here too, so a `-- --continue` finds the
			// sessions of this directory.
			//
			// It needs no repository. The one thing a --cwd launch takes
			// from git is the checkout root, and that root only says
			// whether to install the main guard (runLaunchPath): rotation
			// and usage state are keyed by model, agent and pid,
			// never by a repository or a branch (claude's config_content
			// backup is keyed by the absolute settings path, so it cannot
			// collide either). So a
			// directory with no checkout root — outside any repository,
			// or with no git to ask — launches with root "", like the
			// outside-a-repo launch below: no guard, everything else the
			// same. Only -W (above) and the worktree picker (below) need
			// a repository: -W says so, and the picker is never reached
			// outside one.
			if cwd {
				root, err := worktree.RepoRoot()
				if err != nil {
					root = ""
				}
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				// Standing in the repository root is a warning and a
				// proceed — the directory choice is already the person's.
				t, terr := launchwarn.ForDir(wd)
				if err := confirmLaunch(t, terr, os.Stdin, osStderr, stdinTTY()); err != nil {
					return err
				}
				return runLaunchPath(cmd, a, agent, pinned, tags, family, args, wd, root)
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
	cmd.PersistentFlags().StringP("worktree", "W", "", "Use the worktree for a branch; a name that exists nowhere is created and said so (see --new)")
	cmd.PersistentFlags().StringP("agent", "A", "", "Agent or command to launch (claude, codex, copilot, pi, agy, opencode, shell)")
	cmd.PersistentFlags().StringP("model", "M", "", "Pin the model as <provider>/<name>")
	cmd.PersistentFlags().Bool("replace", false, "With -M, start the model even if it means stopping a running one")
	// --others is read by the start driver (othersMode), so `wt start`, `wt
	// smoke`, and a -M pin or slot profile's local model launched without the
	// picker screens (-W, --cwd, outside a repository) answer it; a launch
	// that opens the picker hands it to tui.Run, where it answers for the -M
	// pinned row only, as --replace does. Reset
	// here: the value is package state, and each rootCmd starts unanswered.
	othersMode = ""
	cmd.PersistentFlags().Var(&othersMode, "others", "When a local start loads a model beside other providers' models: keep them, or stop the ones wt can stop (default: ask; when stdin is not a terminal keep and warn). Read by wt start, wt smoke and a -M launch; in the picker it answers only for the -M pinned model, and any other start asks in a dialog")
	cmd.PersistentFlags().StringP("tags", "T", "", "Comma-delimited tags to filter models (OR within flag)")
	cmd.PersistentFlags().StringP("family", "F", "", "Comma-delimited model families to filter models (OR within flag)")
	cmd.PersistentFlags().Bool("cwd", false, "Launch in the current directory, inside a git repository or not; no worktree picker (wt smoke: run the agents there, not in a temporary directory)")
	cmd.PersistentFlags().BoolVar(&showVersion, "version", false, "Print version and exit")
	cmd.Flags().Bool("root", false, "Launch in the repository's primary checkout, from anywhere; accepts the root warning, though any other warning still asks (default for a bare wt is a worktree)")
	cmd.Flags().Bool("new", false, "With -W, accepted and ignored: a name that exists nowhere is created and said so either way (kept for old scripts)")

	// Legacy short flag rejection: `-w` was removed in favor of `-W`.
	// Register it as a hidden string flag so pflag parses the old arity
	// (`-w name` or `-wname`), then error out with the migration message in
	// RunE.
	cmd.Flags().StringVarP(&legacyShortW, "legacy-w", "w", "", "Deprecated; use -W")
	cmd.Flags().MarkHidden("legacy-w")

	// Test-only flags.
	cmd.Flags().Bool("debug-worktrees", false, "List worktrees and branches (test helper)")

	// Seed agent instruction files and exit (no agent binary required).
	cmd.Flags().Bool("init", false, "Seed agent instruction files and exit")

	// Guard management flags.
	cmd.Flags().Bool("check-guard", false, "Check if the main guard is installed and exit")
	cmd.Flags().Bool("no-guard", false, "Uninstall the main guard and exit")

	cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), servedCmd(a), warmCmd(a), litellmCmd(a), profileCmd(a), modelCmd(a), cloudSyncCmd(a), worktreesCmd(a), removeCmd(a), rescueCmd(a), restoreRootCmd(a), jobCmd(a))
	silenceUsageOnError(cmd)
	return cmd
}

// usageOnMistakeAnnotation marks a command that prints cobra's usage block
// itself when its arguments or flags are a usage mistake (`wt stop`, `wt
// smoke`, set in their RunE), so silenceUsageOnError leaves it alone.
const usageOnMistakeAnnotation = "wt-usage-on-mistake"

// silenceUsageOnError sets SilenceUsage on every runnable subcommand that
// does not carry usageOnMistakeAnnotation: an error such a command returns
// is a failure to report, not a usage mistake, and main prints it as the one
// "wt:" line (style guide M3). It is a walk, not a literal on each command,
// so a new subcommand cannot forget it (TestEveryRunnableCommandSilencesUsage).
// The root is left out: SilenceUsage on it would silence every subcommand.
func silenceUsageOnError(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.Runnable() && c.Annotations[usageOnMistakeAnnotation] == "" {
			c.SilenceUsage = true
		}
		silenceUsageOnError(c)
	}
}

// failedRoot is the root command rootCmd returns when newApp cannot start
// (a theme file that cannot be read): it runs nothing and returns that
// error, so main reports it through the same single path as every other
// failure, as "wt: <message>" with exit 1.
func failedRoot(err error) *cobra.Command {
	return &cobra.Command{
		Use:                "wt",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE:               func(*cobra.Command, []string) error { return err },
	}
}
