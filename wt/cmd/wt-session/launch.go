package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// loadProfileStore is a seam for tests: production reads
// ~/.config/agent-wt/profiles.toml (via profiles.Load); tests swap it so
// no test depends on the developer's real file.
var loadProfileStore = profiles.Load

// precomputedProfiles carries a.profiles/a.profilesLoadErr/
// a.profilesValidateErr (newApp() already loaded and validated
// profiles.toml once at startup) through to applyProfileForLaunch, so a
// launch never re-reads and re-parses the file a second time in the same
// process. nil means "not precomputed" — every direct test call, and any
// caller with no *app in scope — and applyProfileForLaunch falls back to
// loadProfileStore()/profiles.Validate exactly as before.
type precomputedProfiles struct {
	store       profiles.Store
	loadErr     error
	validateErr error
}

// confirmProfile is a seam for tests: production asks on /dev/tty whether
// to apply a resolved profile, defaulting to yes on a bare Enter or when
// no TTY is available (non-interactive launches — scripts, wt smoke, CI —
// must never block on input that can't arrive); tests swap it so nothing
// blocks on real terminal input.
var confirmProfile = promptProfile

// openTTY is a seam for tests: production opens the controlling terminal
// directly; tests swap it so a non-TTY (or TTY-available-but-must-not-
// block) scenario is deterministic regardless of whether the test runner
// itself happens to have a real /dev/tty attached.
var openTTY = func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }

func promptProfile(rp profiles.ResolvedProfile) (bool, error) {
	f, err := openTTY()
	if err != nil {
		return true, nil
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "wt: local-model profile available for this launch (%s) — apply? [Y/n] ", strings.Join(rp.Sources, ", ")); err != nil {
		return false, err
	}
	return cli.ReadYesNo(f, true)
}

// shellDir is a test seam over os.Getwd: the directory the user typed the
// command in, which is what they meant a relative passthrough path against.
var shellDir = os.Getwd

// runLaunch is the one way a built agent command is run: it prints the
// pre-launch notes (to osStderr, the seam the start progress already uses),
// then hands off to runAgentCmd. The note it carries is agents.RelativeArgNotes
// (root drift is not one of them: RunE's confirmLaunch already names it, once,
// from launchwarn.Target) — a passthrough argument that names a file from
// the directory the command was typed in, but not from the worktree the agent
// starts in, would otherwise fail (or open the wrong file) with nothing to
// connect it to where wt started the agent. Every plain launch site goes
// through here so none can run an agent without the notes; slot-profile and
// TUI launches take other paths and carry their own notes (Ruling R412c).
func runLaunch(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles, extraArgs []string, tail runTail) error {
	if wd, err := shellDir(); err == nil {
		for _, note := range agents.RelativeArgNotes(agent, wd, cmd.Dir, extraArgs) {
			fmt.Fprintln(osStderr, note)
		}
	}
	return runAgentCmd(cmd, agent, m, cfg, pp, tail)
}

// selfHealAgentConfigContentTarget checks agent's config_content file
// target (claude, codex — opencode has none, it merges into env instead)
// for an orphaned backup left by a previous session that never restored it
// (e.g. wt was killed with `kill -9` mid-launch) and restores it if found,
// printing a one-line notice. It runs UNCONDITIONALLY, on every launch of
// that agent — not only a launch that itself resolves a matching profile —
// closing the gap where a stale, profile-rewritten file would otherwise
// only get cleaned up the next time a profile happened to match for that
// exact agent+target again. Its only job is repairing PAST session state,
// so it deliberately runs before profiles.toml is even loaded: it must
// still self-heal when the profile layer is globally disabled (`enabled =
// false`) or profiles.toml doesn't exist at all. A ConfigFileTarget error
// (e.g. codex's $HOME unresolvable) or a restore error is reported to
// stderr and otherwise ignored — this must never fail the launch, the same
// graceful-degradation posture as every other profile step here.
func selfHealAgentConfigContentTarget(agent, worktreePath string) {
	target, _, ok, err := profiles.ConfigFileTarget(agent, worktreePath)
	if err != nil || !ok {
		return
	}
	restored, herr := profiles.SelfHeal(target)
	if herr != nil {
		fmt.Fprintf(os.Stderr, "wt: profile self-heal check for %s: %v\n", target, herr)
		return
	}
	if restored {
		fmt.Fprintf(os.Stderr, "wt: restored a leftover profile-managed file from a previous session: %s\n", target)
	}
}

