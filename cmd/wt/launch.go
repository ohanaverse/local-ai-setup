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
	sess, _ := session.LatestForAgent(agent, worktreePath)
	return buildLaunch(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// buildCommandForCommand builds the exec.Cmd for a command-like agent (e.g.
// shell). It prints a note when -M is supplied because pinned models are
// meaningless for command agents, then runs in the requested worktree.
func buildCommandForCommand(agent, worktreePath string, cfg *config.Config, yolo bool, pinned string, extraArgs []string) (*exec.Cmd, error) {
	if pinned != "" {
		fmt.Fprintf(os.Stderr, "note: -M ignored for command agent %q\n", agent)
	}
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
		cmd, berr := buildCommandForCommand(agent, worktreePath, cfg, yolo, pinned, extraArgs)
		return config.Model{}, cmd, berr
	}
	if err != nil {
		return config.Model{}, nil, err
	}
	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	return m, cmd, berr
}

// launchFiltered is the non-TUI launch path used by main.go. It resolves the
// model through the -M/-T/-F flags (or detects a command agent), fails fast if
// the selected ollama model is unavailable, then runs the agent in
// worktreePath with stdio wired through so the agent owns the terminal and its
// exit code propagates to the caller. When multiple models are eligible and
// no -M pin is given, it advances through the eligible list using the per-slot
// rotation state (agent+tag+family) so successive launches rotate rather than
// repeat the same model.
func launchFiltered(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, extraArgs []string) error {
	var rot *rotation.Rotation

	// Resolve the model first. Command agents short-circuit here.
	m, err := resolveModel(agent, cfg, tags, family, pinned)
	if errors.Is(err, errCommandAgent) {
		cmd, berr := buildCommandForCommand(agent, worktreePath, cfg, yolo, pinned, extraArgs)
		if berr != nil {
			return berr
		}
		return runAgentCmd(cmd)
	}
	if err != nil {
		// When multiple models are eligible and no pin was supplied, rotate
		// through the eligible list by (agent, firstTag, family) slot.
		if pinned == "" {
			eligible, eerr := cfg.EligibleModels(agent, tags, family)
			if eerr != nil {
				return eerr
			}
			if len(eligible) > 1 {
				firstTag := firstOrDefault(tags, cfg.DefaultTag)
				slot := rotation.SlotFromFlags(agent, firstTag, family)
				rot = rotation.NewForSlot(slot, eligible, "")
				last, _ := rot.LastLaunched()
				next, ok := rotation.FirstAfter(eligible, last)
				if ok {
					m = next
					err = nil
				}
			}
		}
	}
	if err != nil {
		return err
	}

	// Fail fast if the selected ollama model is not available locally.
	// This runs before session lookup and full command construction so we
	// don't waste work on a model we can't launch.
	if ollamacheck.IsOllamaModel(m) {
		ok, oerr := ollamacheck.Available(m.ModelName)
		if oerr != nil {
			return fmt.Errorf("ollama check failed: %w", oerr)
		}
		if !ok {
			return ollamaUnavailableError(m.ModelName)
		}
	}

	cmd, berr := buildCommandForModel(agent, m, worktreePath, cfg, yolo, extraArgs)
	if berr != nil {
		return berr
	}
	if rot != nil {
		if rerr := rot.RecordLaunch(m); rerr != nil {
			fmt.Fprintf(os.Stderr, "note: rotation state not saved: %v\n", rerr)
		}
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

// firstOrDefault returns the first comma-delimited tag from s, or
// fallback if s is empty. Used by callers that derive a rotation slot's tag
// component from -T when no tag is supplied.
func firstOrDefault(s, fallback string) string {
	parts := config.ParseFilterList(s)
	if len(parts) == 0 {
		return fallback
	}
	return parts[0]
}

// launchDirect runs the agent in the current directory (passthrough when
// outside a git repo).
func launchDirect(agent string, cfg *config.Config, yolo bool, extraArgs []string) error {
	// Use buildFilteredCmd with worktreePath="." to build against the CWD.
	// buildFilteredCmd handles command agents correctly (zero-value model,
	// stderr note on -M), so launchDirect becomes a one-liner.
	_, cmd, err := buildFilteredCmd(agent, ".", cfg, yolo, "", "", "", extraArgs)
	if err != nil {
		return err
	}
	return runAgentCmd(cmd)
}
