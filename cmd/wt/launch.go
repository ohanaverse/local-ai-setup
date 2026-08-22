package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/ollamacheck"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

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
	if !m.IsNative() {
		sess, _ = session.LatestForAgent(agent, worktreePath)
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
	m, err := resolveModel(agent, cfg, tags, family, pinned)
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
// the eligible model list (via cfg.EligibleModels), pins the model when -M
// is provided, and otherwise advances through the eligible list using the
// per-slot rotation state (agent+tag+family). For a pinned match or a
// single eligible model, no rotation is consulted. Command agents (shell,
// etc.) bypass the model layer but still run in worktreePath — they route
// through buildFilteredCmd so the same worktree-path threading that
// TestBuildFilteredCmdCommandAgentUsesWorktree locks down is on the launch
// path, instead of an ad-hoc CWD helper that silently dropped the path.
//
// pinnedSupplied distinguishes "user passed -M with an empty value" from
// "user did not pass -M". It's used to surface a stderr note when -M is
// passed together with a command agent (where the pin would otherwise be
// silently dropped).
func launchFiltered(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string) error {
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
		return runAgentCmd(cmd)
	}

	// Resolve the model first. Command agents short-circuit in resolveModel.
	m, err := resolveModel(agent, cfg, tags, family, pinned)
	if err != nil {
		// When multiple models are eligible and no pin was supplied, rotate
		// through the global model list to the next model supported by agent.
		if pinned == "" {
			next, ok := rotation.New().Next(cfg, agent, tags, family)
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
	// don't waste work on a model we can't launch.
	ok, oerr := ollamacheck.Check(m)
	if oerr != nil {
		return fmt.Errorf("ollama check failed: %w", oerr)
	}
	if !ok {
		return ollamaUnavailableError(m.ModelName)
	}

	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	if berr != nil {
		return berr
	}
	if rerr := rotation.New().Record(m.ID); rerr != nil {
		fmt.Fprintf(os.Stderr, "note: rotation state not saved: %v\n", rerr)
	}
	return runAgentCmd(cmd)
}

// runAgentCmd wires stdio through to the agent and runs it. The agent's exit
// code propagates to the caller.
func runAgentCmd(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

func ollamaUnavailableError(modelName string) error {
	return fmt.Errorf("model %q is not available locally. Run: ollama pull %s", modelName, modelName)
}