// applyProfileForLaunch resolves and, on confirmation, applies a
// local-model profile for agent/m to cmd before it runs. It returns a
// cleanup func that MUST be called after cmd.Run() returns (success or
// failure) to restore any config file a profile's config_content
// mechanism rewrote — called explicitly, never via defer, since the
// os.Exit branch in runAgentCmd below would otherwise skip a deferred
// cleanup. Command agents (m.ID == "") and native models never match a
// profile (neither has a route of its own), so this no-ops for them
// without even loading profiles.toml. pp, when non-nil, is newApp()'s
// already-loaded profiles.toml state (see precomputedProfiles) — reused
// instead of loading and validating the file again.
func applyProfileForLaunch(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) (cleanup func() error, err error) {
	noop := func() error { return nil }
	// Self-heal runs before the early-return guards below: it repairs PAST
	// session state (an orphaned config_content backup left by a prior
	// launch that was killed mid-run) and depends only on agent/cmd.Dir,
	// never on m or cfg — it must still run for a native-model or
	// command-agent launch of the same agent, which is exactly the launch
	// class the guard below skips for THIS launch's own profile
	// resolution.
	selfHealAgentConfigContentTarget(agent, cmd.Dir)
	if m.ID == "" || m.Native || cfg == nil {
		return noop, nil
	}

	// A load error wins over a validation error, and a store that failed to
	// load is not validated.
	var store profiles.Store
	var perr error
	if pp != nil {
		store, perr = pp.store, pp.loadErr
		if perr == nil {
			perr = pp.validateErr
		}
	} else {
		store, perr = loadProfileStore()
		if perr == nil {
			perr = profiles.Validate(store, agentProfileMechanisms, agentRequiredMechanism)
		}
	}
	if perr != nil {
		fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", perr)
		return noop, nil
	}
	if !store.Enabled {
		return noop, nil
	}
	rp := profiles.Resolve(store, agent, cfg, m)
	if rp.Empty() {
		return noop, nil
	}
	apply, err := confirmProfile(rp)
	if err != nil {
		// Per the design's global constraint, a profile resolution/
		// application error must degrade to a normal, unprofiled launch —
		// never block the agent from starting — so a confirm-prompt
		// failure (e.g. a /dev/tty write error) is a warning, not a
		// launch-aborting error.
		fmt.Fprintf(os.Stderr, "wt: profile confirm prompt: %v (profiles disabled for this launch)\n", err)
		return noop, nil
	}
	if !apply {
		return noop, nil
	}
	return applyResolvedProfile(cmd, agent, rp, nil)
}

