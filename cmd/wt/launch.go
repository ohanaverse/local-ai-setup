package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// buildLaunch constructs the agent command for the given model and worktree,
// appending a resume flag when a prior session exists. It is separated from
// run so tests can assert the command shape without exec'ing an agent.
func buildLaunch(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	if s, ok := d.(agents.Syncer); ok {
		if err := s.SyncModels(cfg); err != nil {
			return nil, err
		}
	}
	cmd, err := agents.Command(d, m, yolo, worktreePath)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", sess.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", sess.ID)
		}
	}
	return cmd, nil
}

// launch builds and runs the agent directly (no TUI), wiring stdio and
// preserving the agent's exit code so scripts see it.
func launch(agent, worktreePath string, cfg *config.Config, yolo bool) error {
	m := defaultModel(cfg, agent)
	sess, _ := session.LatestForAgent(agent, worktreePath)
	cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess, cfg)
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
func launchDirect(agent string, cfg *config.Config, yolo bool) error {
	return launch(agent, ".", cfg, yolo)
}
