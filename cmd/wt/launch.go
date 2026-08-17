package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/ollamacheck"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// buildLaunch constructs the agent command for the given model and worktree,
// appending passthrough args and a resume flag when a prior session exists.
// It is a thin wrapper around agents.BuildLaunchCmd so tests can assert the
// command shape without exec'ing an agent.
func buildLaunch(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	return agents.BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// launch builds and runs the agent directly (no TUI), wiring stdio and
// preserving the agent's exit code so scripts see it. extraArgs are the
// user's passthrough args after --.
func launch(agent, worktreePath string, cfg *config.Config, yolo bool, extraArgs []string) error {
	m := defaultModel(cfg, agent)

	// Fail fast if the selected ollama model is not available locally.
	// Skip the check for shell — it has no model.
	if agent != "shell" && ollamacheck.IsOllamaModel(m) {
		ok, err := ollamacheck.Available(m.ModelName)
		if err != nil {
			return fmt.Errorf("ollama check failed: %w", err)
		}
		if !ok {
			return fmt.Errorf("model %q is not available locally. Run: ollama pull %s", m.ModelName, m.ModelName)
		}
	}

	sess, _ := session.LatestForAgent(agent, worktreePath)
	cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
	if err != nil {
		return err
	}
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

// launchDirect runs the agent in the current directory (passthrough when
// outside a git repo).
func launchDirect(agent string, cfg *config.Config, yolo bool, extraArgs []string) error {
	return launch(agent, ".", cfg, yolo, extraArgs)
}