// applyResolvedProfile applies an already-confirmed ResolvedProfile to
// cmd, in three steps: env/args first, then config_content, then the
// wrapper last. This ordering means a codex profile's own declared args
// (the "args" mechanism) land BEFORE config_content's own
// "--profile agent-wt-profile" flag rather than after it, and the wrapper
// — applied last — still captures everything appended before it in its
// {{args}} splice (the one interaction this package's tests pin; no
// Phase-1 profile combines config_file with wrapper, but this keeps the
// ordering correct if one ever does). Extracted from applyProfileForLaunch
// so this config_content/wrapper interaction — specifically that a
// wrapper failure must still trigger the already-obtained content cleanup
// — is directly testable with a hand-built ResolvedProfile, without
// needing a profiles.toml entry that passes Validate.
//
// oneShotArgs is nil for a real (interactive) launch — the caller above
// never has a one-shot subcommand to place. wt smoke passes the agent's
// OneShotArgs (e.g. codex's ["exec", prompt]) here instead of appending
// them to cmd.Args itself, because they must land in a specific slot:
// AFTER every profile-injected CLI flag (ExtraArgs, then config_content's
// "--profile" flag) but BEFORE the wrapper runs. codex's CLI silently
// drops an earlier "-c model_provider=..." override — falling back to its
// default "openai" provider — whenever any "-c"/"--profile" flag trails
// "exec <prompt>" in argv (confirmed 2026-09-25 by hand-reproducing wt
// smoke's exact argv with only the flag order changed), so those flags can
// never land after oneShotArgs. The wrapper mechanism (pi's little-coder)
// needs the opposite: it splices the CURRENT cmd.Args into its own argv,
// so oneShotArgs must already be present by the time ApplyWrapper runs, or
// the one-shot prompt would silently fall outside the wrapped command.
//
// Because ApplyEnvAndArgs now runs before ApplyConfigContent (that
// ordering is the whole point of this split — see the doc above), a
// config_content failure must undo whatever ApplyEnvAndArgs already
// appended to cmd.Env/cmd.Args before degrading to an unprofiled launch;
// otherwise the launch would be silently half-profiled (env vars and
// extra args applied) while the user is told profiles are disabled.
// ApplyEnvAndArgs only ever appends, so resetting to a pre-append snapshot
// is a clean, complete undo — but oneShotArgs must still be appended onto
// that reverted snapshot, or the degraded-unprofiled command would be
// missing its one-shot subcommand/prompt entirely.
func applyResolvedProfile(cmd *exec.Cmd, agent string, rp profiles.ResolvedProfile, oneShotArgs []string) (cleanup func() error, err error) {
	noop := func() error { return nil }
	origEnv := slices.Clone(cmd.Env)
	origArgs := slices.Clone(cmd.Args)
	profiles.ApplyEnvAndArgs(cmd, rp)
	contentCleanup, err := profiles.ApplyConfigContent(cmd, agent, cmd.Dir, rp)
	if err != nil {
		// Per the design's global constraint, a profile resolution/
		// application error must degrade to a normal, unprofiled launch —
		// never block the agent from starting — so a file-write error here
		// (permissions, a resolve-home-dir failure for codex, …) is a
		// warning, not a launch-aborting error. Restore cmd.Env/cmd.Args to
		// their pre-ApplyEnvAndArgs state (see the doc comment above) so
		// this degrade is genuinely unprofiled, not half-profiled — then
		// reattach oneShotArgs, since this is the only path back to the
		// caller and it must still produce a runnable one-shot command.
		profiles.RestoreAndReattach(cmd, origEnv, origArgs, oneShotArgs)
		fmt.Fprintf(os.Stderr, "wt: profile config_content: %v (profiles disabled for this launch)\n", err)
		return noop, nil
	}
	// This append must happen here — before the rp.Wrapper branch below —
	// even though it means a wrapper's one error case (missing binary)
	// wastes the append: ApplyWrapper splices the CURRENT cmd.Args
	// verbatim into its "{{args}}" template, so oneShotArgs must already
	// be present by the time it runs, or a wrapper-profiled agent's
	// one-shot prompt would silently never be spliced in at all. Moving
	// this after the wrapper block would fix nothing on the rare
	// missing-binary error path and would break the far more important
	// success path.
	if len(oneShotArgs) > 0 {
		cmd.Args = append(cmd.Args, oneShotArgs...)
	}
	if rp.Wrapper != nil {
		if err := profiles.ApplyWrapper(cmd, rp.Wrapper); err != nil {
			// ApplyWrapper's one error case (a wrapper mechanism naming a
			// missing binary) is the sole exception the design calls out
			// as fatal — but any config file ApplyConfigContent already
			// wrote or backed up above must still be restored before we
			// return, or a combined config_content+wrapper profile would
			// leak the rewritten file on disk.
			if cerr := contentCleanup(); cerr != nil {
				fmt.Fprintf(os.Stderr, "wt: profile cleanup after wrapper error: %v\n", cerr)
			}
			return noop, err
		}
	}
	return contentCleanup, nil
}

