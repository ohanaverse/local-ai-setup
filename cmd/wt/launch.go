package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/spf13/cobra"
)

// mustGetString returns a string flag value, ignoring the error. The flag is
// always registered, so GetString cannot fail here.
func mustGetString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// yolo reports whether the --yolo flag was set.
func yolo(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("yolo")
	return v
}

// inGitRepo reports whether the current directory is inside a git repo.
func inGitRepo() bool {
	return inGitRepoAt(".")
}

// inGitRepoAt reports whether dir is inside a git repo. Separated from
// inGitRepo so tests can point it at a temp repo without chdir'ing the
// process.
func inGitRepoAt(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

// defaultAgent returns the agent to launch when --agent is not given: the
// first configured agent, falling back to "claude".
func defaultAgent(cfg *config.Config) string {
	if cfg != nil && len(cfg.Agents) > 0 {
		return cfg.Agents[0].Name
	}
	return "claude"
}

// defaultModel returns the model to launch for an agent: the agent's native
// model (e.g. claude/native) if present, else the first model in the default
// tag group.
func defaultModel(cfg *config.Config, agent string) config.Model {
	for _, m := range cfg.Models {
		if m.ID == agent+"/native" {
			return m
		}
	}
	ms := cfg.ModelsWithTag(cfg.DefaultTag)
	if len(ms) > 0 {
		return ms[0]
	}
	return config.Model{ID: "(none)", Location: config.LocationCloud}
}

// buildLaunch constructs the agent command for the given model and worktree,
// appending a resume flag when a prior session exists. It is separated from
// run so tests can assert the command shape without exec'ing an agent.
func buildLaunch(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
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
	cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess)
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
