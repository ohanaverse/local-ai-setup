package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// emitPriceNotice is a seam for tests: production prints modelman's
// stale-pricing notice (issue #69) after the summary; tests swap it to
// observe whether the call was made without touching the real modelman.toml.
var emitPriceNotice = realEmitPriceNotice

func realEmitPriceNotice() {
	agents.PrintPriceNotice()
}

// releaseSession is a seam for tests: production releases this wt process's own
// refcount entry (survey.ReleaseSession) so the post-exit stop picker does not
// count the finished session as a user of its model. Best-effort.
var releaseSession = survey.ReleaseSession

// runStopPicker is a seam for tests: production offers to stop running local
// models no other wt session uses (issue #115); tests swap it so nothing
// probes live servers or stops a real model.
var runStopPicker = realRunStopPicker

func realRunStopPicker(cfg *config.Config) {
	survey.Picker(os.Stdin, os.Stdout, cfg)
}

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
// loadProfileStore()/profiles.Validate exactly as before. Mirrors the
// nil-means-not-precomputed convention launchFilteredImpl's own `eligible`
// parameter already uses.
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
	return askYesNoDefault(f, true)
}

func askYesNoDefault(r io.Reader, def bool) (bool, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// buildLaunch constructs the agent command for the given model and worktree,
// appending passthrough args and a resume flag when a prior session exists.
// It is a thin wrapper around agents.BuildLaunchCmd so tests can assert the
// command shape without exec'ing an agent.
func buildLaunch(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	return agents.BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// buildCommandForModel performs session lookup and builds the exec.Cmd for a
// model-driven agent. It is extracted so tests can assert command shape without
// exec'ing an agent.
func buildCommandForModel(agent string, m config.Model, worktreePath string, cfg *config.Config, yolo bool, extraArgs []string) (*exec.Cmd, error) {
	// Native models launch fresh: resuming a session would restore the
	// session's stored model, overriding the user's "native" choice. Skip
	// the session lookup so no --resume/--session flag is ever appended.
	var sess *session.Session
	if !m.Native {
		if r, ok := agents.ByName(agent).(agents.Resumer); ok {
			sess, _ = r.LatestSession(worktreePath)
		}
	}
	return buildLaunch(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// buildCommandForCommand builds the exec.Cmd for a command-like agent (e.g.
// shell). It runs in the requested worktree; the -M warning lives in
// launchFiltered where the pinnedSupplied signal is available.
func buildCommandForCommand(agent, worktreePath string, cfg *config.Config, yolo bool, extraArgs []string) (*exec.Cmd, error) {
	return agents.BuildLaunchCmd(agent, config.Model{}, worktreePath, yolo, nil, cfg, extraArgs)
}

// buildFilteredCmd is the build-only core of launchFiltered: it resolves the
// model (or detects a command agent) and constructs the launch command for
// worktreePath without running it. It is extracted so tests can assert the
// command shape — including that command agents run in the worktree, not the
// caller's CWD — without exec'ing an agent.
//
// Note: the ollama availability check lives in launchFiltered, not here, so
// tests can build commands without requiring a local ollama server.
func buildFilteredCmd(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, extraArgs []string) (config.Model, *exec.Cmd, error) {
	m, _, err := resolveModel(agent, cfg, tags, family, pinned)
	if errors.Is(err, errCommandAgent) {
		cmd, berr := buildCommandForCommand(agent, worktreePath, cfg, yolo, extraArgs)
		return config.Model{}, cmd, berr
	}
	if err != nil {
		return config.Model{}, nil, err
	}
	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	return m, cmd, berr
}

// launchFiltered is the wired-up launch path used by main.go for every
// non-TUI launch (-w, --cwd, and outside-a-repo passthrough). It resolves
// the eligible model list (via cfg.EligibleModels), resolves the -M pin
// against all rows (a start row is started before this point), and
// otherwise advances through the eligible list using the per-slot
// rotation state (agent+tag+family). For a pinned match or a single
// eligible model, no rotation is consulted. A -M pin on a start row
// was already started by resolveModel before this point — launchFiltered
// only ever sees the launch decision. Command agents (shell,
// etc.) bypass the model layer but still run in worktreePath — they route
// through buildFilteredCmd so the same worktree-path threading that
// TestBuildFilteredCmdCommandAgentUsesWorktree locks down is on the launch
// path, instead of an ad-hoc CWD helper that silently dropped the path.
//
// launchFiltered is a package-level variable so tests can stub it (see
// TestCommandAgentWithoutModelLaunchesDirectly and
// TestAgentWithOneEligibleModelAutoLaunches). The implementation lives in
// launchFilteredImpl — keeping it in a separate function makes the var
// re-assignable without losing the original behavior.
var launchFiltered = launchFilteredImpl

// pinnedSupplied distinguishes "user passed -M with an empty value" from
// "user did not pass -M". It's used to surface a stderr note when -M is
// passed together with a command agent (where the pin would otherwise be
// silently dropped).
func launchFilteredImpl(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model, pp *precomputedProfiles) error {
	if agents.IsCommand(agent) {
		if pinnedSupplied {
			fmt.Fprintf(os.Stderr, "wt: -M ignored for command %q\n", agent)
		}
		// buildFilteredCmd dispatches command agents to buildCommandForCommand
		// with the real worktreePath (not "."), so shell-wt -W foo runs in the
		// worktree. runAgentCmd then wires stdio and execs it.
		_, cmd, err := buildFilteredCmd(agent, worktreePath, cfg, yolo, "", "", "", extraArgs)
		if err != nil {
			return err
		}
		return runAgentCmd(cmd, agent, config.Model{}, cfg, pp)
	}

	// Resolve the model. If the caller precomputed the eligible list, reuse
	// it: the precomputed eligible a caller passes is the LAUNCHABLE list
	// resolveModel returned, so any -M pin was already resolved against all
	// rows (and a pinned start already ran) before this point — there is no
	// pinned branch here. Otherwise resolveModel computes it (and returns it
	// for rotation).
	// nil (not len == 0) signals "not precomputed" so a caller that legitimately
	// passes an empty-but-non-nil slice isn't silently recomputed against.
	var m config.Model
	var err error
	if eligible == nil {
		m, eligible, err = resolveModel(agent, cfg, tags, family, pinned)
	} else {
		m, err = resolveModelFromEligible(agent, eligible)
	}
	if err != nil {
		// When multiple models are eligible and no pin was supplied, rotate
		// through the global model list to the next model supported by agent.
		if pinned == "" {
			next, ok := rotation.New().NextFromEligible(eligible, cfg)
			if ok {
				m = next
				err = nil
			}
		}
	}
	if err != nil {
		return err
	}

	// Fail fast if the selected ollama model is not available locally.
	// This runs before session lookup and full command construction so we
	// don't waste work on a model we can't launch. The check is skipped when
	// this agent×model pairing will actually route through LiteLLM (any
	// upstream may serve the model) and when the model is not served by
	// ollama at all — ollamacheck only probes the local ollama daemon. Uses
	// the per-model resolved route rather than the raw cfg.IsLitellm()
	// toggle: a protocol mismatch (e.g. codex+ollama) forces LiteLLM even
	// when the toggle is off, and the toggle-only check spuriously blocked
	// that case on an unready-locally ollama model that would launch fine
	// once forced through the proxy. Route resolution errors are ignored
	// here — buildCommandForModel resolves the same route again and
	// surfaces the error properly.
	route, _ := cfg.ResolveRoute(m, agents.ProtocolsFor(agent))
	if !route.Litellm && ollamacheck.IsOllamaModel(m) {
		ok, oerr := ollamacheck.Check(m)
		if oerr != nil {
			// Print the summary before returning so the user sees the same
			// post-run line on pre-launch config errors as on a real exit.
			// Duration is 0 — the subprocess never started.
			fmt.Println("\n" + agents.Summary(agent, m, 0))
			return fmt.Errorf("ollama check failed: %w", oerr)
		}
		if !ok {
			fmt.Println("\n" + agents.Summary(agent, m, 0))
			return ollamaUnavailableError(m.ModelName)
		}
	}

	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	if berr != nil {
		return berr
	}
	if rerr := rotation.New().RecordFor(agent, m.ID); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: rotation state not saved: %v\n", rerr)
	}
	if rerr := refcount.NewStore().Record(os.Getpid(), m.ID); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: refcount state not saved: %v\n", rerr)
	}
	return runAgentCmd(cmd, agent, m, cfg, pp)
}

// launchPassthrough builds and runs the bare-agent command for an
// unconfigured model-driven agent (agents.IsConfigured false) — no model
// routing, equivalent to running the installed binary directly. The zero
// config.Model passed to runAgentCmd makes the post-exit flow behave exactly
// like a command-agent launch: no survey, no stop picker, no price notice,
// and a summary line without a model segment.
//
// launchPassthrough is a package-level variable so tests can stub it,
// mirroring launchFiltered above.
var launchPassthrough = launchPassthroughImpl

func launchPassthroughImpl(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config, pp *precomputedProfiles) error {
	cmd, err := agents.BuildPassthroughCmd(agent, worktreePath, yolo, extraArgs)
	if err != nil {
		return err
	}
	return runAgentCmd(cmd, agent, config.Model{}, cfg, pp)
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
// cleanup (the same reasoning that already makes
// lifecycle.WaitPendingRoutes an explicit call there, not a defer).
// Command agents (m.ID == "") and native models never match a profile —
// ResolveRoute returns a zero Route for both — so this no-ops for them
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

	var store profiles.Store
	if pp != nil {
		if pp.loadErr != nil {
			fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", pp.loadErr)
			return noop, nil
		}
		if pp.validateErr != nil {
			fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", pp.validateErr)
			return noop, nil
		}
		store = pp.store
	} else {
		var loadErr error
		store, loadErr = loadProfileStore()
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", loadErr)
			return noop, nil
		}
		if verr := profiles.Validate(store, agentProfileMechanisms); verr != nil {
			fmt.Fprintf(os.Stderr, "wt: profiles.toml: %v (profiles disabled for this launch)\n", verr)
			return noop, nil
		}
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
	return applyResolvedProfile(cmd, agent, rp)
}

// applyResolvedProfile applies an already-confirmed ResolvedProfile to
// cmd: config_content first, then ApplyToCmd (env/args/wrapper) — so a
// codex profile's "--profile agent-wt-profile" arg it appends is captured
// by a later wrapper's {{args}} splice (no Phase-1 profile combines the
// two, but this keeps the ordering correct if one ever does). Extracted
// from applyProfileForLaunch so this config_content/wrapper interaction —
// specifically that a wrapper failure must still trigger the
// already-obtained content cleanup — is directly testable with a
// hand-built ResolvedProfile, without needing a profiles.toml entry that
// passes Validate (no Phase-1 agent declares both config_file and wrapper
// mechanisms together).
func applyResolvedProfile(cmd *exec.Cmd, agent string, rp profiles.ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	contentCleanup, err := profiles.ApplyConfigContent(cmd, agent, cmd.Dir, rp)
	if err != nil {
		// Per the design's global constraint, a profile resolution/
		// application error must degrade to a normal, unprofiled launch —
		// never block the agent from starting — so a file-write error here
		// (permissions, a resolve-home-dir failure for codex, …) is a
		// warning, not a launch-aborting error.
		fmt.Fprintf(os.Stderr, "wt: profile config_content: %v (profiles disabled for this launch)\n", err)
		return noop, nil
	}
	if err := profiles.ApplyToCmd(cmd, rp); err != nil {
		// ApplyToCmd's one error case (a wrapper mechanism naming a
		// missing binary) is the sole exception the design calls out as
		// fatal — but any config file ApplyConfigContent already wrote or
		// backed up above must still be restored before we return, or a
		// combined config_content+wrapper profile would leak the
		// rewritten file on disk.
		if cerr := contentCleanup(); cerr != nil {
			fmt.Fprintf(os.Stderr, "wt: profile cleanup after wrapper error: %v\n", cerr)
		}
		return noop, err
	}
	return contentCleanup, nil
}

// runAgentCmd wires stdio through to the agent, runs it, then runs the
// post-exit flow and propagates the agent's exit code to the caller. Order
// (issues #115/#116): release this session's refcount entry → survey (skipped
// for native models) → stop picker → summary line → after-survey stats →
// pricing notice. The user's
// interactive steps come first and the informational output last, so it is
// not scrolled away by the prompts. The duration is measured when the agent
// exits, not when the summary prints. The survey call sits before the
// os.Exit(ExitCode) branch so a non-zero agent exit is still surveyed — a
// crashed session is exactly a "did it work? no" data point.
func runAgentCmd(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	profileCleanup, perr := applyProfileForLaunch(cmd, agent, m, cfg, pp)
	if perr != nil {
		// Mirrors the ollama-check failure path in launchFilteredImpl above:
		// a pre-launch config error must still release the refcount entry
		// already recorded before runAgentCmd was called, and print the
		// summary line, so the user sees it on a pre-launch config error
		// exactly as on a real exit. Duration is 0 — the subprocess never
		// started. Survey and the stop picker are deliberately skipped:
		// there is no session to ask "did it work?" about.
		releaseSession()
		fmt.Println("\n" + agents.Summary(agent, m, 0))
		return perr
	}

	start := time.Now()
	err := cmd.Run()
	if cerr := profileCleanup(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
	}
	summary := agents.Summary(agent, m, time.Since(start))

	releaseSession()
	stats := survey.PromptRun(os.Stdin, os.Stdout, survey.NewStore(), agent, m)
	// Command agents (e.g. shell) launch with a zero-value config.Model
	// (m.ID == "") and native models run no local server, so a session on
	// either has nothing of its own to stop — offering unrelated idle models
	// after it would be a surprise.
	if m.ID != "" && !m.Native {
		runStopPicker(cfg)
	}

	// Leading "\n" guards against the agent's last byte being non-newline
	// (e.g. a bare prompt or a SIGINT-truncated line) — without it the
	// summary would glue to that partial output. Println adds the trailing
	// newline itself, so the line is always self-terminated.
	fmt.Println("\n" + summary)
	if stats != "" {
		fmt.Println(stats)
	}
	// The reminder is meaningless for command agents (no priced model) —
	// same convention survey.PromptRun already uses.
	if m.ID != "" {
		emitPriceNotice()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// This os.Exit bypasses main's own wait, and the stop picker just
			// above may have kicked off an async LiteLLM proxy restart (route
			// removal). Exiting now would kill it mid-flight.
			lifecycle.WaitPendingRoutes()
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

func ollamaUnavailableError(modelName string) error {
	return fmt.Errorf("model %q is not available locally. Run: ollama pull %s", modelName, modelName)
}