// runTail is what runAgentCmd does after the agent exits beyond the shared
// steps: End ends the client session (it runs before the os.Exit branch, so
// the DELETE goes out on every exit) and PriceNote, when set, prints the
// stale-pricing notice after the summary. There is no stop picker: the model
// server stops a model after its idle grace.
type runTail struct {
	End       func()
	PriceNote func()
}

// release is the tail's first step: the client session's End.
func (t runTail) release() {
	if t.End != nil {
		t.End()
	}
}

// runAgentCmd wires stdio through to the agent, runs it, then runs the
// post-exit flow and propagates the agent's exit code to the caller. Order
// (issues #115/#116): end the client session → survey (skipped for native
// models) → summary line → after-survey stats → pricing notice. The user's
// interactive steps come first and the informational output last, so it is
// not scrolled away by the prompts. The duration is measured when the agent
// exits, not when the summary prints. The survey call sits before the
// os.Exit(ExitCode) branch so a non-zero agent exit is still surveyed — a
// crashed session is exactly a "did it work? no" data point.
func runAgentCmd(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles, tail runTail) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Every launch claims the worktree the agent runs in, for exactly the
	// agent's run (Task 405): another launch there warns about this session
	// (held) while it lasts. The site is read from cmd.Dir — the directory
	// the agent will actually run in — after any worktree creation, so a
	// bare-branch launch claims the worktree it created, never the root.
	// A command with no Dir names no directory and claims nothing:
	// filepath.Abs("") is the working directory, and a claim there would
	// give every other session a false held warning for a root this launch
	// never named (claimLaunch guards the same way). holdLaunch's early claim
	// (launchHere) is the same claim file, twice-safe.
	// HoldLaunch's others are dropped on purpose: RunE's confirmLaunch named
	// any other holder already, and a launch prints exactly one held line. A
	// claim that cannot be read or written is one note and never a refusal.
	claimRelease := func() {} // the no-Dir release, so the defer below is unconditional
	if cmd.Dir != "" {
		release, _, cerr := rootclaim.HoldLaunch(cmd.Dir, agent)
		claimRelease = release
		if cerr != nil {
			fmt.Fprintf(osStderr, "wt: note: %s\n", sessionclaim.CheckFailed(cmd.Dir, cerr))
		}
	}
	defer claimRelease() // every exit path below releases; safe twice

	profileCleanup, perr := applyProfileForLaunch(cmd, agent, m, cfg, pp)
	if perr != nil {
		// A pre-launch config error must still end the client session
		// registered before runAgentCmd was called, and print the summary
		// line, so the user sees it on a pre-launch config error exactly as
		// on a real exit. Duration is 0 — the subprocess never started. The
		// survey is deliberately skipped: there is no session to ask "did it
		// work?" about.
		tail.release()
		releaseRootClaim()
		fmt.Println("\n" + agents.Summary(agent, m, 0))
		return perr
	}

	duration, err := agents.RunAndCleanup(cmd, profileCleanup)
	// Explicit, not deferred, for the same reason as profileCleanup: the
	// os.Exit below would skip a deferred release. Both releases take down
	// the same claim file and are safe twice.
	claimRelease()
	releaseRootClaim()
	summary := agents.Summary(agent, m, duration)

	tail.release()
	stats := survey.PromptRun(os.Stdin, os.Stdout, survey.NewStore(), agent, m)

	// Leading "\n" guards against the agent's last byte being non-newline
	// (e.g. a bare prompt or a SIGINT-truncated line) — without it the
	// summary would glue to that partial output. Println adds the trailing
	// newline itself, so the line is always self-terminated.
	fmt.Println("\n" + summary)
	if stats != "" {
		fmt.Println(stats)
	}
	// The reminder is meaningless for command agents (no priced model): their
	// tail carries no PriceNote.
	if tail.PriceNote != nil {
		tail.PriceNote()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
