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

// buildFilteredCmd is the build-only core of launchFiltered: it resolves the
// model (or detects a command agent) and constructs the launch command for
// worktreePath without running it. It is extracted so tests can assert the
// command shape — including that command agents run in the worktree, not the
// caller's CWD — without exec'ing an agent.
//
// The returned model is the zero value for command agents; callers skip the
// ollama availability check in that case (command agents have no model).
func buildFilteredCmd(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, extraArgs []string) (config.Model, *exec.Cmd, error) {
	m, err := resolveModel(agent, cfg, tags, family, pinned)
	if errors.Is(err, errCommandAgent) {
		// Command agents (shell) have no model layer. -M is meaningless
		// here: surface a note so the user knows the pin was ignored, then
		// build directly against the worktree so `wt -W foo --agent shell`
		// runs in the worktree rather than the caller's CWD.
		if pinned != "" {
			fmt.Fprintf(os.Stderr, "note: -M ignored for command agent %q\n", agent)
		}
		cmd, berr := agents.BuildLaunchCmd(agent, config.Model{}, worktreePath, yolo, nil, cfg, extraArgs)
		return config.Model{}, cmd, berr
	}
	if err != nil {
		return config.Model{}, nil, err
	}
	sess, _ := session.LatestForAgent(agent, worktreePath)
	cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
	if err != nil {
		return m, nil, err
	}
	return m, cmd, nil
}

func ollamaUnavailableError(modelName string) error {
	return fmt.Errorf("model %q is not available locally. Run: ollama pull %s", modelName, modelName)
}

// launchFiltered is the non-TUI launch path used by main.go. It resolves the
// model through the -M/-T/-F flags (or detects a command agent), fails fast if
// the selected ollama model is unavailable, then runs the agent in
// worktreePath with stdio wired through so the agent owns the terminal and its
// exit code propagates to the caller. PR 3 will add rotation through the
// eligible list.
func launchFiltered(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, extraArgs []string) error {
	m, cmd, err := buildFilteredCmd(agent, worktreePath, cfg, yolo, tags, family, pinned, extraArgs)
	if err != nil {
		return err
	}

	// Fail fast if the selected ollama model is not available locally.
	// Skip command agents (shell) — they have no model.
	if !agents.IsCommand(agent) && ollamacheck.IsOllamaModel(m) {
		ok, oerr := ollamacheck.Available(m.ModelName)
		if oerr != nil {
			return fmt.Errorf("ollama check failed: %w", oerr)
		}
		if !ok {
			return ollamaUnavailableError(m.ModelName)
		}
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
